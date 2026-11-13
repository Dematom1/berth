//go:build integration

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	shellpkg "github.com/Dematom1/dibs/internal/adapter/shell"
)

// TestIntegrationShellLease drives a real interactive login shell on a real
// terminal: adopt it with a helper child under an exec owner, prove reclaim
// refuses while the owner lives and while a foreground job runs, then prove a
// confirmed sweep removes the shell and its helper once the owner is gone.
func TestIntegrationShellLease(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash required for shell integration: %v", err)
	}
	dir := t.TempDir()
	for name, value := range map[string]string{"DIBS_STATE": filepath.Join(dir, "state"), "XDG_CONFIG_HOME": filepath.Join(dir, "config"), "DIBS_SHELL_ADAPTER": "1", "DIBS_EXEC_OWNER": "1", "DIBS_CONSUMER_CONTRACT": "1", "DIBS_LEASE_POLICIES": "1"} {
		t.Setenv(name, value)
	}
	master, slave, err := openPTY()
	if err != nil {
		t.Fatalf("open pseudo-terminal: %v", err)
	}
	defer master.Close()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	// A real login shell, isolated from the user's profile by an empty HOME.
	shell := exec.Command(bash, "--login", "-i")
	shell.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=dumb", "PS1=$ "}
	shell.Stdin, shell.Stdout, shell.Stderr = slave, slave, slave
	shell.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := shell.Start(); err != nil {
		t.Fatal(err)
	}
	slave.Close()
	exited := make(chan struct{})
	go func() { shell.Wait(); close(exited) }()
	defer func() {
		shell.Process.Kill()
		<-exited
	}()
	var screen bytes.Buffer
	var mu sync.Mutex
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			mu.Lock()
			screen.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	pid := shell.Process.Pid
	waitFor := func(what string, ok func(map[int]shellpkg.Process) bool) map[int]shellpkg.Process {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if procs, err := shellpkg.PS(context.Background()); err == nil && ok(procs) {
				return procs
			}
		}
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("timed out waiting for %s; terminal:\n%s", what, screen.String())
		return nil
	}
	children := func(procs map[int]shellpkg.Process, name string) []shellpkg.Process {
		var found []shellpkg.Process
		for _, p := range procs {
			if p.PPID == pid && strings.HasSuffix(p.Name, name) {
				found = append(found, p)
			}
		}
		return found
	}
	waitFor("the shell to own its terminal", func(procs map[int]shellpkg.Process) bool {
		p := procs[pid]
		return p.TPGID == p.PGID && p.TPGID > 0
	})
	// The helper stands in for gitstatusd: a background child of the shell.
	io.WriteString(master, "sleep 600 &\n")
	procs := waitFor("the helper", func(procs map[int]shellpkg.Process) bool {
		return len(children(procs, "sleep")) == 1 && procs[pid].TPGID == procs[pid].PGID
	})
	helper := children(procs, "sleep")[0]

	marker := filepath.Join(dir, "task.open")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	owner := "exec:test -e " + marker
	cli := func(want int, args ...string) map[string]any {
		t.Helper()
		var out bytes.Buffer
		if code := run(context.Background(), append([]string{"--json"}, args...), &out); code != want {
			t.Fatalf("dibs %s = %d, want %d: %s", strings.Join(args, " "), code, want, out.String())
		}
		var result map[string]any
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("dibs %s output: %v: %s", strings.Join(args, " "), err, out.String())
		}
		return result
	}
	adopted := cli(0, "adopt", "shell", strconv.Itoa(pid), "--owner", owner)
	record := adopted["record"].(map[string]any)
	id := record["id"].(string)
	members, _ := json.Marshal(record["resource"].(map[string]any)["process"])
	if !strings.Contains(string(members), `"pid":`+strconv.Itoa(helper.PID)) {
		t.Fatalf("lease did not record helper %d: %s", helper.PID, members)
	}
	if p := record["policy"].(map[string]any); p["row"] != "shell" || p["binding"] != "hybrid" {
		t.Fatalf("shell lease policy = %v", p)
	}
	listed := cli(0, "list", "--owner", owner)
	if listed["count"].(float64) != 1 {
		t.Fatalf("list by owner = %v", listed)
	}

	// A live owner: reclaim refuses and sweep leaves the shell alone.
	if refused := cli(3, "reclaim", id, "--confirm"); refused["reason"] != "owner_alive" {
		t.Fatalf("reclaim with a live owner = %v", refused)
	}
	if swept := cli(0, "sweep", "--confirm"); len(swept["reclaimed"].([]any)) != 0 {
		t.Fatalf("sweep with a live owner = %v", swept)
	}
	explained := cli(0, "explain", id)
	if explained["held_because"] != "owner_alive" || !strings.Contains(explained["why"].(string), "owner-bound") || explained["remaining"] == "" {
		t.Fatalf("explain with a live owner = %v", explained)
	}

	// A foreground job holds the shell even after its owner is gone.
	io.WriteString(master, "sleep 300\n")
	waitFor("the foreground job", func(procs map[int]shellpkg.Process) bool {
		p := procs[pid]
		return p.TPGID > 0 && p.TPGID != p.PGID
	})
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if refused := cli(3, "reclaim", id, "--confirm"); refused["reason"] != "resource_busy" {
		t.Fatalf("reclaim with a foreground job = %v", refused)
	}
	if refused := cli(3, "release", id, "--confirm"); refused["reason"] != "resource_busy" {
		t.Fatalf("release with a foreground job = %v", refused)
	}
	swept := cli(0, "sweep", "--confirm")
	if held := swept["held"].(map[string]any); held[id] != "resource_busy" || len(swept["reclaimed"].([]any)) != 0 {
		t.Fatalf("sweep with a foreground job = %v", swept)
	}
	explained = cli(0, "explain", id)
	if explained["held_because"] != "resource_busy" || explained["trigger"] != "owner_dead" {
		t.Fatalf("explain with a foreground job = %v", explained)
	}

	// Once the job ends, the orphaned shell and its helper are reclaimed.
	io.WriteString(master, "\x03")
	waitFor("the shell to take its terminal back", func(procs map[int]shellpkg.Process) bool {
		p := procs[pid]
		return p.TPGID == p.PGID
	})
	swept = cli(0, "sweep", "--confirm")
	if reclaimed := swept["reclaimed"].([]any); len(reclaimed) != 1 || reclaimed[0] != id {
		t.Fatalf("sweep after the owner died = %v", swept)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("shell survived reclaim")
	}
	waitFor("the helper to exit", func(procs map[int]shellpkg.Process) bool {
		p, ok := procs[helper.PID]
		return !ok || p.Start != helper.Start || strings.HasPrefix(p.Stat, "Z")
	})
	if listed := cli(0, "list"); listed["count"].(float64) != 0 {
		t.Fatalf("lease survived reclaim: %v", listed)
	}
}
