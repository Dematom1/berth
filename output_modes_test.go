package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func snapshot(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "output", name+".golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s output changed\nwant:\n%s\ngot:\n%s", name, want, got)
	}
}

func fixedBudget(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"cores":14,"memory":"32GiB","room_cores":3,"room_memory":"8GiB","classes":{"default":{"cores":1,"memory":"1GiB","timeout":"1h"}}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERTH_CONFIG", path)
}

func TestOutputSelection(t *testing.T) {
	for _, tc := range []struct {
		env  string
		mode outputMode
		want outputMode
	}{
		{"", autoOutput, axiOutput}, {"1", autoOutput, axiOutput},
		{"0", autoOutput, axiOutput}, {"1", jsonOutput, jsonOutput}, {"1", axiOutput, axiOutput},
	} {
		t.Setenv("BERTH_AXI", tc.env)
		if got := tc.mode.effective(&bytes.Buffer{}); got != tc.want {
			t.Fatalf("mode %q env %q: %q", tc.mode, tc.env, got)
		}
	}
	// The same resolved mode is carried over SSH rather than redetected on its pipe.
	t.Setenv("BERTH_AXI", "1")
	if got := autoOutput.effective(&modeWriter{Writer: &bytes.Buffer{}, mode: humanOutput}); got != humanOutput {
		t.Fatal(got)
	}
}

func TestOutputCLIGoldens(t *testing.T) {
	t.Setenv("BERTH_STATE", t.TempDir())
	fixedBudget(t)
	for _, mode := range []string{"axi", "json"} {
		for _, alias := range []string{"list", "ps", "who"} {
			var out bytes.Buffer
			if code := run(context.Background(), []string{alias, "--" + mode}, &out); code != 0 {
				t.Fatalf("%d %s", code, &out)
			}
			snapshot(t, alias+"-"+mode, out.String())
		}
		for name, args := range map[string][]string{
			"error":     {"ps", "--typo"},
			"job-error": {"job", "--cores", "999", "--", "true"},
		} {
			var out bytes.Buffer
			if code := run(context.Background(), append([]string{"--" + mode}, args...), &out); code != 2 {
				t.Fatalf("%d %s", code, &out)
			}
			snapshot(t, name+"-"+mode, out.String())
		}
	}
}

func TestHomeOutputSnapshots(t *testing.T) {
	t.Setenv("BERTH_STATE", t.TempDir())
	fixedBudget(t)
	bin, _ := os.Executable()
	for _, mode := range []outputMode{humanOutput, axiOutput, jsonOutput} {
		var out bytes.Buffer
		if code := runWithOutput(context.Background(), nil, &out, mode); code != 0 {
			t.Fatal(code, out.String())
		}
		snapshot(t, "home-"+string(mode), strings.ReplaceAll(out.String(), bin, "<berth>"))
	}
}

func TestLeaseOutputSnapshots(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	l := Lease{ID: strings.Repeat("a", 32), Machine: "lab", Owner: Owner{Kind: "pid", ID: "123"}, Created: now.Add(-5 * time.Minute), Expires: now.Add(time.Hour), TTL: time.Hour, Resources: "test, lab", Labels: map[string]string{"task": "lab"}, Resource: Resource{Adapter: "kind", Name: "berth-lab", File: "/tmp/lab's kubeconfig"}}
	value := map[string]any{"count": 1, "leases": []map[string]any{leaseView(l, Alive, now)}, "records": []map[string]any{leaseRecord(l)}}
	for _, mode := range []outputMode{axiOutput, jsonOutput} {
		var out bytes.Buffer
		emit(&out, mode, value)
		snapshot(t, "lease-"+string(mode), out.String())
	}
	var full bytes.Buffer
	emit(&modeWriter{Writer: &full, mode: axiOutput, full: true}, axiOutput, value)
	snapshot(t, "lease-full-axi", full.String())
	for name, data := range map[string]map[string]any{"list": value, "create": {"lease": leaseView(l, Alive, now), "record": leaseRecord(l)}} {
		var out bytes.Buffer
		humanObject(&out, data, now)
		snapshot(t, name+"-human", out.String())
		if strings.Contains(out.String(), "1073741824B") || !strings.Contains(out.String(), "lab's kubeconfig") || !strings.Contains(out.String(), "in 1h0m0s") {
			t.Fatal(out.String())
		}
	}
}

func TestLeaseWarningSnapshots(t *testing.T) {
	for _, orphan := range []bool{false, true} {
		name := "expiring-lease"
		l := Lease{ID: strings.Repeat("a", 32), Machine: "lab", Owner: Owner{Kind: "pid", ID: "123"}, Created: testNow.Add(-8 * time.Minute), Expires: testNow.Add(4 * time.Minute), TTL: 12 * time.Minute, Resource: Resource{Adapter: "none"}}
		live := Alive
		if orphan {
			name, live = "orphan-job", Dead
			l.Resource.Adapter = "process"
			l.Job = &JobResource{Budget: Budget{1, 1 << 30}, Class: "suite", OrphanSuspected: true}
		}
		value := map[string]any{"count": 1, "leases": []map[string]any{leaseView(l, live, testNow)}}
		for _, mode := range []outputMode{humanOutput, axiOutput, jsonOutput} {
			var out bytes.Buffer
			if mode == humanOutput {
				humanObject(&out, value, testNow)
			} else {
				emit(&out, mode, value)
			}
			snapshot(t, name+"-"+string(mode), out.String())
		}
	}
}

func TestHumanLeaseWarnings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERTH_STATE", dir)
	t.Setenv("BERTH_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
	id := invoke(t, 0, "create")["lease"].(map[string]any)["id"].(string)
	jobStore(t, dir, func(s *Store) {
		l := s.Leases[id]
		l.TTL, l.Expires = 12*time.Minute, time.Now().Add(4*time.Minute)
		s.Leases[id] = l
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
	})
	for _, args := range [][]string{nil, {"ps"}} {
		var out bytes.Buffer
		if code := runWithOutput(context.Background(), args, &out, humanOutput); code != 0 || !strings.Contains(out.String(), "Warning: "+id+": expires in 4m0s - extend or it will be reclaimed") {
			t.Fatalf("human warning missing: %d %s", code, out.String())
		}
	}
}

func TestAXIHelpAndErrors(t *testing.T) {
	for _, command := range []string{"", "create", "list", "extend", "release", "sweep", "job", "machine", "setup", "env"} {
		var out bytes.Buffer
		emitHelp(&out, axiOutput, command)
		name := command
		if name == "" {
			name = "home"
		}
		snapshot(t, "help-"+name+"-axi", out.String())
		if !strings.Contains(out.String(), "usage:") || !strings.Contains(out.String(), "reference[") {
			t.Fatal(out.String())
		}
	}
	for _, code := range []int{1, 2, 75} {
		var out bytes.Buffer
		if emitError(&out, axiOutput, code, "operation failed", "Run berth ps") != code {
			t.Fatal("exit changed")
		}
		snapshot(t, "error-code-"+strconv.Itoa(code)+"-axi", out.String())
	}
}

func TestAXIBoundedListsAndFull(t *testing.T) {
	t.Setenv("BERTH_STATE", t.TempDir())
	fixedBudget(t)
	s, err := openStore(context.Background(), os.Getenv("BERTH_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	host, _ := machine()
	fixtureMachine := "fixture-" + host
	owner := Owner{Kind: "heartbeat", ID: "/missing-berth-test-owner"}
	longText := strings.Repeat("界", 501)
	for i := 0; i < 101; i++ {
		id := fmt.Sprintf("%032x", i)
		l := Lease{ID: id, Machine: fixtureMachine, Owner: owner, Created: now, Expires: now.Add(time.Hour), TTL: time.Hour, Resources: "lab", Resource: Resource{Adapter: "none"}}
		if i == 0 {
			l.Resources = longText
		}
		s.Leases[id] = l
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var out bytes.Buffer
	writer := &modeWriter{Writer: &out, mode: axiOutput, machine: "remote ' lab"}
	if code := run(context.Background(), []string{"ps", "--owner", owner.String()}, writer); code != 0 {
		t.Fatal(code, out.String())
	}
	snapshot(t, "list-truncated-axi", strings.ReplaceAll(out.String(), fixtureMachine, "<machine>"))
	for _, text := range []string{"leases[100]", "total: 101", "shown: 100", "501 chars total", "--machine", "--owner", "all 101 matching leases"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("missing %q: %s", text, &out)
		}
	}
	if strings.Contains(out.String(), longText) {
		t.Fatal("unbounded default text")
	}
	out.Reset()
	if code := run(context.Background(), []string{"--axi", "ps", "--full"}, &out); code != 0 || !strings.Contains(out.String(), "leases[101]") || !strings.Contains(out.String(), "records[101]") || !strings.Contains(out.String(), longText) {
		t.Fatal(code, out.String())
	}
	jsonView := invoke(t, 0, "ps")
	if jsonView["count"] != float64(101) || len(jsonView["records"].([]any)) != 101 || jsonView["leases"].([]any)[0].(map[string]any)["resources"] != longText {
		t.Fatal("JSON was truncated")
	}
	// Presentation must not mutate the canonical view, including labels.
	data := map[string]any{"lease": map[string]any{"resources": longText, "labels": map[string]string{"long": longText}}}
	compact := axiView(data, "berth ps --full")
	if compact["lease"].(map[string]any)["resources"] == longText || data["lease"].(map[string]any)["resources"] != longText || data["lease"].(map[string]any)["labels"].(map[string]string)["long"] != longText {
		t.Fatal("presentation mutated data or failed to truncate")
	}
}

func TestHumanErrorsAreTwoShortLines(t *testing.T) {
	fixedBudget(t)
	t.Setenv("BERTH_STATE", t.TempDir())
	for _, args := range [][]string{{"job", "--cores", "999", "--", "true"}, {"ps", "--typo"}, {"release"}, {"unknown"}, {"create", "--ttl", "0s"}, {"machine"}, {"setup", "invalid"}, {"env", strings.Repeat("b", 32)}} {
		var out bytes.Buffer
		if code := runWithOutput(context.Background(), args, &out, humanOutput); code != 2 {
			t.Fatalf("%v: %d %s", args, code, &out)
		}
		if len(strings.Split(strings.TrimSpace(out.String()), "\n")) != 2 || strings.Contains(out.String(), "Exit codes:") {
			t.Fatalf("full help leaked: %s", &out)
		}
		if args[0] == "job" {
			snapshot(t, "job-error-human", out.String())
		}
	}
}

func TestConflictingOutputFlags(t *testing.T) {
	t.Setenv("BERTH_STATE", filepath.Join(t.TempDir(), "unused"))
	for _, args := range [][]string{{"--json", "--axi", "ps"}, {"ps", "--axi", "--json"}, {"--json", "job", "--axi", "--", "true"}, {"machine", "install", "--axi", "--json"}, {"setup", "hooks", "--axi", "--json"}} {
		var out bytes.Buffer
		if code := run(context.Background(), args, &out); code != 2 || !strings.Contains(out.String(), "mutually exclusive") {
			t.Fatalf("%v: %d %s", args, code, &out)
		}
	}
	if _, err := os.Stat(os.Getenv("BERTH_STATE")); !os.IsNotExist(err) {
		t.Fatal("flag conflict reached state")
	}
	// Explicit JSON overrides the hook's ambient AXI selection.
	t.Setenv("BERTH_AXI", "1")
	var out bytes.Buffer
	if run(context.Background(), []string{"--json", "release", strings.Repeat("b", 32)}, &out) != 0 || !json.Valid(out.Bytes()) {
		t.Fatal(out.String())
	}
}

func shellExport(t *testing.T, script string) string {
	t.Helper()
	out, err := exec.Command("sh", "-c", script+"\nprintf '%s' \"$KUBECONFIG\"").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestOutputProcessesAndTTY(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "berth")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	t.Setenv("BERTH_STATE", t.TempDir())
	t.Setenv("BERTH_AXI", "")
	fixedBudget(t)
	out, err := exec.Command(binary, "ps").Output()
	if err != nil {
		t.Fatal(err)
	}
	snapshot(t, "ps-axi", string(out))
	for _, flags := range [][]string{nil, {"--axi"}, {"--json"}} {
		args := append(append([]string{}, flags...), "job", "--", "sh", "-c", `printf 'child %s\000' "$1"; printf 'stderr!\000' >&2; exit 37`, "_", "--axi")
		cmd := exec.Command(binary, args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 37 || stdout.String() != "child --axi\x00" || stderr.String() != "stderr!\x00" {
			t.Fatalf("streams/code changed: %v %q %q", err, &stdout, &stderr)
		}
	}
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script unavailable for PTY check")
	}
	var tty *exec.Cmd
	if runtime.GOOS == "darwin" {
		tty = exec.Command("script", "-q", "/dev/null", binary, "ps")
	} else {
		tty = exec.Command("script", "-q", "-e", "-c", shellQuote(binary)+" ps", "/dev/null")
	}
	out, err = tty.Output()
	if err != nil {
		t.Fatal(err)
	}
	// macOS script echoes its synthetic EOF before the command's output.
	snapshot(t, "ps-human", strings.TrimPrefix(strings.ReplaceAll(string(out), "\r\n", "\n"), "^D\b\b"))
}

func TestBudgetSummary(t *testing.T) {
	fixedBudget(t)
	config, err := loadJobConfig()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	l := Lease{ID: "job", Machine: "lab", Created: now, Expires: now.Add(time.Hour), Resource: Resource{Adapter: "process"}, Job: &JobResource{Budget: Budget{Cores: 9, Memory: 1 << 30}, Class: "suite"}}
	foreign := l
	foreign.Machine = "elsewhere"
	budget := budgetView(config, &Store{Leases: map[string]Lease{"job": l, "foreign": foreign}}, "lab")
	if budget["used_cores"] != 9 || budget["usable_cores"] != 11 || budget["used_memory_bytes"] != int64(1<<30) || budget["usable_memory_bytes"] != int64(24<<30) {
		t.Fatal(budget)
	}
	value := map[string]any{"budget": budget, "leases": []map[string]any{leaseView(l, Alive, now)}}
	var out bytes.Buffer
	humanObject(&out, value, now)
	snapshot(t, "budget-human", out.String())
	for _, mode := range []outputMode{axiOutput, jsonOutput} {
		out.Reset()
		emit(&out, mode, value)
		snapshot(t, "budget-"+string(mode), out.String())
	}
	// Listing leases must remain available even when job configuration is broken.
	t.Setenv("BERTH_STATE", t.TempDir())
	t.Setenv("BERTH_CONFIG", filepath.Join(t.TempDir(), "missing"))
	if invoke(t, 0, "ps")["budget"].(map[string]any)["summary"] != "jobs: budget unavailable (check BERTH_CONFIG)" {
		t.Fatal("lost budget warning")
	}
}

func TestQuietAndKubeconfig(t *testing.T) {
	t.Setenv("BERTH_STATE", filepath.Join(t.TempDir(), "space ' quote $(touch never)\nstate"))
	t.Setenv("BERTH_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
	for i, flag := range []string{"--quiet", "-q"} {
		var out bytes.Buffer
		if code := run(context.Background(), []string{"create", flag, []string{"--axi", "--json"}[i]}, &out); code != 0 {
			t.Fatal(code, out.String())
		}
		id := strings.TrimSpace(out.String())
		if !leaseID.MatchString(id) || out.String() != id+"\n" {
			t.Fatalf("not just id: %q", out.String())
		}
		out.Reset()
		if code := run(context.Background(), []string{"env", id}, &out); code != 2 || !strings.Contains(out.String(), "no kubeconfig") {
			t.Fatal(code, out.String())
		}
	}
	s, err := openStore(context.Background(), os.Getenv("BERTH_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	for i, adapter := range []string{"kind", "k3d"} {
		id := strings.Repeat(strconv.Itoa(i+1), 32)
		file, name := "kubeconfig", "berth-"+id
		if adapter == "k3d" {
			file, name = "k3d", "berth-"+id[:26]
		}
		path := filepath.Join(os.Getenv("BERTH_STATE"), "resources", id, file)
		l, err := newLease(Owner{Kind: "pid", ID: strconv.Itoa(os.Getpid())}, time.Hour, "", nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		l.ID, l.Resource = id, Resource{Adapter: adapter, Name: name, Context: "fixture", DockerHost: "unix:///fixture", File: path}
		s.Leases[id] = l
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
		s.Close()
		var out bytes.Buffer
		if code := run(context.Background(), []string{"env", id}, &out); code != 0 || out.String() != "export KUBECONFIG="+shellQuote(path)+"\n" {
			t.Fatal(code, out.String())
		}
		// Evaluate the actual shell output, not just its spelling.
		got := shellExport(t, out.String())
		if got != path {
			t.Fatalf("shell quoting: %q", got)
		}
		if leaseView(l, Alive, time.Now())["kubeconfig"] != path || leaseRecord(l)["kubeconfig"] != path {
			t.Fatal("missing kubeconfig")
		}
		s, err = openStore(context.Background(), os.Getenv("BERTH_STATE"))
		if err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
}
