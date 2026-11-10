---
status: accepted
---

# FIFO admission within budget minus owner room

Job admission reserves logical cores and memory from machine totals minus owner room, rather than counting fixed slots or measuring current utilization. Owner room leaves capacity outside berth's job accounting for interactive work or other schedulers. These reservations coordinate cooperating invocations, not operating-system limits, and metadata resource descriptions do not consume budget.

Admission is FIFO across classes with no backfill. Queue order is lock-acquisition order, not shell launch order. A smaller request cannot bypass a larger earlier waiter, accepting head-of-line blocking in exchange for that ordering guarantee. A class supplies defaults for cores, memory, and timeout, not priority. Each value can be overridden independently.

The wait deadline is distinct from the job deadline. Dead or expired local waiters are pruned, while unknown liveness alone keeps an unexpired waiter. Admission attempts to reclaim eligible local job leases before counting reservations, but failed cleanup retains their budget. Successful admission commits the reservation and starts the job deadline. The lock is released between polls and while user code runs.

A request larger than usable budget fails before starting a child. An exhausted admission wait returns 75, and `--wait 0` makes one admission attempt without joining the queue or bypassing existing waiters. Each machine applies its own config and ledger. Config changes affect new requests, not the budget or deadline already recorded for running jobs.

Evidence: [budget and class definitions](../../internal/config/config.go), [queue and admission](../../internal/lease/job_admission.go), [R-B4 and local-job rationale](https://github.com/Dematom1/berth/pull/2), and [budget configuration](../../README.md#machine-budget-and-classes).
