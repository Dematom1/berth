# Jobs, admission, budgets, and deadlines

`dibs job` runs one command under a reservation of logical cores and memory. The reservation is a lease with adapter `process`, owned by the `dibs job` process. Admission is FIFO against a machine budget. The deadline starts at admission, and dibs kills the process group when the deadline passes. This page describes that flow.

Source: `internal/job/command.go` (flag parsing and the reservation request), `internal/lease/manager.go` (`Admit`), `internal/lease/job_admission.go` (budget and FIFO admission), `internal/lease/job_start.go` (start gate and `Exec`), `internal/lease/job_run.go` (waiting and exit codes), `internal/lease/job_process.go` (process group cleanup), `internal/lease/job_finish.go` (lease removal), `internal/lease/job_wait_{darwin,linux}.go` (exit observation), `main.go` (the `__job_exec` helper), and the budget types in `internal/config/config.go`. Tests: `internal/lease/job_test.go`, `TestFormalBudgetDomain` and `TestFormalConcurrentAdmission` in `internal/lease/formal_test.go` (build tag `formal`), and `TestFormalEOF` and `TestFormalAuthoritativeResult` in `internal/cli/formal_test.go`. Formal models: `verification/Admission.tla`, `verification/JobSession.tla`. Decisions: [ADR 0004](adr/0004-fifo-admission-and-owner-room.md), [ADR 0002](adr/0002-reclaim-with-proof.md#existing-process-exception).

## Reservation request

`Run` in `internal/job/command.go` builds the request before it touches state. A usage error exits 2 and starts no child.

1. Everything after the first `--` is the child's argv. dibs never parses it. A missing or empty child command is a usage error.
2. `configpkg.Load` reads the budget and classes. See [Configuration](config.md).
3. With `--machine`, dibs forwards the job to that SSH machine and stops here. See [Remote jobs over SSH](remote.md).
4. The class sets cores, memory, and timeout. `--class` defaults to `default`.
5. Explicit `--cores`, `--memory`, and `--timeout` flags override the class values independently.
6. Cores, memory, and timeout must be positive. The request must also fit within the usable budget, which is the totals minus owner room. Otherwise dibs exits 2. The message names the usable amount and the owner room, and suggests a `--cores` or `--memory` value that fits.
7. `exec.LookPath` resolves the child executable. If it fails, dibs exits 1, and no child starts.
8. The owner is `pid:<dibs job pid>` for a local job, or `session:<agent pid>` when the SSH agent runs the job.
9. The lease description records only the executable's base name, never its arguments.

Test: `TestJobsRealProcesses` subtests "usage and prestart errors" and "command metadata excludes arguments".

## Budget

`JobConfig.Available` returns the usable budget: `cores - room_cores` and `memory - room_memory`. Owner room is capacity kept free for interactive work.

`budgetFits` in `internal/lease/job_admission.go` subtracts the reservation budget of every job lease on this machine from the usable budget and clamps each value at zero. Admitted reservations that have not started yet also count. Stack leases (`kind`, `k3d`, `compose`, and `none`) never count against the budget. Their description text is only a description.

Reservations coordinate cooperating dibs jobs. They are not OS CPU or memory limits.

Test: `TestJobBudgetAndConfig`.

## Admission

`admitJob` loops until it admits the job, the wait expires, or the context ends. Each pass:

1. Opens the store with a lock deadline equal to the wait deadline.
2. Prunes waiters on this machine whose expiry has passed or whose owner is `dead`. Waiters with an `unknown` owner stay until they expire. Test: `TestJobQueuePrunesExpiredButKeepsUnknown`.
3. Appends this job to `waiters` on the first pass when `--wait` is positive.
4. Reclaims job leases on this machine that `reclaimable` marks eligible, with a shared 5-second cleanup budget per pass. A failed cleanup keeps the reservation and prints the error. Test: `TestAdmissionReclaimHasLiveSharedCleanupBudget`.
5. Admits the job only when it is the first waiter for this machine and `budgetFits` passes. With `--wait 0`, the job is never queued, so it is admitted only when no waiter is queued for this machine.
6. On admission, removes the waiter, sets `Created = now` and `Expires = now + timeout`, and saves the lease.
7. Otherwise saves any queue change, closes the store, and polls again after 50 ms. The lock is not held during the sleep.

When the wait ends without admission, `ErrNoCapacity` maps to exit 75 (`EX_TEMPFAIL`), and no child starts. A deferred cleanup removes this job's waiter. If that cleanup fails, the waiter expires on its own. Test: `TestExpiredWaiterCleanupAfterLockContention`.

The queue is FIFO across classes, with no backfill. A smaller request never overtakes an earlier, larger waiter. Queue order is the order in which requests take the state lock. Test: `TestJobsRealProcesses` subtest "FIFO no backfill wait code and ps".

## Start

`startJob` in `internal/lease/job_start.go` makes the process group durable before any user code runs:

1. It opens the store and checks that the reservation still exists and has not expired.
2. It starts the dibs binary again as `dibs __job_exec <argv...>` in a new process group (`Setpgid`). The child gets a pipe on file descriptor 3, and `DIBS_LEASE=<id>` and `BERTH_LEASE=<id>` in its environment.
3. It binds the helper as an owner reference `pid:<pid>`, including its start time, records it as `job.leader`, and saves.
4. It checks the context and the deadline again.
5. It writes one byte to the pipe.

`Exec` reads one byte from file descriptor 3 and only then calls `exec` with the child argv. If the parent dies before step 5, the helper reads EOF and exits 125 without running user code. If the exec itself fails, the helper exits 126. Test: `TestJobHelperEOFDoesNotRun`.

## Wait without reaping

`waitChildExit` waits for the child to exit without reaping it. It uses `kqueue` `NOTE_EXIT` on macOS and `waitid` with `WNOWAIT` on Linux. While the zombie exists, its PID cannot be reused, so the group cleanup that follows signals the right group. `childExited` is the non-blocking probe used at the deadline. Test: `TestDeadlineProbeSeesCompletedUnreapedChild`.

`RunJob` in `internal/lease/job_run.go` waits for one of three events:

| Event | Action | Exit code |
| --- | --- | --- |
| Child exits | Clean up the group, release the lease | Child's code, or `128 + signal` |
| Deadline (`Expires`) | Check whether the child already exited. If not, clean up the group. | `124` if the child had not exited, otherwise the child's code |
| `SIGINT`, `SIGTERM`, or SSH session EOF | Clean up the group, release the lease | Child's code after cleanup, usually `128 + signal` |

If dibs cannot observe the exit, or cannot verify completion at the deadline, it kills the child, keeps the lease, and tells the user that sweep will retry.

## Process group cleanup

`processResource.Destroy` in `internal/lease/job_process.go` terminates the group:

1. With no recorded leader, the reservation never started. `Destroy` succeeds.
2. It reads the leader's current identity. If liveness is `unknown`, or the PID belongs to a different process, `Destroy` fails and keeps the lease. Tests: `TestDestroyRejectsReusedLeaderPID`, `TestDestroyRejectsReusedZombieGroupLeader`.
3. If the leader is gone and already reaped, dibs warns on stderr and succeeds without signaling a group it cannot verify. Descendants may survive. This is the process exception to verify-gone that [ADR 0002](adr/0002-reclaim-with-proof.md#existing-process-exception) records. Test: `TestDestroyDoesNotSignalGroupAfterLeaderReaped`.
4. Otherwise it sends `SIGTERM` to the group, polls `ps -A -o pgid= -o stat=` for up to one second, then repeats with `SIGKILL`. Zombie members do not count as alive.
5. If members survive both signals, `Destroy` fails and keeps the lease.

Normal completion also runs this cleanup, so descendants that outlive the child are terminated. Test: `TestJobsRealProcesses` subtest "normal completion cleans descendants".

`finishJob` in `internal/lease/job_finish.go` then removes the lease. When group cleanup already succeeded, it deletes the record directly. Otherwise it calls `releaseLease`, which runs `Destroy` again. If cleanup fails, the lease stays, dibs prints the error to stderr, and `sweep --confirm` retries later.

A program that calls `setsid` or changes its process group escapes cleanup. Process groups are not a security boundary.

## Exit codes

`dibs job` owns the codes in the `exitCodes` constant in `internal/cli/help.go`. The full table is in [CLI](cli.md#exit-codes).

- The child's exit code passes through when the child runs to completion, even when it equals 1, 2, 75, or 124.
- dibs's own 1 and 2 apply only when no child started.
- Cleanup errors after start go to stderr and never replace the child's code. Test: `TestJobsRealProcesses` subtest "cleanup failure retains child code and lease".
- `--axi` and `--json` change only errors before start. Child stdin, stdout, and stderr pass through unchanged. Test: `TestOutputProcessesAndTTY`.

## Known limitations

`verification/findings.md` records two limitations in the current code. Neither has a fix yet.

- **F2: a reaped leader can leave an orphan group.** When the leader is gone and reaped, `Destroy` succeeds without signaling, and the lease is removed. Surviving descendants then use CPU and memory outside any reservation. `TestDestroyDoesNotSignalGroupAfterLeaderReaped` shows the behavior.
- **F3: the last deadline check and the start gate are not atomic.** `startJob` checks the deadline and then writes the gate byte. If the process is descheduled between the two steps, user code can start after the deadline, until the timer cleanup runs.

Reviewers treat changes in these areas as behavior changes that need a decision, not as cleanup.

## Invariants

1. **No user code runs before the group identity is durable.** The helper waits on file descriptor 3 until the leader is saved. Test: `TestJobHelperEOFDoesNotRun`.
2. **dibs signals only a verified group.** The leader's start identity must match before any signal. A reaped leader means no signal. Tests: `TestDestroyRejectsReusedLeaderPID`, `TestDestroyDoesNotSignalGroupAfterLeaderReaped`.
3. **A record is removed only after cleanup succeeds.** With a verified leader, the lease is deleted only after `ps` shows no live group members. The one exception is a leader that is already gone and reaped: dibs then releases without proof that descendants are gone (ADR 0002). Changing that exception is a behavior change that needs its own decision.
4. **FIFO with no backfill.** Only the first waiter for a machine can be admitted. Test: `TestJobsRealProcesses`.
5. **Job deadlines never move.** `extendLease` refuses reservations. The deadline starts at admission, not at command launch.
6. **The child's exit code is authoritative.** dibs's codes 1, 2, and 75 apply only before start. 124 means the deadline fired and dibs did not confirm that the child had already exited. It is returned when the child had not exited, and also when completion cannot be verified at the deadline, even if the child had already exited.
7. **The budget counts every reservation on the machine**, including admitted but not started ones, until verify-gone removes it. Test: `TestJobBudgetAndConfig`.
8. **The state lock is never held while waiting or while the child runs.** `admitJob` closes the store before each sleep, and `startJob` and `finishJob` each open it briefly.

## Planned

The approved direction in [VISION.md](../VISION.md) names opt-in capabilities that are not built: a resident sweeper, deadline-bounded backfill, owner preemption of lower-class jobs, and cgroup or container enforcement of reservations. Each must be opt-in, and none is a silent default. Accounting and an audit trail of who held what, and for how long, are in scope but not built. No accounting exists in the store.
