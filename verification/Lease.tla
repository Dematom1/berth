------------------------------ MODULE Lease ------------------------------
EXTENDS TLC
CONSTANT UnsafeUnknown
VARIABLES present, live, local, expired, resource, event, evidence
vars == <<present, live, local, expired, resource, event, evidence>>

\* lease.go: reclaimable. TTL expiry permits reclaim even when the owner is Alive.
Eligible == local /\ (live = "dead" \/ expired \/ UnsafeUnknown)
Init == /\ present = TRUE /\ resource = TRUE
        /\ live \in {"alive", "dead", "unknown"}
        /\ local \in BOOLEAN /\ expired \in BOOLEAN
        /\ event = "none" /\ evidence = <<"none", FALSE, FALSE>>

\* sweep holds Store's flock; evidence is the observation at its decision,
\* NOT a promise that the external owner cannot change after checkOwner.
Sweep == /\ present /\ Eligible
         /\ event' = "sweep" /\ present' = FALSE /\ resource' = FALSE
         /\ evidence' = <<live, local, expired>>
         /\ UNCHANGED <<live, local, expired>>
\* releaseLease: explicit release can destroy a live owner's resource.
Release == /\ present /\ local
           /\ present' = FALSE /\ resource' = FALSE /\ event' = "release"
           /\ UNCHANGED <<live, local, expired, evidence>>
\* Destroy failure retains intent. Save failure after Destroy also retains
\* intent, but the resource can already be absent. Retry is idempotent.
Failure == /\ present /\ local
           /\ resource' \in {resource, FALSE} /\ event' = "failure"
           /\ UNCHANGED <<present, live, local, expired, evidence>>
\* extendLease rejects jobs separately (covered by executable binding check).
Extend == /\ present /\ local /\ live = "alive" /\ ~expired
          /\ event' = "extend"
          /\ UNCHANGED <<present, live, local, expired, resource, evidence>>
Expire == /\ ~expired /\ expired' = TRUE
          /\ UNCHANGED <<present, live, local, resource, event, evidence>>
Next == Sweep \/ Release \/ Failure \/ Extend \/ Expire
Spec == Init /\ [][Next]_vars
ForeignSafe == event = "sweep" => evidence[2]
UnknownSafe == event = "sweep" /\ evidence[1] = "unknown" => evidence[3]
FailureRetains == event = "failure" => present
NeverReclaimLive == event = "sweep" => (evidence[1] # "alive" \/ evidence[3])
=============================================================================
