---
name: lease-job-safety
description: "Check lease reclaim, FIFO admission and budgets, job deadlines and cleanup, or SSH owner-channel liveness after changing lease.go, store.go, owner.go, job.go, or ssh.go. Runs bounded TLA+ models and real-code mutation checks."
---

# Check lease and job safety

TLA+ with TLC fits this task because the dangerous cases depend on event order, lock scope, and crashes. The Go package has no installed code-level verifier. The models check protocol ordering, while exhaustive small-domain tests and mutations execute the real Go code to expose mapping drift.

Read [the findings](../../../verification/findings.md) first. F1 is resolved by policy: TTL expiry permits reclaim even for Alive owners, while an unexpired live owner must not be reclaimed. F2 and F3 now pass for automatic reservation cleanup and the helper's deadline decision. A green check covers the bounded models, not hard real-time execution.

## Run the checks

1. Install Go as specified by `go.mod`, Python 3, and Java 17 or newer. CI uses Java 21.
2. From the repository root, run `make formal`.
3. Read every `PASS`, `COUNTEREXAMPLE`, and `REJECTED real-code mutant` line. Read the TLC logs at the printed path if any result is unexpected.
4. `python3 verification/check.py --strict` requires every bounded property to pass, including F2 and F3. The default command has the same contract.

`check.py` downloads the checksum-pinned `tla2tools.jar` v1.7.4 release. `TLA2TOOLS_JAR` selects an already downloaded copy, which still needs the same checksum. `JAVA` selects the Java executable. `FORMAL_LOG_DIR` selects a directory outside the checkout for configs, traces, and checker state. Without it, the checker creates a temporary directory and prints its path.

CI runs the same command on `ubuntu-24.04` with a five-minute job timeout. The artifact `formal-counterexamples` contains complete TLC traces. Docker, a remote machine, and paid services are not required.

## Check the code mapping

Last confirmed source commit: `81070056d00383ec40bb8aa1db9b8bc65f3d91db`.

Run `git diff 81070056d00383ec40bb8aa1db9b8bc65f3d91db -- lease.go store.go owner.go job.go job_wait_linux.go job_wait_darwin.go ssh.go config.go cli.go adapter.go`. Re-read changed covered functions before trusting a pass.

- `verification/Lease.tla` covers `lease.go:reclaimable`, `extendLease`, `releaseLease`, and `sweep`. A sweep uses owner evidence at its eligibility decision. Explicit release may destroy a live resource. Destroy or Save failure retains the cleanup intent. An unverifiable surviving job group is saved as orphan-suspected. Only explicit `release --force --confirm` may forget that reservation before group absence is proved, with a warning.
- `verification/Admission.tla` covers `job.go:admitJob`, `budgetFits`, `pruneWaiters`, and `removeWaiter`, plus `store.go:openStore`, `Save`, and `Close`. A successful locked store transaction is one model step. This is valid only while read, prune, FIFO check, budget check, insertion, and Save share the same flock. Do not preserve this abstraction if the lock scope changes.
- `verification/JobSession.tla` covers `job.go:startJob`, `jobExec`, `childCompletion`, `processResource.Destroy`, `runJob`, and `finishJob`; `job_wait_linux.go` and `job_wait_darwin.go` supply unreaped completion observations. The parent arms the timer before starting the helper. Persist, parent deadline check, gate write, and helper deadline decision are separate steps. Time and owner-channel EOF can advance between them. The helper refuses an expired deadline with 124. `Exec` models the helper's last clock observation and exec decision together, not an atomic clock-check-and-kernel-exec operation.
- The same model covers `ssh.go:runAgent` and `runRemote`. Ready follows version validation, child output cannot create a result frame, delivered stdin EOF cancels the remote context, and a received result is authoritative during transport teardown.
- `owner.go:checkOwner` and `processIdentity` supply Alive, Dead, and Unknown evidence. `config.go:available` supplies a common capacity after owner room. The models assume these observations and parsed positive requests are valid. `formal_test.go` and existing tests execute the real functions at these boundaries.
- `cli.go` and `adapter.go` provide command routing and Destroy semantics. External adapter implementations are not proved. `releaseLease` trusts a successful Destroy.

Move the source commit forward only after confirming that these abstractions still describe the code and rerunning every check. Use `/maintain-formal-verification-skill` after changes that did not recheck this record.

## Interpret the properties

The independent safety promises and their harms are:

- `ForeignSafe` and `UnknownSafe`: a sweep does not destroy a foreign resource or an unexpired resource with uncertain ownership. A wrong decision deletes someone else's work.
- `FailureRetains` and `CleanupBeforeDelete`: failed cleanup keeps its retry record. Dropping it leaks resources and frees budget prematurely.
- `BudgetSafe`: admitted reservations fit both core and memory capacity. An over-admission makes jobs contend beyond the agreed budget.
- `NoBackfill`: a smaller request cannot bypass the first eligible waiter, including a new zero-wait request. Bypassing can starve large jobs.
- `DurableBeforeExec`: the helper cannot execute user code without a durable group identity. A premature exec leaves no safe cleanup target after a crash.
- `NoUnrecordedGroup`: automatic cleanup never drops a reservation while its workload group remains live. Missing or unverifiable leaders do not permit signaling. An orphan-suspected reservation remains until a later scan proves group absence. Explicit forced release is an operator override outside this property.
- `TimerBeforeGate` and `NoLateStart`: the parent arms its timer before opening the gate, and the helper rejects an already-expired deadline before deciding to exec. The OS can still deschedule it between the clock check and `exec` or before user instructions run.
- `CompletionWins`: the deadline branch preserves a child's already-observed completion code. Returning 124 instead hides the child's real result.
- `ReceivedResult`: receiving a result frame preserves the child's code, including 255, even if SSH teardown fails. Otherwise a completed job looks like an unknown remote result.
- `EOFEventuallyCancels`: after an accepted handshake and owner-channel EOF, the context eventually cancels or the agent dies, under weak scheduling fairness. A missed cancel leaves the remote job running after the caller disappears.

`NeverReclaimLive` means a sweep cannot reclaim a live owner before TTL expiry. `NoUnrecordedGroup` and `NoLateStart` now pass and run separately so one failure cannot hide another. CI requires these properties to pass and verifies that deliberately broken variants reproduce their invariant violations. The original findings and fix mapping are in `verification/findings.md`.

## Respect the bounds

- Lease exploration uses one lease, three liveness values, local or foreign ownership, and before or at expiry. Exact durations and token generation are abstracted.
- Admission explores every ordering for three distinct callers, all core and memory requests in `{1,2} x {1,2}`, capacity `{2,2}`, every combination of waiting and zero-wait callers, and queued expiry or death. An unexpired Unknown waiter is retained, not treated as dead. Jobs finish at most once. All invocations share a host, state directory, and capacity configuration.
- Job and session exploration uses one job, a boolean deadline boundary, matching or mismatching versions, five group-identity observations, and child codes 0, 37, and 255. Failed cleanup can retry. Weak fairness applies only to consuming delivered owner-channel EOF.

A pass covers these finite models, not arbitrary queue lengths or the Go binary. The checks do not prove crash durability during filesystem rollback, PID birth-token precision, operating-system signal delivery, external cleanup, escaped process groups, real-time scheduling bounds, or detection of a silent network partition. They do not guarantee eventual admission or cleanup if capacity or inspectability never returns. Different invocations using different `BERTH_CONFIG` capacities have no common budget invariant.

## Reject a broken change

`make formal` checks intentionally broken model variants and runs `verification/mutations.py`. The latter copies the Go package into temporary directories and changes real code only in those copies. It removes unknown-owner protection, locking, budget checks, FIFO gating, helper EOF protection, cleanup retention, completion probing, EOF cancellation, result preservation, orphan reservation retention, and the helper deadline check. Each variant must fail a behavior test, not compilation or source-text matching. The checkout stays unchanged.

For an unexpected trace, map each action to the named function and check the atomicity boundary first. If the code permits the sequence, record a finding before proposing a product fix. If the model invented a sequence, correct the abstraction and rerun the mutation checks. Never weaken a safety promise to make a failing code path green.
