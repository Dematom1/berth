package cli

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

	adapterpkg "github.com/Dematom1/berth/internal/adapter"
	configpkg "github.com/Dematom1/berth/internal/config"
	jobpkg "github.com/Dematom1/berth/internal/job"
	leasepkg "github.com/Dematom1/berth/internal/lease"
	outputpkg "github.com/Dematom1/berth/internal/output"
	ownerpkg "github.com/Dematom1/berth/internal/owner"
	remotepkg "github.com/Dematom1/berth/internal/remote"
)

type application struct {
	version   string
	resources adapterpkg.Registry
	owners    ownerpkg.Registry
	leases    *leasepkg.Manager
}

func Run(ctx context.Context, args []string, out io.Writer, version string) int {
	resources, owners := newAdapters(), newOwners()
	app := application{version, resources, owners, leasepkg.New(leasepkg.Options{Adapters: resources, Owners: owners})}
	return app.runWithOutput(ctx, args, out, outputpkg.Auto)
}
func (app application) leaseError(out io.Writer, mode outputpkg.Mode, command string, err error) int {
	hint := "Run berth ps to inspect retained leases; retry berth " + command + " --help for options"
	var failure *leasepkg.Failure
	if errors.As(err, &failure) {
		switch failure.Stage {
		case leasepkg.ResolveDirectory:
			hint = "Set BERTH_STATE to a writable private directory and retry berth " + command
		case leasepkg.ReadLedger:
			hint = "Check BERTH_STATE permissions and state.json; retry berth " + command
		case leasepkg.ResolveMachine:
			hint = "Retry berth " + command + " after the OS machine identity is available"
		}
	}
	return outputpkg.Error(out, mode, 1, err.Error(), hint)
}
func (app application) runJob(ctx context.Context, args []string, out io.Writer, mode outputpkg.Mode, input io.Reader, ownerKind string) int {
	return jobpkg.Run(ctx, args, input, out, mode, jobpkg.Options{Leases: app.leases, Owners: app.owners, Version: app.version, Usage: help("job"), OwnerKind: ownerKind})
}

func (app application) runWithOutput(ctx context.Context, args []string, out io.Writer, mode outputpkg.Mode) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v" || args[0] == "-V") {
		fmt.Fprintln(out, app.version)
		return 0
	}
	command, home := "list", len(args) == 0
	topHelp := len(args) == 1 && args[0] == "--help"
	// Global output flags may also precede the command.
	jsonOutput := outputpkg.Auto
	for len(args) > 0 {
		name, value, hasValue := strings.Cut(args[0], "=")
		if name != "--json" && name != "--axi" {
			break
		}
		if !hasValue {
			value = "true"
		}
		if err := outputpkg.SetFlag(&jsonOutput, outputpkg.Mode(strings.TrimPrefix(name, "--")), value); err != nil {
			return outputpkg.Error(out, jsonOutput, 2, err.Error(), "Choose --axi for agents or --json for scripts")
		}
		args = args[1:]
		home = len(args) == 0
	}
	topHelp = len(args) == 1 && args[0] == "--help"
	// Remote sessions carry the caller's resolved display mode, not their pipe's mode.
	if mode != outputpkg.Auto {
		out = outputpkg.WithMode(out, mode)
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
			return outputpkg.Error(out, jsonOutput, 2, "help takes at most one command", help(""))
		}
		if len(args) == 1 {
			if _, ok := commandHelp[args[0]]; !ok {
				return outputpkg.Error(out, jsonOutput, 2, "unknown command", help(""))
			}
			emitHelp(out, jsonOutput, args[0])
		} else {
			emitHelp(out, jsonOutput, "")
		}
		return 0
	}
	if _, ok := commandHelp[command]; !ok {
		return outputpkg.Error(out, jsonOutput, 2, "unknown command "+command, help(""))
	}
	if command == "agent" {
		return app.runAgent(ctx, args, os.Stdin, out)
	}
	if command == "setup" {
		return runSetup(args, out, jsonOutput)
	}
	if command == "machine" {
		return app.runMachine(ctx, args, out, jsonOutput)
	}
	if command == "job" {
		return app.runJob(ctx, args, out, jsonOutput, os.Stdin, "pid")
	}
	if command == "adopt" || command == "orphans" {
		return app.runInventory(ctx, command, args, out, jsonOutput)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	wantHelp := fs.Bool("help", false, "show help")
	outputpkg.Flags(fs, &jsonOutput)
	full := fs.Bool("full", false, "complete AXI records, text and list (JSON is always complete)")
	var owner, resources, driver, file, machineName string
	var ttl, stale time.Duration
	var confirm, quiet bool
	labels := labelsFlag{}
	if command == "list" || command == "sweep" {
		fs.StringVar(&machineName, "machine", "", "SSH machine name")
	}
	if command == "create" || command == "list" {
		fs.StringVar(&owner, "owner", "", "owner selector")
	}
	if command == "create" {
		fs.BoolVar(&quiet, "quiet", false, "print only the new lease id")
		fs.BoolVar(&quiet, "q", false, "print only the new lease id")
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
		return outputpkg.Error(out, jsonOutput, 2, err.Error(), help(command))
	}
	if *wantHelp {
		if topHelp {
			emitHelp(out, jsonOutput, "")
		} else {
			emitHelp(out, jsonOutput, command)
		}
		return 0
	}
	expected := 0
	if command == "extend" || command == "release" || command == "env" {
		expected = 1
	}
	if fs.NArg() != expected || (expected == 1 && !leasepkg.ValidID(fs.Arg(0))) {
		return outputpkg.Error(out, jsonOutput, 2, "invalid or unexpected arguments", help(command))
	}
	if (command == "create" || command == "extend") && ttl <= 0 {
		return outputpkg.Error(out, jsonOutput, 2, "--ttl must be positive", help(command))
	}
	if command == "create" {
		if err := app.resources.CheckCreate(driver, file); err != nil {
			return outputpkg.Error(out, jsonOutput, 2, err.Error(), help(command))
		}
	}
	fullCommand := "berth --axi ps"
	hintMachine := machineName
	if hintMachine == "" {
		hintMachine = outputpkg.WriterMachine(out)
	}
	if hintMachine != "" {
		fullCommand += " --machine " + remotepkg.ShellQuote(hintMachine)
	}
	if owner != "" {
		fullCommand += " --owner " + remotepkg.ShellQuote(owner)
	}
	fullCommand += " --full"
	out = &outputpkg.Writer{Writer: out, Mode: jsonOutput.Effective(out), Full: *full, FullCommand: fullCommand}
	if machineName != "" {
		config, err := configpkg.Load()
		if err != nil {
			return outputpkg.Error(out, jsonOutput, 2, err.Error(), help(command))
		}
		remoteArgs := []string{command}
		fs.Visit(func(f *flag.Flag) {
			if f.Name != "machine" && f.Name != "axi" && f.Name != "json" {
				remoteArgs = append(remoteArgs, "--"+f.Name+"="+f.Value.String())
			}
		})

		return remotepkg.Run(ctx, app.version, config, machineName, remoteArgs, out, jsonOutput)
	}
	now := time.Now().UTC()
	var newOwner ownerpkg.Reference
	if command == "create" {
		if owner == "" {
			owner = os.Getenv("BERTH_OWNER")
		}
		if owner == "" {
			owner = "pid:" + strconv.Itoa(os.Getppid())
		}
		var err error
		newOwner, err = app.owners.Bind(ctx, owner, stale, now)
		if err != nil {
			return outputpkg.Error(out, jsonOutput, 2, err.Error(), help(command))
		}
	}
	var err error
	var result map[string]any
	switch command {
	case "list":
		listing, listErr := app.leases.List(ctx, leasepkg.Filter{Owner: owner})
		if listErr != nil {
			return app.leaseError(out, jsonOutput, command, listErr)
		}
		config, configErr := configpkg.Load()
		views := []map[string]any{}
		records := []map[string]any{}
		for _, observed := range listing.Leases {
			l := observed.Lease
			kube := kubeconfigFor(app.resources, l)
			views = append(views, outputpkg.LeaseView(l, observed.Liveness, listing.At, kube))
			records = append(records, outputpkg.LeaseRecord(l, kube))
		}
		budget := map[string]any{"summary": "jobs: budget unavailable (check BERTH_CONFIG)"}
		if configErr == nil {
			budget = outputpkg.BudgetView(config, listing.Reserved)
		}
		hints := []string{"Run berth create --help to lease a resource", "Run berth sweep to preview reclamation"}
		if hintMachine != "" {
			hints = []string{"Run berth job --machine " + remotepkg.ShellQuote(hintMachine) + " -- <cmd> to reserve resources", "Run berth sweep --machine " + remotepkg.ShellQuote(hintMachine) + " to preview reclamation"}
		}
		result = map[string]any{"budget": budget, "count": len(views), "leases": views, "help": hints}
		result["records"] = records
		if home {
			bin, _ := os.Executable()
			if homeDir, err := os.UserHomeDir(); err == nil && strings.HasPrefix(bin, homeDir+string(filepath.Separator)) {
				bin = "~" + strings.TrimPrefix(bin, homeDir)
			}
			result["bin"], result["description"] = bin, "Owner-and-expiry leases for local stacks and clusters"
		}
	case "create":
		l, createErr := app.leases.Create(ctx, leasepkg.CreateRequest{Owner: newOwner, TTL: ttl, Description: resources, Labels: labels, Adapter: driver, File: file})
		if createErr != nil {
			err = createErr
			break
		}
		now = l.Created
		result = map[string]any{"lease": outputpkg.LeaseView(l, ownerpkg.Alive, now, kubeconfigFor(app.resources, l)), "help": []string{"Run berth extend " + l.ID + " --ttl " + ttl.String(), "Run berth release " + l.ID + " --confirm to reclaim"}}
		result["record"] = outputpkg.LeaseRecord(l, kubeconfigFor(app.resources, l))
		if quiet {
			fmt.Fprintln(out, l.ID)
			return 0
		}
	case "env":
		l, exists, getErr := app.leases.Get(ctx, fs.Arg(0))
		if getErr != nil {
			return app.leaseError(out, jsonOutput, command, getErr)
		}
		if !exists {
			return outputpkg.Error(out, jsonOutput, 2, "lease does not exist", "Run berth ps to find a lease")
		}
		path := kubeconfigFor(app.resources, l)
		if path == "" {
			return outputpkg.Error(out, jsonOutput, 2, "lease has no kubeconfig", "Create a kind or k3d lease with berth create --adapter kind or --adapter k3d")
		}
		fmt.Fprintln(out, "export KUBECONFIG="+remotepkg.ShellQuote(path))
		return 0
	case "extend":
		id := fs.Arg(0)
		l, extendErr := app.leases.Extend(ctx, id, ttl)
		err = extendErr
		if err == nil {
			now = l.Expires.Add(-ttl)
			result = map[string]any{"lease": outputpkg.LeaseView(l, ownerpkg.Alive, now, kubeconfigFor(app.resources, l))}
		}
	case "release":
		id := fs.Arg(0)
		released, releaseErr := app.leases.Release(ctx, leasepkg.ReleaseRequest{ID: id, Confirm: confirm})
		err = releaseErr
		status := "already absent (no-op)"
		if released.Exists {
			status = "preview; rerun with --confirm to reclaim"
		}
		if released.Released {
			status = "released"
		}
		result = map[string]any{"id": id, "status": status}
	case "sweep":
		r, sweepErr := app.leases.Sweep(ctx, leasepkg.SweepRequest{Confirm: confirm})
		if sweepErr != nil {
			err = sweepErr
			break
		}
		result = map[string]any{"confirmed": confirm, "eligible": r.Eligible, "reclaimed": r.Reclaimed, "unknown": r.Unknown}
		if !confirm {
			result["help"] = []string{"Run berth sweep --confirm to reclaim eligible leases"}
		}
		if len(r.Failures) > 0 {
			result["failures"] = r.Failures
			result["error"] = "cleanup failed; leases retained; retry berth sweep --confirm"
			result["code"], result["exit_code"] = "COMMAND_FAILED", 1
			retry := "Run berth sweep"
			if hintMachine != "" {
				retry += " --machine " + remotepkg.ShellQuote(hintMachine)
			}
			result["help"] = []string{retry + " --confirm to retry retained leases"}
			outputpkg.Emit(out, jsonOutput, result)
			return 1
		}
	}
	if err != nil {
		return app.leaseError(out, jsonOutput, command, err)
	}
	outputpkg.Emit(out, jsonOutput, result)
	return 0
}
