package cli

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

	adapterpkg "github.com/Dematom1/berth/internal/adapter"
	leasepkg "github.com/Dematom1/berth/internal/lease"
	outputpkg "github.com/Dematom1/berth/internal/output"
	ownerpkg "github.com/Dematom1/berth/internal/owner"
	remotepkg "github.com/Dematom1/berth/internal/remote"
)

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
	for _, mode := range []outputpkg.Mode{outputpkg.Human, outputpkg.AXI, outputpkg.JSON} {
		var out bytes.Buffer
		if code := runWithOutput(context.Background(), nil, &out, mode); code != 0 {
			t.Fatal(code, out.String())
		}
		snapshot(t, "home-"+string(mode), strings.ReplaceAll(out.String(), bin, "<berth>"))
	}
}

func TestAXIHelpAndErrors(t *testing.T) {
	for _, command := range []string{"", "create", "list", "extend", "release", "sweep", "job", "machine", "setup", "env"} {
		var out bytes.Buffer
		emitHelp(&out, outputpkg.AXI, command)
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
		if outputpkg.Error(&out, outputpkg.AXI, code, "operation failed", "Run berth ps") != code {
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
	owner := ownerpkg.Reference{Kind: "heartbeat", ID: "/missing-berth-test-owner"}
	longText := strings.Repeat("界", 501)
	for i := 0; i < 101; i++ {
		id := fmt.Sprintf("%032x", i)
		l := leasepkg.Lease{ID: id, Machine: fixtureMachine, Owner: owner, Created: now, Expires: now.Add(time.Hour), TTL: time.Hour, Description: "lab", ResourceRef: adapterpkg.ResourceRef{Adapter: "none"}}
		if i == 0 {
			l.Description = longText
		}
		s.Leases[id] = l
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var out bytes.Buffer
	writer := &outputpkg.Writer{Writer: &out, Mode: outputpkg.AXI, Machine: "remote ' lab"}
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
}

func TestHumanErrorsAreTwoShortLines(t *testing.T) {
	fixedBudget(t)
	t.Setenv("BERTH_STATE", t.TempDir())
	for _, args := range [][]string{{"job", "--cores", "999", "--", "true"}, {"ps", "--typo"}, {"release"}, {"unknown"}, {"create", "--ttl", "0s"}, {"machine"}, {"setup", "invalid"}, {"env", strings.Repeat("b", 32)}} {
		var out bytes.Buffer
		if code := runWithOutput(context.Background(), args, &out, outputpkg.Human); code != 2 {
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
	if out, err := exec.Command("go", "build", "-o", binary, moduleRoot(t)).CombinedOutput(); err != nil {
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
		tty = exec.Command("script", "-q", "-e", "-c", remotepkg.ShellQuote(binary)+" ps", "/dev/null")
	}
	out, err = tty.Output()
	if err != nil {
		t.Fatal(err)
	}
	// macOS script echoes its synthetic EOF before the command's output.
	snapshot(t, "ps-human", strings.TrimPrefix(strings.ReplaceAll(string(out), "\r\n", "\n"), "^D\b\b"))
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
		if !leasepkg.ValidID(id) || out.String() != id+"\n" {
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
		l, err := newLease(ownerpkg.Reference{Kind: "pid", ID: strconv.Itoa(os.Getpid())}, time.Hour, "", nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		l.ID, l.ResourceRef = id, adapterpkg.ResourceRef{Adapter: adapter, Name: name, Context: "fixture", DockerHost: "unix:///fixture", File: path}
		s.Leases[id] = l
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
		s.Close()
		var out bytes.Buffer
		if code := run(context.Background(), []string{"env", id}, &out); code != 0 || out.String() != "export KUBECONFIG="+remotepkg.ShellQuote(path)+"\n" {
			t.Fatal(code, out.String())
		}
		// Evaluate the actual shell output, not just its spelling.
		got := shellExport(t, out.String())
		if got != path {
			t.Fatalf("shell quoting: %q", got)
		}
		if outputpkg.LeaseView(l, ownerpkg.Alive, time.Now(), kubeconfig(l))["kubeconfig"] != path || outputpkg.LeaseRecord(l, kubeconfig(l))["kubeconfig"] != path {
			t.Fatal("missing kubeconfig")
		}
		s, err = openStore(context.Background(), os.Getenv("BERTH_STATE"))
		if err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
}
func TestBrokenBudgetStillLists(t *testing.T) {
	// Listing leases must remain available even when job configuration is broken.
	t.Setenv("BERTH_STATE", t.TempDir())
	t.Setenv("BERTH_CONFIG", filepath.Join(t.TempDir(), "missing"))
	if invoke(t, 0, "ps")["budget"].(map[string]any)["summary"] != "jobs: budget unavailable (check BERTH_CONFIG)" {
		t.Fatal("lost budget warning")
	}
}
