package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// BERTH_* aliases are supported for the first minor release after the rename.
func dibsEnv(name string) string {
	if value, ok := os.LookupEnv("DIBS_" + name); ok {
		return value
	}
	return os.Getenv("BERTH_" + name)
}

func warnLegacyEnv(out io.Writer) {
	var names []string
	for _, name := range []string{"AXI", "CONFIG", "OWNER", "STATE"} {
		if _, preferred := os.LookupEnv("DIBS_" + name); !preferred && os.Getenv("BERTH_"+name) != "" {
			names = append(names, "BERTH_"+name)
		}
	}
	if len(names) != 0 {
		fmt.Fprintf(out, "warning: %s deprecated after berth renamed to dibs; use DIBS_* (aliases retained for one minor release)\n", strings.Join(names, ", "))
	}
}
