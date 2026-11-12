package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLegacyEnvCLIStreams(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "dibs")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", out, err)
	}
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "DIBS_") && !strings.HasPrefix(entry, "BERTH_") {
			env = append(env, entry)
		}
	}
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte(`{"cores":4,"room_cores":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	owner := "pid:" + strconv.Itoa(os.Getpid())
	env = append(env, "BERTH_STATE="+t.TempDir(), "BERTH_OWNER="+owner, "BERTH_CONFIG="+config, "BERTH_AXI=1")
	for _, args := range [][]string{{"--json", "create", "--ttl", "1h"}, {"--json", "ps"}, {"--axi", "ps"}} {
		cmd := exec.Command(binary, args...)
		cmd.Env = env
		var out, diagnostics bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostics
		if err := cmd.Run(); err != nil {
			t.Fatalf("%v: %v: %s %s", args, err, &out, &diagnostics)
		}
		if strings.Count(diagnostics.String(), "\n") != 1 || !strings.Contains(diagnostics.String(), "deprecated") || strings.Contains(out.String(), "deprecated") {
			t.Fatalf("stream separation: stdout=%s stderr=%s", &out, &diagnostics)
		}
		if args[0] == "--json" {
			var result map[string]any
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatalf("invalid JSON: %s", &out)
			}
			if args[1] == "create" && result["lease"].(map[string]any)["owner"] != owner {
				t.Fatal("legacy owner was not honored")
			}
		} else if !strings.Contains(out.String(), "leases[") {
			t.Fatalf("invalid AXI: %s", &out)
		}
	}
	cmd := exec.Command(binary, "job", "--", "sh", "-c", `test -n "$DIBS_LEASE" && test "$DIBS_LEASE" = "$BERTH_LEASE"`)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil || strings.Count(string(out), "deprecated") != 1 {
		t.Fatalf("job lease aliases or duplicate internal-helper warning: %s: %v", out, err)
	}
}

func TestBerthCommandMatchesDibs(t *testing.T) {
	dir := t.TempDir()
	dibs, berth := filepath.Join(dir, "dibs"), filepath.Join(dir, "berth")
	if out, err := exec.Command("go", "build", "-o", dibs, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", out, err)
	}
	// The Nix package links berth to dibs; release archives ship a copy named berth.
	if err := os.Symlink("dibs", berth); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "DIBS_") && !strings.HasPrefix(entry, "BERTH_") {
			env = append(env, entry)
		}
	}
	env = append(env, "DIBS_STATE="+t.TempDir(), "DIBS_CONFIG="+filepath.Join(t.TempDir(), "absent.json"))
	run := func(binary string, args ...string) (string, string, int) {
		cmd := exec.Command(binary, args...)
		cmd.Env = env
		var out, diagnostics bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostics
		err := cmd.Run()
		code := 0
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return out.String(), diagnostics.String(), code
	}
	for _, args := range [][]string{{"--version"}, {"--json", "ps"}, {"--json", "no-such-command"}} {
		wantOut, wantErr, wantCode := run(dibs, args...)
		gotOut, gotErr, gotCode := run(berth, args...)
		if gotOut != wantOut || gotCode != wantCode {
			t.Fatalf("%v: berth stdout/exit %q/%d, dibs %q/%d", args, gotOut, gotCode, wantOut, wantCode)
		}
		if strings.Contains(wantErr, "deprecated") || gotErr != "warning: berth command deprecated after berth renamed to dibs; run dibs (berth retained for one minor release)\n"+wantErr {
			t.Fatalf("%v: berth stderr %q, dibs stderr %q", args, gotErr, wantErr)
		}
	}
}
