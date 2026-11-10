package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}

func TestRenamedEnvPrecedenceAndWarning(t *testing.T) {
	for _, name := range []string{"AXI", "CONFIG", "OWNER", "STATE"} {
		unsetEnv(t, "DIBS_"+name)
		t.Setenv("BERTH_"+name, "legacy")
		if got := dibsEnv(name); got != "legacy" {
			t.Fatalf("%s alias = %q", name, got)
		}
	}
	var warning bytes.Buffer
	warnLegacyEnv(&warning)
	if strings.Count(warning.String(), "\n") != 1 || !strings.Contains(warning.String(), "renamed to dibs") {
		t.Fatalf("expected one deprecation line: %q", warning.String())
	}
	for _, name := range []string{"AXI", "CONFIG", "OWNER", "STATE"} {
		for _, value := range []string{"preferred", ""} {
			t.Setenv("DIBS_"+name, value)
			if got := dibsEnv(name); got != value {
				t.Fatalf("%s preferred %q = %q", name, value, got)
			}
		}
	}
	warning.Reset()
	warnLegacyEnv(&warning)
	if warning.Len() != 0 {
		t.Fatalf("overridden aliases warned: %q", warning.String())
	}
}

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
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("job lease aliases: %s: %v", out, err)
	}
}

func TestRenamedConfigFallback(t *testing.T) {
	for _, xdg := range []bool{false, true} {
		t.Run(strconv.FormatBool(xdg), func(t *testing.T) {
			unsetEnv(t, "DIBS_CONFIG")
			unsetEnv(t, "BERTH_CONFIG")
			home := t.TempDir()
			t.Setenv("HOME", home)
			base := filepath.Join(home, ".config")
			t.Setenv("XDG_CONFIG_HOME", "")
			if xdg {
				base = t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", base)
			}
			for i, product := range []string{"berth", "dibs"} {
				dir := filepath.Join(base, product)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"cores":`+strconv.Itoa(4+i)+`,"room_cores":1,"memory":"8GiB","room_memory":"1GiB"}`), 0600); err != nil {
					t.Fatal(err)
				}
				c, err := loadJobConfig()
				if err != nil || c.Cores != 4+i {
					t.Fatalf("%s config: %+v, %v", product, c, err)
				}
			}
			if err := os.WriteFile(filepath.Join(base, "dibs", "config.json"), []byte("broken"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadJobConfig(); err == nil {
				t.Fatal("invalid preferred config silently fell back")
			}
		})
	}
}

func TestRenamedStateSelection(t *testing.T) {
	unsetEnv(t, "DIBS_STATE")
	unsetEnv(t, "BERTH_STATE")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	if dir, err := stateDir(); err != nil || dir != filepath.Join(home, ".local", "state", "dibs") {
		t.Fatalf("fresh state: %q, %v", dir, err)
	}
	t.Setenv("BERTH_STATE", filepath.Join(home, "old-explicit"))
	if dir, err := stateDir(); err != nil || dir != os.Getenv("BERTH_STATE") {
		t.Fatalf("legacy explicit state: %q, %v", dir, err)
	}
	t.Setenv("DIBS_STATE", filepath.Join(home, "new-explicit"))
	if dir, err := stateDir(); err != nil || dir != os.Getenv("DIBS_STATE") {
		t.Fatalf("preferred explicit state: %q, %v", dir, err)
	}
}

func TestStateRenameKeepsLegacyLeasesAndLock(t *testing.T) {
	unsetEnv(t, "DIBS_STATE")
	unsetEnv(t, "BERTH_STATE")
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	legacy := filepath.Join(base, "berth")
	s, err := openStore(context.Background(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, adapter := range []string{"kind", "k3d", "compose"} {
		l := testLease(t)
		file, suffix := "kubeconfig", l.ID
		if adapter == "k3d" {
			file, suffix = "k3d", l.ID[:26]
		} else if adapter == "compose" {
			file = "compose.json"
		}
		l.Resource = Resource{Adapter: adapter, Name: "berth-" + suffix, Context: "old-context", DockerHost: "unix:///old.sock", File: filepath.Join(legacy, "resources", l.ID, file)}
		s.Leases[l.ID] = l
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(legacy, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.Stat(filepath.Join(legacy, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		dir, err := stateDir()
		if err != nil || dir != legacy {
			t.Fatalf("migration: %q, %v", dir, err)
		}
	}
	preferred := filepath.Join(base, "dibs")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if second, err := openStore(ctx, preferred); err == nil {
		second.Close()
		t.Fatal("migration split the state lock")
	}
	s.Close()
	s, err = openStore(context.Background(), preferred)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	currentLock, err := os.Stat(filepath.Join(preferred, "lock"))
	if err != nil || !os.SameFile(lock, currentLock) {
		t.Fatal("migration replaced the lock inode", err)
	}
	after, err := os.ReadFile(filepath.Join(preferred, "state.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("migration changed lease data", err)
	}
	for _, l := range s.Leases {
		if eligible, _ := reclaimable(l, l.Machine, testNow, func(Owner, time.Time) Liveness { return Alive }); eligible {
			t.Fatal("migration lost live owner or TTL")
		}
	}
	host, err := machine()
	if err != nil {
		t.Fatal(err)
	}
	r := sweep(context.Background(), s, host, testNow.Add(time.Hour), true, func(Owner, time.Time) Liveness { return Alive }, map[string]Adapter{"kind": noResource{}, "k3d": noResource{}, "compose": noResource{}})
	if len(r.Reclaimed) != 3 || len(s.Leases) != 0 {
		t.Fatalf("old leases no longer reclaimable: %+v", r)
	}
}

func TestStateRenameCrashRetryAndConflict(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "berth")
	if err := os.Mkdir(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	original := syncDirectory
	t.Cleanup(func() { syncDirectory = original })
	syncDirectory = func(string) error { return errors.New("injected crash after alias publication") }
	if _, err := migrateStateDir(base); err == nil {
		t.Fatal("failed sync reported success")
	}
	syncDirectory = original
	if dir, err := migrateStateDir(base); err != nil || dir != legacy {
		t.Fatalf("migration did not converge: %q, %v", dir, err)
	}
	preferred := filepath.Join(base, "dibs")
	if err := os.Remove(preferred); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(preferred, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateStateDir(base); err == nil {
		t.Fatal("separate existing ledgers silently hid old leases")
	}
}

func TestLegacyComposeLabelsCleanedByRecordedProject(t *testing.T) {
	s := testStore(t)
	l := testLease(t)
	l.Resource = Resource{Adapter: "compose", Name: "berth-" + l.ID, Context: "old-context", File: filepath.Join(s.dir, "resources", l.ID, "compose.json")}
	if err := os.MkdirAll(filepath.Dir(l.Resource.File), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"services":{"web":{"image":"old","labels":{"io.berth.lease":"` + l.ID + `"}}}}`)
	if err := os.WriteFile(l.Resource.File, data, 0600); err != nil {
		t.Fatal(err)
	}
	s.Leases[l.ID] = l
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	t.Setenv("DIBS_STATE", s.dir)
	if invoke(t, 0, "ps")["count"] != float64(1) {
		t.Fatal("ps lost old-label lease")
	}
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "docker.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(log) + "\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	invoke(t, 0, "release", l.ID, "--confirm")
	if invoke(t, 0, "ps")["count"] != float64(0) {
		t.Fatal("release did not reclaim old-label resource")
	}
	s, err := openStore(context.Background(), s.dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Expires = time.Now().Add(-time.Hour)
	s.Leases[l.ID] = l
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if len(invoke(t, 0, "sweep", "--confirm")["reclaimed"].([]any)) != 1 {
		t.Fatal("sweep did not reclaim old-label resource")
	}
	commands, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(commands), "compose -p "+l.Resource.Name+" -f "+l.Resource.File+" down --volumes --remove-orphans") || strings.Count(string(commands), "label=com.docker.compose.project="+l.Resource.Name) != 6 {
		t.Fatalf("cleanup must verify the recorded project, independent of io label namespace: %s, %v", commands, err)
	}
	unchanged, err := os.ReadFile(l.Resource.File)
	if err != nil || !bytes.Equal(unchanged, data) {
		t.Fatal("cleanup rewrote the legacy snapshot", err)
	}
}
