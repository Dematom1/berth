package cli

import (
	"context"
	"flag"
	"io"
	"os"

	configpkg "github.com/Dematom1/berth/internal/config"
	outputpkg "github.com/Dematom1/berth/internal/output"
	remotepkg "github.com/Dematom1/berth/internal/remote"
)

func (app application) runMachine(ctx context.Context, args []string, out io.Writer, jsonOutput outputpkg.Mode) int {
	fs := flag.NewFlagSet("machine install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	file := fs.String("file", "", "trusted binary built for the remote OS/architecture")
	wantHelp := fs.Bool("help", false, "help")
	outputpkg.Flags(fs, &jsonOutput)
	if len(args) == 1 && args[0] == "--help" {
		emitHelp(out, jsonOutput, "machine")
		return 0
	}
	if len(args) < 1 || args[0] != "install" {
		return outputpkg.Error(out, jsonOutput, 2, "use berth machine install <name> --file <binary>", help("machine"))
	}
	if err := parseFlags(fs, args[1:]); err != nil {
		return outputpkg.Error(out, jsonOutput, 2, err.Error(), help("machine"))
	}
	if *wantHelp {
		emitHelp(out, jsonOutput, "machine")
		return 0
	}
	if fs.NArg() != 1 || *file == "" {
		return outputpkg.Error(out, jsonOutput, 2, "machine name and --file are required", help("machine"))
	}
	config, err := configpkg.Load()
	if err != nil {
		return outputpkg.Error(out, jsonOutput, 2, err.Error(), help("machine"))
	}
	m, err := remotepkg.LookupTarget(config, fs.Arg(0))
	if err != nil {
		return outputpkg.Error(out, jsonOutput, 2, err.Error(), help("machine"))
	}
	binary, err := os.Open(*file)
	if err != nil {
		return outputpkg.Error(out, jsonOutput, 1, err.Error(), help("machine"))
	}
	defer binary.Close()
	info, err := binary.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return outputpkg.Error(out, jsonOutput, 2, "--file must be a nonempty regular binary file", help("machine"))
	}
	if err := remotepkg.Install(ctx, app.version, m, binary); err != nil {
		return outputpkg.Error(out, jsonOutput, 1, "remote installation failed; check SSH diagnostics and binary OS/architecture/version", help("machine"))
	}
	outputpkg.Emit(out, jsonOutput, map[string]any{"machine": fs.Arg(0), "version": app.version, "status": "installed", "help": []string{"Run berth ps --machine " + fs.Arg(0)}})
	return 0
}
