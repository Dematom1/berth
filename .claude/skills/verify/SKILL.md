---
name: verify
description: Drive dibs's real CLI to verify leases, sweep, heartbeat owners, FIFO jobs, Docker Compose, kind, k3d, and SSH machines. Use after behavior changes or to reproduce a demo failure.
---

# Verify dibs

Read [the feature map](features/README.md), then select a flow. This is behavioral verification, not a formal proof.

## Launch

Run from the checkout root with Go 1.26+, Python 3, and a POSIX shell:

```sh
PROOF=$(mktemp -d "${TMPDIR:-/tmp}/dibs-proof.XXXXXX")
.claude/skills/verify/verify.py local --evidence "$PROOF/local"
```

The helper builds the checkout into a fresh private scratch directory. A successful `--version` and empty `ps --json` establish readiness. Each short-lived CLI invocation is noninteractive; no server or PTY is needed. Every run owns its state, config, children, and kubeconfig. Do not point it at existing leases. Do not use Python's `-O` option: assertions are the checks.

For external flows, install Docker with Compose, kind, k3d, kubectl, OpenSSH, and ssh-keygen. Start Docker yourself if it is not running. On macOS, `brew install kind k3d kubectl` installs the cluster clients. On Linux, follow the pinned kind and k3d installation steps in `.github/workflows/ci.yml`; GitHub-hosted Ubuntu supplies Docker, Compose, SSH, and kubectl. Missing prerequisites fail, never skip.

## Doctor

Use the **read-only** version probe for the binary path recorded in `events.jsonl`: `<recorded-binary-path> --version`. Confirm that path is inside this run's scratch directory, not an installed release. The helper executes this probe before driving each flow. Once cleanup removes that binary, there is no instance to inspect: start a new run.

Before external flows, run `docker info`, `docker compose version`, `kind --version`, `k3d version`, and `kubectl version --client`. A failed prerequisite is a failed verification, not proof about dibs. Never change or restart a shared Docker engine to repair a run.

## Drive

The executable helper uses Python's standard library and real subprocesses. Run one flow per new evidence directory:

```sh
.claude/skills/verify/verify.py local --evidence "$PROOF/local-second"
.claude/skills/verify/verify.py adapters --evidence "$PROOF/adapters"
.claude/skills/verify/verify.py ssh --evidence "$PROOF/ssh"
# Or all three, in order:
.claude/skills/verify/verify.py all --evidence "$PROOF/all"
```

`local` covers metadata leases, owner death, heartbeat renewal, jobs, and FIFO/no-backfill. `adapters` creates real Compose, kind, and k3d resources. `ssh` reuses `make ssh-integration`, which builds the CLI and drives real OpenSSH in a disposable Alpine container. Read the mapped feature before interpreting results. CI runs all three flows on `ubuntu-24.04`, plus the existing adapter retry tests.

## Evidence

Keep `$PROOF` outside the checkout. Each flow writes `events.jsonl` with argv, stdout, stderr, exit codes, and labeled observations, plus background job logs and `result.json`. SSH command transcripts are inside the recorded `make ssh-integration` stdout. A valid proof requires `passed: true` **and** `cleaned: true`.

Exercise public CLI commands; do not call Go internals, edit lease records, or substitute fake tools. The sole queue-state read observes waiter registration so launch timing cannot masquerade as FIFO. Check action and result: preview must retain a lease, HTTP must reach Compose, nodes must be Ready, cleanup must remove resources, and jobs must return actual child/deadline/admission/transport codes. The harness observes preview side effects instead of trusting the word "preview".

The SSH container is a real protocol target, not evidence of a Lenovo rollout. Its setup installs OpenSSH/procps, provisions fixture keys and host trust, creates a quoted workdir, installs a Linux dibs binary, and supplies a remote budget and sshd keepalives. Those provisioning steps on an actual machine, its checkout/dependencies, and its sweep timer remain unverified until tested there. No source or credentials are synchronized.

## Cleanup

The helper terminates only PIDs it started, releases its own leases through dibs, verifies an empty listing, and removes scratch state. The SSH fixture removes only its uniquely named container. Evidence survives teardown. If cleanup fails, `events.jsonl` names `retained_state`; keep that directory and use its binary and `DIBS_STATE` to retry `release <id> --confirm`. Never delete a cleanup record while a resource remains. Never kill by process name or prune Docker resources.

After any failure, inspect `result.json` and the final cleanup event before retrying. Verify that `$PROOF/<flow>/events.jsonl` still exists after cleanup. Report failures with the command, output, expected result, and evidence path; do not weaken a journey to pass it.

## Helpers

`.claude/skills/verify/verify.py` is executable. Its only arguments are `local|adapters|ssh|all` and required `--evidence <new-absolute-directory>`; `--help` prints usage. It refuses to overwrite evidence. Use `/maintain-verification-skill` when features or CLI entry points change.
