# Formal verification findings and fixes

The original review covered `17c0265225197abd1010bec959eed501be5cc02f` with
TLA+ TLC 1.7.4. The counterexamples below described that source, not measured
production incidents. The follow-up fixes retain the TTL-wins policy and make
F2 and F3 passing checks. `make formal` requires all bounded properties to pass
and reproduces each former failure with deliberately broken variants.

## F1: TTL wins, including for live owners

R-B3 says: "A lease is reclaimable when its owner is dead or when it has
expired. A live owner that misses either cadence loses its resources."

The original stronger property, never reclaim an Alive owner even after
expiry, conflicted with this requirement. The accepted property is that a sweep
never reclaims an unexpired live-owned lease. `lease.go:reclaimable` still
implements owner death OR expiry. No policy change was made.

`cli.go:leaseView` exposes `expiring_soon` for live-owned leases in their last
third of TTL. The warning says to extend before reclamation. Jobs instead warn
that their deadlines cannot be extended. README and `extend --help` explain
TTL-wins. `TestLeaseExpiryWarnings`, `TestReclaimAndExtend`, and
`TestCLIExpiredLiveOwnerAndFailure` bind the warning boundary and policy to Go.

## F2: retain reservations for unverifiable surviving groups

Original counterexample: reserve, start with durable identity, parent dies,
leader exits, OS reaps leader, sweep forgets the lease while descendants survive.
Avoiding a signal to a potentially reused PGID was safe. Forgetting its budget
reservation was not.

`job.go:processResource.Destroy` now scans for live members of the recorded
process group without signaling when the leader is missing or unverifiable.
A failed scan also retains the reservation. `lease.go:releaseLease` saves
`orphan_suspected` so `ps` shows it and later admissions still count the budget.
Subsequent cleanup never signals a marked group. The record is removed only
when an inspection proves group absence, or an explicit
`release --force --confirm` forgets it with a warning about budget overcommit.
Force never signals an unverifiable group and does not bypass foreign-host or
other adapter failures.

`NoUnrecordedGroup` now passes for automatic cleanup. Explicit forced release
is an intentional operator override outside that property. `ForgetOrphan`
restores the old behavior and must fail. Real-code tests include
`TestDestroyDoesNotSignalGroupAfterLeaderReaped`, with normal and forced
release, `TestUnverifiableGroupScanRetainsReservation`, and reused-leader
safety checks. The orphan mutation must fail the real-process regression.

## F3: check the absolute deadline in the helper

Original counterexample: parent checks the deadline, is descheduled past it,
then opens the gate. The helper used to exec without a deadline check, before
the parent installed its timer.

`runJob` now arms the timer before `startJob` can open the gate. `startJob`
passes the absolute deadline to `jobExec`. After reading the gate, the helper
refuses to exec at or after that deadline and exits 124. An already-expired
helper deadline is tested directly by `TestJobHelperRefusesExpiredDeadline`.
Malformed deadlines and gate EOF still refuse execution.

`TimerBeforeGate` and `NoLateStart` now pass. The latter describes the helper's
final clock observation and exec decision. It does not claim an atomic
wall-clock check plus kernel exec: the OS can deschedule the helper between
those instructions or delay the user's first instruction. `IgnoreHelperDeadline`
and the real-code late-start mutation must both fail their checks.

## Passing scope and limits

Unknown owners are retained before expiry. Foreign leases cannot be swept.
Failed cleanup retains intent. FIFO admission has no backfill and both budget
dimensions fit a common capacity under one Store flock. Helper exec requires
durable identity. Completion probing preserves an already-completed child's
status. Authoritative SSH results survive teardown. Delivered owner-channel
EOF eventually cancels under weak scheduling fairness.

These are finite-model results, not proof of the Go binary, arbitrary queue
lengths, crash durability, kernel process identity, real network partition
detection, external adapters, or heterogeneous `BERTH_CONFIG` capacities.
Real-code bindings and deliberately broken model and code variants guard
mapping drift. `python3 verification/check.py --strict` and the default command
now have the same passing contract.
