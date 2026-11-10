---------------------------- MODULE Admission ----------------------------
EXTENDS Integers, Sequences, FiniteSets, TLC
CONSTANTS BypassFIFO, IgnoreBudget
Callers == 1..3
Requests == {<<1,1>>, <<1,2>>, <<2,1>>, <<2,2>>}
VARIABLES request, waiting, active, done, queue, expired, dead, lock, badFIFO
vars == <<request, waiting, active, done, queue, expired, dead, lock, badFIFO>>

\* job.go: budgetFits subtracts both dimensions from available owner-room
\* capacity. Units are abstract positive tokens, capacity is <<2,2>>.
RECURSIVE Sum(_, _)
Sum(s, d) == IF s = {} THEN 0 ELSE
             LET i == CHOOSE x \in s: TRUE IN request[i][d] + Sum(s \ {i}, d)
Fits(i) == /\ Sum(active, 1) + request[i][1] <= 2
           /\ Sum(active, 2) + request[i][2] <= 2
Range(q) == {q[n]: n \in 1..Len(q)}
Filter(q, removed) == SelectSeq(q, LAMBDA i: i \notin removed)
Pruned == Filter(queue, expired \cup dead)
Init == /\ request \in [Callers -> Requests]
        /\ waiting \in SUBSET Callers \* wait>0 vs wait=0
        /\ active = {} /\ done = {} /\ queue = <<>>
        /\ expired = {} /\ dead = {} /\ lock = 0 /\ badFIFO = FALSE

\* store.go: openStore holds one host/state-dir flock until Close. Other
\* invocations cannot observe local append/prune/remove/insertion until Save.
\* A successful critical section is therefore one state transition. Save
\* failure is a stutter (old durable state, no child may start). No crashes
\* during filesystem rollback are modeled. All callers use SAME capacity.
Acquire(i) == /\ lock = 0 /\ i \notin done /\ i \notin active
              /\ lock' = i
              /\ UNCHANGED <<request, waiting, active, done, queue, expired, dead, badFIFO>>
Turn(i) ==
  /\ lock = i
  /\ LET q == IF i \in waiting /\ i \notin Range(Pruned)
                 /\ i \notin expired /\ i \notin dead
              THEN Append(Pruned, i) ELSE Pruned
         first == Len(q) = 0 \/ (i \in waiting /\ Head(q) = i)
         admit == i \notin expired /\ i \notin dead
                  /\ (first \/ BypassFIFO) /\ (Fits(i) \/ IgnoreBudget)
     IN /\ active' = IF admit THEN active \cup {i} ELSE active
        /\ queue' = IF admit THEN Filter(q, {i}) ELSE q
        /\ done' = IF admit \/ i \notin waiting \/ i \in expired \cup dead
                   THEN done \cup {i} ELSE done
        /\ badFIFO' = (badFIFO \/ (admit /\ ~first))
  /\ lock' = 0
  /\ UNCHANGED <<request, waiting, expired, dead>>
\* releaseLease/finishJob also take the same lock. Capacity is released only
\* after Destroy succeeds. Failed cleanup is a stutter, retaining reservation.
Finish(i) == /\ lock = 0 /\ i \in active
             /\ active' = active \ {i} /\ done' = done \cup {i}
             /\ UNCHANGED <<request, waiting, queue, expired, dead, lock, badFIFO>>
\* Expiration/death may occur outside flock. Unknown owners are NOT dead;
\* they remain in Pruned unless their wait deadline expired (pruneWaiters).
Expire(i) == /\ i \in Range(queue) /\ i \notin expired
             /\ expired' = expired \cup {i}
             /\ UNCHANGED <<request, waiting, active, done, queue, dead, lock, badFIFO>>
Die(i) == /\ i \in Range(queue) /\ i \notin dead
          /\ dead' = dead \cup {i}
          /\ UNCHANGED <<request, waiting, active, done, queue, expired, lock, badFIFO>>
Next == \E i \in Callers: Acquire(i) \/ Turn(i) \/ Finish(i) \/ Expire(i) \/ Die(i)
Spec == Init /\ [][Next]_vars
BudgetSafe == Sum(active, 1) <= 2 /\ Sum(active, 2) <= 2
NoBackfill == ~badFIFO
QueueUnique == Len(queue) = Cardinality(Range(queue))
=============================================================================
