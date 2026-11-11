package job

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	budgetpkg "github.com/Dematom1/dibs/internal/budget"
	configpkg "github.com/Dematom1/dibs/internal/config"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	outputpkg "github.com/Dematom1/dibs/internal/output"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	remotepkg "github.com/Dematom1/dibs/internal/remote"
)

type Options struct {
	Leases    *leasepkg.Manager
	Owners    ownerpkg.Registry
	Version   string
	Usage     string
	OwnerKind string
}

func Run(ctx context.Context, args []string, input io.Reader, out io.Writer, jsonOutput outputpkg.Mode, opts Options) int {
	fs := flag.NewFlagSet("job", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	wantHelp := fs.Bool("help", false, "help")
	outputpkg.Flags(fs, &jsonOutput)
	cores := fs.Int("cores", 0, "requested logical cores")
	memory := fs.String("memory", "", "requested memory")
	class := fs.String("class", "default", "resource class")
	timeout := fs.Duration("timeout", 0, "job deadline")
	wait := fs.Duration("wait", 30*time.Minute, "admission wait")
	machine := fs.String("machine", "", "SSH machine name; local by default")
	// Unlike lease commands, everything after -- is opaque child argv.
	split := -1
	for i, a := range args {
		if a == "--" {
			split = i
			break
		}
	}
	options := args
	if split >= 0 {
		options = args[:split]
	}
	usageError := func(err error) int { return outputpkg.Error(out, jsonOutput, 2, err.Error(), opts.Usage) }
	if err := fs.Parse(options); err != nil {
		return usageError(err)
	}
	if *wantHelp {
		outputpkg.Help(out, jsonOutput, opts.Usage)
		return 0
	}
	if fs.NArg() != 0 || split < 0 || split == len(args)-1 || *wait < 0 {
		return usageError(errors.New("use dibs job [flags] -- <cmd>; --wait must be nonnegative"))
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	config, err := configpkg.Load()
	if err != nil {
		return usageError(err)
	}
	if *machine != "" {
		remoteArgs := []string{"job"}
		fs.Visit(func(f *flag.Flag) {
			if f.Name != "machine" && f.Name != "axi" && f.Name != "json" {
				remoteArgs = append(remoteArgs, "--"+f.Name+"="+f.Value.String())
			}
		})

		remoteArgs = append(remoteArgs, "--")
		remoteArgs = append(remoteArgs, args[split+1:]...)
		return remotepkg.Run(ctx, opts.Version, config, *machine, remoteArgs, out, jsonOutput)
	}
	capacity, err := config.Available()
	if err != nil {
		return usageError(err)
	}
	request, duration, err := config.Class(*class)
	if err != nil {
		return usageError(err)
	}
	if explicit["cores"] {
		request.Cores = *cores
	}
	if explicit["memory"] {
		request.Memory, err = budgetpkg.ParseMemory(*memory)
		if err != nil {
			return usageError(err)
		}
	}
	if explicit["timeout"] {
		duration = *timeout
	}
	if request.Cores <= 0 || request.Memory <= 0 || duration <= 0 {
		return usageError(errors.New("cores, memory and timeout must be positive"))
	}
	if request.Cores > capacity.Cores {
		return outputpkg.Error(out, jsonOutput, 2, fmt.Sprintf("requested %d cores; usable %d after owner room %d", request.Cores, capacity.Cores, config.RoomCores), fmt.Sprintf("Try --cores %d or set DIBS_CONFIG", capacity.Cores))
	}
	if request.Memory > capacity.Memory {
		return outputpkg.Error(out, jsonOutput, 2, fmt.Sprintf("requested %d bytes memory; usable %d after owner room %s", request.Memory, capacity.Memory, config.RoomMemory), fmt.Sprintf("Try --memory %d or set DIBS_CONFIG", capacity.Memory))
	}
	child := append([]string(nil), args[split+1:]...)
	child[0], err = exec.LookPath(child[0])
	if err != nil {
		return outputpkg.Error(out, jsonOutput, 1, "child executable not found; no child started", "Check the command and PATH, then retry dibs job")
	}
	owner, err := opts.Owners.Bind(ctx, opts.OwnerKind+":"+strconv.Itoa(os.Getpid()), 0, time.Now())
	if err != nil {
		return outputpkg.Error(out, jsonOutput, 1, err.Error(), "Retry dibs job when the process is inspectable")
	}
	reservation, err := opts.Leases.Admit(ctx, leasepkg.AdmissionRequest{Owner: owner, TTL: duration, Description: filepath.Base(child[0]), Request: request, Capacity: capacity, Class: *class, Wait: *wait})
	var failure *leasepkg.Failure
	if errors.As(err, &failure) {
		switch failure.Stage {
		case leasepkg.ResolveDirectory:
			return outputpkg.Error(out, jsonOutput, 1, err.Error(), "Set DIBS_STATE to a private writable directory")
		case leasepkg.Transition:
			return outputpkg.Error(out, jsonOutput, 1, err.Error(), "Retry dibs job")
		}
	}
	if err != nil {
		code := 1
		if errors.Is(err, leasepkg.ErrNoCapacity) {
			code = 75
		}
		return outputpkg.Error(out, jsonOutput, code, err.Error(), "Run dibs ps to inspect reservations; retry dibs job with --wait")
	}
	result := opts.Leases.RunJob(ctx, reservation, child, input, out)
	if result.Err != nil {
		return outputpkg.Error(out, jsonOutput, 1, result.Err.Error(), "Run dibs ps to inspect retained reservations")
	}
	return result.Code
}
