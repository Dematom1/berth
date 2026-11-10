# Formal verification findings

Source reviewed: `17c0265225197abd1010bec959eed501be5cc02f`.
Tool: TLA+ TLC 1.7.4 (tla2tools release), bounded exhaustive exploration.
Run `just formal` to generate full machine traces `F1-live-expiry.log`, `F2-orphan-group.log`, and `F3-late-start.log` in the printed log directory. CI publishes them in the `formal-counterexamples` artifact.

## F1: resolved by policy

Authorized policy clarification R-B3: TTL expiry permits reclaim even when the owner is Alive. The verified safety property is therefore narrower: never reclaim a live owner before expiry. `NeverReclaimLive` checks that boundary. Real-code tests `TestReclaimAndExtend/expired_live_owner` and `TestSweepDecisions` retain coverage of the documented expiry behavior. No product-code change was needed.

## F2: reaped leader permits forgetting a surviving group (confirmed limitation)

Property: successful cleanup does not delete the only reservation while its workload group still lives.
Harm: an orphan consumes CPU/memory after the reservation is deleted; subsequent admissions can exceed the intended workload budget even though reservation arithmetic is correct.
Code: `job.go:processResource.Destroy`, `live == Dead && identity == ""` branch; `releaseLease` then deletes and saves the lease.
Trace: reserve -> start gated child with durable identity -> parent dies -> group leader exits but descendant survives -> OS reaps leader -> sweep -> Destroy returns nil without signaling unverifiable group -> lease removed, descendant/group still alive.
The existing real-process test `TestDestroyDoesNotSignalGroupAfterLeaderReaped` explicitly demonstrates both durable lease deletion and a surviving group. Avoiding a signal to a potentially reused PGID is deliberate and safe; forgetting the reservation is the budget/cleanup limitation. No fix here.

## F3: final deadline check and gate write are not atomic (model counterexample)

Property: user code never starts after its absolute job deadline.
Harm: under descheduling, a job can start after timeout and briefly execute side effects before the timer cleanup branch runs.
Code: `job.go:startJob`, final `!time.Now().Before(current.Expires)` check, then creation of observation goroutine and `start.Write([]byte{1})`; `jobExec` checks only gate byte, not deadline. `runJob` starts its timer after `startJob` returns.
Trace: reserve -> helper waits -> persist group identity -> final deadline check succeeds -> parent descheduled, deadline expires -> parent resumes and writes gate -> helper may exec user code -> timer/cleanup is only installed afterward.
This is a faithful split at real instructions, not a reproduced timing failure against an unmodified binary. Treat it as a hard-deadline design limitation needing review, not evidence of a measured timing incident. A hard wall-clock guarantee across exec/scheduling needs a precisely stated contract. No fix here.

## Passing scope

Unknown owners are retained before expiry; foreign leases cannot be swept; failed cleanup retains intent; FIFO admission has no backfill and both reservation dimensions stay within a common capacity under one Store flock; helper exec requires durable identity; completion probe preserves an already-completed child's status; authoritative SSH result survives teardown; delivered owner-channel EOF eventually cancels under weak scheduling fairness.

These are finite-model results, not a proof of the Go binary, arbitrary queue lengths, crash durability, kernel process identity, real network partition detection, external adapters, or heterogeneous DIBS_CONFIG capacities. Real-code bindings and deliberately broken model/code variants guard drift. Known failing properties remain failing; default CI reproduces them explicitly, while `python3 verification/check.py --strict` exits nonzero.
