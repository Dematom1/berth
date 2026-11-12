# Leases and owner liveness

A lease records who owns a resource on this machine and when the resource expires. Automatic reclaim (`sweep` and job admission) happens only when the owner is dead or the lease has expired. An explicit `release --confirm` destroys a lease regardless of owner liveness or expiry. This page describes the lease lifecycle and the owner liveness checks that decide reclaim.

Source: `internal/lease/lease.go` (record, reclaim, extend, release, sweep), `internal/lease/manager.go` (the commands' ordering under the state lock), `internal/owner/reference.go` and `internal/owner/registry.go` (owner parsing and liveness), and the lease commands in `internal/cli/cli.go`. Tests: `internal/lease/lease_test.go`, `internal/owner/reference_test.go`, `internal/cli/cli_test.go`, and `TestFormalLeaseDomain` in `internal/lease/formal_test.go` (build tag `formal`, run with `just formal`). Formal model: `verification/Lease.tla`. Decisions: [ADR 0001](adr/0001-lease-lifecycle-and-ttl.md), [ADR 0002](adr/0002-reclaim-with-proof.md), [ADR 0003](adr/0003-owner-identity-and-liveness.md).

## Lease record

`newLease` in `internal/lease/lease.go` builds every lease.

- `ID` is 32 lowercase hex characters from 16 bytes of `crypto/rand`. The store validates the shape with the `leaseID` pattern `^[a-f0-9]{32}$`.
- `Machine` is the stable OS identity of the host, such as `linux:<machine-id>`. The lease belongs to that host for its whole life. See [State](state.md#machine-identity).
- `Created` and `Expires` are UTC times. At creation, `Expires` is `Created + TTL`. `extend` changes `Expires` to the extension time plus the new TTL, and `Created` stays the same, so `Created + TTL` is no longer the expiry after an extension.
- `TTL` must be positive. `newLease` and `extendLease` both reject `ttl <= 0`.
- `Description` is free-text metadata, stored as `resources`. It is not a CPU or memory limit.
- `Labels` is free-form `key=value` metadata from repeatable `--label` flags.
- `ResourceRef` names the adapter and the managed resource identity. A lease without a managed resource uses adapter `none`. See [Adapters](adapters.md).
- `Reservation` (JSON `job`) is set only for job reservations, which use adapter `process`. See [Jobs](jobs.md).

The on-disk layout of these fields is in [State](state.md#format-version-2).

## Owner kinds

`newOwners` in `internal/cli/owners.go` registers three owner kinds. Each is a `Definition` in `internal/owner/registry.go` with a `Bind` function that parses and validates the owner when it is created, and a `Check` function that reports liveness later.

| Kind | ID | Recorded identity | Liveness source |
| --- | --- | --- | --- |
| `pid` | Positive PID that fits in a signed 32-bit integer | `Identity`, the `ps -o lstart=` start time | `kill(pid, 0)` plus `ps` |
| `session` | PID of the process that holds a session (not a Unix session ID) | Same as `pid` | Same as `pid` |
| `heartbeat` | File path, made absolute at parse time | `Stale`, the `--stale` duration (default `15m`) | File modification time |

The CLI picks the owner in this order: `--owner`, then `DIBS_OWNER` (or the legacy `BERTH_OWNER`), then `pid:<parent-pid>` (`run` in `internal/cli/cli.go`). `job` uses `pid:<own-pid>`. The SSH agent runs remote jobs as `session:<agent-pid>` (`runAgent` in `internal/cli/agent.go`).

`bind` in `internal/owner/reference.go` refuses an owner that is not alive at creation:

- A `pid` or `session` owner must be alive and inspectable.
- A `heartbeat` owner must be an existing, fresh, regular file.

## Liveness states

`Check` returns one of three `Liveness` values: `alive`, `dead`, or `unknown`. `unknown` means dibs cannot prove the owner either way. It is never treated as `dead`.

For `pid` and `session` owners, `ProcessIdentity` and the registry's `Check` decide:

| Observation | Result |
| --- | --- |
| Stored PID is invalid, out of range, or has no recorded identity | `unknown` |
| `kill(pid, 0)` fails with `ESRCH` | `dead` |
| `kill(pid, 0)` fails with any other error, such as `EPERM` | `unknown` |
| `ps` fails and the PID is now gone | `dead` |
| `ps` fails for another reason, or prints an unexpected shape | `unknown` |
| `ps` state starts with `Z` (zombie) | `dead` |
| Process is alive but its start time differs from the recorded identity (PID reuse) | `dead` |
| Process is alive with the recorded start time | `alive` |

`ps` runs with `LC_ALL=C` and `TZ=UTC` and a 3-second timeout. Start times have one-second precision, so PID reuse within the same second is not detected. A `ponytail:` comment in `ProcessIdentity` names that limit.

For `heartbeat` owners, `check` in `internal/owner/reference.go` decides:

| Observation | Result |
| --- | --- |
| `Stale <= 0`, or the path is not absolute | `unknown` |
| File does not exist | `dead` |
| `stat` fails for another reason, the path is not a regular file, or its modification time is in the future | `unknown` |
| File age is at least `Stale` | `dead` |
| Otherwise | `alive` |

Tests: `TestOwnerLiveness`, `TestOutOfRangePIDIsUnknownNotDead`, `TestHeartbeatIgnoresProcessIdentity`.

## Reclaim rule

`reclaimable` in `internal/lease/lease.go` is the single reclaim decision. `sweep`, `list`, and job admission all call it.

1. If the lease's `Machine` is not this host, the lease is not reclaimable. The owner check does not run, and liveness reports `unknown`.
2. Otherwise the lease is reclaimable when the owner is `dead` **or** `now >= Expires`.

These cases follow from the rule:

| Owner | Expired | Reclaimable |
| --- | --- | --- |
| `alive` | no | no |
| `unknown` | no | no |
| `dead` | either | yes |
| `alive` or `unknown` | yes | yes |
| any, on a foreign machine | either | no |

Tests: `TestReclaimAndExtend`, `TestSweepDecisions`, `TestCLIExpiredLiveOwnerAndFailure`.

## Lifecycle commands

### Create

`Manager.Create` in `internal/lease/manager.go` creates a lease in this order:

1. `open` takes the host-wide state lock (see [State](state.md#locking)), then reads the clock. The clock is read after the lock, because acquiring the lock can wait behind a slow adapter.
2. The owner is checked again. A stale or unavailable owner fails the create.
3. `newLease` builds the record, and the adapter's `Prepare` captures the cleanup scope. See [Adapters](adapters.md).
4. The lease is saved to `state.json`.
5. The adapter's `Create` runs.

If `Create` fails, the lease stays in state so that `release` or `sweep` can clean up a partial resource. The error names the retained lease ID: `creation incomplete; lease <id> retained for cleanup`. Test: `TestCLICreateFailureKeepsDurableCleanupIntent`.

With `--key`, `keyed` runs first, right after the lock: a lease on this machine with the same key, owner, adapter, and handle is returned unchanged, and a different request with that key is refused. With `--pending`, `recordPending` replaces steps 3 to 5: it records the scope and optional pinned handle, refuses a pinned handle that already exists or that another lease claims, and saves the lease without running the adapter. The caller builds the resource and binds it with `adopt --pending`. See [Consumer contract](consumer-contract.md#crash-safe-acquire). Tests: `TestIdempotencyKeys`, `TestPendingPinnedHandleLifecycle`.

### Extend

`extendLease` sets `Expires = now + ttl`. It refuses when any of these is true:

- `ttl` is not positive.
- The lease belongs to another machine.
- The lease is a job reservation (`Reservation != nil`). Job deadlines never move.
- The lease is reclaimable, or its owner is not `alive`.

An unprotected expired lease cannot come back. A protected lease can reattach after expiry only when its owner is alive and its pinned resource still exists; the result is `status: reattached`. Protected renewal refusals for owner or resource state use contract 1 and exit 3 with stable reasons; other extend refusals keep exit 1. When owner state is dead or unknown, that reason takes precedence over expiry. Renewal may be repeated on the same lease. Test: `TestExtendRefusalReasons`, `TestProtectedReclaimAndRenew`.

### Release

`dibs release <id>` is a preview unless `--confirm` is set. It now returns contract 1 statuses `preview`, `released`, or `already_done` (replacing the previous free-form status strings). With `--confirm`, `Manager.Release` calls `releaseLease`, which:

1. Returns `already_done` with no change when the lease is absent.
2. Refuses a lease from another machine with `wrong_machine` (exit 3).
3. Refuses a lease whose adapter this binary does not know, and keeps the lease. The `process` adapter is handled by the lease package itself, not the adapter registry.
4. For a pending lease, `settlePending` looks for its match first. One match is bound, then destroyed below. No match and no conflict means nothing was built, and the lease is forgotten without an adapter call. Several matches refuse with `ambiguous` (exit 3); a match already claimed by another lease refuses with `already_leased` (exit 3). Failed discovery exits 1 and retains the lease. Tests: `TestPendingCleanupAfterCrash`, `TestPendingCleanupRetainsConflictingResource`, `TestReconcileAmbiguousConflictUnknown`.
5. Calls the adapter's `Destroy`, or the process group's `Destroy`. On error, keeps the lease.
6. Deletes the lease and saves (`forget`). If the save fails, restores the lease in memory and returns the error.

`release --confirm` does not check owner liveness or expiry. It is an explicit operator action on a specific lease. Cleanup failures exit 1 and retain the lease. Tests: `TestCLILeaseLifecycle`, `TestCLIReleaseWrongMachineRefused`.

### Sweep

`sweep` in `internal/lease/lease.go` walks leases in sorted ID order. It records `unknown` owners, collects reclaimable leases as `eligible`, and, with `--confirm`, calls `releaseLease` on each eligible lease. A failed release goes into `Failures` and keeps its lease. `dibs sweep` exits 1 when any failure occurred. A second sweep after success is a no-op. Test: `TestSweepPreviewRetryAndIdempotence`.

### Reclaim and explain

`Manager.Reclaim` in `internal/lease/pending.go` is a targeted reclaim that is stricter than both `release` and the reclaim rule above. It acts only when the owner check returns `dead` on this machine. An `alive` owner is refused even after expiry, and `unknown` liveness is refused. It previews without `--confirm`, then calls `releaseLease`. An absent lease is success. `Manager.Explain` reports the reclaim rule's answer, the targeted reclaim's answer, and a readable reason, without acting. Tests: `TestReclaimRequiresProvenDeath`, `TestExplainUnexpired`.

dibs has no daemon. Reclaim happens only when someone runs `dibs sweep --confirm`, `dibs release --confirm`, `dibs reclaim --confirm`, or `dibs job` (whose admission reclaims eligible job leases). The help text tells users to run sweep from a host timer.

## Renewal cadence

The `cadence` constant in `internal/cli/help.go` is printed in `create` and `extend` help:

- Touch heartbeat files at most every `stale / 3`.
- Extend a lease at the latest when one third of its TTL remains.

The cadence is a recommendation. Missing it does not by itself make a lease reclaimable. A live owner that misses it can still extend an unexpired lease. If the lease then expires, or its owner dies, a later `sweep --confirm` reclaims it. `TestCLIHomeHelpVersionAndTOON` asserts the help text.

## Invariants

Reviewers check every change to `internal/lease/lease.go`, `internal/lease/manager.go`, `internal/owner/`, and the lease commands in `internal/cli/cli.go` against these rules.

1. **Never reclaim a live, unexpired owner.** Automatic reclaim (`sweep`, job admission) goes through `reclaimable`, which requires `dead` or expired. Expiry wins even over a live owner, by policy (ADR 0001, finding F1 in `verification/findings.md`). Tests: `TestReclaimAndExtend`, `TestSweepDecisions`, `TestFormalLeaseDomain`.
2. **`unknown` is never `dead`.** Any error that does not prove absence returns `unknown`. An `unknown` owner keeps its lease until expiry. Tests: `TestOwnerLiveness`, `TestOutOfRangePIDIsUnknownNotDead`.
3. **A host acts only on its own leases.** `reclaimable`, `extendLease`, and `releaseLease` compare `Machine` with this host before anything else. Expired foreign leases stay. Test: `TestReclaimAndExtend`.
4. **A record is removed only after verify-gone.** `releaseLease` deletes the lease only after `Destroy` returns nil, and every built-in adapter's `Destroy` verifies absence before returning nil. The one exception is a job reservation whose group leader is already gone and reaped: `processResource.Destroy` then succeeds without verifying that descendants are gone. See [Jobs](jobs.md#process-group-cleanup) and [ADR 0002](adr/0002-reclaim-with-proof.md#existing-process-exception). See [Adapters](adapters.md#invariants). Test: `TestSweepPreviewRetryAndIdempotence`.
5. **Cleanup intent is durable before the resource exists.** `Manager.Create` saves the lease before the adapter's `Create` runs. Test: `TestCLICreateFailureKeepsDurableCleanupIntent`.
6. **Preview by default.** `release` and `sweep` change nothing without `--confirm`. Test: `TestSweepPreviewRetryAndIdempotence`.
7. **Expiry is checked after the lock.** `Manager.open` reads the clock after `openStore` has acquired the lock.
8. **PID owners pin start identity.** A reused PID reads as `dead`, never as the original owner. The registry's `Check` compares the recorded identity with the live one. Test: `TestOwnerLiveness`.
9. **Job deadlines never move.** `extendLease` refuses reservations. Test: `TestJobsRealProcesses` covers the job lifecycle. See [Jobs](jobs.md#invariants).

## Planned

The approved extension direction in [ADR 0005](adr/0005-adapter-and-owner-extension-contracts.md#approved-direction-not-an-implemented-extension-interface) plans these owner extensions. None exists in the code today. The owner registry in `internal/owner/registry.go` is internal, not a versioned public interface.

- A versioned Owner contract: `check` returns `alive`, `dead`, or `unknown` with an identity. Today the registry holds the three native kinds only.
- An `exec` owner from a local script, such as `--owner exec:./check.sh`.
- Out-of-process `dibs-owner-<name>` plugins on `PATH` that speak a JSON protocol over stdin and stdout.
- A conformance kit for owners, alongside the planned `dibs adapter test <name>`. Neither command exists.
- Owner kinds such as container, pod, unit, GitHub run, pull request, agent session, task, parent lease, URL, and device.
