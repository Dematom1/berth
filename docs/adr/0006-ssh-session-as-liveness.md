---
status: accepted
---

# SSH session lifetime supplies owner liveness

A remote job belongs to the remote berth agent's `session:<holder-pid>` identity, not to a client PID that the remote cannot verify. One agent runs per SSH session without a resident daemon. The remote machine owns its ledger, budget, classes, deadlines, and cleanup, so a client does not impose its local defaults or act on foreign-machine records.

SSH stdin is reserved for session liveness, and remote child stdin is `/dev/null`. EOF or session cancellation cancels a queued or running job and attempts cleanup under the existing process rules. Idle local stdin does not end the session. Lost-network detection on the server depends on sshd's keepalive policy, not only client keepalives. Remote deadlines and a host sweep timer remain necessary for agent crashes and failed cleanup.

The same binary implements a private, version-checked framed protocol that separates arbitrary child output bytes from the final result. A child exit of 255 remains a child result. Once SSH has started, a transport or protocol failure without an authoritative result returns 76 and leaves the remote outcome unknown. An authoritative result takes precedence over a later transport teardown failure.

This design provides liveness and cleanup coordination, not isolation for untrusted code. Worktree transfer and Kubernetes-backed machines remain deferred.

Evidence: [SSH transport and agent](../../ssh.go), [session owners](../../owner.go), [remote-placement change](https://github.com/Dematom1/berth/pull/3), and [SSH operations and limitations](../../README.md#run-jobs-over-ssh).
