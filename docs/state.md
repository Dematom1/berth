# State store and format

dibs keeps all lease state in one JSON file per host and Unix user. A kernel lock serializes every reader and writer, and every write is atomic. This page describes where state lives, how the store reads and writes it, the machine identity recorded in it, and the current format.

Source: `internal/lease/store.go` (paths, locking, reading, validation, saving), `internal/lease/machine.go` (machine identity and migration), and `internal/atomicfile/write.go` (atomic writes). Tests: `TestOpenStoreRestrictsExistingDirectory`, `TestStoreDurabilityLockAndCorruption`, and `TestStoreLockReleasedByProcessDeath` in `internal/lease/lease_test.go`, `TestAtomicWriteRestoresOldStateWhenDirectorySyncFails` in `internal/atomicfile/write_test.go`, `TestSessionWaiterStateRoundTrip` in `internal/lease/session_test.go`, `TestOpenMigratedStoreWithoutHostname` and `TestMachineMigrationCurrentHostnameAndQueue` in `internal/lease/machine_migration_test.go`, and `TestRenamedStateSelection` and `TestStateRenameKeepsLegacyLeasesAndLock` in `internal/lease/compat_test.go`. Decisions: [ADR 0005](adr/0005-adapter-and-owner-extension-contracts.md#current-implementation-limits) records the current format limits.

## State directory

`resolveStateDir` picks the first match and makes it absolute:

1. `DIBS_STATE`, or the legacy `BERTH_STATE`. An explicit path is used as given and is never moved.
2. `$XDG_STATE_HOME/dibs`.
3. `~/.local/state/dibs`.

When the `dibs` directory is missing and a legacy `berth` directory exists, `migrateStateDir` publishes `dibs` as an atomic symlink to `berth`. The ledger, lock inode, snapshots, owner identities, reservations, waiters, and TTLs stay in place. If both directories exist and resolve to different ledgers, dibs refuses with an error and names `DIBS_STATE` as the way to select one. Read-only inventory never publishes the alias.

| Path | Mode | Contents |
| --- | --- | --- |
| `<dir>/` | `0700`, enforced on every open | State directory |
| `<dir>/lock` | `0600` | Lock file. Never replaced or deleted. |
| `<dir>/state.json` | `0600`, enforced on every open | Lease records and the job queue |
| `<dir>/resources/<lease-id>/` | `0700` | Private adapter files: `kubeconfig` (kind), `k3d` (k3d kubeconfig), or `compose.json` (resolved Compose config) |

`openStore` runs `chmod` on the directory and on `state.json` even when they already exist. Test: `TestOpenStoreRestrictsExistingDirectory`.

Release does not delete `resources/<lease-id>/`. The README says these snapshots remain for diagnosis. `compose.json` holds resolved Compose values, which can include secrets.

## Locking

`openStore` takes an exclusive `flock` on `<dir>/lock`.

- It tries a non-blocking lock every 50 ms until it succeeds or the context ends.
- The kernel drops the lock when the process dies, so a crashed dibs never leaves a stale lock. Test: `TestStoreLockReleasedByProcessDeath`.
- Lease commands hold the lock for the whole command, including adapter `Create` and `Destroy`. A `ponytail:` comment in `openStore` names this as a deliberate host-wide lock.
- `dibs job` does not hold the lock while it waits for admission or while the child runs. `admitJob` opens and closes the store on each poll, and `startJob` and `finishJob` each open it briefly. See [Jobs](jobs.md#admission).

## Reading

`openStore` reads `state.json` after it has the lock.

- A missing file is an empty store at the current format version.
- A file that fails to parse or fails `validate` is an error: `cannot read lease state (kept unchanged)`. The store never treats corrupt state as empty and never rewrites it. Test: `TestStoreDurabilityLockAndCorruption`.
- A `version` above the newest this binary supports is refused before the rest of the file is decoded. The error names the version found, the recorded `writer_version` when present, the newest version and the dibs version of this binary, and the fix: upgrade dibs, and run `which -a dibs berth` to find a stale binary earlier on `PATH`. The file is kept unchanged and the exit code is the same as for any unreadable state. Test: `TestNewerStateRefusedWithUpgradeHint`.
- After validation, `migrateMachines` runs. See [Machine identity](#machine-identity).

`validate` rejects the whole file when any of these is true:

- `version` is below `1` or above `4`, or `leases` is missing.
- A waiter has a malformed ID, an empty machine, a zero `expires`, an owner kind other than `pid` or `session`, or an empty owner identity.
- A lease map key does not match the lease's own `id`, or the ID is not 32 lowercase hex characters.
- A lease has an empty `machine`, a zero `created` or `expires`, or a non-positive `ttl`.
- A `process` lease has no `job`, a non-positive budget, or an empty class. Its leader, when set, must be a `pid` owner with a PID above 1 and a recorded identity.
- A non-`process` lease has a `job`.
- A lease marked adopted appears in a version 1 file. Adopted identities need version 2.
- A lease with `protect_live_owner` appears in a file below version 3, or on a `process` lease.
- A lease with a `key` or a pending `resource` appears in a file below version 4, two leases on one machine share a key, or a pending record names an unknown adapter, an adapter without discovery, an invalid handle, or is also adopted.
- The adapter's `Validate` rejects the managed resource. Each built-in checks its name pattern (`dibs-`, or the legacy `berth-` name for existing leases), its Docker context, and that its private file sits inside `<dir>/resources/<id>/`. See [Adapters](adapters.md#built-in-adapters).

## Writing

`Save` first sets the format version (`store.settleVersion`). A store with any keyed or pending lease is saved as version 4. Otherwise a store with any protected lease is saved as version 3, and a store with neither goes back to version 2, so older binaries can read it again. Tests: `TestProtectedStateFormat`, `TestProtectedOnlyLedgerStaysVersionThree`.

`Save` then encodes the store with two-space indentation and a trailing newline, then calls `atomicfile.Write`:

1. Read the previous file content, if any.
2. Write the new content to a temporary file `.dibs-*` in the same directory.
3. `fsync` the temporary file and close it.
4. Rename it over `state.json`.
5. `fsync` the directory.

If the directory `fsync` fails, `Write` restores the previous content (or removes a file that did not exist before), syncs the directory again, and returns the error. Test: `TestAtomicWriteRestoresOldStateWhenDirectorySyncFails`.

Callers that delete a lease and then fail to save put the lease back in memory, so the in-memory store matches the file (`releaseLease` in `internal/lease/lease.go`, `finishJob` in `internal/lease/job_finish.go`).

## Machine identity

Version 1 state identified a host by hostname. Version 2 identifies it by a stable OS identity, so a hostname change does not change lease ownership.

`machine()` in `internal/lease/machine.go` reads the identity once per process:

- macOS: `IOPlatformUUID` from `ioreg`, recorded as `darwin:<uuid>`.
- Linux: `/etc/machine-id`, recorded as `linux:<id>`.

The value is normalized to lowercase hexadecimal without hyphens and must decode to 16 non-zero bytes. A missing or invalid identity is an error. dibs never falls back to a hostname. The identity is cached per process so that a rename during a process's lifetime cannot change which leases it can act on.

`migrateMachines` runs on every open that reaches it:

- It does work when the file is version 1, or when any lease or waiter still records a non-stable name (one without a `:`).
- Legacy records that match the current hostname, or the explicit alias in `DIBS_LEGACY_MACHINE` (or `BERTH_LEGACY_MACHINE`), receive the stable identity. Other legacy names stay foreign. Unrecognized names remain unknown and cannot be reclaimed, even if expired.
- The store is set to version 2 and saved, if anything changed, under the same lock. Test: `TestMachineMigrationCurrentHostnameAndQueue`, which asserts version 2 after opening version 1 state.
- A fully migrated version 2 file needs no hostname lookup. Test: `TestOpenMigratedStoreWithoutHostname`.

`DIBS_LEGACY_MACHINE` is a same-user attestation that the named hostname belongs to this machine. Never set it from a project file, and never use it for another host. The alias affects only hostname records, never another host's stable identity.

Cloned hosts must have distinct OS identities. dibs does not generate or modify them, and state should stay on its original host.

## Format version 2

`state.json` is one JSON object. Durations are Go `time.Duration` values, which JSON encodes as integer nanoseconds. Times are RFC 3339 with nanoseconds. New state is written as version 2. Version 1 files still read, and migrate to version 2 on open as described above. Version 3 is version 2 plus at least one lease with `protect_live_owner: true`. It exists so that an older binary rejects the ledger instead of ignoring protection and reclaiming the lease at expiry. Version 4 adds the consumer contract's `key` and pending `resource` fields. A binary that reads up to version 3 would drop them on save, which would turn a pending lease into an ordinary one and let a later keyed request create a second lease, so it refuses version 4 instead. Test: `TestVersionThreeReaderRefusesKeysAndPending`.

```json
{
  "version": 2,
  "writer_version": "0.2.3",
  "leases": {
    "<id>": {
      "id": "<id>",
      "machine": "linux:<machine-id>",
      "owner": {"kind": "pid", "id": "1234", "start": "Mon Nov  9 10:00:00 2026"},
      "created": "2026-11-09T10:00:00Z",
      "expires": "2026-11-09T18:00:00Z",
      "ttl": 28800000000000,
      "resources": "2 CPUs, test lab",
      "labels": {"task": "lab"},
      "resource": {"adapter": "kind", "name": "dibs-<id>", "docker_context": "default", "file": "<dir>/resources/<id>/kubeconfig"}
    }
  },
  "waiters": [
    {"id": "<id>", "machine": "linux:<machine-id>", "owner": {"kind": "pid", "id": "1234", "start": "..."}, "expires": "..."}
  ]
}
```

| Field | Go field and type | Notes |
| --- | --- | --- |
| `version` | `store.Version`, int | `2` for new state, `3` while any lease is protected, `4` while any lease is keyed or pending. `1` to `4` are accepted. |
| `writer_version` | `store.Writer`, string | Version of the dibs binary that last saved the file. Additive; it does not change the schema version. Omitted when empty. Test: `TestStateWithWriterVersionLoadsAndRecordsWriter`. |
| `leases` | `map[string]Lease` | Keyed by lease ID |
| `waiters` | `[]waiter` | FIFO job admission queue. Omitted when empty. |
| `owner.kind`, `owner.id` | `owner.Reference` | See [Leases](leases.md#owner-kinds) |
| `owner.start` | `Reference.Identity` | `pid` and `session` only. Omitted when empty. |
| `owner.stale` | `Reference.Stale` | `heartbeat` only, in nanoseconds. Omitted when zero. |
| `resource.adapter` | `ResourceRef.Adapter` | `none`, `kind`, `k3d`, `compose`, or `process` |
| `resource.name` | `ResourceRef.Name` | Managed resource name. Omitted for `none` and `process`. |
| `resource.adopted` | `ResourceRef.Adopted` | `true` for a resource taken over by `dibs adopt`. Version 2 only. |
| `resource.docker_context` | `ResourceRef.Context` | Docker context captured at create |
| `resource.docker_host` | `ResourceRef.DockerHost` | k3d and adopted resources: the endpoint of that context |
| `resource.file` | `ResourceRef.File` | Private adapter file under `resources/<id>/` |
| `job.budget.cores`, `job.budget.memory_bytes` | `budget.Amount` | Reserved cores and bytes |
| `job.class` | `ReservationView.Class` | Class name used for the request |
| `job.leader` | `ReservationView.Leader` | `pid` owner of the process group leader. Empty until the child starts. |
| `protect_live_owner` | `Lease.ProtectLiveOwner` | `true` for live-owner protection. Version 3 or later. Omitted when false. |
| `key` | `Lease.Key` | Idempotency key from `--key`. At most one lease per machine has a given key. Version 4 only. Omitted when empty. |
| `resource.pending` | `ResourceRef.Pending` | `true` while a pending lease waits for its resource. `name` is then the pinned handle or empty, and `file` is the caller's absolute Compose file. Version 4 only. |

## Invariants

1. **Corrupt state is an error, never an empty list.** `openStore` returns an error and leaves the file unchanged. Test: `TestStoreDurabilityLockAndCorruption`.
2. **Every write is atomic and durable.** Writes go through `atomicfile.Write`. Nothing writes `state.json` directly.
3. **The lock file is never replaced.** Replacing it would split lock holders across two inodes.
4. **Every mutation happens under the lock.** Load, change, and `Save` happen between `openStore` and `Close` in one process.
5. **State stays private.** The directory is `0700`, and files are `0600`. Test: `TestOpenStoreRestrictsExistingDirectory`.
6. **State is local to one host and one Unix user.** Leases carry a stable `machine`, and other hosts never act on them. Do not share the directory over a network filesystem.
7. **Format changes are contract changes.** `validate` accepts versions 1 to 4, with version 3 used only while a protected lease exists and version 4 only while a keyed or pending lease exists. A new field, a renamed field, a new adapter name, or a new version changes what older binaries accept. Older binaries reject version 2, so a version 2 file needs a compatible binary for every invocation, as the README states. Binaries that record `writer_version` refuse any newer version with an upgrade hint instead of a bare format error.
8. **Resource files stay inside their state directory.** Each adapter's `Validate` checks its file with `SnapshotMatches` (`internal/adapter/snapshot.go`), which accepts the legacy `berth` root only when it resolves to the same ledger.

## Planned

The approved direction in [ADR 0005](adr/0005-adapter-and-owner-extension-contracts.md) includes a state schema with migrations and a stable CLI contract. Only the version 1 to version 2 steps above, the protection-driven version 3, and the consumer-contract version 4 exist. A general migration framework is not built, and each future format change needs its own migration and version bump. Accounting and an audit trail are in scope for dibs but not built. No accounting record exists in the store.
