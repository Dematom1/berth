package cli

import (
	"io"
	"strings"

	outputpkg "github.com/Dematom1/dibs/internal/output"
)

const exitCodes = `Exit codes: 0 success (including no-ops), 1 operation failed, 2 invalid usage.
dibs job: child's exit code on completion; 124 deadline killed child;
75 (EX_TEMPFAIL) no slot within wait. Dibs's own 1/2 apply only when no child
was started. Signal termination returns 128 + signal number. Helper/wait
failures after start use 125/126 and diagnostics on stderr.
76 is SSH transport/protocol failure with unknown remote result (stderr).
Child exit 255 is preserved and is not confused with SSH failure.`

const contractCodes = `Contract 1 (new commands and the --pending/--key flags): exit 0 success with
status; 1 failed, records retained; 2 usage; 3 refused, nothing changed, with a
stable reason: not_found, wrong_machine, not_pending, already_leased,
key_conflict, handle_exists, handle_mismatch, resource_absent, ambiguous,
owner_alive, owner_unknown. Output carries contract: 1. See docs/consumer-contract.md.`

const cadence = `Touch heartbeat files at most every staleness/3. Extend a lease at the latest
when one third of its TTL remains. Owner death OR expiry makes a lease
reclaimable: a live owner missing either cadence loses its resources.`

var commandHelp = map[string]string{
	"adopt": `dibs adopt <kind|k3d|compose> <name> --owner KIND:ID --ttl D [--key K]
  [--context NAME] [--file PATH (required for compose)] [--stale 15m] [--full] [--axi | --json]
dibs adopt <kind|k3d|compose> <handle> --pending ID [--file PATH] [--axi | --json]
Explicitly lease an existing resource. Existence must be verified and no lease
may already own it. Captures cleanup configuration; does not recreate resources.
Owner and positive TTL are required. Release/sweep keep their preview defaults.
--key K: a retry with the same key and request returns status already_done.
--pending ID registers a resource the caller created for a pending lease (see
dibs create --pending); owner, TTL and context come from that lease. A retry
returns status already_done. Statuses: adopted, bound, already_done.
Examples:
  dibs adopt kind my-lab --owner pid:1234 --ttl 1h
  dibs adopt compose my-stack --file compose.yaml --owner pid:1234 --ttl 2h
  dibs adopt kind lab-7 --pending <id>`,
	"reconcile": `dibs reconcile [<id>] [--confirm] [--axi | --json]
Resolve pending leases after a crash between creating a resource and dibs adopt
--pending. A resource matches only by evidence recorded before it existed: the
handle pinned with create --name, or the label io.dibs.lease=<id>. Preview by
default. --confirm binds each single unclaimed match. Per lease: bindable, bound,
absent (nothing created; create it now), ambiguous (several matches, never
bound), conflict (match under another lease), unknown (discovery failed).
With <id>: a lease that is no longer pending reports already_done; ambiguous
and conflict exit 3. Unknown exits 1. Age and names never decide a match.
Examples:
  dibs reconcile
  dibs reconcile <id> --confirm`,
	"reclaim": `dibs reclaim <id> [--confirm] [--axi | --json]
Reclaim one lease only when its owner is proven dead on this machine. Preview by
default. --confirm destroys the resource, verifies it is gone, then forgets the
lease. An alive owner is refused (owner_alive) even after expiry; unknown
liveness is refused (owner_unknown). Expiry-driven reclaim stays with sweep.
A lease already gone is status already_done. Statuses: preview, reclaimed,
already_done.
Examples:
  dibs reclaim <id>
  dibs reclaim <id> --confirm`,
	"explain": `dibs explain <id> [--full] [--axi | --json]
Read-only: the lease, its owner liveness, expiry, machine, whether sweep or
reclaim would act now, and why. Status explained; a missing lease is not_found.
Examples:
  dibs explain <id>
  dibs explain <id> --json`,
	"orphans": `dibs orphans [--context NAME] [--full] [--axi | --json]
Read-only inventory of unleased kind, k3d and Compose resources in the selected
Docker context. Includes labels, creation evidence and adoption command templates.
Unleased does not mean unused: age never authorizes deletion. No state is written.
Examples:
  dibs orphans
  dibs orphans --context orbstack --json`,
	"setup": `dibs setup hooks [--remove] [--axi | --json]
Install or remove managed Claude Code, Codex, Pi and OpenCode integrations.
Sets DIBS_AXI=1 in agent shell environments. Repeated setup is a no-op.
Examples:
  dibs setup hooks
  dibs setup hooks --remove`,
	"env": `dibs env <id>
Print a shell-safe export for a kind/k3d lease's private kubeconfig.
This command always prints shell text, even with --axi or --json.
Examples:
  eval "$(dibs env <id>)"
  dibs env <id>`,
	"create": `dibs create [--owner KIND:ID] [--ttl 8h] [--stale 15m]
  [--resources TEXT] [--label key=value ...] [--adapter {{adapters}}]
  [--file PATH (required for compose)] [--key K] [--pending [--name HANDLE]]
  [--quiet | -q] [--axi | --json]
Owner defaults to DIBS_OWNER, then pid:<parent-pid>. pid and session pin
process start time; session:<holder-pid> follows a session-holding process,
not a Unix SID. heartbeat:<path> must already be a fresh regular file.
kind and k3d create uniquely named clusters; kind uses a private kubeconfig.
compose creates
a uniquely named project with a durable resolved config. none records metadata
only and performs no external cleanup. up is an alias.
--key K: a retry with the same key and request returns status already_done.
--pending records the lease before the caller creates a kind, k3d or Compose
resource in the selected Docker context; the caller then stamps the label
io.dibs.lease=<id> or uses the handle pinned with --name (which must not exist
yet), and runs dibs adopt <type> <handle> --pending <id>. Release and sweep
of a pending lease bind its single match first, so a crash still reclaims.
Statuses: created, recorded, already_done.
Examples:
  dibs create --owner pid:1234 --ttl 2h --resources '2 CPUs, test lab'
  dibs create --adapter kind --owner session:1234 --ttl 1h
  dibs create --adapter compose --file compose.yaml --label task=lab
  dibs create --adapter kind --pending --name lab-7 --key task-7:lab --quiet`,
	"list": `dibs list [--owner KIND:ID] [--machine NAME] [--full] [--axi | --json]
List every recorded lease with owner liveness, age, expiry, machine, resource
identity, and description. --json is always complete. AXI defaults to 100
compact rows and 500-character text previews; --full shows all fields, records,
text and matching leases. Counts and --full hints identify truncated output.
Aliases: ls, ps, who. No arguments shows the live home view.
Examples:
  dibs ps
  dibs who --owner pid:1234 --json`,
	"extend": `dibs extend <id> --ttl D [--json]
Set expiry to now + D (positive Go duration). Only an unexpired lease with a
verified live owner on this machine can be extended. It cannot be resurrected.
Examples:
  dibs extend <id> --ttl 2h
  dibs extend <id> --ttl 30m --json`,
	"release": `dibs release <id> [--confirm] [--json]
Preview by default. --confirm destroys managed resources (including Compose
volumes) and removes the lease only on success. Already absent is success.
A failed cleanup retains the lease for retry. down is an alias.
Examples:
  dibs release <id>
  dibs release <id> --confirm`,
	"sweep": `dibs sweep [--confirm] [--machine NAME] [--json]
Preview by default. --confirm reclaims on owner dead OR expiry. Unknown owners
are retained unless expired. Foreign-machine leases are always retained, even
when expired. Failed cleanup retains its lease. No unleased resources are touched.
Run this command from a host timer for automatic reclamation; no daemon runs.
Examples:
  dibs sweep
  dibs sweep --confirm --json`,
	"agent": `dibs agent
Internal, version-checked JSON protocol over SSH stdin/stdout. EOF on stdin
cancels the session's job and releases its remote reservation. No daemon.
Use dibs job --machine <name> -- <cmd> rather than invoking the protocol.
Examples:
  dibs job --machine lenovo -- go test ./...
  dibs ps --machine lenovo`,
	"machine": `dibs machine install <name> --file <binary> [--json]
Install or upgrade a trusted dibs binary built for the remote OS/architecture.
The remote checks --version before atomic replacement; mismatches retain the
existing binary. Machine config requires host, user, absolute workdir and agent
paths; optional port and identity_file. SSH config aliases are supported.
No source, worktree, environment, credentials, or build tools are synchronized.
Examples:
  GOOS=linux GOARCH=amd64 go build -o /tmp/dibs-linux .
  dibs machine install lenovo --file /tmp/dibs-linux`,
	"job": `dibs job [--machine NAME] [--cores N --memory M --class C --timeout D --wait W] -- <cmd...>
Local by default; --machine selects a configured SSH host. SSH admission,
classes, budget, owner room, state, deadlines and cleanup are remote-authoritative.
Remote jobs use the configured workdir and remote environment; stdin is /dev/null
(the SSH stdin channel is reserved for session liveness). Disconnect cancels the
remote process group and reclaims its lease. No worktree transfer or daemon.
Reserve logical cores and memory minus owner room, FIFO
(no backfill). Defaults: --class default (1 core, up to 1GiB, timeout 1h),
--wait 30m. --cores, --memory and --timeout override class defaults.
Memory: integer bytes or KiB/MiB/GiB/KB/MB/GB. Durations: positive Go durations;
--wait 0 tries once. Oversized requests fail with 2 before starting a child.
DIBS_CONFIG selects strict JSON config; default ~/.config/dibs/config.json
(or $XDG_CONFIG_HOME/dibs/config.json). Budget defaults derive from the host.
Jobs run in a process group owned by the dibs job PID. Use noninteractive
commands; no terminal job-control
handoff is provided. Deadline
or cancellation terminates the group (TERM, then KILL after 1s); sweep reclaims
after owner death or expiry. Children that call setsid can escape the group.
Reservations are cooperative admission, not hard CPU/memory limits.
Child stdin/stdout/stderr pass through unchanged, including with --json;
--json applies only to pre-start errors. Post-start cleanup errors go to stderr
and retain the lease, never replacing the child's exit code.
Examples:
  dibs job --cores 2 --memory 2GiB --timeout 10m -- go test ./...
  dibs job --class suite --wait 0 -- just check`,
}

func help(command string) string {
	body, ok := commandHelp[command]
	if !ok {
		body = `dibs: owner-and-expiry leases for local resources (macOS and Linux).
Commands: create (up), adopt, orphans, list (ls, ps, who), extend, release (down), sweep, job,
reconcile, reclaim, explain, env, setup hooks, agent (internal SSH protocol), machine install.
Global flags: --axi (agents), --json (scripts), --help. No --human flag.
Human output on terminals; AXI when DIBS_AXI=1 or stdout is not a TTY.
--axi and --json are mutually exclusive; either overrides DIBS_AXI.
Version: --version, -v, -V.
Environment: DIBS_OWNER; DIBS_STATE (default $XDG_STATE_HOME/dibs or
~/.local/state/dibs). State is local to one host and one Unix user.
Examples:
  dibs
  dibs create --adapter kind --ttl 1h --resources '2 CPUs, test lab'
  dibs sweep --confirm
Run dibs <command> --help for command flags.`
	}
	body = strings.ReplaceAll(body, "{{adapters}}", strings.Join(newAdapters().CreateNames(), "|"))
	if command == "create" || command == "extend" {
		body += "\n\n" + cadence
	}
	if command != "agent" {
		body += "\nOutput: --axi for agents, --json for scripts (mutually exclusive).\nDefault: human on terminals; AXI for pipes or DIBS_AXI=1.\nLease commands: --full restores complete AXI records/text; JSON is always complete."
	}
	switch command {
	case "create", "adopt", "reconcile", "reclaim", "explain":
		body += "\n\n" + contractCodes
	}
	return body + "\n\n" + exitCodes + "\n"
}

func emitHelp(w io.Writer, mode outputpkg.Mode, command string) {
	outputpkg.Help(w, mode, help(command))
}
