# Formal verification findings (no product fixes)

Source reviewed: `17c0265225197abd1010bec959eed501be5cc02f`.
Tool: TLA+ TLC 1.7.4 (tla2tools release), bounded exhaustive exploration.
Run `make formal` to generate full machine traces `F1-live-expiry.log`, `F2-orphan-group.log`, and `F3-late-start.log` in the printed log directory. CI publishes them in the `formal-counterexamples` artifact.

## F1: live owners are reclaimable at expiry (confirmed policy conflict)

Property: a sweep never reclaims a lease whose owner was observed Alive.
Harm: a confirmed sweep can destroy a still-used Compose/kind/k3d resource.
Code: `lease.go:reclaimable` (return `live == Dead || !now.Before(l.Expires)`), `sweep`, `releaseLease`.
Trace: local lease exists; owner Alive; TTL expired -> `Sweep` -> resource destroyed, lease removed, owner still Alive.
Existing real-code tests already confirm this behavior: `TestReclaimAndExtend/expired_live_owner`, `TestSweepDecisions`. This is the current documented TTL policy, not a newly introduced regression. It conflicts with the requested stronger "never reclaim a live owner" property. A policy decision is needed before a fix; this PR does not change it.

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

These are finite-model results, not a proof of the Go binary, arbitrary queue lengths, crash durability, kernel process identity, real network partition detection, external adapters, or heterogeneous BERTH_CONFIG capacities. Real-code bindings and deliberately broken model/code variants guard drift. Known failing properties remain failing; default CI reproduces them explicitly, while `python3 verification/check.py --strict` exits nonzero.
