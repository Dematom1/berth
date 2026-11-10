---------------------------- MODULE JobSession ----------------------------
EXTENDS Integers, TLC
CONSTANTS SkipDurableGate, DropOnFailure, OverwriteResult, WrongDeadline, IgnoreEOF
VARIABLE s
vars == <<s>>

\* One remote job. Client's stdin is an owner-liveness channel, NOT child
\* stdin. Child output is an output frame; it never becomes a result frame.
Init == s \in [phase: {"request"}, match: BOOLEAN, connected: {TRUE},
                canceled: {FALSE}, parent: {TRUE}, expired: {FALSE},
                durable: {FALSE}, gate: {FALSE}, group: {FALSE},
                identity: {"same"}, completed: {FALSE}, cleaned: {FALSE},
                lostGroup: {FALSE}, lateStart: {FALSE}, premature: {FALSE},
                code: {0, 37, 255}, selected: {-1}, frame: {-1}, client: {-1}]

\* ssh.go: runAgent validates version before ready, then calls runJob.
Handshake == /\ s.phase = "request"
             /\ s' = [s EXCEPT !.phase = IF s.match THEN "ready" ELSE "refused"]
Reserve == /\ s.phase = "ready" /\ ~s.canceled
           /\ s' = [s EXCEPT !.phase = "reserved"]
\* job.go: startJob creates helper waiting on fd 3, persists group identity,
\* checks ctx/deadline, THEN writes gate byte. These are separate steps:
\* flock does not stop real time advancing or the SSH reader canceling ctx.
Helper == /\ s.phase = "reserved" /\ s.parent /\ ~s.canceled /\ ~s.expired
          /\ s' = [s EXCEPT !.phase = "helper"]
Persist == /\ s.phase = "helper" /\ s.parent
           /\ s' = [s EXCEPT !.durable = TRUE, !.phase = "durable"]
CheckStart == /\ (s.phase = "durable" \/ (SkipDurableGate /\ s.phase = "helper"))
              /\ s.parent /\ ~s.expired /\ ~s.canceled
              /\ s' = [s EXCEPT !.phase = "checked"]
OpenGate == /\ s.phase = "checked" /\ s.parent
            /\ s' = [s EXCEPT !.gate = TRUE, !.phase = "running", !.group = TRUE,
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
\* processResource.Destroy: unknown/reused identities fail without signaling;
\* missing leader currently returns nil WITHOUT inspecting/killing group (F2).
Cleanup == /\ s.phase = "stopping"
           /\ (IF s.identity = "missing"
               THEN s' = [s EXCEPT !.cleaned = TRUE, !.phase = "stopped"]
               ELSE IF s.identity \in {"same", "zombie"}
                    THEN s' = [s EXCEPT !.group = FALSE, !.cleaned = TRUE,
                               !.phase = "stopped"]
                    ELSE s' = [s EXCEPT !.phase = "failed"])
\* A failed cleanup leaves the reservation. A retry can later succeed if
\* inspectability returns. Successful Save is the Delete action; failure is
\* stuttering with intent retained (releaseLease / finishJob).
Retry == /\ s.phase = "failed"
         /\ s' = [s EXCEPT !.phase = "stopping", !.identity = "same"]
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
Next == Handshake \/ Reserve \/ Helper \/ Persist \/ CheckStart \/ OpenGate
        \/ ParentDies \/ HelperEOF \/ Expire \/ Disconnect \/ ReadEOF \/ Exit
        \/ Reap \/ IdentityLost \/ Observe \/ Sweep \/ Cleanup \/ Retry \/ Delete
        \/ Result \/ Receive \/ TransportFailure
Spec == Init /\ [][Next]_vars /\ WF_vars(ReadEOF)
EOFEventuallyCancels == (s.match /\ s.phase # "request" /\ ~s.connected)
                        ~> (s.canceled \/ ~s.parent)
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
