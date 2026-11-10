package cli

import (
	"io"
	"strings"

	outputpkg "github.com/Dematom1/berth/internal/output"
)

const exitCodes = `Exit codes: 0 success (including no-ops), 1 operation failed, 2 invalid usage.
berth job: child's exit code on completion; 124 deadline killed child;
75 (EX_TEMPFAIL) no slot within wait. Berth's own 1/2 apply only when no child
was started. Signal termination returns 128 + signal number. Helper/wait
failures after start use 125/126 and diagnostics on stderr.
76 is SSH transport/protocol failure with unknown remote result (stderr).
Child exit 255 is preserved and is not confused with SSH failure.`

const cadence = `Touch heartbeat files at most every staleness/3. Extend a lease at the latest
when one third of its TTL remains. Owner death OR expiry makes a lease
reclaimable: a live owner missing either cadence loses its resources.`

var commandHelp = map[string]string{
	"setup": `berth setup hooks [--remove] [--axi | --json]
Install or remove managed Claude Code, Codex, Pi and OpenCode integrations.
Sets BERTH_AXI=1 in agent shell environments. Repeated setup is a no-op.
Examples:
  berth setup hooks
  berth setup hooks --remove`,
	"env": `berth env <id>
Print a shell-safe export for a kind/k3d lease's private kubeconfig.
This command always prints shell text, even with --axi or --json.
Examples:
  eval "$(berth env <id>)"
  berth env <id>`,
	"create": `berth create [--owner KIND:ID] [--ttl 8h] [--stale 15m]
  [--resources TEXT] [--label key=value ...] [--adapter {{adapters}}]
  [--file PATH (required for compose)] [--quiet | -q] [--axi | --json]
Owner defaults to BERTH_OWNER, then pid:<parent-pid>. pid and session pin
process start time; session:<holder-pid> follows a session-holding process,
not a Unix SID. heartbeat:<path> must already be a fresh regular file.
kind and k3d create uniquely named clusters; kind uses a private kubeconfig.
compose creates
a uniquely named project with a durable resolved config. none records metadata
only and performs no external cleanup. up is an alias.
Examples:
  berth create --owner pid:1234 --ttl 2h --resources '2 CPUs, test lab'
  berth create --adapter kind --owner session:1234 --ttl 1h
  berth create --adapter compose --file compose.yaml --label task=lab`,
	"list": `berth list [--owner KIND:ID] [--machine NAME] [--full] [--axi | --json]
List every recorded lease with owner liveness, age, expiry, machine, resource
identity, and description. --json is always complete. AXI defaults to 100
compact rows and 500-character text previews; --full shows all fields, records,
text and matching leases. Counts and --full hints identify truncated output.
Aliases: ls, ps, who. No arguments shows the live home view.
Examples:
  berth ps
  berth who --owner pid:1234 --json`,
	"extend": `berth extend <id> --ttl D [--json]
Set expiry to now + D (positive Go duration). Only an unexpired lease with a
verified live owner on this machine can be extended. It cannot be resurrected.
Examples:
  berth extend <id> --ttl 2h
  berth extend <id> --ttl 30m --json`,
	"release": `berth release <id> [--confirm] [--json]
Preview by default. --confirm destroys managed resources (including Compose
volumes) and removes the lease only on success. Already absent is success.
A failed cleanup retains the lease for retry. down is an alias.
Examples:
  berth release <id>
  berth release <id> --confirm`,
	"sweep": `berth sweep [--confirm] [--machine NAME] [--json]
Preview by default. --confirm reclaims on owner dead OR expiry. Unknown owners
are retained unless expired. Foreign-machine leases are always retained, even
when expired. Failed cleanup retains its lease. No unleased resources are touched.
Run this command from a host timer for automatic reclamation; no daemon runs.
Examples:
  berth sweep
  berth sweep --confirm --json`,
	"agent": `berth agent
Internal, version-checked JSON protocol over SSH stdin/stdout. EOF on stdin
cancels the session's job and releases its remote reservation. No daemon.
Use berth job --machine <name> -- <cmd> rather than invoking the protocol.
Examples:
  berth job --machine lenovo -- go test ./...
  berth ps --machine lenovo`,
	"machine": `berth machine install <name> --file <binary> [--json]
Install or upgrade a trusted berth binary built for the remote OS/architecture.
The remote checks --version before atomic replacement; mismatches retain the
existing binary. Machine config requires host, user, absolute workdir and agent
paths; optional port and identity_file. SSH config aliases are supported.
No source, worktree, environment, credentials, or build tools are synchronized.
Examples:
  GOOS=linux GOARCH=amd64 go build -o /tmp/berth-linux .
  berth machine install lenovo --file /tmp/berth-linux`,
	"job": `berth job [--machine NAME] [--cores N --memory M --class C --timeout D --wait W] -- <cmd...>
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
BERTH_CONFIG selects strict JSON config; default ~/.config/berth/config.json
(or $XDG_CONFIG_HOME/berth/config.json). Budget defaults derive from the host.
Jobs run in a process group owned by the berth job PID. Use noninteractive
commands; no terminal job-control
handoff is provided. Deadline
or cancellation terminates the group (TERM, then KILL after 1s); sweep reclaims
after owner death or expiry. Children that call setsid can escape the group.
Reservations are cooperative admission, not hard CPU/memory limits.
Child stdin/stdout/stderr pass through unchanged, including with --json;
--json applies only to pre-start errors. Post-start cleanup errors go to stderr
and retain the lease, never replacing the child's exit code.
Examples:
  berth job --cores 2 --memory 2GiB --timeout 10m -- go test ./...
  berth job --class suite --wait 0 -- just check`,
}

func help(command string) string {
	body, ok := commandHelp[command]
	if !ok {
		body = `berth: owner-and-expiry leases for local resources (macOS and Linux).
Commands: create (up), list (ls, ps, who), extend, release (down), sweep, job,
env, setup hooks, agent (internal SSH protocol), machine install.
Global flags: --axi (agents), --json (scripts), --help. No --human flag.
Human output on terminals; AXI when BERTH_AXI=1 or stdout is not a TTY.
--axi and --json are mutually exclusive; either overrides BERTH_AXI.
Version: --version, -v, -V.
Environment: BERTH_OWNER; BERTH_STATE (default $XDG_STATE_HOME/berth or
~/.local/state/berth). State is local to one host and one Unix user.
Examples:
  berth
  berth create --adapter kind --ttl 1h --resources '2 CPUs, test lab'
  berth sweep --confirm
Run berth <command> --help for command flags.`
	}
	body = strings.ReplaceAll(body, "{{adapters}}", strings.Join(newAdapters().CreateNames(), "|"))
	if command == "create" || command == "extend" {
		body += "\n\n" + cadence
	}
	if command != "agent" {
		body += "\nOutput: --axi for agents, --json for scripts (mutually exclusive).\nDefault: human on terminals; AXI for pipes or BERTH_AXI=1.\nLease commands: --full restores complete AXI records/text; JSON is always complete."
	}
	return body + "\n\n" + exitCodes + "\n"
}

func emitHelp(w io.Writer, mode outputpkg.Mode, command string) {
	outputpkg.Help(w, mode, help(command))
}
