# dibs

Owner-and-expiry leases for local stacks and clusters. One Go binary, no daemon.
Supports leases, kind and k3d clusters, Docker Compose projects, running shells,
and local jobs with machine-budget admission on macOS and Linux. Jobs can run on
configured SSH machines.

## Install

Requires Go 1.26 or later.

```sh
go install github.com/Dematom1/dibs@latest
dibs --version
```

`--version` reports the module version without the leading `v` (for example,
`0.2.0`). Checkout builds can report a commit-bearing module pseudo-version,
such as `0.1.1-0.20261110022950-b59f79420c85`. Without a module version, embedded
VCS metadata produces `devel-<12-character-commit>`, with `-dirty` for local
changes. Builds without either form of metadata, such as `go run .`, report
`devel`. Release builds can override this with `-ldflags '-X main.version=0.2.0'`.

The module path is case-sensitive; `github.com/dematom1/dibs` fails. Prebuilt
Linux and macOS binaries are available from the [GitHub Releases](https://github.com/Dematom1/dibs/releases) page.

From a checkout, `just install` installs to Go's binary directory. `just build`
writes `bin/dibs`. Installing with `go install` does not require just. Resource adapters also require `kind`, `k3d`, `docker`,
and Docker Compose, plus a running Docker engine.

## Upgrade from berth

Dibs is the new name for berth. Update install commands, sweep timers, scripts,
and remote binary paths to `dibs`. The Nix package and release archives
provide a deprecated `berth` command through the 0.3 releases; both it and the
`BERTH_*` aliases are removed in 0.4.0. `berth` is the same program as `dibs`,
with identical stdout and exit codes, plus one stderr deprecation line naming
`dibs`. Existing berth-named leases remain reclaimable. See the [rename release
notes](docs/releases/rename-to-dibs.md) for compatibility details and
repository-rename ordering.

## Output for people, agents, and scripts

On a terminal, dibs prints aligned tables, relative expiry times, and readable
memory units. `dibs ps` and the no-args home view show job usage, usable capacity,
and the cores and memory kept free for you. Usage errors have one error line and
one short hint, not a help dump.

Use `--axi` or `DIBS_AXI=1` for agent output in TOON. Use `--json` for complete,
stable JSON suitable for `jq` and scripts. Both flags work before or after the
command. They are mutually exclusive, and an explicit flag overrides `DIBS_AXI`.
There is no `--human` flag.

AXI is a first-class agent view, not a debug format. Lists default to 100 compact
rows with `total` for all matching leases and `shown` when the list is truncated.
Descriptions and detail label values have 500-character previews. Truncated text
states the full character count. Help lines carry the filters and remote machine
needed to fetch the complete result. `--full` on lease commands removes these
bounds and restores complete records. **AXI with `--full` has the same fields and
data as `--json`**, which never truncates. Canonical state is never shortened.

When stdout is not a TTY and neither output flag is present, dibs emits AXI as
a safety net for agents and pipes. **Human default means terminal output**, not
redirected output. Use `--json` explicitly for scripts that need JSON.

```sh
dibs ps                              # human on a terminal
dibs --axi ps                        # bounded agent view
dibs --axi ps --full                 # complete records, same data as --json
DIBS_AXI=1 dibs ps                   # agent session environment
dibs ps --json | jq '.budget.used_cores'
```

### Install agent integrations

```sh
dibs setup hooks
dibs setup hooks --remove
```

Setup reports the files it changed. Repeating either command is a no-op.
It preserves unrelated settings and refuses malformed configs or unmanaged files
at its plugin paths. Only this explicit setup command edits agent configuration.
Restart agent sessions after installation or removal.

- Claude Code uses a managed `SessionStart` hook in `~/.claude/settings.json`.
  The hook appends `export DIBS_AXI=1` to `CLAUDE_ENV_FILE`.
- Codex uses `SessionStart` in `~/.codex/hooks.json` and enables `features.hooks`
  in `~/.codex/config.toml`. It uses `CODEX_ENV_FILE`, with `CLAUDE_ENV_FILE` as a
  compatibility fallback. Removal restores a `false` toggle changed by setup,
  or removes the toggle if setup added it. Existing enabled hooks stay enabled.
- Pi loads `~/.pi/agent/extensions/dibs-axi.ts`, which sets the environment at
  `session_start`.
- OpenCode loads `~/.config/opencode/plugins/dibs-axi.js`, which sets the
  environment through `shell.env`.

Setup respects `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `PI_CODING_AGENT_DIR`, and
`XDG_CONFIG_HOME`. Claude Code or Codex versions without an environment-file
hook receive a short instruction to use `dibs --axi` instead. Agents without
these integrations still get AXI when they capture stdout through a pipe.
No shell profile changes or executable shims are installed.

## Start a leased test lab

Keep the shell or session that owns the lab alive.

```sh
 id=$(dibs create --adapter kind --owner "pid:$$" --ttl 2h \
   --resources '2 CPUs, Kubernetes test lab' --label task=lab --quiet)
 dibs ps
 eval "$(dibs env "$id")"          # select this lab's private kubeconfig
 kubectl get nodes
 dibs extend "$id" --ttl 2h
 dibs release "$id"                # preview
 dibs release "$id" --confirm      # delete cluster and release lease
```

Kind cluster names are `dibs-<id>`. K3d names use the first 26 ID characters
to fit its 32-character limit. Both use the selected Docker context and private
kubeconfigs under `$DIBS_STATE/resources/<id>/`: `kubeconfig` for kind and `k3d`
for k3d. Creation and cleanup do not edit the caller's kubeconfig or current
context. Human create/list output shows the kubeconfig path. JSON and AXI expose
`kubeconfig` in both the lease view and full record, with an empty string for
leases without a kubeconfig. `dibs env <id>` always prints a shell-safe
`export KUBECONFIG=...` line, even under `--axi` or `--json`. It exits 2 for a
missing lease or a lease without a kubeconfig. It does not read the kubeconfig
or change your shell itself.

`create --quiet` or `create -q` prints only the new lease ID and a newline on
success, regardless of output mode. Failures retain the usual error format and
exit code. Without quiet mode, create shows the lease details. For `--pending`
and `--key` creates, see the quiet rules in the next section but one.

The resources description is metadata, not a CPU or memory limit.
Create new labs through dibs to record cleanup intent before creation. For
existing labs, explicitly adopt the resource as described below. Dibs never
deletes an unleased resource.

To create a Compose stack, pass its file.

```sh
 dibs create --adapter compose --file compose.yaml --owner "pid:$$" --ttl 2h
 dibs who --json
 dibs sweep                        # preview dead or expired leases
 dibs sweep --confirm               # reclaim them
```

Compose projects also use `dibs-<id>`. Dibs resolves the Compose file once,
saves a private config snapshot, and labels its services, volumes, and networks
with `io.dibs.lease`, `io.dibs.owner`, `io.dibs.expires`, and `io.dibs.machine`.
Cleanup does not require the original file, environment, or worktree. Labels
record creation-time expiry; the lease record is authoritative after `extend`.
Fixed `container_name` values and nonexternal volume or network names outside
the generated project are rejected. Mark shared volumes and networks `external`
to keep them out of cleanup. Release runs Compose down with `--volumes` and
`--remove-orphans`, so data in managed volumes is deleted.

Adapters tolerate already absent resources. Cleanup errors retain the
lease and exit 1. Dibs verifies that managed resources are gone before removing
the record. A create that fails after saving its lease also retains the record
for release or sweep to clean up partial resources.

## Lease an existing lab

`orphans` is a read-only inventory of unleased kind clusters, k3d clusters, and
Compose projects in the selected Docker context. It includes stopped containers
and Compose projects with only volumes or networks left. It does not list generic
containers, images, or volumes outside those projects. Unleased does not mean
unused, and age alone never authorizes cleanup.

```sh
 dibs orphans --context orbstack
 dibs orphans --context orbstack --json
 dibs adopt kind my-lab --context orbstack --owner "pid:$$" --ttl 2h
 dibs adopt k3d my-k3d-lab --owner "pid:$$" --ttl 2h
 dibs adopt compose my-stack --file compose.yaml --owner "pid:$$" --ttl 2h
 dibs sweep                         # preview dead-owner or expired leases
 dibs sweep --confirm               # destroy only eligible leased resources
```

Both new commands are explicit opt-ins. Existing create, release, and sweep
behavior is unchanged. `adopt` requires an explicit owner and positive TTL, even
when `DIBS_OWNER` is set. Supported owners and `--stale` match `create`.
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
Existing resource labels are not changed or recreated. Dibs records the
`io.dibs.lease`, `io.dibs.owner`, `io.dibs.expires`, and `io.dibs.machine`
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

## Lease a resource you build yourself

When your own tool builds the resource, record the lease first. A crash between
building the resource and registering it then leaves a lease that dibs can
finish, instead of an unleased resource. This is part of the consumer contract,
which is off by default. Turn it on per machine with `"consumer_contract": true`
in the user config, or with `DIBS_CONSUMER_CONTRACT=1`; the variable overrides
the config either way, and `0` turns a config opt-in off. Project files cannot
enable it. While it is off, `--pending`, `--key`, `reconcile`, `reclaim`, and
`explain` fail with code 2 and name the setting, and every other command keeps
its exit codes and output exactly.

```sh
 export DIBS_CONSUMER_CONTRACT=1
 id=$(dibs create --adapter kind --pending --name lab-7 --owner "pid:$$" \
   --ttl 2h --key task-7:lab --quiet) || exit 1   # never build without a lease
 kind create cluster --name lab-7
 dibs adopt kind lab-7 --pending "$id"      # register; a retry reports already_done
 dibs reconcile "$id" --confirm             # after a crash: bind it if it was built
 dibs reclaim "$id" --confirm               # only when the owner is proven dead
 dibs explain "$id"                         # what sweep or reclaim would do, and why
```

With `--pending` or `--key`, `--quiet` prints exactly the lease ID and a newline
on success, including the same ID on an idempotent retry, and no ID on
failure; the exit code carries the outcome, and errors keep their usual output.
It cannot be combined with an explicit `--axi` or `--json` on those creates.

`--pending` works with kind, k3d, and Compose (which also needs `--file`). It
records the selected Docker context and, with `--name`, the handle, which must
not exist yet. When your tool picks the handle, label the resource
`io.dibs.lease=<id>` instead. Only that pinned handle or label matches a
pending lease; age and names never do. A matching resource is not an orphan,
and plain `adopt` refuses it. `reconcile` previews by default and binds only a
single unclaimed match. Release and sweep of a pending lease bind its match and
destroy it, or forget the lease when no resource carries its evidence; an ambiguous match, a
match under another lease, or failed discovery keeps the lease. Keep a pending
Compose lease's file readable until the lease is bound: binding resolves it,
and release keeps the lease if it cannot.

`--key K` on `create` and `adopt` makes a matching retry return the existing
lease with `status: already_done`; retries ignore TTL, description, and labels,
returning the original lease unchanged. A key is free again after release.
`reclaim` always requires proven owner death. `sweep` and job admission also
preserve live or unknown owners for shell leases and leases created with
`--protect-live-owner` (see below).

With the contract on, these commands, `extend`, and `release` follow version 1
of the [consumer contract](docs/consumer-contract.md): `contract: 1`, a
`status` on success, and exit 3 with a stable `reason` when dibs refuses. Every
`extend` refusal then exits 3, and `release` reports `preview`, `released`, or
`already_done`. The contract page lists every status and reason, and maps the
lifecycle of a supervisor, an agent session, a CI job, or a script onto these
commands.

## Lease a running shell

Agents open login shells, treehouse subshells, and the shells inside rex, tmux,
and herdr panes. Each one can start helper processes, such as `gitstatusd` for
the prompt. A shell lease ties a shell, and its helpers, to the task that owns
it. The shell adapter is off by default. Turn it on per machine with
`"shell_adapter": true` in the user config, or with `DIBS_SHELL_ADAPTER=1`; the
variable overrides the config either way. While it is off, `adopt shell` fails
with code 2 and names the setting. Leases already recorded can still be listed,
released, and swept.

```sh
 export DIBS_SHELL_ADAPTER=1 DIBS_EXEC_OWNER=1 DIBS_CONSUMER_CONTRACT=1   # explain needs the contract
 dibs adopt shell "$$" --owner 'exec:test -e /run/task-7.open' --ttl 8h
 dibs list --owner 'exec:test -e /run/task-7.open'   # shells held for the task
 dibs explain <id>                                   # why it is held
 dibs sweep                                          # preview
 dibs sweep --confirm
```

A shell enters dibs only through an explicit `adopt` by PID; dibs never starts
shells, and `orphans` does not list unleased ones. Adopt refuses a process that
is not a known shell (`sh`, `bash`, `zsh`, `fish`, `dash`, `ksh`, and similar,
including login names such as `-zsh`), a process that is not running, and a
shell that is already leased. It pins the shell's PID and start time, and
records its current descendants with their start times. A reused PID is never
mistaken for the shell or a helper.

Cleanup covers the shell's whole tree: every recorded helper whose start time
still matches, and every current descendant, including helpers started after
adoption. It refuses, and keeps the lease, in these cases:

- The shell has a foreground job: a process group other than the shell's own
  holds its terminal. This holds for `release --confirm` too, and whatever the
  owner or policy says.
- The process table cannot be read.
- Dibs itself runs inside the shell. Release it from another shell.

Otherwise dibs sends SIGHUP to every member, then SIGTERM to survivors after 5
seconds, then SIGKILL after 5 more. It reports each step on stderr, re-reads
each process's start time immediately before each signal round (with a remaining
race between that read and the signal), and forgets the lease only after
every member is gone. A shell that has already exited is verified gone.

Pane shells and treehouse subshells are ordinary shells found by PID. This
adapter does not drive tmux, herdr, or rex. A terminal lease that closes a pane
hangs up its shell, and the shell lease then finds its shell gone.

## Run a local job

```sh
 dibs job --cores 2 --memory 2GiB --timeout 10m -- go test ./...
 dibs job --class suite --wait 5m -- just check
 dibs ps                           # includes each job's cores, memory and class
```

`job` reserves logical cores and memory on this machine, minus **owner room**
reserved for interactive work. Reservations coordinate cooperating dibs jobs;
they are not OS CPU or memory caps, and stack leases' resource descriptions do
not count against them. Configure a static partition if other tools such as
runner-up also schedule work on the host; dibs does not read their ledgers.
Jobs inherit the working directory and environment, and pass stdin, stdout and
stderr through unchanged. `DIBS_LEASE` identifies the job.
`--axi` and `--json` change only pre-start errors, not child output. Use noninteractive
commands. Dibs does not hand terminal foreground job control to the child.

Admission is FIFO across classes, with no backfill: a smaller request cannot
bypass an earlier larger waiter. The default wait is `30m`; `--wait 0` tries
once. A wait that exhausts returns 75 without starting user code. Dead or
expired waiters are discarded. Admission also attempts to reclaim dead or
expired job leases before counting their reservations; failed cleanup retains
the reservation. A request larger than the usable budget fails with code 2.
Queue order is the order requests acquire the state lock, not shell launch time.
The state lock is released while waiting and while a child runs.

Each job is a lease owned by the `dibs job` process, not `DIBS_OWNER` or its
invoking shell. Its deadline starts at admission and is the explicit `--timeout`
or class timeout. Job deadlines cannot be extended. On timeout or cancellation,
dibs sends TERM to the process group, then KILL after one second if needed.
Normal completion also cleans up remaining group members before releasing the
reservation. Cleanup failures go to stderr and retain the lease; they never
replace the child's exit code. If dibs itself is killed, `sweep --confirm`
reclaims the job lease on owner death or expiry. Keep a host sweep timer. A
matching live leader permits process-group cleanup; if the leader is missing or
reaped, dibs warns and releases the lease without signaling an unverifiable
group. Orphan descendants may then require manual cleanup.

Process groups are not a security boundary: programs that call `setsid` or
change groups can escape cleanup. Container/cgroup enforcement is not part of
local job admission. Cleanup verifies the recorded group leader's start identity
before signaling and retains the record if identity is unknown or reused while
the leader is present.

### Machine budget and classes

The optional config is strict JSON at `$XDG_CONFIG_HOME/dibs/config.json`, or
`~/.config/dibs/config.json`. `DIBS_CONFIG` selects a different file; an
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
      "agent": "/home/worker/.local/bin/dibs"
    }
  }
}
```

Build a trusted binary for the remote OS and architecture, then install it:

```sh
 GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/dibs-linux .
 dibs machine install lenovo --file /tmp/dibs-linux
 dibs job --machine lenovo --class suite -- go test ./...
 dibs ps --machine lenovo
 dibs sweep --machine lenovo --confirm
```

Installation streams the binary over SSH, checks its version on the remote,
and atomically replaces the configured agent path. A wrong version or
architecture fails without replacing the installed binary. Running agents
keep their executable. Every connection checks the remote version before
requesting work, and the agent checks the client's version again. Mismatches
refuse to start a child and explain how to install the matching binary.

The same dibs binary runs as `dibs agent` on the remote, once per SSH session.
It is not a daemon. The remote config decides classes, total budget, owner room,
and admission across all clients. The client does not forward its budget or
class defaults. Remote state stays on the remote machine, under that SSH user's
usual dibs state directory. `ps --machine` and `sweep --machine` use that state.
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
run. This code avoids reporting dibs's own 1 or 2 after a child may have started.
A completed child can itself return 76, as with any reserved job exit code.

For a Lenovo rollout, provision key-based SSH and trusted host keys, a remote
checkout and its dependencies, the matching Linux binary, and a remote dibs
config with a budget partition that leaves owner room and avoids other
schedulers. Configure an sshd keepalive policy and a per-user sweep timer.
Kubernetes-backed machines remain deferred.

## Owners and renewal

`--owner` overrides `DIBS_OWNER`. Without either, dibs uses the parent PID,
usually the invoking shell. A spawner can export `DIBS_OWNER` for all of its
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
- `pr:OWNER/REPO#N`, `pr:HOST/OWNER/REPO#N`, or
  `pr:https://HOST/OWNER/REPO/pull/N` follows a GitHub pull request. It is off
  until you opt in; see below.
- `exec:<command>` follows a liveness command that any tool supplies. It is off
  until you opt in; see below.

### Exec owners

The `exec` owner is off by default. Turn it on per machine with
`"exec_owner": true` in the user config, or with `DIBS_EXEC_OWNER=1`; the
variable overrides the config either way. While it is off, `--owner exec:...`
fails with code 2, and an already recorded `exec` lease reads as unknown. Only
expiry then reclaims an unprotected time-bound lease; shell, owner-bound, hybrid,
and `--protect-live-owner` leases are kept while their owner is unknown.

Dibs runs the command with `/bin/sh -c` from `/`, with stdin from `/dev/null`,
output discarded, and a 10-second timeout, in its own process group so a timeout
stops everything it started. It runs once per owner check, so a list, sweep, or
explain runs every `exec` lease's command once.

| Probe result | Liveness |
| --- | --- |
| Exit 0 | alive |
| Exit 1 through 125 | dead |
| Timeout, a signal, exit 126 (cannot execute) or 127 (not found), or a failure to start | unknown |

`create` and `adopt` require the probe to report alive. Use absolute paths, and
keep secrets out of the command: it is stored in the lease and shown as the owner.

### Pull request owners

The `pr` owner is off by default. Turn it on per machine with `"pr_owner": true`
in the user config, or with `DIBS_PR_OWNER=1`. `DIBS_PR_OWNER` overrides the
config either way, so `DIBS_PR_OWNER=0` turns a config opt-in off. Project files
cannot enable it. While it is off, `--owner pr:...` fails with code 2, and an
already recorded `pr` lease reads as unknown. Only expiry then reclaims an
unprotected time-bound lease; shell, owner-bound, hybrid, and
`--protect-live-owner` leases are kept while their owner is unknown.

Dibs reads the pull request with one `gh api` GET per owner check, using your
existing `gh` authentication for that host. It never writes to GitHub and never
passes or logs a token. Each check has a 10-second timeout and no retries. A
sweep, list, or extend costs about one API call per `pr` lease on this machine.
GitHub Enterprise hosts work through `gh api --hostname`.

- Open, including draft, means alive. Closed or merged means dead.
- Any API error, timeout, rate limit, authentication failure, missing `gh`,
  malformed response, or 404 means unknown. GitHub hides private resources you
  cannot read behind a 404, so a 404 is not proof that the PR is gone.
- `create` requires an open PR and pins the host, the PR's `node_id`, and its
  base repository's `node_id`. A later response with a different identity, such
  as a new PR at the same path, means unknown. A renamed or transferred
  repository keeps working because GitHub redirects the old path.

TTL and sweep stay the backstop. Expiry still reclaims an open PR's lease, so
extend it on the usual cadence. Dibs sees only the state at each check. It
misses a close and reopen between two checks, and reopening a PR does not bring
back a lease that sweep already reclaimed.

```sh
export DIBS_PR_OWNER=1
dibs create --adapter compose --file compose.yaml \
  --owner pr:https://github.com/OWNER/REPO/pull/123 --ttl 8h
dibs sweep --confirm   # after the PR closes or merges
```

Touch a heartbeat file at most every staleness/3. Extend a lease at the latest
when one third of its TTL remains. A lease is reclaimable when its owner is
dead **or** when it has expired. A live owner that misses either cadence loses
its resources, except shell leases and protected leases. Unknown liveness alone
never permits reclaim for shell leases or protected leases; other time-bound
leases may still be reclaimed at expiry. `extend` refuses expired leases and owners that are dead or unknown,
except that a protected lease with a live owner reattaches after expiry while
its resource is present (see below).

```sh
 touch /tmp/my-lab-heartbeat
 dibs create --owner heartbeat:/tmp/my-lab-heartbeat --stale 15m --ttl 1h
 # The owner touches the file at least every 5m, and extends before 20m remain.
 dibs extend <id> --ttl 1h
```

### Protect a live owner's lease

A supervisor that renews leases for long-lived workers can opt a lease out of
the expiry backstop. Protection is off by default. Turn it on per machine with
`"live_owner_protection": true` in the user config, or with
`DIBS_LIVE_OWNER_PROTECTION=1`; the variable overrides the config either way.
Project files cannot enable it. While it is off, `--protect-live-owner` fails
with code 2. Turning it off later does not unprotect leases already recorded
with it.

Pass `--protect-live-owner` to `create` or `adopt`. Then sweep reclaims the lease only when its owner is proven dead. An owner that is
alive or unknown keeps the lease, even after expiry, so a supervisor outage
never costs a running worker its resources. Leases without the flag behave
exactly as above.

```sh
 export DIBS_LIVE_OWNER_PROTECTION=1
 id=$(dibs create --adapter kind --owner "pid:$worker_pid" --ttl 30m \
   --protect-live-owner --quiet)
 dibs extend "$id" --ttl 30m       # status: renewed, or reattached after expiry
 dibs release "$id" --confirm      # explicit release needs nothing extra
```

Use the worker's own process, session, or heartbeat as the owner. If the
supervisor's PID is the owner, or the supervisor touches the heartbeat, its
outage reads as owner death.

`extend` on a protected lease with a live owner succeeds before expiry
(`status: renewed`). After expiry it reattaches the same lease (`status:
reattached`) only when the adapter proves the pinned resource is still present.
For kind, k3d, and Compose, the cluster or project must still be visible in the
pinned Docker context, at the pinned endpoint for adopted resources. A renew
never creates a lease. A refusal for one of these reasons exits 3, leaves the
lease unchanged, and names the `reason` in AXI and JSON output; any other
refusal, such as a lease on another machine, exits 1 as for any lease:

| `reason` | Meaning | Next step |
| --- | --- | --- |
| `owner_dead` | The owner is proven dead | `dibs release <id> --confirm` or `dibs sweep --confirm` |
| `owner_unknown` | The owner cannot be verified | Retry later; the lease is kept and not reclaimable |
| `resource_absent` | The expired lease's resource is gone | `dibs release <id> --confirm`, then lease a new resource |
| `resource_unknown` | The resource could not be checked | Retry once the resource tool is reachable |

A protected lease whose owner stays unknown is never reclaimed automatically.
It stays in `dibs ps` until someone releases it. Job reservations cannot be
protected.

Durations use Go syntax such as `30m`, `2h`, or `1h30m`. Every lease has a positive
TTL. `create` defaults to `8h`; `adopt` requires an explicit `--ttl`.
`extend --ttl` sets the expiry to now plus that duration.

### Lease policies

Leases differ: a shell should live while its task lives, and a test lab should
live for a few renewable hours with a hard cap. The policy table decides each
new lease's lifetime by resource kind, optionally narrowed by owner kind. It is
off by default. Turn it on per machine with `"lease_policies": true` in the user
config, or with `DIBS_LEASE_POLICIES=1`; the variable overrides the config either
way. While it is off, `--idle` and `--max-lifetime` fail with code 2 and name the
setting, and every lease follows its TTL as described above.

| Field | Meaning |
| --- | --- |
| `binding` | `time`: the lease ends at expiry or owner death. `owner`: it lives exactly while its owner is alive; expiry never reclaims it. `hybrid`: owner-bound, and it also ends after `idle` or `max_lifetime` |
| `ttl` | TTL when `--ttl` is omitted, including for `adopt`; default `8h` |
| `renew` | Advised renewal cadence; `explain` shows `renew_by` |
| `max_lifetime` | Hard cap from creation; live-owner reclamation requires `reclaim_live_owner: true` on a hybrid row; `extend` never passes it |
| `grace` | How long a reclaim condition must hold before dibs acts; for owner death it counts from the first confirmed sweep that saw the owner dead; `reclaim` never starts it |
| `idle` | Hybrid only: reclaim after this idle duration. A live owner is reclaimed only with `reclaim_live_owner: true`; unknown owners are never reclaimed. Shell idle means no foreground job, measured from terminal input/output; a shell without a terminal never goes idle |
| `reclaim_live_owner` | Hybrid only, default `false`: allow idle or max_lifetime to reclaim a shell with a proven-live owner; unknown owners and TTL expiry remain protected |
| `action` | `destroy` (default) reclaims; `report` lists the lease under `held` in `sweep` but never reclaims it automatically. Explicit `release` and `reclaim` still act |

Built-in rows apply when the table is on:

| Row | Binding | TTL | Renew | Max lifetime | Idle | Reclaim live owner |
| --- | --- | --- | --- | --- | --- | --- |
| `shell` | `hybrid` | `8h` | - | - | `30m` | `false` |
| `kind`, `k3d`, `compose` | `time` | `4h` | `2h` | `24h` | - | - |
| `none` | `time` | `8h` | - | - | - | - |

Job deadlines come from job classes, and a `process` row is refused. A row you
configure replaces the built-in row with the same key; omitted fields are off.
Key a row by resource kind, or by `<kind>/<owner kind>`, which takes precedence:

```json
{
  "lease_policies": true,
  "policies": {
    "shell": {"binding": "hybrid", "idle": "45m", "grace": "2m", "reclaim_live_owner": true},
    "kind/pr": {"binding": "owner"},
    "compose": {"binding": "time", "ttl": "2h", "max_lifetime": "12h", "action": "report"}
  }
}
```

An `owner` binding takes no `idle` or `max_lifetime`, a `time` binding takes no
`idle`, and a `hybrid` binding needs one of them. Only adapters that report
activity accept `idle`; today that is `shell`. Unknown fields and invalid rows
fail `create` and `adopt` with code 2.

`--ttl` on `create` or `adopt` must fit within `max_lifetime`. `--idle D` and
`--max-lifetime D` may only tighten the row. Each lease records its effective
policy when it is created, so later config edits, or turning the setting off,
never loosen or tighten an existing lease. `--protect-live-owner` outranks a
policy: only proven owner death reclaims a protected lease.

Shell leases are owner-bound even without a recorded policy: TTL expiry never reclaims them while the owner is alive or unknown. The default shell hybrid idle timeout is inert for a live owner; set `reclaim_live_owner: true` explicitly to permit idle or maximum-lifetime reclamation of a live owner. Unknown owners and busy resources are always retained. `dibs explain`
shows the governing policy (`source: lease`, or `legacy` for a lease acquired
without one), the condition that fired, when the next condition ends
(`reclaim_at`, `remaining`), and why the lease is still held (`held_because`).

## Command reference

| Command | Behavior |
| --- | --- |
| `dibs` | Live home view with executable path, description, lease count, and hints |
| `dibs create` or `dibs up` | Create a lease, optionally with `--adapter kind`, `k3d`, or `compose`; with the consumer contract, `--pending` records it before you build the resource and `--key` makes retries idempotent |
| `dibs adopt <type> <name> --owner KIND:ID --ttl D` | Lease an existing kind, k3d, or Compose resource after verifying existence |
| `dibs adopt <type> <handle> --pending ID` | Consumer contract: register the resource you built for a pending lease |
| `dibs adopt shell <pid> --owner KIND:ID` | Lease a running shell and its helpers by PID and start time (`shell_adapter`) |
| `dibs reconcile [ID] [--confirm]` | Consumer contract: find and bind resources built for pending leases after a crash |
| `dibs reclaim ID [--confirm]` | Consumer contract: reclaim one lease only when its owner is proven dead |
| `dibs explain ID` | Consumer contract: owner liveness, expiry, and whether sweep or reclaim would act, and why |
| `dibs orphans [--context NAME]` | Read-only unleased-resource evidence and adoption command templates |
| `dibs list`, `ls`, `ps`, or `who` | Owner, liveness, age, expiry, machine, and resource description for every lease |
| `dibs env <id>` | Print a shell-safe kubeconfig export for a kind or k3d lease |
| `dibs setup hooks [--remove]` | Install or remove managed agent integrations |
| `dibs extend <id> --ttl D` | Renew an unexpired lease with a live owner; a protected lease may also reattach after expiry |
| `dibs release <id>` or `down` | Preview cleanup; `--confirm` performs it |
| `dibs sweep` | Preview reclaim of dead-owner or expired leases; `--confirm` performs it |
| `dibs job [--machine NAME] [flags] -- <cmd...>` | FIFO machine-budget admission, deadline, and child exit-code passthrough |
| `dibs machine install <name> --file <binary>` | Version-checked SSH agent installation or upgrade |
| `dibs agent` | Internal SSH protocol with session-as-liveness |

`list` and `sweep` also accept `--machine NAME` to operate on that SSH machine.

Commands accept `--help`, `--axi`, and `--json`, except the internal `agent`
protocol. Human output is the terminal default. AXI and JSON have explicit empty
states and structured errors on stdout with short help pointers. Structured errors
include a symbolic `code` and the numeric `exit_code`. Codes are
`VALIDATION_ERROR`, `COMMAND_FAILED`, and `NO_CAPACITY`; actual exit codes do not
change. Refusals that exit 3 also include `reason`, and consumer contract output
includes `contract`. Explicit help requests also use structured output in these modes.

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
There are no prompts. Unknown dibs arguments
and flags fail with code 2 and help before state or resource operations run;
everything after a job's required `--` belongs to the child.

`create --adapter none` records a metadata-only lease. It does not manage or
delete an external resource. Labels are repeatable `--label key=value` metadata.
`list --owner KIND:ID` filters by canonical owner identity, resolving relative
heartbeat paths, numeric PID spellings, and PR selector forms (lowercased
`HOST/OWNER/REPO#N`) without checking owner liveness.

### AXI exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success, including already absent releases and empty sweeps |
| `1` | Dibs operation failed; cleanup records are retained on resource failure |
| `2` | Invalid command, argument, or flag; no child was started |
| `3` | `dibs extend` refused a protected lease; with the consumer contract on, any refusal by a contract command, `extend`, or `release`. Nothing changed; see `reason` |
| Child's code | `dibs job` when its child runs to completion |
| `124` | `dibs job` deadline killed the child (coreutils `timeout` convention) |
| `75` | `dibs job`: no slot frees within `--wait`, `EX_TEMPFAIL` |
| `128 + signal` | Child terminated by a signal, e.g. 143 for SIGTERM |
| `125` | Job child started, but its wait status could not be obtained |
| `76` | SSH transport or protocol failed; the remote result is unknown |

`dibs job` uses dibs's own codes 1 and 2 only when no user child was started.
Child completion passes through the child's code even when it overlaps these
numbers, including 1, 2, 75, and 124. `dibs job --help` documents this contract.
After start, cleanup errors never override the child's result. Signals and
startup failures of the gated execution helper may return 125 or 126; a helper
execution failure is reported on stderr.

### Demo expectations

On a terminal, `dibs ps` starts with a `jobs:` budget summary and then an aligned
lease table, or `No leases.`. The no-args home view also names the executable and
describes dibs. A kind or k3d lease includes its kubeconfig path. Job rows show
cores and readable memory, and expiries read `in 1h0m0s` rather than a timestamp.

For a 14-core, 32-GiB configuration with 3 cores and 8 GiB of owner room, an empty
machine reports:

```text
jobs: 0 of 11 cores, 0 B of 24.0 GiB in use; 3 cores / 8.0 GiB kept free for you
No leases.
```

With the same config, `dibs job --cores 999 -- true` exits 2 without starting a
child. Its human output is exactly two lines:

```text
Error: requested 999 cores; usable 11 after owner room 3
Try --cores 11 or set DIBS_CONFIG
```

AXI keeps the same message under `error:`, `code: VALIDATION_ERROR`,
`exit_code: 2`, and a short `help` array. Piped
`dibs ps` uses AXI; `dibs ps --json` remains JSON. Byte-for-byte expectations
for list, ps, who, full lease records, truncated lists with counts, structured
help, budget summaries, and errors are in
[`testdata/output`](testdata/output). `go test ./...` also checks real PTY output,
child stdout/stderr passthrough, quiet IDs, shell exports, and setup/removal under
a temporary HOME. To update snapshots after an intentional format change, run
`UPDATE_GOLDEN=1 go test ./...` and review the fixture diff.

## Sweep from a host timer

Dibs does not sweep by itself. Run `dibs sweep --confirm` from cron, a systemd
timer, launchd, or your session teardown. Set the same absolute `DIBS_STATE`
and use an absolute executable path in the timer. Sweep frequency determines
how long a dead or expired lease can retain resources. Preview first to check
which resources will be deleted.

## State and safety

`DIBS_STATE` defaults to `$XDG_STATE_HOME/dibs`, or
`~/.local/state/dibs` when XDG is unset. State files and snapshots are private to
the invoking user. State contains resolved Compose values, which may include
secrets. Keep this directory on a local filesystem, not a network share.

State schema version 2 adds explicitly adopted resource identities and stable
machine identities. Any command that opens version 1 state under the state lock
migrates it to version 2, preserving existing lease records (see below).
Read-only `orphans` inventory reads a snapshot without migrating it. Older
binaries reject version 2 rather than interpreting adopted names as generated
ones. Keep a compatible binary while leases remain. State records the version
of the dibs binary that last saved it as `writer_version`. A binary that
records `writer_version` and finds a state version newer than it supports exits
1, keeps the state unchanged, and names the version found, the recorded writer
version, and the newest version it supports. Upgrade dibs, and run
`which -a dibs berth` to find a stale binary earlier on `PATH`. Binaries built
before `writer_version` existed report `unsupported or incomplete state format`
for the same state and do not name the versions.

State schema version 3 is version 2 plus at least one lease with live-owner
protection. Older binaries reject it rather than ignoring the protection and
reclaiming the lease at expiry. The state returns to version 2 once no
protected lease remains, unless a keyed or pending lease keeps it at version 4.

State schema version 4 adds the consumer contract's idempotency keys and pending
leases. Dibs writes version 4 while any lease has a key or is pending, version 3
while only protected leases remain, and version 2 otherwise. A binary that reads
up to version 3 would drop keys and pending records when it saved, so it
refuses version 4 instead.

State schema version 5 adds recorded lease policies, owner-death stamps for grace
periods, and shell leases. Dibs writes version 5 while any lease has one of
them, and the lower versions above otherwise. A binary that reads up to version
4 would reclaim an owner-bound lease at expiry, so it refuses version 5 instead.

A stable `lock` file serializes ordinary readers and writers through kernel `flock`.
`orphans` reads an atomic state snapshot without locks or writes. Its answer is
point-in-time evidence, not a reservation. `adopt` rechecks ownership under the
state lock.
Each state change writes and syncs a temporary file, renames it over `state.json`,
and syncs the directory. Dibs saves cleanup intent **before** starting a
resource. If dibs dies midway through create or delete, the next release or
eligible sweep retries the same unique resource. Kernel locks end on process
death. Corrupt or unsupported state is an error, never an empty lease list.

Each new lease records a stable machine identity: `darwin:<IOPlatformUUID>` on
macOS or `linux:<machine-id>` on Linux, normalized to lowercase hexadecimal
without hyphens. Hostname changes no longer change lease ownership. An unavailable
or invalid OS identity is an error; dibs never falls back to a hostname.
Cloned hosts must have distinct OS machine identities. On minimal Linux hosts
without `/etc/machine-id`, provision a unique persistent ID before running dibs
(for example, `systemd-machine-id-setup` on systemd hosts). Dibs does not generate
or modify OS identities. Keep state on its original host, and do not copy machine
IDs between hosts.

Opening existing state migrates schema version 1 to version 2 atomically under
the state lock, including during previews and listing. Legacy lease and queue
records with the current hostname receive the stable identity. Other legacy
hostnames remain foreign until explicitly identified by you. Fully migrated
version 2 state does not require a hostname lookup. Older binaries that only
support schema version 1 cannot read version 2 state; use a schema-compatible
binary for every invocation. The naming migration does not change the schema
or bypass machine-identity migration.

If this machine was renamed before upgrading, first verify that the old hostname
belongs to this machine and that you are using its original local state directory.
Then authorize that exact legacy hostname through the invocation's environment:

```sh
DIBS_LEGACY_MACHINE=Mac.localdomain dibs sweep  # migrate and preview
dibs sweep --confirm                              # separate cleanup decision
```

`DIBS_LEGACY_MACHINE` is an explicit same-user attestation, not automatic proof
from the old ledger. Never set it from a project file or use it for another host.
The migration persists the stable identity, so later commands need no alias.
The alias affects only hostname records, never another host's stable identity.
Unrecognized legacy names remain unknown and cannot be reclaimed, even if expired.

A host never acts on another machine's leases, even after expiry. Expiry overrides
unknown **owner** liveness only after machine ownership matches. Run cleanup on
the original machine with its original state path, directly or through
`dibs sweep --machine <name> --confirm`. Human, AXI, and JSON output all use the
same machine identity and reclamation decisions.

Adapters pin the Docker context selected at creation. `DOCKER_HOST` overrides
are rejected; select a named context instead. Keep that context's endpoint
unchanged while leases exist. Dibs never sweeps unleased resources. This slice
does not reconstruct leases after state loss; keep the state directory until
all managed resources are released. Small resource snapshots remain after
release for diagnosis and may be removed once their leases are absent.

Dibs is a cooperation protocol, not a security boundary for programs with the
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
PROOF=$(mktemp -d "${TMPDIR:-/tmp}/dibs-proof.XXXXXX")
.claude/skills/verify/verify.py all --evidence "$PROOF/all"
```

The helper needs Python 3 and the real adapter/SSH tools; missing prerequisites
fail rather than skip. CI runs these workflows and uploads `dibs-verification`
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
With the consumer contract on, they also record pending leases, build each
resource out of band as a crashed caller would, then reconcile or register it,
check that `reclaim` refuses the live owner, and reclaim it after the owner dies.
The shell test starts a real `bash` login shell on a pseudo-terminal with a
background helper, adopts it under an `exec` owner, shows that reclaim refuses
while the owner lives and while a foreground job runs, and shows that sweep
removes the shell and its helper once the owner is gone; it needs `bash`.
Missing adapter tools, Docker, or Compose fail the command rather
than skip; failed cleanup retains the state directory and reports its path.

`just ssh-integration` runs real SSH end-to-end tests against a disposable
Alpine container with OpenSSH and procps. It requires Docker, SSH, ssh-keygen,
and Go. It fails rather than skips when a prerequisite is absent. Tests use
isolated keys and a strict private known-hosts file. They cover quoting, binary
output, remote budget and owner room, child exit codes including 255, deadline
124, wait 75, session disconnect cleanup, remote listing and sweep, version
refusal, atomic upgrades, and transport failure 76.

`just pr-owner-integration` drives the opt-in `pr` owner against the real,
read-only GitHub API with stable public pull requests: open and draft PRs stay
alive, closed and merged PRs are dead and swept, and a mismatched identity is
unknown and kept. It needs `gh` with authentication or `GH_TOKEN`, and fails
rather than skips without them.

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
and macOS archives before publishing a release marked Latest ([#29](https://github.com/Dematom1/dibs/issues/29)).
macOS Developer ID signing is disabled until signing credentials and identity
are configured. Prebuilt macOS binaries are not Developer ID signed and may be
quarantined by Gatekeeper. Prefer `go install github.com/Dematom1/dibs@latest`;
for a downloaded binary, verify its trusted release checksum before removing
quarantine from that binary with `xattr -d com.apple.quarantine /path/to/dibs`.
Do not disable Gatekeeper globally.

Build with `nix build`, or use
`nix-build default.nix -A packages.x86_64-linux.default` without flakes.
The flake provides `packages.<system>.dibs` for `x86_64-linux`, `aarch64-linux`,
and `aarch64-darwin` for NixOS or home-manager package lists. Its binary reports
the version in `.release-please-manifest.json`.

## License

Licensed under [Apache-2.0](LICENSE). See [NOTICE](NOTICE).
