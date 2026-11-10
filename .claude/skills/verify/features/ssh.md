# SSH machines

A user installs a matching agent binary and runs jobs on a configured SSH machine. The remote machine owns admission and cleanup; a broken transport reports an unknown result rather than a false child result.

## Sub-features

- `machine-install`: version-checked atomic installation and refusal of incompatible upgrades.
- `remote-job`: quoting, binary stdout, remote workdir, no child stdin, and child exit codes including 255.
- `remote-budget`: remote classes, owner room, deadline 124, oversized 2, and wait 75.
- `session-cleanup`: disconnect removes waiters, kills descendants, and releases reservations without sweep.
- `transport-76`: transport loss after child start returns 76, distinct from a known child exit.

## How to get to it (user POV)

Configure `machines.<name>` with host, user, workdir, agent, port, and identity_file. Run `dibs machine install <name> --file <linux-binary>`, `dibs job --machine <name> -- <command...>`, `dibs ps --machine <name>`, and `dibs sweep --machine <name> --confirm`.

## Driving it with verify.py

Preconditions: Docker, Go, SSH, and ssh-keygen are available. Docker can fetch `alpine:3.23`; the container can install OpenSSH and procps.

- **Run:** `.claude/skills/verify/verify.py ssh --evidence "$PROOF/ssh"`. This runs the existing real `make ssh-integration` harness, not an SSH mock.
- **Install:** build local and Linux binaries, start an isolated sshd with fixture keys and strict private host trust, and run `machine install fixture --file <linux-binary>`. An incompatible upgrade must fail while the previous executable still runs.
- **Results:** run remote children with codes 0, 1, 2, 37, 75, 76, 124, and 255. Require exact passthrough. Observe quoted argv and workdir, binary output bytes, and empty child stdin.
- **Admission:** a two-core job must succeed under the remote budget despite a smaller local budget. Three cores must fail with 2. Explicit and class deadlines must return 124. Saturated admission must return 75.
- **Session:** list an active `session:<pid>` lease, queue another client, kill only that queued client, and observe the waiter disappear. Sweep must preserve the live session. Kill the holder client, then require descendant death, zero remote leases, and a successful full-budget replacement job.
- **Transport:** after a child-start marker, kill the SSH transport PID owned by that client. Require 76 and eventual remote cleanup. A missing agent also returns 76.
- **Proof:** verbose test output inside `events.jsonl` records commands, responses, and exit codes. Require every named subtest to pass and the helper's final cleanup result to be true. The local state path must remain absent during remote invocations.

## Gotchas

- The fixture is a stand-in for a configured remote machine, not a verified Lenovo deployment. Read the skill's list of provisioning differences before making rollout claims.
- Code 76 means the result is unknown, not that user code never started. The test checks this after a real child starts.
- Normal SSH host-key verification stays enabled. Never use `StrictHostKeyChecking=no` or write fixture trust into the user's known_hosts.
- The fixture uses server-side keepalives. Client-only keepalives cannot bound server-side detection of a lost client.
