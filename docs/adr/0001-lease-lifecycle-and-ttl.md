---
status: accepted
---

# Lease lifecycle and TTL-wins

A lease is durable cleanup intent, saved before managed resource creation so interrupted or failed creation remains reclaimable. On the lease's machine, owner death **or** expiry permits reclaim: expiry wins even when the owner is alive or unknown, while unknown liveness alone does not permit reclaim. This hard backstop prevents a stale ownership signal from keeping resources indefinitely, without treating a failed owner check as proof of death.

R-B3 requires touching a heartbeat file at most every staleness/3 and extending a lease at the latest when one third of its TTL remains. Heartbeat refresh does not extend expiry. `extend --ttl D` sets expiry to now plus D only for an unexpired lease with a verified live owner on its own machine. It cannot resurrect an expired lease or extend a job deadline.

Expiry marks eligibility, not immediate deletion. Sweep runs only when invoked, commonly by a host timer. Explicit confirmed release does not require owner death or expiry. Foreign-machine leases are never reclaimed locally, even after expiry. Cleanup failure keeps the record for retry, as detailed in [reclaim with proof](0002-reclaim-with-proof.md).

Evidence: [lease decisions](../../lease.go), [durable creation](../../cli.go), [R-B3 in the original lease change](https://github.com/Dematom1/dibs/pull/1), and [renewal guidance](../../README.md#owners-and-renewal).
