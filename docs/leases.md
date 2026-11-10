# Leases and owner liveness

A lease records who owns a resource on this machine and when the resource expires. Berth reclaims a lease only when its owner is dead or the lease has expired. This page describes the lease lifecycle and the owner liveness checks that decide reclaim.

Source: `lease.go`, `owner.go`, and the lease commands in `cli.go`. Tests: `lease_test.go` and `cli_test.go`.

## Lease record

`newLease` in `lease.go` builds every lease.

- `ID` is 32 lowercase hex characters from 16 bytes of `crypto/rand`. `store.go` validates the shape with the `leaseID` pattern `^[a-f0-9]{32}$`.
- `Machine` is `os.Hostname()` at creation. The lease belongs to that host for its whole life.
- `Created` and `Expires` are UTC times. `Expires` is `Created + TTL`.
- `TTL` must be positive. `newLease` and `extendLease` both reject `ttl <= 0`.
- `Resources` is free-text metadata. It is not a CPU or memory limit.
- `Labels` is free-form `key=value` metadata from repeatable `--label` flags.
- `Resource` names the adapter and the managed resource identity. A lease without a managed resource uses adapter `none`. See [Adapters](adapters.md).
- `Job` is set only for job reservations, which use adapter `process`. See [Jobs](jobs.md).

The on-disk layout of these fields is in [State](state.md).

## Owner kinds

`parseOwner` in `owner.go` parses `--owner KIND:ID`. The CLI picks the owner in this order: `--owner`, then `BERTH_OWNER`, then `pid:<parent-pid>` (`run` in `cli.go`).

| Kind | ID | Recorded identity | Liveness source |
| --- | --- | --- | --- |
| `pid` | Positive PID that fits in a signed 32-bit integer | `Start`: the `ps -o lstart=` start time | `kill(pid, 0)` plus `ps` |
| `session` | PID of the process that holds a session (not a Unix session ID) | Same as `pid` | Same as `pid` |
| `heartbeat` | File path, made absolute at parse time | `Stale`: the `--stale` duration (default `15m`) | File modification time |

`pid` and `session` use the same check. They differ only in meaning: `berth job` uses `pid:<own-pid>`, and the SSH agent runs remote jobs as `session:<agent-pid>` (`runAgent` in `ssh.go`).

`parseOwner` refuses an owner that is not alive at creation:

- A `pid` or `session` owner must be alive and inspectable.
- A `heartbeat` owner must be an existing, fresh, regular file.

## Liveness states

`checkOwner` returns one of three `Liveness` values: `alive`, `dead`, or `unknown`. `unknown` means berth cannot prove the owner either way. It is never treated as `dead`.

For `pid` and `session` owners, `processIdentity` and `checkOwner` decide:

| Observation | Result |
| --- | --- |
| Stored PID is invalid, out of range, or has no recorded `Start` | `unknown` |
| `kill(pid, 0)` fails with `ESRCH` | `dead` |
| `kill(pid, 0)` fails with any other error, such as `EPERM` | `unknown` |
| `ps` fails and the PID is now gone | `dead` |
| `ps` fails for another reason, or prints an unexpected shape | `unknown` |
| `ps` state starts with `Z` (zombie) | `dead` |
| Process is alive but its start time differs from `Start` (PID reuse) | `dead` |
| Process is alive with the recorded start time | `alive` |

`ps` runs with `LC_ALL=C` and `TZ=UTC` and a 3-second timeout. Start times have one-second precision, so PID reuse within the same second is not detected. A `ponytail:` comment in `processIdentity` names that limit.

For `heartbeat` owners:

| Observation | Result |
| --- | --- |
| `Stale <= 0`, or the path is not absolute | `unknown` |
| File does not exist | `dead` |
| `stat` fails for another reason, the path is not a regular file, or its modification time is in the future | `unknown` |
| File age is at least `Stale` | `dead` |
| Otherwise | `alive` |

Tests: `TestOwnerLiveness`, `TestOutOfRangePIDIsUnknownNotDead`.

## Reclaim rule

`reclaimable` in `lease.go` is the single reclaim decision. `sweep`, `list`, and job admission all call it.

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

`run` in `cli.go` creates a lease in this order:

1. Parse the owner. It must be alive.
2. Open the store, which takes the host-wide lock and can wait.
3. Read the clock again, then check the owner again. A stale owner fails the create.
4. Build the lease and call `prepareResource` for the adapter.
5. Save the lease to `state.json`.
6. Call the adapter's `Create`.

If `Create` fails, the lease stays in state so that `release` or `sweep` can clean up a partial resource. The error names the retained lease ID. Test: `TestCLICreateFailureKeepsDurableCleanupIntent`.

### Extend

`extendLease` sets `Expires = now + ttl`. It refuses when any of these is true:

- `ttl` is not positive.
- The lease belongs to another machine.
- The lease is a job reservation (`Job != nil`). Job deadlines never move.
- The lease is reclaimable, or its owner is not `alive`.

An expired lease cannot come back. Create a new one instead. Test: `TestReclaimAndExtend`.

### Release

`berth release <id>` is a preview unless `--confirm` is set. With `--confirm`, `releaseLease`:

1. Returns success with no change when the lease is absent.
2. Refuses a lease from another machine.
3. Refuses a lease whose adapter this binary does not know, and keeps the lease.
4. Calls the adapter's `Destroy`. On error, keeps the lease.
5. Deletes the lease and saves. If the save fails, restores the lease in memory and returns the error.

`release --confirm` does not check owner liveness or expiry. It is an explicit operator action on a specific lease. Tests: `TestCLILeaseLifecycle`, `TestCLIReleaseFailureDoesNotReportReleased`.

### Sweep

`sweep` walks leases in sorted ID order. For each lease it records `unknown` owners, collects reclaimable leases as `eligible`, and, with `--confirm`, calls `releaseLease` on each eligible lease. A failed release goes into `Failures` and keeps its lease. `berth sweep` exits 1 when any failure occurred. A second sweep after success is a no-op. Test: `TestSweepPreviewRetryAndIdempotence`.

Berth has no daemon. Reclaim happens only when someone runs `berth sweep --confirm`, `berth release --confirm`, or `berth job` (whose admission reclaims eligible job leases). The help text tells users to run sweep from a host timer.

## Renewal cadence

The `cadence` constant in `cli.go` is printed in `create` and `extend` help:

- Touch heartbeat files at most every `stale / 3`.
- Extend a lease at the latest when one third of its TTL remains.

A live owner that misses either cadence loses its resources, because expiry alone makes a lease reclaimable. `TestCLIHomeHelpVersionAndTOON` asserts that the extend help contains this text.

## Invariants

Reviewers check every change to `lease.go`, `owner.go`, and the lease commands in `cli.go` against these rules.

1. **Never reclaim a live, unexpired owner.** Automatic reclaim (`sweep`, job admission) goes through `reclaimable`, which requires `dead` or expired. Tests: `TestReclaimAndExtend`, `TestSweepDecisions`.
2. **`unknown` is never `dead`.** Any error that does not prove absence returns `unknown`. An `unknown` owner keeps its lease until expiry. Tests: `TestOwnerLiveness`, `TestOutOfRangePIDIsUnknownNotDead`.
3. **A host acts only on its own leases.** `reclaimable`, `extendLease`, and `releaseLease` compare `Machine` with this host before anything else. Expired foreign leases stay. Test: `TestReclaimAndExtend` ("expired foreign machine").
4. **A record is removed only after verify-gone.** `releaseLease` deletes the lease only after `Destroy` returns nil, and every adapter's `Destroy` verifies absence before returning nil. See [Adapters](adapters.md). Test: `TestSweepPreviewRetryAndIdempotence`.
5. **Cleanup intent is durable before the resource exists.** The lease is saved before the adapter's `Create` runs. Test: `TestCLICreateFailureKeepsDurableCleanupIntent`.
6. **Preview by default.** `release` and `sweep` change nothing without `--confirm`. Test: `TestSweepPreviewRetryAndIdempotence`.
7. **Expiry is checked after the lock.** The CLI reads the clock again after `openStore`, because acquiring the lock can wait behind a slow adapter.
8. **PID owners pin start identity.** A reused PID reads as `dead`, never as the original owner. Test: `TestOwnerLiveness`.

## Planned

The captain-approved scope (2026-11-09) plans these owner extensions. None exists in the code today.

- A versioned Owner contract: `check` returns `alive`, `dead`, or `unknown` with an identity.
- An `exec` owner from a local script, such as `--owner exec:./check.sh`.
- Out-of-process `berth-owner-<name>` plugins on `PATH` that speak a JSON protocol over stdin and stdout.
- A conformance kit for owners, alongside `berth adapter test <name>`.
- Owner kinds such as container, pod, unit, GitHub run, pull request, agent session, task, parent lease, URL, and device.
