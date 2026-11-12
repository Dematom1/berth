------------------------------ MODULE Lease ------------------------------
EXTENDS TLC
CONSTANT UnsafeUnknown, UnsafeProtected
VARIABLES present, live, local, expired, protected, resource, event, evidence
vars == <<present, live, local, expired, protected, resource, event, evidence>>

\* internal/lease/lease.go: reclaimable. TTL expiry permits reclaim even when the owner is Alive,
\* unless the lease opted into live-owner protection: then only proven death reclaims.
Eligible == local /\ (live = "dead" \/ ((~protected \/ UnsafeProtected) /\ expired) \/ UnsafeUnknown)
Init == /\ present = TRUE /\ resource = TRUE
        /\ live \in {"alive", "dead", "unknown"}
        /\ local \in BOOLEAN /\ expired \in BOOLEAN /\ protected \in BOOLEAN
        /\ event = "none" /\ evidence = <<"none", FALSE, FALSE, FALSE>>

\* sweep holds Store's flock; evidence is the observation at its decision,
\* NOT a promise that the external owner cannot change after checkOwner.
Sweep == /\ present /\ Eligible
         /\ event' = "sweep" /\ present' = FALSE /\ resource' = FALSE
         /\ evidence' = <<live, local, expired, protected>>
         /\ UNCHANGED <<live, local, expired, protected>>
\* releaseLease: explicit release can destroy a live owner's resource, protected or not.
Release == /\ present /\ local
           /\ present' = FALSE /\ resource' = FALSE /\ event' = "release"
           /\ UNCHANGED <<live, local, expired, protected, evidence>>
\* Destroy failure retains intent. Save failure after Destroy also retains
\* intent, but the resource can already be absent. Retry is idempotent.
Failure == /\ present /\ local
           /\ resource' \in {resource, FALSE} /\ event' = "failure"
           /\ UNCHANGED <<present, live, local, expired, protected, evidence>>
\* extendLease rejects jobs separately (covered by executable binding check).
\* A protected lease with a live owner reattaches after expiry only to a present resource.
Extend == /\ present /\ local /\ live = "alive"
          /\ (~expired \/ (protected /\ resource))
          /\ expired' = FALSE /\ event' = "extend"
          /\ UNCHANGED <<present, live, local, protected, resource, evidence>>
Expire == /\ ~expired /\ expired' = TRUE
          /\ UNCHANGED <<present, live, local, protected, resource, event, evidence>>
Next == Sweep \/ Release \/ Failure \/ Extend \/ Expire
Spec == Init /\ [][Next]_vars
ForeignSafe == event = "sweep" => evidence[2]
UnknownSafe == event = "sweep" /\ evidence[1] = "unknown" => evidence[3]
FailureRetains == event = "failure" => present
NeverReclaimLive == event = "sweep" => (evidence[1] # "alive" \/ evidence[3])
\* A protected lease is swept only on proven owner death, expired or not.
ProtectedSafe == event = "sweep" /\ evidence[4] => evidence[1] = "dead"
=============================================================================
