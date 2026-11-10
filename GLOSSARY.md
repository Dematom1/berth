# Dibs

Dibs manages owner-and-expiry leases and cooperative machine-budget admission.
This language distinguishes a lease's authority from the resource or work it covers.

## Language

### Lease lifecycle

**Lease**:
A time-bounded claim associated with an owner and one authoritative machine, covering a managed resource, a job reservation, or metadata alone.
_Avoid_: resource, job, slot

**Lease ledger**:
The authoritative collection of recorded leases and admission waiters for cooperating invocations on a machine.
_Avoid_: cluster inventory, cross-host ledger

**Owner**:
The identifiable process, session holder, or heartbeat source whose liveness supports a lease.
An owner is distinct from the machine's interactive user and from the job's child process.
_Avoid_: user, child, resource

**Liveness**:
An observation of an owner's identity and continued presence, with outcomes alive, dead, or unknown.
Unknown means there is insufficient evidence to call the owner alive or dead, not that the lease has expired.
_Avoid_: health, readiness, expiry

**TTL**:
The positive lifetime used to set a lease's expiry, measured from creation or renewal and from admission for a job.
_Avoid_: heartbeat staleness, admission wait

**Expiry**:
The instant at which a lease becomes reclaimable regardless of owner liveness, on its authoritative machine.
_Avoid_: owner death, automatic deletion

**Heartbeat**:
A periodically refreshed indication that an owner remains present, with a freshness limit independent of the lease's expiry.
_Avoid_: renewal, lease extension

**Reclaim**:
Cleanup of a recorded lease's managed resource or reservation, followed by removal of its ledger record under the applicable cleanup rules.
Eligibility for reclaim is not evidence that cleanup has succeeded.
_Avoid_: forget, expire

**Verify-gone**:
Evidence that the resource identified by a lease is absent in the scope where that resource was created.
A successful delete request alone is not this evidence.
_Avoid_: deletion requested, owner dead

**Adapter**:
A resource-lifecycle integration that creates and cleans up the kind of resource named by a lease.
It is distinct from an owner-liveness integration and from the lease ledger's authority.
_Avoid_: owner, scheduler, machine

### Admission

**Admission**:
The decision that a waiting job may acquire a reservation within its machine's usable budget, in FIFO order without backfill.
_Avoid_: execution, placement, CPU enforcement

**Waiter**:
A job's pending admission request, with an owner and a wait deadline but no budget reservation yet.
_Avoid_: running job, lease reservation

**Budget**:
An amount of logical cores and memory used for cooperative admission accounting, as a capacity, a request, or a reservation.
A budget is not an operating-system resource limit.
_Avoid_: slot count, hard cap, resource description

**Owner room**:
The portion of machine capacity excluded from job admission to leave room for interactive work or other schedulers.
It is not a separate allocation for each lease owner.
_Avoid_: per-owner quota, free memory

**Reservation**:
The portion of usable machine budget accounted to an admitted job until its lease is released.
_Avoid_: waiter, measured usage, CPU limit

**Class**:
A named set of default core, memory, and timeout requests for a job, with independently overridable values.
A class does not establish admission priority or a separate queue.
_Avoid_: priority, pool, machine type

**Job**:
An invocation of a command associated with a budget reservation and a fixed lease deadline.
_Avoid_: lease, waiter, daemon

### Machines and sessions

**Machine**:
The host authoritative for a lease's owner checks, admission accounting, and cleanup.
A configured SSH machine name selects a route to that host, not a shared cross-host ledger.
_Avoid_: adapter, global worker pool

**Session**:
A connection lifetime represented by a local holder process whose identity supports owner liveness.
For SSH jobs, the holder is the remote agent, not a client PID or a Unix session ID.
_Avoid_: Unix SID, login shell, remote client process
