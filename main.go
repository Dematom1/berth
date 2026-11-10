package main

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
)

var version string

func init() {
	info, _ := debug.ReadBuildInfo()
	version = resolveVersion(version, info)
}

func resolveVersion(override string, info *debug.BuildInfo) string {
	if override != "" {
		return override
	}
	if info == nil {
		return "devel"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return strings.TrimPrefix(v, "v")
	}
	revision, modified := "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	v := "devel"
	if revision != "" {
		v += "-" + revision[:min(12, len(revision))]
	}
	if modified {
		v += "-dirty"
	}
	return v
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__job_exec" {
		os.Exit(jobExec(os.Args[2:]))
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdout))
}
