package cli

import (
	"context"
	"flag"
	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	outputpkg "github.com/Dematom1/dibs/internal/output"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	remotepkg "github.com/Dematom1/dibs/internal/remote"
	"io"
	"strings"
	"time"
)

func adoptionCommand(row map[string]any) string {
	command := "dibs adopt " + row["type"].(string) + " " + remotepkg.ShellQuote(row["name"].(string)) + " --context " + remotepkg.ShellQuote(row["docker_context"].(string))
	if row["type"] == "compose" {
		// Multiple original files need an explicitly resolved single config, not a guessed split on commas.
		file, _ := row["config_files"].(string)
		if file == "" || strings.Contains(file, ",") {
			file = "<resolved-compose-file>"
		}
		command += " --file " + remotepkg.ShellQuote(file)
	}
	return command + " --owner '<owner>' --ttl '<duration>'"
}
func (app application) runInventory(ctx context.Context, command string, args []string, out io.Writer, mode outputpkg.Mode) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	wantHelp := fs.Bool("help", false, "help")
	full := fs.Bool("full", false, "complete AXI lease record")
	outputpkg.Flags(fs, &mode)
	contextName := fs.String("context", "", "Docker context (default selected context)")
	var owner, file, key, pending string
	var ttl time.Duration
	stale := 15 * time.Minute
	if command == "adopt" {
		fs.StringVar(&owner, "owner", "", "required named owner")
		fs.DurationVar(&ttl, "ttl", 0, "required positive lifetime")
		fs.DurationVar(&stale, "stale", stale, "heartbeat staleness")
		fs.StringVar(&file, "file", "", "Compose file")
		fs.StringVar(&key, "key", "", "idempotency key")
		fs.StringVar(&pending, "pending", "", "bind to this pending lease")
	}
	if err := parseFlags(fs, args); err != nil {
		return outputpkg.Error(out, mode, 2, err.Error(), help(command))
	}
	if *wantHelp {
		emitHelp(out, mode, command)
		return 0
	}
	out = &outputpkg.Writer{Writer: out, Mode: mode.Effective(out), Full: *full, FullCommand: "dibs --axi ps --full"}
	var parsedOwner ownerpkg.Reference
	if command == "adopt" {
		d, known := app.resources.Lookup(fs.Arg(0))
		if pending != "" {
			if fs.NArg() != 2 || !known || d.Discover == nil || !adapterpkg.ValidResourceName(fs.Arg(1)) || !leasepkg.ValidID(pending) || owner != "" || ttl != 0 || key != "" || *contextName != "" || (!d.RequiresFile && file != "") {
				return outputpkg.Error(out, mode, 2, "--pending takes type, handle and a lease id; the pending lease already fixes owner, TTL, key and context", help(command))
			}
			return app.adopt(ctx, out, mode, leasepkg.AdoptRequest{Resource: adapterpkg.ResourceRef{Adapter: fs.Arg(0), Name: fs.Arg(1)}, File: file, Pending: pending}, true)
		}
		if fs.NArg() != 2 || !known || d.Discover == nil || !adapterpkg.ValidResourceName(fs.Arg(1)) || owner == "" || ttl <= 0 || d.RequiresFile != (file != "") {
			return outputpkg.Error(out, mode, 2, "require type, name, explicit --owner and positive --ttl; --file is required only for compose", help(command))
		}
		if key != "" && !leasepkg.ValidKey(key) {
			return outputpkg.Error(out, mode, 2, keyRule, help(command))
		}
		var err error
		parsedOwner, err = app.owners.Bind(ctx, owner, stale, time.Now().UTC())
		if err != nil {
			return outputpkg.Error(out, mode, 2, err.Error(), help(command))
		}
	} else if fs.NArg() != 0 {
		return outputpkg.Error(out, mode, 2, "orphans takes no positional arguments", help(command))
	}
	r, err := adapterpkg.ResolveDockerContext(ctx, *contextName, adapterpkg.RunCommand)
	if err != nil {
		return outputpkg.Error(out, mode, 1, err.Error(), "Select a reachable Docker context and retry dibs "+command)
	}
	if command == "adopt" {
		r.Adapter, r.Name = fs.Arg(0), fs.Arg(1)
		return app.adopt(ctx, out, mode, leasepkg.AdoptRequest{Owner: parsedOwner, TTL: ttl, Resource: r, File: file, Key: key}, key != "")
	}
	rows, err := app.leases.Orphans(ctx, r)
	if err != nil {
		return app.leaseError(out, mode, command, err)
	}
	for _, row := range rows {
		row["adopt"] = adoptionCommand(row)
	}
	outputpkg.Emit(out, mode, map[string]any{"count": len(rows), "orphans": rows, "help": []string{"Unleased does not mean unused. Choose an owner and TTL explicitly; age alone never authorizes cleanup."}})
	return 0
}

func (app application) adopt(ctx context.Context, out io.Writer, mode outputpkg.Mode, r leasepkg.AdoptRequest, contract bool) int {
	l, done, err := app.leases.Adopt(ctx, r)
	if err != nil && contract {
		return app.contractError(out, mode, "adopt", r.Pending, err)
	}
	if err != nil {
		return app.leaseError(out, mode, "adopt", err)
	}
	live := ownerpkg.Alive
	if r.Pending != "" {
		// A pending lease's owner was not rechecked here, so report what it is now.
		live = app.owners.Check(ctx, l.Owner, time.Now().UTC()).Liveness
	}
	kube := kubeconfigFor(app.resources, l)
	result := map[string]any{"lease": outputpkg.LeaseView(l, live, l.Created, kube), "record": outputpkg.LeaseRecord(l, kube), "help": []string{"Run dibs release " + l.ID + " to preview cleanup"}}
	if contract {
		status := map[bool]string{false: "adopted", true: "bound"}[r.Pending != ""]
		if done {
			status = "already_done"
		}
		result = contractResult(status, result)
	}
	outputpkg.Emit(out, mode, result)
	return 0
}
