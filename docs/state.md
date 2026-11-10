# State store and format

Berth keeps all lease state in one JSON file per host and Unix user. A kernel lock serializes every reader and writer, and every write is atomic. This page describes where state lives, how the store reads and writes it, and the version 1 format.

Source: `store.go`. Tests: `TestOpenStoreRestrictsExistingDirectory`, `TestStoreDurabilityLockAndCorruption`, `TestStoreLockReleasedByProcessDeath`, and `TestAtomicWriteRestoresOldStateWhenDirectorySyncFails` in `lease_test.go`, and `TestSessionWaiterStateRoundTrip` in `ssh_test.go`.

## State directory

`stateDir` picks the first match and makes it absolute:

1. `$BERTH_STATE`
2. `$XDG_STATE_HOME/berth`
3. `~/.local/state/berth`

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
- The kernel drops the lock when the process dies, so a crashed berth never leaves a stale lock. Test: `TestStoreLockReleasedByProcessDeath`.
- Lease commands hold the lock for the whole command, including adapter `Create` and `Destroy`. A `ponytail:` comment in `openStore` names this as a deliberate host-wide lock.
- `berth job` does not hold the lock while it waits for admission or while the child runs. `admitJob` opens and closes the store on each poll, and `startJob` and `finishJob` each open it briefly.

## Reading

`openStore` reads `state.json` after it has the lock.

- A missing file is an empty store with `version: 1`.
- A file that fails to parse or fails `validate` is an error: `cannot read lease state (kept unchanged)`. The store never treats corrupt state as empty and never rewrites it. Test: `TestStoreDurabilityLockAndCorruption`.

`validate` rejects the whole file when any of these is true:

- `version` is not `1`, or `leases` is missing.
- A waiter has a malformed ID, an empty machine, a zero `expires`, an owner kind other than `pid` or `session`, or an empty owner `start`.
- A lease map key does not match the lease's own `id`, or the ID is not 32 lowercase hex characters.
- A lease has an empty `machine`, a zero `created` or `expires`, or a non-positive `ttl`.
- A `process` lease has no `job`, a non-positive budget, or an empty class. Its leader, when set, must be a `pid` owner with a PID above 1 and a recorded start.
- A non-`process` lease has a `job`.
- The adapter is not one of `none`, `kind`, `k3d`, `compose`, or `process`.
- A managed resource has the wrong name or no Docker context. The name is `berth-<id>`, or `berth-<first 26 ID characters>` for k3d.
- A k3d resource has no captured Docker endpoint.
- A kind, k3d, or Compose resource file is not exactly `<dir>/resources/<id>/<name>`. State cannot point cleanup at a file outside its own directory.

## Writing

`Save` encodes the store with two-space indentation and a trailing newline, then calls `atomicWrite`:

1. Read the previous file content, if any.
2. Write the new content to a temporary file `.berth-*` in the same directory.
3. `fsync` the temporary file and close it.
4. Rename it over `state.json`.
5. `fsync` the directory.

If the directory `fsync` fails, `atomicWrite` restores the previous content (or removes a file that did not exist before), syncs the directory again, and returns the error. Test: `TestAtomicWriteRestoresOldStateWhenDirectorySyncFails`.

Callers that delete a lease and then fail to save put the lease back in memory, so the in-memory store matches the file (`releaseLease` in `lease.go`, `finishJob` in `job.go`).

## Format version 1

`state.json` is one JSON object. Durations are Go `time.Duration` values, which JSON encodes as integer nanoseconds. Times are RFC 3339 with nanoseconds.

```json
{
	"version": 1,
	"leases": {
		"<id>": {
			"id": "<id>",
			"machine": "<hostname>",
			"owner": {"kind": "pid", "id": "1234", "start": "Mon Nov  9 10:00:00 2026"},
			"created": "2026-11-09T10:00:00Z",
			"expires": "2026-11-09T18:00:00Z",
			"ttl": 28800000000000,
			"resources": "2 CPUs, test lab",
			"labels": {"task": "lab"},
			"resource": {"adapter": "kind", "name": "berth-<id>", "docker_context": "default", "file": "<dir>/resources/<id>/kubeconfig"}
		}
	},
	"waiters": [
		{"id": "<id>", "machine": "<hostname>", "owner": {"kind": "pid", "id": "1234", "start": "..."}, "expires": "..."}
	]
}
```

| Field | Go type | Notes |
| --- | --- | --- |
| `version` | `Store.Version` | Always `1` |
| `leases` | `map[string]Lease` | Keyed by lease ID |
| `waiters` | `[]JobWaiter` | FIFO job admission queue. Omitted when empty. |
| `owner.kind`, `owner.id` | `Owner` | See [Leases](leases.md) |
| `owner.start` | `Owner.Start` | `pid` and `session` only. Omitted when empty. |
| `owner.stale` | `Owner.Stale` | `heartbeat` only, in nanoseconds. Omitted when zero. |
| `resource.adapter` | `Resource.Adapter` | `none`, `kind`, `k3d`, `compose`, or `process` |
| `resource.name` | `Resource.Name` | Managed resource name. Omitted for `none` and `process`. |
| `resource.docker_context` | `Resource.Context` | Docker context captured at create |
| `resource.docker_host` | `Resource.DockerHost` | k3d only: endpoint of that context |
| `resource.file` | `Resource.File` | Private adapter file under `resources/<id>/` |
| `job.budget.cores`, `job.budget.memory_bytes` | `Budget` | Reserved cores and bytes |
| `job.class` | `JobResource.Class` | Class name used for the request |
| `job.leader` | `JobResource.Leader` | `pid` owner of the process group leader. Empty until the child starts. |

## Invariants

1. **Corrupt state is an error, never an empty list.** `openStore` returns an error and leaves the file unchanged.
2. **Every write is atomic and durable.** Writes go through `atomicWrite`. Never write `state.json` directly.
3. **The lock file is never replaced.** Replacing it would split lock holders across two inodes.
4. **Every mutation happens under the lock.** Load, change, and `Save` happen between `openStore` and `Close` in one process.
5. **State stays private.** The directory is `0700`, and files are `0600`.
6. **State is local to one host and one Unix user.** Leases carry `machine`, and other hosts never act on them. Do not share the directory over a network filesystem.
7. **Format changes are contract changes.** `validate` accepts only `version: 1`, and no migration code exists. A new field, a renamed field, or a new adapter name changes what older binaries accept.

## Planned

The captain-approved scope (2026-11-09) plans a versioned state schema with migrations. Today the store accepts only version 1 and has no migration path. Accounting and an audit trail are also in scope but not built.
