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
	var owner, file string
	var protect bool
	var ttl time.Duration
	stale := 15 * time.Minute
	if command == "adopt" {
		fs.StringVar(&owner, "owner", "", "required named owner")
		fs.DurationVar(&ttl, "ttl", 0, "required positive lifetime")
		fs.DurationVar(&stale, "stale", stale, "heartbeat staleness")
		fs.StringVar(&file, "file", "", "Compose file")
		fs.BoolVar(&protect, "protect-live-owner", false, "reclaim only on proven owner death")
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
		if fs.NArg() != 2 || !known || d.Discover == nil || !adapterpkg.ValidResourceName(fs.Arg(1)) || owner == "" || ttl <= 0 || d.RequiresFile != (file != "") {
			return outputpkg.Error(out, mode, 2, "require type, name, explicit --owner and positive --ttl; --file is required only for compose", help(command))
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
		l, err := app.leases.Adopt(ctx, leasepkg.AdoptRequest{Owner: parsedOwner, TTL: ttl, Resource: r, File: file, ProtectLiveOwner: protect})
		if err != nil {
			return app.leaseError(out, mode, command, err)
		}
		kube := kubeconfigFor(app.resources, l)
		outputpkg.Emit(out, mode, map[string]any{"lease": outputpkg.LeaseView(l, ownerpkg.Alive, l.Created, kube), "record": outputpkg.LeaseRecord(l, kube), "help": []string{"Run dibs release " + l.ID + " to preview cleanup"}})
		return 0
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
