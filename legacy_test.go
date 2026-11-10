package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func unsetenv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
}

func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := warnings
	warnings = &buf
	warned.Clear()
	t.Cleanup(func() { warnings = previous; warned.Clear() })
	return &buf
}

func TestLegacyEnvAliases(t *testing.T) {
	for _, tc := range []struct {
		name         string
		dibs, berth  *string
		want         string
		wantWarnings int
	}{
		{"dibs only", ptr("new"), nil, "new", 0},
		{"berth only", nil, ptr("old"), "old", 1},
		{"dibs wins", ptr("new"), ptr("old"), "new", 0},
		{"empty dibs wins", ptr(""), ptr("old"), "", 0},
		{"empty berth ignored", nil, ptr(""), "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureWarnings(t)
			unsetenv(t, "DIBS_OWNER", "BERTH_OWNER")
			if tc.dibs != nil {
				t.Setenv("DIBS_OWNER", *tc.dibs)
			}
			if tc.berth != nil {
				t.Setenv("BERTH_OWNER", *tc.berth)
			}
			for range 2 {
				if got := getenv("OWNER"); got != tc.want {
					t.Fatalf("getenv(OWNER) = %q, want %q", got, tc.want)
				}
			}
			lines := strings.Count(buf.String(), "BERTH_OWNER is renamed to DIBS_OWNER")
			if lines != tc.wantWarnings || strings.Count(buf.String(), "\n") != tc.wantWarnings {
				t.Fatalf("want %d deprecation warning(s), got %q", tc.wantWarnings, buf.String())
			}
		})
	}
}

func ptr(s string) *string { return &s }

// The real binary: its default name, and a legacy variable driving it end to end.
func TestLegacyBinary(t *testing.T) {
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", bin+string(filepath.Separator), ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	binary := filepath.Join(bin, "dibs")
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("go build did not produce a dibs binary: %v", err)
	}
	state := filepath.Join(t.TempDir(), "state")
	cmd := exec.Command(binary, "--json", "create", "--ttl", "1h")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "DIBS_") && !strings.HasPrefix(entry, "BERTH_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "BERTH_STATE="+state, "BERTH_OWNER=pid:"+strconv.Itoa(os.Getpid()))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("create with BERTH_* variables: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "dibs: deprecated: BERTH_STATE is renamed to DIBS_STATE") || !strings.Contains(stderr.String(), "BERTH_OWNER is renamed to DIBS_OWNER") {
		t.Fatalf("missing deprecation warning: %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(state, "state.json")); err != nil {
		t.Fatalf("BERTH_STATE was not honored: %v", err)
	}
	job := exec.Command(binary, "job", "--", "sh", "-c", `printf '%s %s' "$DIBS_LEASE" "$BERTH_LEASE"`)
	job.Env = append(cmd.Env, "XDG_CONFIG_HOME="+t.TempDir())
	out, err := job.Output()
	if ids := strings.Fields(string(out)); err != nil || len(ids) != 2 || ids[0] != ids[1] || !leaseID.MatchString(ids[0]) {
		t.Fatalf("job child must see DIBS_LEASE and the deprecated BERTH_LEASE: %q, %v", out, err)
	}
	var created struct {
		Record Lease `json:"record"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &created); err != nil || created.Record.Owner.ID != strconv.Itoa(os.Getpid()) {
		t.Fatalf("BERTH_OWNER not honored or stdout not machine-readable: %q", stdout.String())
	}
}

func TestLegacyConfigFallback(t *testing.T) {
	buf := captureWarnings(t)
	unsetenv(t, "DIBS_CONFIG", "BERTH_CONFIG")
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	write := func(name string, cores int) {
		t.Helper()
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		config := `{"cores":` + strconv.Itoa(cores) + `,"memory":"8GiB","room_cores":1,"room_memory":"1GiB"}`
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("berth", 3)
	c, err := loadJobConfig()
	if err != nil || c.Cores != 3 {
		t.Fatalf("legacy config not read: %+v, %v", c, err)
	}
	if !strings.Contains(buf.String(), filepath.Join(base, "berth", "config.json")+"; move it to "+filepath.Join(base, "dibs", "config.json")) {
		t.Fatalf("missing move hint: %q", buf.String())
	}
	write("dibs", 5)
	if c, err = loadJobConfig(); err != nil || c.Cores != 5 {
		t.Fatalf("dibs config must win over the legacy one: %+v, %v", c, err)
	}
}

// legacyComposeLease records a lease the way berth v0.1.0 did: a berth-<id>
// project and an io.berth.* labeled snapshot under the berth state directory.
func legacyComposeLease(t *testing.T, s *Store, host string) Lease {
	t.Helper()
	l := testLease(t)
	l.Machine = host
	l.Resource = Resource{Adapter: "compose", Name: "berth-" + l.ID, Context: "default", File: filepath.Join(s.dir, "resources", l.ID, "compose.json")}
	labels := map[string]any{"io.berth.lease": l.ID, "io.berth.owner": l.Owner.String(), "io.berth.machine": host}
	config := map[string]any{"name": l.Resource.Name, "services": map[string]any{"test": map[string]any{"image": "alpine:3.23", "command": []string{"sleep", "600"}, "labels": labels}}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(l.Resource.File), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Resource.File, data, 0600); err != nil {
		t.Fatal(err)
	}
	s.Leases[l.ID] = l
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLegacyStateMigrationKeepsLeasesReclaimable(t *testing.T) {
	buf := captureWarnings(t)
	unsetenv(t, "DIBS_STATE", "BERTH_STATE")
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	legacy, want := filepath.Join(base, "berth"), filepath.Join(base, "dibs")
	old, err := openStore(context.Background(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	l := legacyComposeLease(t, old, "lab")
	old.Close()

	for range 2 { // The second resolution must be a no-op.
		dir, err := stateDir(context.Background())
		if err != nil || dir != want {
			t.Fatalf("stateDir = %q, %v; want %q", dir, err, want)
		}
	}
	if strings.Count(buf.String(), "migrated berth state from "+legacy+" to "+want) != 1 {
		t.Fatalf("migration must be reported once: %q", buf.String())
	}
	notice, err := os.ReadFile(legacy)
	if err != nil || !strings.Contains(string(notice), "berth was renamed to dibs") {
		t.Fatalf("legacy path must hold a rename notice: %q, %v", notice, err)
	}
	// An older berth binary cannot start a second, unreclaimed ledger there.
	if err := os.MkdirAll(legacy, 0700); err == nil {
		t.Fatal("older berth could recreate its state directory")
	}

	s, err := openStore(context.Background(), want)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	migrated, ok := s.Leases[l.ID]
	if !ok || migrated.Resource.File != filepath.Join(want, "resources", l.ID, "compose.json") || migrated.Resource.Name != "berth-"+l.ID {
		t.Fatalf("lease not carried over intact: %+v", migrated)
	}
	if snapshot, err := os.ReadFile(migrated.Resource.File); err != nil || !strings.Contains(string(snapshot), `"io.berth.lease":"`+l.ID+`"`) {
		t.Fatalf("labeled snapshot not moved: %q, %v", snapshot, err)
	}

	var calls [][]string
	runner := func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}
	changed, err := releaseLease(context.Background(), s, l.ID, "lab", map[string]Adapter{"compose": composeAdapter{runner}})
	if err != nil || !changed {
		t.Fatalf("legacy lease not reclaimable: %v", err)
	}
	down := []string{"docker", "--context", "default", "compose", "-p", "berth-" + l.ID, "-f", migrated.Resource.File, "down", "--volumes", "--remove-orphans"}
	if len(calls) == 0 || !reflect.DeepEqual(calls[0], down) {
		t.Fatalf("cleanup did not target the berth-named project: %v", calls)
	}
	for _, call := range calls[1:] {
		if call[len(call)-1] != "label=com.docker.compose.project=berth-"+l.ID {
			t.Fatalf("verify-gone did not check the berth-named project: %v", call)
		}
	}
}

func TestLegacyStateNotMigratedOverExistingDibsState(t *testing.T) {
	buf := captureWarnings(t)
	unsetenv(t, "DIBS_STATE", "BERTH_STATE")
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	for _, name := range []string{"berth", "dibs"} {
		s, err := openStore(context.Background(), filepath.Join(base, name))
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	if _, err := stateDir(context.Background()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(base, "berth")); err != nil || !info.IsDir() {
		t.Fatalf("berth state must stay untouched: %v", err)
	}
	if !strings.Contains(buf.String(), "dibs does not manage leases recorded only in "+filepath.Join(base, "berth")) {
		t.Fatalf("missing warning: %q", buf.String())
	}
}
