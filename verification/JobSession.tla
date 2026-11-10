---------------------------- MODULE JobSession ----------------------------
EXTENDS Integers, TLC
CONSTANTS SkipDurableGate, DropOnFailure, OverwriteResult, WrongDeadline, IgnoreEOF,
          ForgetOrphan, IgnoreHelperDeadline
VARIABLE s
vars == <<s>>

\* One remote job. Client's stdin is an owner-liveness channel, NOT child
\* stdin. Child output is an output frame; it never becomes a result frame.
Init == s \in [phase: {"request"}, match: BOOLEAN, connected: {TRUE},
                canceled: {FALSE}, parent: {TRUE}, expired: {FALSE},
                durable: {FALSE}, gate: {FALSE}, group: {FALSE}, timer: {FALSE},
                orphan: {FALSE},
                identity: {"same"}, completed: {FALSE}, cleaned: {FALSE},
                lostGroup: {FALSE}, lateStart: {FALSE}, premature: {FALSE},
                code: {0, 37, 255}, selected: {-1}, frame: {-1}, client: {-1}]

\* ssh.go: runAgent validates version before ready, then calls runJob.
Handshake == /\ s.phase = "request"
             /\ s' = [s EXCEPT !.phase = IF s.match THEN "ready" ELSE "refused"]
Reserve == /\ s.phase = "ready" /\ ~s.canceled
           /\ s' = [s EXCEPT !.phase = "reserved"]
\* runJob arms the deadline timer BEFORE startJob can write the gate. The
\* helper receives the absolute deadline and checks it after reading fd 3.
\* Parent check, gate write and helper decision remain separate steps.
ArmTimer == /\ s.phase = "reserved" /\ s.parent /\ ~s.canceled
            /\ s' = [s EXCEPT !.timer = TRUE, !.phase = "armed"]
Helper == /\ s.phase = "armed" /\ s.parent /\ ~s.canceled /\ ~s.expired
          /\ s' = [s EXCEPT !.phase = "helper"]
Persist == /\ s.phase = "helper" /\ s.parent
           /\ s' = [s EXCEPT !.durable = TRUE, !.phase = "durable"]
CheckStart == /\ (s.phase = "durable" \/ (SkipDurableGate /\ s.phase = "helper"))
              /\ s.parent /\ ~s.expired /\ ~s.canceled
              /\ s' = [s EXCEPT !.phase = "checked"]
OpenGate == /\ s.phase = "checked" /\ s.parent
            /\ s' = [s EXCEPT !.gate = TRUE, !.phase = "gated"]
\* This step models the helper's final wall-clock observation and exec
\* decision, not a hard-real-time guarantee about subsequent CPU scheduling.
Exec == /\ s.phase = "gated"
        /\ IF s.expired /\ ~IgnoreHelperDeadline
           THEN s' = [s EXCEPT !.phase = "stopped", !.cleaned = TRUE,
                              !.selected = 124]
           ELSE s' = [s EXCEPT !.phase = "running", !.group = TRUE,
                              !.premature = ~s.durable, !.lateStart = s.expired]
ParentDies == /\ s.parent /\ s.phase \notin {"request", "refused", "result"}
              /\ s' = [s EXCEPT !.parent = FALSE]
HelperEOF == /\ ~s.parent /\ ~s.gate /\ s.phase \in {"helper", "durable", "checked"}
             /\ s' = [s EXCEPT !.phase = "stopped", !.cleaned = TRUE]
Expire == /\ ~s.expired /\ s' = [s EXCEPT !.expired = TRUE]
\* EOF delivery is not instantaneous detection of a silent network partition.
Disconnect == /\ s.connected /\ s' = [s EXCEPT !.connected = FALSE]
ReadEOF == /\ ~IgnoreEOF /\ s.match /\ s.phase # "request"
           /\ s.parent /\ ~s.connected /\ ~s.canceled
           /\ s' = [s EXCEPT !.canceled = TRUE]
\* Child may exit while descendants survive. Parent normally retains the
\* unreaped zombie as the process-group identity (waitChildExit/childExited).
Exit == /\ s.phase = "running" /\ ~s.completed
        /\ s' = [s EXCEPT !.completed = TRUE, !.identity = "zombie"]
\* After parent death the OS can reap the leader while descendants remain.
Reap == /\ ~s.parent /\ s.completed /\ s.identity = "zombie"
        /\ s' = [s EXCEPT !.identity = "missing"]
IdentityLost == /\ s.phase = "running" /\ s.identity \in {"same", "zombie"}
                /\ \E id \in {"unknown", "reused"}: s' = [s EXCEPT !.identity = id]
\* runJob's timer probes completion before choosing 124; an already exited
\* child retains its exit code even when the timer branch wins the select.
Observe == /\ s.parent /\ s.phase = "running"
           /\ (s.completed \/ s.expired \/ s.canceled)
           /\ s' = [s EXCEPT !.phase = "stopping",
                    !.selected = IF s.completed /\ ~WrongDeadline THEN s.code
                                 ELSE IF s.expired THEN 124 ELSE 143]
Sweep == /\ ~s.parent /\ s.phase = "running"
         /\ s' = [s EXCEPT !.phase = "stopping"]
\* processResource.Destroy never signals an unverifiable/orphan-suspected
\* group. releaseLease saves orphan status and keeps its budget until ps
\* proves absence. ForgetOrphan restores the old unsafe missing-leader path.
Cleanup == /\ s.phase = "stopping"
           /\ (IF ForgetOrphan /\ s.identity = "missing"
               THEN s' = [s EXCEPT !.cleaned = TRUE, !.phase = "stopped"]
               ELSE IF ~s.orphan /\ s.identity \in {"same", "zombie"}
                    THEN s' = [s EXCEPT !.group = FALSE, !.cleaned = TRUE,
                               !.phase = "stopped"]
                    ELSE IF ~s.group
                         THEN s' = [s EXCEPT !.cleaned = TRUE, !.phase = "stopped"]
                         ELSE s' = [s EXCEPT !.phase = "failed", !.orphan = TRUE])
\* Natural exit, not a signal from Berth; an uncertain scan is stuttering.
GroupGone == /\ s.orphan /\ s.group /\ s' = [s EXCEPT !.group = FALSE]
\* A failed cleanup leaves the reservation. A retry can later succeed if
\* inspectability returns. Successful Save is the Delete action; failure is
\* stuttering with intent retained (releaseLease / finishJob).
Retry == /\ s.phase = "failed"
         /\ s' = [s EXCEPT !.phase = "stopping"]
Delete == /\ (s.phase = "stopped" \/ (DropOnFailure /\ s.phase = "failed"))
          /\ s' = [s EXCEPT !.phase = "deleted", !.lostGroup = s.group]
\* runAgent emits result only AFTER runJob cleanup/retention; client trusts
\* the result frame even when SSH exits 255 during transport teardown.
Result == /\ s.parent /\ s.phase \in {"deleted", "failed"} /\ s.selected # -1
          /\ s' = [s EXCEPT !.phase = "result", !.frame = s.selected]
Receive == /\ s.phase = "result" /\ s.client = -1
           /\ s' = [s EXCEPT !.client = IF OverwriteResult THEN 76 ELSE s.frame]
TransportFailure == /\ s.phase # "result" /\ ~s.connected /\ s.client = -1
                    /\ s' = [s EXCEPT !.client = 76]
Next == Handshake \/ Reserve \/ ArmTimer \/ Helper \/ Persist \/ CheckStart \/ OpenGate \/ Exec
        \/ ParentDies \/ HelperEOF \/ Expire \/ Disconnect \/ ReadEOF \/ Exit
        \/ Reap \/ IdentityLost \/ Observe \/ Sweep \/ Cleanup \/ GroupGone \/ Retry \/ Delete
        \/ Result \/ Receive \/ TransportFailure
Spec == Init /\ [][Next]_vars /\ WF_vars(ReadEOF)
EOFEventuallyCancels == (s.match /\ s.phase # "request" /\ ~s.connected)
                        ~> (s.canceled \/ ~s.parent)
TimerBeforeGate == s.gate => s.timer
DurableBeforeExec == ~s.premature
FailureRetains == s.phase = "failed" => ~s.cleaned
CleanupBeforeDelete == s.phase = "deleted" => s.cleaned
CompletionWins == s.selected = 124 => ~s.completed
\* A prior transport error is not receipt of a result. 76 is not one of the
\* modeled child's codes, including the otherwise ambiguous child exit 255.
ReceivedResult == s.phase = "result" /\ s.client # -1 /\ s.connected
                  => s.client = s.frame
NoUnrecordedGroup == ~s.lostGroup
NoLateStart == ~s.lateStart
=============================================================================
