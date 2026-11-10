# berth

Owner-and-expiry leases for local stacks and clusters. One Go binary, no daemon.
Supports leases, kind and k3d clusters, Docker Compose projects, and local jobs
with machine-budget admission on macOS and Linux. Jobs can run on configured
SSH machines.

## Install

Requires Go 1.26 or later.

```sh
go install github.com/Dematom1/berth@latest
berth --version
```

The module path is case-sensitive; `github.com/dematom1/berth` fails. Prebuilt
Linux and macOS binaries are available from the [GitHub Releases](https://github.com/Dematom1/berth/releases) page.

From a checkout, `make install` installs to Go's binary directory. `make build`
writes `bin/berth`. Resource adapters also require `kind`, `k3d`, `docker`,
and Docker Compose, plus a running Docker engine.

## Start a leased test lab

Keep the shell or session that owns the lab alive.

```sh
 berth create --adapter kind --owner "pid:$$" --ttl 2h \
   --resources '2 CPUs, Kubernetes test lab' --label task=lab
 berth ps
 berth extend <id> --ttl 2h
 berth release <id>                  # preview
 berth release <id> --confirm        # delete cluster and release lease
```

Kind cluster names are `berth-<id>`. K3d names use the first 26 ID characters
to fit its 32-character limit. Both use the selected Docker context and private
kubeconfigs under `$BERTH_STATE/resources/<id>/`: `kubeconfig` for kind and `k3d`
for k3d. Creation and cleanup do not edit the caller's kubeconfig or current
context. The resources description is metadata, not a CPU or memory limit.
An orphaned lab such as `k8s-axi-lab` is prevented by creating the new cluster
through berth. This slice does not adopt or delete existing unleased clusters.

To create a Compose stack, pass its file.

```sh
 berth create --adapter compose --file compose.yaml --owner "pid:$$" --ttl 2h
 berth who --json
 berth sweep                        # preview dead or expired leases
 berth sweep --confirm               # reclaim them
```

Compose projects also use `berth-<id>`. Berth resolves the Compose file once,
saves a private config snapshot, and labels its services, volumes, and networks
with `io.berth.lease`, `io.berth.owner`, `io.berth.expires`, and `io.berth.machine`.
Cleanup does not require the original file, environment, or worktree. Labels
record creation-time expiry; the lease record is authoritative after `extend`.
Fixed `container_name` values and nonexternal volume or network names outside
the generated project are rejected. Mark shared volumes and networks `external`
to keep them out of cleanup. Release runs Compose down with `--volumes` and
`--remove-orphans`, so data in managed volumes is deleted.

Adapters tolerate already absent resources. Cleanup errors retain the
lease and exit 1. Berth verifies that managed resources are gone before removing
the record. A create that fails after saving its lease also retains the record
for release or sweep to clean up partial resources.

## Run a local job

```sh
 berth job --cores 2 --memory 2GiB --timeout 10m -- go test ./...
 berth job --class suite --wait 5m -- make check
 berth ps                           # includes each job's cores, memory and class
```

`job` reserves logical cores and memory on this machine, minus **owner room**
reserved for interactive work. Reservations coordinate cooperating berth jobs;
they are not OS CPU or memory caps, and stack leases' resource descriptions do
not count against them. Configure a static partition if other tools such as
runner-up also schedule work on the host; berth does not read their ledgers.
Jobs inherit the working directory and environment, and pass stdin, stdout and
stderr through unchanged. `BERTH_LEASE` identifies the job.
`--json` changes only pre-start errors, not child output. Use noninteractive
commands. Berth does not hand terminal foreground job control to the child.

Admission is FIFO across classes, with no backfill: a smaller request cannot
bypass an earlier larger waiter. The default wait is `30m`; `--wait 0` tries
once. A wait that exhausts returns 75 without starting user code. Dead or
expired waiters are discarded. Admission also attempts to reclaim dead or
expired job leases before counting their reservations; failed cleanup retains
the reservation. A request larger than the usable budget fails with code 2.
Queue order is the order requests acquire the state lock, not shell launch time.
The state lock is released while waiting and while a child runs.

Each job is a lease owned by the `berth job` process, not `BERTH_OWNER` or its
invoking shell. Its deadline starts at admission and is the explicit `--timeout`
or class timeout. Job deadlines cannot be extended. On timeout or cancellation,
berth sends TERM to the process group, then KILL after one second if needed.
Normal completion also cleans up remaining group members before releasing the
reservation. Cleanup failures go to stderr and retain the lease; they never
replace the child's exit code. If berth itself is killed, `sweep --confirm`
reclaims the job lease on owner death or expiry. Keep a host sweep timer. A
matching live leader permits process-group cleanup; if the leader is missing or
reaped, berth warns and releases the lease without signaling an unverifiable
group. Orphan descendants may then require manual cleanup.

Process groups are not a security boundary: programs that call `setsid` or
change groups can escape cleanup. Container/cgroup enforcement is not part of
local job admission. Cleanup verifies the recorded group leader's start identity
before signaling and retains the record if identity is unknown or reused while
the leader is present.

### Machine budget and classes

The optional config is strict JSON at `$XDG_CONFIG_HOME/berth/config.json`, or
`~/.config/berth/config.json`. `BERTH_CONFIG` selects a different file; an
explicitly selected missing file is an error. Unknown fields are rejected.

Without a config, totals come from the host's logical CPU count and physical
memory (`sysctl hw.memsize` on macOS, `/proc/meminfo` on Linux). Owner room is
`max(1, cores/4)` integer cores, capped at `cores-1` (zero on a single-core host),
and `max(512MiB, memory/4)` bytes, capped at half the host's memory. The default
class requests one core and up to 1GiB of usable memory, with a `1h` timeout.

For example, this gives jobs a shared 4-core/8GiB partition on a 16-core/32GiB
machine, leaving the rest for its owner and other schedulers:

```json
{
  "cores": 16,
  "memory": "32GiB",
  "room_cores": 12,
  "room_memory": "24GiB",
  "classes": {
    "default": {"cores": 1, "memory": "1GiB", "timeout": "1h"},
    "suite": {"cores": 2, "memory": "4GiB", "timeout": "30m"},
    "image": {"cores": 4, "memory": "8GiB", "timeout": "45m"}
  }
}
```

Omitted top-level fields keep host defaults. Each configured class supplies
positive `cores`, `memory`, and `timeout`; `default` remains available unless
explicitly replaced. `--cores`, `--memory`, and `--timeout` override the selected
class independently. Memory accepts integer bytes or `KiB`, `MiB`, `GiB`, `KB`,
`MB`, `GB`, and `B`; cores are positive integers. Timeouts are positive Go
durations. Config edits apply to new invocations; existing reservations still
count, and running jobs keep their original deadline. Use the same config and
state directory for every cooperating invocation on a machine.

## Run jobs over SSH

Add a machine to your local config. Paths refer to the remote machine and must
be absolute. `host` can be an SSH config alias. `port` and `identity_file` are
optional. SSH uses batch authentication, normal host-key checking, a 15-second
connection timeout, and keepalives every 5 seconds with 3 unanswered probes.

```json
{
  "machines": {
    "lenovo": {
      "host": "lenovo",
      "user": "worker",
      "workdir": "/home/worker/src/project",
      "agent": "/home/worker/.local/bin/berth"
    }
  }
}
```

Build a trusted binary for the remote OS and architecture, then install it:

```sh
 GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/berth-linux .
 berth machine install lenovo --file /tmp/berth-linux
 berth job --machine lenovo --class suite -- go test ./...
 berth ps --machine lenovo
 berth sweep --machine lenovo --confirm
```

Installation streams the binary over SSH, checks its version on the remote,
and atomically replaces the configured agent path. A wrong version or
architecture fails without replacing the installed binary. Running agents
keep their executable. Every connection checks the remote version before
requesting work, and the agent checks the client's version again. Mismatches
refuse to start a child and explain how to install the matching binary.

The same berth binary runs as `berth agent` on the remote, once per SSH session.
It is not a daemon. The remote config decides classes, total budget, owner room,
and admission across all clients. The client does not forward its budget or
class defaults. Remote state stays on the remote machine, under that SSH user's
usual berth state directory. `ps --machine` and `sweep --machine` use that state.
Each machine reclaims only its own leases. There is no shared cross-host ledger.

Remote jobs run in the configured workdir with the environment of a
non-interactive SSH command. Login profiles such as `~/.profile` are not read.
Make required tools available on that PATH, or invoke them by absolute path.
No source, worktree, environment variables, build tools, or credentials are
copied. Remote child stdin is `/dev/null`; SSH stdin is reserved for liveness,
so remote commands must be noninteractive. Child stdout and stderr pass through.
The private protocol frames stdout, including arbitrary bytes, and the final
exit code separately. Child exit 255 is preserved rather than mistaken for an
SSH transport failure.

The agent is the lease's `session:<holder-pid>` owner. EOF on its SSH stdin or
session cancellation kills the job's process group and releases its lease,
including while admission is waiting. An idle local stdin does not end the SSH
session. For broken-network detection on the server, configure sshd's
`ClientAliveInterval` and `ClientAliveCountMax`, for example 5 and 3. Client
keepalives alone do not bound how long the server waits for a lost client.
Remote deadlines still apply. Keep a sweep timer on each machine for agent
crashes and failed cleanup. Process-group escape limitations apply remotely too.

A missing or broken transport returns 76 with diagnostics on stderr once SSH
has started. The remote result is then unknown; do not assume the child did not
run. This code avoids reporting berth's own 1 or 2 after a child may have started.
A completed child can itself return 76, as with any reserved job exit code.

For a Lenovo rollout, provision key-based SSH and trusted host keys, a remote
checkout and its dependencies, the matching Linux binary, and a remote berth
config with a budget partition that leaves owner room and avoids other
schedulers. Configure an sshd keepalive policy and a per-user sweep timer.
Kubernetes-backed machines remain deferred.

## Owners and renewal

`--owner` overrides `BERTH_OWNER`. Without either, berth uses the parent PID,
usually the invoking shell. A spawner can export `BERTH_OWNER` for all of its
workers. The supported owner kinds are:

- `pid:<pid>` follows a local process and records its start time to detect PID
  reuse. Process start identity comes from `ps` with one-second precision.
- `session:<holder-pid>` follows the local process holding a session, with the
  same start-time check. This is not a Unix session ID. The SSH agent uses its
  own session-holding process as the owner without querying a client PID.
- `heartbeat:<file>` follows an existing regular file's modification time.
  `--stale` defaults to `15m`. A missing file or a stale heartbeat means dead;
  an unreadable file or a timestamp in the future means unknown. Create the
  file before creating the lease.

Touch a heartbeat file at most every staleness/3. Extend a lease at the latest
when one third of its TTL remains. A lease is reclaimable when its owner is
dead **or** when it has expired. A live owner that misses either cadence loses
its resources. Unknown liveness alone never permits reclaim, but expiry still
does. `extend` refuses expired leases and owners that are dead or unknown.

```sh
 touch /tmp/my-lab-heartbeat
 berth create --owner heartbeat:/tmp/my-lab-heartbeat --stale 15m --ttl 1h
 # The owner touches the file at least every 5m, and extends before 20m remain.
 berth extend <id> --ttl 1h
```

Durations use Go syntax such as `30m`, `2h`, or `1h30m`. Every lease has a positive
TTL, defaulting to `8h`. `extend --ttl` sets the expiry to now plus that duration.

## Command reference

| Command | Behavior |
| --- | --- |
| `berth` | Live home view with executable path, description, lease count, and hints |
| `berth create` or `berth up` | Create a lease, optionally with `--adapter kind`, `k3d`, or `compose` |
| `berth list`, `ls`, `ps`, or `who` | Owner, liveness, age, expiry, machine, and resource description for every lease |
| `berth extend <id> --ttl D` | Renew an unexpired lease with a live owner |
| `berth release <id>` or `down` | Preview cleanup; `--confirm` performs it |
| `berth sweep` | Preview reclaim of dead-owner or expired leases; `--confirm` performs it |
| `berth job [--machine NAME] [flags] -- <cmd...>` | FIFO machine-budget admission, deadline, and child exit-code passthrough |
| `berth machine install <name> --file <binary>` | Version-checked SSH agent installation or upgrade |
| `berth agent` | Internal SSH protocol with session-as-liveness |

`list` and `sweep` also accept `--machine NAME` to operate on that SSH machine.

Every command accepts `--help` and `--json`. Output defaults to concise TOON-style
objects and tables. Label cells in text tables contain compact JSON, with `"{}"`
for empty labels. JSON listings keep labels as native objects and include full
records. Both formats have explicit empty states and structured errors on stdout.
The exception is `job`: child output passes through, while pre-start errors are
structured and post-start cleanup diagnostics go to stderr. Resource tool
diagnostics also go to stderr. There are no prompts. Unknown berth arguments
and flags fail with code 2 and help before state or resource operations run;
everything after a job's required `--` belongs to the child.

`create --adapter none` records a metadata-only lease. It does not manage or
delete an external resource. Labels are repeatable `--label key=value` metadata.
`list --owner KIND:ID` filters by canonical owner identity, resolving relative
heartbeat paths and numeric PID spellings without checking owner liveness.

### AXI exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success, including already absent releases and empty sweeps |
| `1` | Berth operation failed; cleanup records are retained on resource failure |
| `2` | Invalid command, argument, or flag; no child was started |
| Child's code | `berth job` when its child runs to completion |
| `124` | `berth job` deadline killed the child (coreutils `timeout` convention) |
| `75` | `berth job`: no slot frees within `--wait`, `EX_TEMPFAIL` |
| `128 + signal` | Child terminated by a signal, e.g. 143 for SIGTERM |
| `125` | Job child started, but its wait status could not be obtained |
| `76` | SSH transport or protocol failed; the remote result is unknown |

`berth job` uses berth's own codes 1 and 2 only when no user child was started.
Child completion passes through the child's code even when it overlaps these
numbers, including 1, 2, 75, and 124. `berth job --help` documents this contract.
After start, cleanup errors never override the child's result. Signals and
startup failures of the gated execution helper may return 125 or 126; a helper
execution failure is reported on stderr.

## Sweep from a host timer

Berth does not sweep by itself. Run `berth sweep --confirm` from cron, a systemd
timer, launchd, or your session teardown. Set the same absolute `BERTH_STATE`
and use an absolute executable path in the timer. Sweep frequency determines
how long a dead or expired lease can retain resources. Preview first to check
which resources will be deleted.

## State and safety

`BERTH_STATE` defaults to `$XDG_STATE_HOME/berth`, or
`~/.local/state/berth` when XDG is unset. State files and snapshots are private to
the invoking user. State contains resolved Compose values, which may include
secrets. Keep this directory on a local filesystem, not a network share.

A stable `lock` file serializes readers and writers through kernel `flock`.
Each state change writes and syncs a temporary file, renames it over `state.json`,
and syncs the directory. Berth saves cleanup intent **before** starting a
resource. If berth dies midway through create or delete, the next release or
eligible sweep retries the same unique resource. Kernel locks end on process
death. Corrupt or unsupported state is an error, never an empty lease list.

Each lease records the hostname of its authoritative machine. A host never
acts on another machine's leases, even after expiry: it cannot verify a foreign
owner's liveness or delete its resources. Run cleanup on the original machine
with its original state path, directly or through
`berth sweep --machine <name> --confirm`. Do not share state between hosts or
change the host's name while leases exist.

Adapters pin the Docker context selected at creation. `DOCKER_HOST` overrides
are rejected; select a named context instead. Keep that context's endpoint
unchanged while leases exist. Berth never sweeps unleased resources. This slice
does not reconstruct leases after state loss; keep the state directory until
all managed resources are released. Small resource snapshots remain after
release for diagnosis and may be removed once their leases are absent.

Berth is a cooperation protocol, not a security boundary for programs with the
same Unix user. SSH session-as-liveness does not isolate untrusted code.
Worktree transport and Kubernetes-backed machines are deferred. A resident
scheduler is not required for these leases; revisit it only when measured queue
fairness or push-event requirements cannot be met by CLI invocations and host
timers.

## Develop and verify

```sh
 make check
 make integration
 make ssh-integration
 make formal
```

`make formal` runs bounded TLA+ models, real-code checks, and mutation checks.
It requires Java 17 or newer, Python 3, and Go. The model checker download is
version- and checksum-pinned. Read the [formal findings](verification/findings.md)
first: TTL expiry may reclaim an Alive owner by policy; the model checks that
an unexpired live owner is not reclaimed. Orphan-group cleanup and a deadline-
check race remain counterexamples, not verified guarantees. CI reproduces those
known failures explicitly. `python3 verification/check.py --strict` exits nonzero
for F2 and F3.
The [lease and job safety skill](.claude/skills/lease-job-safety/SKILL.md)
records the code mapping, bounds, assumptions, and rerun procedure.

The project-local [verify skill](.claude/skills/verify/SKILL.md) drives the demo
through the real CLI, including Compose HTTP, Ready kind/k3d nodes, FIFO jobs,
and SSH session cleanup. To run every flow and retain proof outside the checkout:

```sh
PROOF=$(mktemp -d "${TMPDIR:-/tmp}/berth-proof.XXXXXX")
.claude/skills/verify/verify.py all --evidence "$PROOF/all"
```

The helper needs Python 3 and the real adapter/SSH tools; missing prerequisites
fail rather than skip. CI runs these workflows and uploads `berth-verification`
evidence, including failed runs, for seven days.

`make check` runs formatting checks, vet, race-enabled tests, and build. Unit
tests cover owner death, PID reuse, heartbeat staleness, expiry with a live
owner, unknown liveness, machine boundaries, retry, state corruption, locking,
the CLI lifecycle, budget/owner-room math, FIFO/no-backfill queueing, dead
waiters, and real job subprocesses (child exit codes, deadline/class timeout,
wait exhaustion, cancellation, descendant cleanup, and sweep after owner death).
Real adapter tests are isolated behind the `integration` build tag and run with
`make integration`. They create unique kind, k3d, and Compose resources, then
verify cleanup. Missing adapter tools, Docker, or Compose fail the command rather
than skip; failed cleanup retains the state directory and reports its path.

`make ssh-integration` runs real SSH end-to-end tests against a disposable
Alpine container with OpenSSH and procps. It requires Docker, SSH, ssh-keygen,
and Go. It fails rather than skips when a prerequisite is absent. Tests use
isolated keys and a strict private known-hosts file. They cover quoting, binary
output, remote budget and owner room, child exit codes including 255, deadline
124, wait 75, session disconnect cleanup, remote listing and sweep, version
refusal, atomic upgrades, and transport failure 76.

CI runs on GitHub-hosted `ubuntu-24.04` and executes vet, unit tests, build,
cross-builds, `make ssh-integration`, and `make integration`. It installs pinned,
checksum-verified kind and k3d binaries; the integration target fails if Docker
or any adapter prerequisite is unavailable.
The shared contribution workflow provides `contribution / PR title`,
`contribution / PR must be raised via no-mistakes`,
`contribution / Release PR identity`, and
`contribution / Generated files must not be hand-edited`. The Go validation
job is named `main-required-ci`. Required-check policy is managed separately.

## License

Licensed under [Apache-2.0](LICENSE). See [NOTICE](NOTICE).
