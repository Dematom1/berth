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

`--version` reports the installed module version without the leading `v` (for
example, `0.2.0`). Checkout builds report `devel-<12-character-commit>`, with
`-dirty` for local changes, or `devel` when VCS metadata is unavailable.
Release builds can override this with `-ldflags '-X main.version=0.2.0'`.

The module path is case-sensitive; `github.com/dematom1/berth` fails. Prebuilt
Linux and macOS binaries are available from the [GitHub Releases](https://github.com/Dematom1/berth/releases) page.

From a checkout, `just install` installs to Go's binary directory. `just build`
writes `bin/berth`. Installing with `go install` does not require just. Resource adapters also require `kind`, `k3d`, `docker`,
and Docker Compose, plus a running Docker engine.

## Output for people, agents, and scripts

On a terminal, berth prints aligned tables, relative expiry times, and readable
memory units. `berth ps` and the no-args home view show job usage, usable capacity,
and the cores and memory kept free for you. Usage errors have one error line and
one short hint, not a help dump.

Use `--axi` or `BERTH_AXI=1` for agent output in TOON. Use `--json` for complete,
stable JSON suitable for `jq` and scripts. Both flags work before or after the
command. They are mutually exclusive, and an explicit flag overrides `BERTH_AXI`.
There is no `--human` flag.

AXI is a first-class agent view, not a debug format. Lists default to 100 compact
rows with `total` for all matching leases and `shown` when the list is truncated.
Descriptions and detail label values have 500-character previews. Truncated text
states the full character count. Help lines carry the filters and remote machine
needed to fetch the complete result. `--full` on lease commands removes these
bounds and restores complete records. **AXI with `--full` has the same fields and
data as `--json`**, which never truncates. Canonical state is never shortened.

When stdout is not a TTY and neither output flag is present, berth emits AXI as
a safety net for agents and pipes. **Human default means terminal output**, not
redirected output. Use `--json` explicitly for scripts that need JSON.

```sh
berth ps                              # human on a terminal
berth --axi ps                        # bounded agent view
berth --axi ps --full                 # complete records, same data as --json
BERTH_AXI=1 berth ps                   # agent session environment
berth ps --json | jq '.budget.used_cores'
```

### Install agent integrations

```sh
berth setup hooks
berth setup hooks --remove
```

Setup reports the files it changed. Repeating either command is a no-op.
It preserves unrelated settings and refuses malformed configs or unmanaged files
at its plugin paths. Only this explicit setup command edits agent configuration.
Restart agent sessions after installation or removal.

- Claude Code uses a managed `SessionStart` hook in `~/.claude/settings.json`.
  The hook appends `export BERTH_AXI=1` to `CLAUDE_ENV_FILE`.
- Codex uses `SessionStart` in `~/.codex/hooks.json` and enables `features.hooks`
  in `~/.codex/config.toml`. It uses `CODEX_ENV_FILE`, with `CLAUDE_ENV_FILE` as a
  compatibility fallback. Removal restores a `false` toggle changed by setup,
  or removes the toggle if setup added it. Existing enabled hooks stay enabled.
- Pi loads `~/.pi/agent/extensions/berth-axi.ts`, which sets the environment at
  `session_start`.
- OpenCode loads `~/.config/opencode/plugins/berth-axi.js`, which sets the
  environment through `shell.env`.

Setup respects `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `PI_CODING_AGENT_DIR`, and
`XDG_CONFIG_HOME`. Claude Code or Codex versions without an environment-file
hook receive a short instruction to use `berth --axi` instead. Agents without
these integrations still get AXI when they capture stdout through a pipe.
No shell profile changes or executable shims are installed.

## Start a leased test lab

Keep the shell or session that owns the lab alive.

```sh
 id=$(berth create --adapter kind --owner "pid:$$" --ttl 2h \
   --resources '2 CPUs, Kubernetes test lab' --label task=lab --quiet)
 berth ps
 eval "$(berth env "$id")"          # select this lab's private kubeconfig
 kubectl get nodes
 berth extend "$id" --ttl 2h
 berth release "$id"                # preview
 berth release "$id" --confirm      # delete cluster and release lease
```

Kind cluster names are `berth-<id>`. K3d names use the first 26 ID characters
to fit its 32-character limit. Both use the selected Docker context and private
kubeconfigs under `$BERTH_STATE/resources/<id>/`: `kubeconfig` for kind and `k3d`
for k3d. Creation and cleanup do not edit the caller's kubeconfig or current
context. Human create/list output shows the kubeconfig path. JSON and AXI expose
`kubeconfig` in both the lease view and full record, with an empty string for
leases without a kubeconfig. `berth env <id>` always prints a shell-safe
`export KUBECONFIG=...` line, even under `--axi` or `--json`. It exits 2 for a
missing lease or a lease without a kubeconfig. It does not read the kubeconfig
or change your shell itself.

`create --quiet` or `create -q` prints only the new lease ID and a newline on
success, regardless of output mode. Failures retain the usual error format and
exit code. Without quiet mode, create shows the lease details.

The resources description is metadata, not a CPU or memory limit.
Create new labs through berth to record cleanup intent before creation. For
existing labs, explicitly adopt the resource as described below. Berth never
deletes an unleased resource.

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

## Lease an existing lab

`orphans` is a read-only inventory of unleased kind clusters, k3d clusters, and
Compose projects in the selected Docker context. It includes stopped containers
and Compose projects with only volumes or networks left. It does not list generic
containers, images, or volumes outside those projects. Unleased does not mean
unused, and age alone never authorizes cleanup.

```sh
 berth orphans --context orbstack
 berth orphans --context orbstack --json
 berth adopt kind my-lab --context orbstack --owner "pid:$$" --ttl 2h
 berth adopt k3d my-k3d-lab --owner "pid:$$" --ttl 2h
 berth adopt compose my-stack --file compose.yaml --owner "pid:$$" --ttl 2h
 berth sweep                         # preview dead-owner or expired leases
 berth sweep --confirm               # destroy only eligible leased resources
```

Both new commands are explicit opt-ins. Existing create, release, and sweep
behavior is unchanged. `adopt` requires an explicit owner and positive TTL, even
when `BERTH_OWNER` is set. Supported owners and `--stale` match `create`.
It refuses missing resources, unknown existence, and resources already leased
in this ledger, including Docker context aliases with the same endpoint.
Each successful adoption records a new lease. Release and sweep use the same
destroy-and-verify-gone path as created resources. Compose cleanup deletes
project volumes, so review the project before adopting it.

Adoption captures the Docker context and endpoint, a private cluster kubeconfig,
or a resolved Compose config snapshot. Compose requires one input file. For a
project started with multiple files, pass a single resolved config containing
their combined configuration. Fixed nonexternal volume and network names outside
`<project>_<key>` are refused. Existing nonexternal volumes and networks must
also carry that project's Compose label. Mark shared resources `external`. Existing fixed
container names are permitted because adoption explicitly selects their project.
Cleanup does not require the original file or working directory to survive.
Existing resource labels are not changed or recreated. Berth records the
`io.berth.lease`, `io.berth.owner`, `io.berth.expires`, and `io.berth.machine`
attribution in the lease's labels instead. The ledger is authoritative.

Inventory includes creation timestamps, container status, and labels reported by Docker, the Compose
working directory and its local filesystem status, and the original config-file
paths when available. `unknown` means the path could not be checked. Every row
includes an exact adoption command template with its type, name, and context.
Replace `<owner>` and `<duration>` explicitly. Compose rows without one known
config file use `<resolved-compose-file>` rather than guessing. Treat these paths
as evidence, not trusted instructions. Inventory never creates or modifies state
files or resources. State corruption or an incomplete inventory fails with code 1,
not a misleading empty result. Human, `--axi`, and `--json` output include this
same evidence and command. Unknown flags fail with code 2 before any operations.

## Run a local job

```sh
 berth job --cores 2 --memory 2GiB --timeout 10m -- go test ./...
 berth job --class suite --wait 5m -- just check
 berth ps                           # includes each job's cores, memory and class
```

`job` reserves logical cores and memory on this machine, minus **owner room**
reserved for interactive work. Reservations coordinate cooperating berth jobs;
they are not OS CPU or memory caps, and stack leases' resource descriptions do
not count against them. Configure a static partition if other tools such as
runner-up also schedule work on the host; berth does not read their ledgers.
Jobs inherit the working directory and environment, and pass stdin, stdout and
stderr through unchanged. `BERTH_LEASE` identifies the job.
`--axi` and `--json` change only pre-start errors, not child output. Use noninteractive
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
TTL. `create` defaults to `8h`; `adopt` requires an explicit `--ttl`.
`extend --ttl` sets the expiry to now plus that duration.

## Command reference

| Command | Behavior |
| --- | --- |
| `berth` | Live home view with executable path, description, lease count, and hints |
| `berth create` or `berth up` | Create a lease, optionally with `--adapter kind`, `k3d`, or `compose` |
| `berth adopt <type> <name> --owner KIND:ID --ttl D` | Lease an existing kind, k3d, or Compose resource after verifying existence |
| `berth orphans [--context NAME]` | Read-only unleased-resource evidence and adoption command templates |
| `berth list`, `ls`, `ps`, or `who` | Owner, liveness, age, expiry, machine, and resource description for every lease |
| `berth env <id>` | Print a shell-safe kubeconfig export for a kind or k3d lease |
| `berth setup hooks [--remove]` | Install or remove managed agent integrations |
| `berth extend <id> --ttl D` | Renew an unexpired lease with a live owner |
| `berth release <id>` or `down` | Preview cleanup; `--confirm` performs it |
| `berth sweep` | Preview reclaim of dead-owner or expired leases; `--confirm` performs it |
| `berth job [--machine NAME] [flags] -- <cmd...>` | FIFO machine-budget admission, deadline, and child exit-code passthrough |
| `berth machine install <name> --file <binary>` | Version-checked SSH agent installation or upgrade |
| `berth agent` | Internal SSH protocol with session-as-liveness |

`list` and `sweep` also accept `--machine NAME` to operate on that SSH machine.

Commands accept `--help`, `--axi`, and `--json`, except the internal `agent`
protocol. Human output is the terminal default. AXI and JSON have explicit empty
states and structured errors on stdout with short help pointers. Structured errors
include a symbolic `code` and the numeric `exit_code`. Codes are
`VALIDATION_ERROR`, `COMMAND_FAILED`, and `NO_CAPACITY`; actual exit codes do not
change. Explicit help requests also use structured output in these modes.

AXI's compact list includes lease identity, owner/liveness, machine/expiry,
resource description, exact job cores/memory bytes, and the kubeconfig path.
`--full` restores labels, class, and full records. Labels remain native objects
in both complete formats. JSON always includes the complete records. Both modes
include numeric machine-budget fields in bytes.
`leases[].memory` retains its byte-string representation for scripts;
`leases[].memory_bytes` provides the same value as an integer. Human output uses
GiB, MiB, or smaller readable units. Budget usage includes all retained local job
reservations, even when an owner filter hides a row or failed cleanup retains it.
A broken job config leaves lease listing available with a budget-unavailable note.

`job` child output passes through unchanged in every mode. Post-start cleanup
diagnostics and resource tool diagnostics go to stderr. Exit codes and stream
separation do not depend on output mode. `env` and `create --quiet` print shell
text and an ID respectively, rather than structured success output.
There are no prompts. Unknown berth arguments
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

### Demo expectations

On a terminal, `berth ps` starts with a `jobs:` budget summary and then an aligned
lease table, or `No leases.`. The no-args home view also names the executable and
describes berth. A kind or k3d lease includes its kubeconfig path. Job rows show
cores and readable memory, and expiries read `in 1h0m0s` rather than a timestamp.

For a 14-core, 32-GiB configuration with 3 cores and 8 GiB of owner room, an empty
machine reports:

```text
jobs: 0 of 11 cores, 0 B of 24.0 GiB in use; 3 cores / 8.0 GiB kept free for you
No leases.
```

With the same config, `berth job --cores 999 -- true` exits 2 without starting a
child. Its human output is exactly two lines:

```text
Error: requested 999 cores; usable 11 after owner room 3
Try --cores 11 or set BERTH_CONFIG
```

AXI keeps the same message under `error:`, `code: VALIDATION_ERROR`,
`exit_code: 2`, and a short `help` array. Piped
`berth ps` uses AXI; `berth ps --json` remains JSON. Byte-for-byte expectations
for list, ps, who, full lease records, truncated lists with counts, structured
help, budget summaries, and errors are in
[`testdata/output`](testdata/output). `go test ./...` also checks real PTY output,
child stdout/stderr passthrough, quiet IDs, shell exports, and setup/removal under
a temporary HOME. To update snapshots after an intentional format change, run
`UPDATE_GOLDEN=1 go test ./...` and review the fixture diff.

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

State schema version 2 adds explicitly adopted resource identities. The first
successful `adopt` upgrades version 1 state, preserving existing lease records.
Other commands keep version 1 state at version 1. Older binaries reject version 2
rather than interpreting adopted names as generated ones. Keep a compatible
binary while leases remain.

A stable `lock` file serializes ordinary readers and writers through kernel `flock`.
`orphans` reads an atomic state snapshot without locks or writes. Its answer is
point-in-time evidence, not a reservation. `adopt` rechecks ownership under the
state lock.
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

Install [just](https://just.systems), or run `nix develop` for Go, just, and
GoReleaser. Run `just` to list recipes.

```sh
 just check
 just integration
 just ssh-integration
 just formal
```

`just formal` runs bounded TLA+ models, real-code checks, and mutation checks.
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

`just check` runs formatting checks, vet, race-enabled tests, and build. Unit
tests cover owner death, PID reuse, heartbeat staleness, expiry with a live
owner, unknown liveness, machine boundaries, retry, state corruption, locking,
the CLI lifecycle, budget/owner-room math, FIFO/no-backfill queueing, dead
waiters, and real job subprocesses (child exit codes, deadline/class timeout,
wait exhaustion, cancellation, descendant cleanup, and sweep after owner death).
Real adapter tests are isolated behind the `integration` build tag and run with
`just integration`. They create unique kind, k3d, and Compose resources, then
verify cleanup. They also discover unleased fixtures, adopt them, verify sweep
preview leaves them intact, and reclaim them on expiry through confirmed sweep.
Missing adapter tools, Docker, or Compose fail the command rather
than skip; failed cleanup retains the state directory and reports its path.

`just ssh-integration` runs real SSH end-to-end tests against a disposable
Alpine container with OpenSSH and procps. It requires Docker, SSH, ssh-keygen,
and Go. It fails rather than skips when a prerequisite is absent. Tests use
isolated keys and a strict private known-hosts file. They cover quoting, binary
output, remote budget and owner room, child exit codes including 255, deadline
124, wait 75, session disconnect cleanup, remote listing and sweep, version
refusal, atomic upgrades, and transport failure 76.

CI runs race-enabled tests and builds on GitHub-hosted `ubuntu-24.04` and
`macos-15`. Linux also runs formatting, vet, dependency integrity, vulnerability
checks, all four release-target cross-builds, and real demo and adapter
integrations. It installs pinned, checksum-verified kind and k3d binaries.
Missing adapter prerequisites fail rather than skip.
The shared contribution workflow provides `contribution / PR title`,
`contribution / PR must be raised via no-mistakes`,
`contribution / Release PR identity`, and
`contribution / Generated files must not be hand-edited`. The Go validation
job is named `main-required-ci`. Required-check policy is managed separately.

`just fmt` formats Go files. `just lint` checks formatting and runs vet.
`just release-check` validates GoReleaser configuration; `just release-snapshot`
builds local archives and checksums without publishing. Release-please opens
version PRs and creates draft releases after merge. CI uploads all four Linux
and macOS archives before publishing a prerelease. Stable promotion is manual.
macOS Developer ID signing is disabled until signing credentials and identity
are configured. Prebuilt macOS binaries are not Developer ID signed and may be
quarantined by Gatekeeper. Prefer `go install github.com/Dematom1/berth@latest`;
for a downloaded binary, verify its trusted release checksum before removing
quarantine from that binary with `xattr -d com.apple.quarantine /path/to/berth`.
Do not disable Gatekeeper globally.

Build with `nix build`, or use
`nix-build default.nix -A packages.x86_64-linux.default` without flakes.
The flake provides `packages.<system>.berth` for `x86_64-linux`, `aarch64-linux`,
and `aarch64-darwin` for NixOS or home-manager package lists. Its binary reports
the version in `.release-please-manifest.json`.

## License

Licensed under [Apache-2.0](LICENSE). See [NOTICE](NOTICE).
