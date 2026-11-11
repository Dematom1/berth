package compat

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}

func TestRenamedEnvPrecedenceAndWarning(t *testing.T) {
	for _, name := range []string{"AXI", "CONFIG", "OWNER", "STATE", "LEGACY_MACHINE"} {
		unsetEnv(t, "DIBS_"+name)
		t.Setenv("BERTH_"+name, "legacy")
		if got := Env(name); got != "legacy" {
			t.Fatalf("%s alias = %q", name, got)
		}
	}
	var warning bytes.Buffer
	WarnLegacyEnv(&warning)
	if strings.Count(warning.String(), "\n") != 1 || !strings.Contains(warning.String(), "renamed to dibs") {
		t.Fatalf("expected one deprecation line: %q", warning.String())
	}
	for _, name := range []string{"AXI", "CONFIG", "OWNER", "STATE", "LEGACY_MACHINE"} {
		for _, value := range []string{"preferred", ""} {
			t.Setenv("DIBS_"+name, value)
			if got := Env(name); got != value {
				t.Fatalf("%s preferred %q = %q", name, value, got)
			}
		}
	}
	warning.Reset()
	WarnLegacyEnv(&warning)
	if warning.Len() != 0 {
		t.Fatalf("overridden aliases warned: %q", warning.String())
	}
}
