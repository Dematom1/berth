package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const exitCodes = `Exit codes: 0 success (including no-ops), 1 operation failed, 2 invalid usage.
Reserved for future berth job: child's exit code on completion; 124 deadline
killed child; 75 (EX_TEMPFAIL) no slot within wait. Berth's own 1/2 apply only
when no child was started. The job command is not implemented in this slice.`

const cadence = `Touch heartbeat files at most every staleness/3. Extend a lease at the latest
when one third of its TTL remains. Owner death OR expiry makes a lease
reclaimable: a live owner missing either cadence loses its resources.`

var commandHelp = map[string]string{
	"create": `berth create [--owner KIND:ID] [--ttl 8h] [--stale 15m]
  [--resources TEXT] [--label key=value ...] [--adapter none|kind|k3d|compose]
  [--file PATH (required for compose)] [--json]
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
	"list": `berth list [--owner KIND:ID] [--json]
List every recorded lease with owner liveness, age, expiry, machine, resource
identity, and description. --json includes full records and labels.
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
	"sweep": `berth sweep [--confirm] [--json]
Preview by default. --confirm reclaims on owner dead OR expiry. Unknown owners
are retained unless expired. Foreign-machine leases are always retained, even
when expired. Failed cleanup retains its lease. No unleased resources are touched.
Run this command from a host timer for automatic reclamation; no daemon runs.
Examples:
  berth sweep
  berth sweep --confirm --json`,
	"job": `berth job --help
Reserved for the later job/admission slice; no child is started by this version.`,
}

type labelsFlag map[string]string

func (l labelsFlag) String() string { return "key=value" }
func (l labelsFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || strings.TrimSpace(k) == "" {
		return errors.New("--label requires key=value")
	}
	l[k] = v
	return nil
}

// Standard flag parsing, with positional arguments moved after flag values so
// both `extend ID --ttl 1h` and `extend --ttl 1h ID` have the same meaning.
func parseFlags(fs *flag.FlagSet, args []string) error {
	var options, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		options = append(options, a)
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		f := fs.Lookup(name)
		if f == nil {
			return fmt.Errorf("unknown flag %s", a)
		}
		boolean, isBool := f.Value.(interface{ IsBoolFlag() bool })
		if !hasValue && !(isBool && boolean.IsBoolFlag()) {
			i++
			if i == len(args) {
				return fmt.Errorf("flag --%s needs a value", name)
			}
			options = append(options, args[i])
		}
	}
	return fs.Parse(append(options, positional...))
}

func help(command string) string {
	body, ok := commandHelp[command]
	if !ok {
		body = `berth: owner-and-expiry leases for local resources (macOS and Linux).
Commands: create (up), list (ls, ps, who), extend, release (down), sweep.
Global flags: --json, --help. Version: --version, -v, -V.
Environment: BERTH_OWNER; BERTH_STATE (default $XDG_STATE_HOME/berth or
~/.local/state/berth). State is local to one host and one Unix user.
Examples:
  berth
  berth create --adapter kind --ttl 1h --resources '2 CPUs, test lab'
  berth sweep --confirm
Run berth <command> --help for command flags.`
	}
	if command == "create" || command == "extend" {
		body += "\n\n" + cadence
	}
	return body + "\n\n" + exitCodes + "\n"
}

func run(ctx context.Context, args []string, out io.Writer) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v" || args[0] == "-V") {
		fmt.Fprintln(out, version)
		return 0
	}
	command, home := "list", len(args) == 0
	topHelp := len(args) == 1 && args[0] == "--help"
	// --json is global and may precede the command.
	jsonOutput := false
	if len(args) > 0 && args[0] == "--json" {
		jsonOutput, args = true, args[1:]
		home = len(args) == 0
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	switch command {
	case "up":
		command = "create"
	case "ls", "ps", "who":
		command = "list"
	case "down":
		command = "release"
	}
	if command == "help" {
		if len(args) > 1 {
			return emitError(out, jsonOutput, 2, "help takes at most one command", help(""))
		}
		if len(args) == 1 {
			if _, ok := commandHelp[args[0]]; !ok {
				return emitError(out, jsonOutput, 2, "unknown command", help(""))
			}
			fmt.Fprint(out, help(args[0]))
		} else {
			fmt.Fprint(out, help(""))
		}
		return 0
	}
	if _, ok := commandHelp[command]; !ok {
		return emitError(out, jsonOutput, 2, "unknown command "+command, help(""))
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	wantHelp := fs.Bool("help", false, "show help")
	fs.BoolVar(&jsonOutput, "json", jsonOutput, "JSON output")
	var owner, resources, driver, file string
	var ttl, stale time.Duration
	var confirm bool
	labels := labelsFlag{}
	if command == "create" || command == "list" {
		fs.StringVar(&owner, "owner", "", "owner selector")
	}
	if command == "create" {
		fs.DurationVar(&ttl, "ttl", 8*time.Hour, "lease lifetime")
		fs.DurationVar(&stale, "stale", 15*time.Minute, "heartbeat staleness")
		fs.StringVar(&resources, "resources", "", "resource description")
		fs.StringVar(&driver, "adapter", "none", "managed resource adapter")
		fs.StringVar(&file, "file", "", "compose file")
		fs.Var(labels, "label", "lease metadata key=value (repeatable)")
	}
	if command == "extend" {
		fs.DurationVar(&ttl, "ttl", 0, "new lifetime from now")
	}
	if command == "sweep" || command == "release" {
		fs.BoolVar(&confirm, "confirm", false, "perform cleanup")
	}
	if err := parseFlags(fs, args); err != nil {
		return emitError(out, jsonOutput, 2, err.Error(), help(command))
	}
	if *wantHelp {
		if topHelp {
			fmt.Fprint(out, help(""))
		} else {
			fmt.Fprint(out, help(command))
		}
		return 0
	}
	if command == "job" {
		return emitError(out, jsonOutput, 2, "berth job is not implemented yet; no child started", help("job"))
	}
	expected := 0
	if command == "extend" || command == "release" {
		expected = 1
	}
	if fs.NArg() != expected || (expected == 1 && !leaseID.MatchString(fs.Arg(0))) {
		return emitError(out, jsonOutput, 2, "invalid or unexpected arguments", help(command))
	}
	if (command == "create" || command == "extend") && ttl <= 0 {
		return emitError(out, jsonOutput, 2, "--ttl must be positive", help(command))
	}
	if command == "create" && ((driver != "none" && driver != "kind" && driver != "k3d" && driver != "compose") || (driver == "compose" && file == "") || (driver != "compose" && file != "")) {
		return emitError(out, jsonOutput, 2, "use --adapter none, kind, k3d, or compose; --file is required only for compose", help(command))
	}
	now := time.Now().UTC()
	var newOwner Owner
	if command == "create" {
		if owner == "" {
			owner = os.Getenv("BERTH_OWNER")
		}
		if owner == "" {
			owner = "pid:" + strconv.Itoa(os.Getppid())
		}
		var err error
		newOwner, err = parseOwner(owner, stale, now)
		if err != nil {
			return emitError(out, jsonOutput, 2, err.Error(), help(command))
		}
	}
	dir, err := stateDir()
	if err != nil {
		return emitError(out, jsonOutput, 1, err.Error(), "Set BERTH_STATE to a writable private directory and retry berth "+command)
	}
	s, err := openStore(ctx, dir)
	if err != nil {
		return emitError(out, jsonOutput, 1, err.Error(), "Check BERTH_STATE permissions and state.json; retry berth "+command)
	}
	defer s.Close()
	host, err := machine()
	if err != nil {
		return emitError(out, jsonOutput, 1, err.Error(), "Retry berth "+command+" after the hostname is available")
	}
	// Lock acquisition may wait behind a resource operation; evaluate expiry now, not before that wait.
	now = time.Now().UTC()
	var result map[string]any
	switch command {
	case "list":
		if kind, id, ok := strings.Cut(owner, ":"); ok && id != "" {
			switch kind {
			case "pid", "session":
				if pid, err := strconv.ParseInt(id, 10, 32); err == nil && pid > 0 {
					owner = kind + ":" + strconv.FormatInt(pid, 10)
				}
			case "heartbeat":
				if path, err := filepath.Abs(id); err == nil {
					owner = kind + ":" + path
				}
			}
		}
		views := []map[string]any{}
		full := []Lease{}
		for _, id := range s.IDs() {
			l := s.Leases[id]
			if owner != "" && l.Owner.String() != owner {
				continue
			}
			_, live := reclaimable(l, host, now, checkOwner)
			views = append(views, leaseView(l, live, now))
			full = append(full, l)
		}
		result = map[string]any{"count": len(views), "leases": views, "help": []string{"Run berth create --help to lease a resource", "Run berth sweep to preview reclamation"}}
		if jsonOutput {
			result["records"] = full
		}
		if home {
			bin, _ := os.Executable()
			if homeDir, err := os.UserHomeDir(); err == nil && strings.HasPrefix(bin, homeDir+string(filepath.Separator)) {
				bin = "~" + strings.TrimPrefix(bin, homeDir)
			}
			result["bin"], result["description"] = bin, "Owner-and-expiry leases for local stacks and clusters"
		}
	case "create":
		if checkOwner(newOwner, now) != Alive {
			err = errors.New("owner became stale or unavailable while waiting; refresh the owner and retry berth create")
			break
		}
		l, createErr := newLease(newOwner, ttl, resources, labels, now)
		if createErr != nil {
			err = createErr
			break
		}
		if err = prepareResource(ctx, s, &l, driver, file, runCommand); err != nil {
			break
		}
		s.Leases[l.ID] = l
		if err = s.Save(); err != nil {
			break
		}
		if err = adapters()[driver].Create(ctx, l); err != nil {
			err = fmt.Errorf("creation incomplete; lease %s retained for cleanup: %w", l.ID, err)
			break
		}
		result = map[string]any{"lease": leaseView(l, Alive, now), "help": []string{"Run berth extend " + l.ID + " --ttl " + ttl.String(), "Run berth release " + l.ID + " --confirm to reclaim"}}
		if jsonOutput {
			result["record"] = l
		}
	case "extend":
		id := fs.Arg(0)
		l, exists := s.Leases[id]
		if !exists {
			err = errors.New("lease does not exist; use berth create for a new lease")
			break
		}
		if err = extendLease(&l, host, ttl, now, checkOwner); err == nil {
			s.Leases[id] = l
			err = s.Save()
			result = map[string]any{"lease": leaseView(l, Alive, now)}
		}
	case "release":
		id := fs.Arg(0)
		_, exists := s.Leases[id]
		status := "already absent (no-op)"
		if exists {
			status = "preview; rerun with --confirm to reclaim"
		}
		if exists && confirm {
			_, err = releaseLease(ctx, s, id, host, adapters())
			if err == nil {
				status = "released"
			}
		}
		result = map[string]any{"id": id, "status": status}
	case "sweep":
		r := sweep(ctx, s, host, now, confirm, checkOwner, adapters())
		result = map[string]any{"confirmed": confirm, "eligible": r.Eligible, "reclaimed": r.Reclaimed, "unknown": r.Unknown}
		if !confirm {
			result["help"] = []string{"Run berth sweep --confirm to reclaim eligible leases"}
		}
		if len(r.Failures) > 0 {
			result["failures"] = r.Failures
			result["error"] = "cleanup failed; leases retained; retry berth sweep --confirm"
			emit(out, jsonOutput, result)
			return 1
		}
	}
	if err != nil {
		return emitError(out, jsonOutput, 1, err.Error(), "Run berth ps to inspect retained leases; retry berth "+command+" --help for options")
	}
	emit(out, jsonOutput, result)
	return 0
}

func leaseView(l Lease, live Liveness, now time.Time) map[string]any {
	return map[string]any{"id": l.ID, "owner": l.Owner.String(), "liveness": string(live), "machine": l.Machine, "age": now.Sub(l.Created).Round(time.Second).String(), "expires": l.Expires.Format(time.RFC3339Nano), "resources": l.Resources, "resource": l.Resource.Adapter + ":" + l.Resource.Name, "resource_adapter": l.Resource.Adapter, "resource_name": l.Resource.Name, "labels": l.Labels}
}
