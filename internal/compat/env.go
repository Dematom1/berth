package compat

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Env accepts BERTH_* aliases for one minor release. An explicitly empty new
// variable still takes precedence over its legacy counterpart.
func Env(name string) string {
	if value, ok := os.LookupEnv("DIBS_" + name); ok {
		return value
	}
	return os.Getenv("BERTH_" + name)
}

func WarnLegacyEnv(out io.Writer) {
	var names []string
	for _, name := range []string{"AXI", "CONFIG", "OWNER", "STATE", "LEGACY_MACHINE"} {
		if _, preferred := os.LookupEnv("DIBS_" + name); !preferred && os.Getenv("BERTH_"+name) != "" {
			names = append(names, "BERTH_"+name)
		}
	}
	if len(names) != 0 {
		fmt.Fprintf(out, "warning: %s deprecated after berth renamed to dibs; use DIBS_* (aliases retained for one minor release)\n", strings.Join(names, ", "))
	}
}

// WarnLegacyCommand keeps callers that still run berth working for the same
// window as the BERTH_* aliases, without touching stdout or exit codes.
func WarnLegacyCommand(arg0 string, out io.Writer) {
	if filepath.Base(arg0) == "berth" {
		fmt.Fprintln(out, "warning: berth command deprecated after berth renamed to dibs; run dibs (berth retained for one minor release)")
	}
}
