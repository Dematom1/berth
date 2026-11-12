package lease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	budgetpkg "github.com/Dematom1/dibs/internal/budget"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

func TestJobBudgetAndConfig(t *testing.T) {
	s := &store{Leases: map[string]Lease{"one": {Machine: "local", Reservation: &ReservationView{Budget: budgetpkg.Amount{Cores: 2, Memory: gib}}}, "foreign": {Machine: "remote", Reservation: &ReservationView{Budget: budgetpkg.Amount{Cores: 100, Memory: 100 * gib}}}}}
	if !budgetFits(s, "local", budgetpkg.Amount{Cores: 3, Memory: 2 * gib}, budgetpkg.Amount{Cores: 1, Memory: gib}) {
		t.Fatal("exact budget fit refused")
	}
	for _, req := range []budgetpkg.Amount{{Cores: 2, Memory: gib}, {Cores: 1, Memory: gib + 1}} {
		if budgetFits(s, "local", budgetpkg.Amount{Cores: 3, Memory: 2 * gib}, req) {
			t.Fatal("overbooked budget")
		}
	}
	stale := &store{Leases: map[string]Lease{
		"large": {Machine: "local", Reservation: &ReservationView{Budget: budgetpkg.Amount{Cores: 4, Memory: 3 * gib}}},
		"small": {Machine: "local", Reservation: &ReservationView{Budget: budgetpkg.Amount{Cores: 1, Memory: gib}}},
	}}
	if budgetFits(stale, "local", budgetpkg.Amount{Cores: 3, Memory: 2 * gib}, budgetpkg.Amount{Cores: 1, Memory: 1}) {
		t.Fatal("reduced capacity freed resources held by stale reservations")
	}
}

func jobTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"cores":3,"memory":"4GiB","room_cores":1,"room_memory":"2GiB","classes":{"default":{"cores":1,"memory":"1GiB","timeout":"5s"},"short":{"cores":1,"memory":"1GiB","timeout":"3s"}}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_CONFIG", path)
	return path
}

func childExit(t *testing.T, cmd *exec.Cmd, want int) string {
	t.Helper()
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	t.Logf("command=%q exit=%d output=%q", cmd.Args, code, out)
	if code != want {
		t.Fatalf("%v exited %d want %d: %s", cmd.Args, code, want, out)
	}
	return string(out)
}

func awaitJob(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for job state")
}

func jobStore(t *testing.T, dir string, f func(*store)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := testManager().openStore(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f(s)
}

func cleanupProcessGroup(cmd *exec.Cmd) {
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func registerProcessGroupCleanup(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	t.Cleanup(func() { cleanupProcessGroup(cmd) })
}

func TestProcessGroupCleanupRunsOnSubtestExit(t *testing.T) {
	var pid int
	t.Run("started process", func(t *testing.T) {
		cmd := exec.Command("sh", "-c", "exec sleep 30")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pid = cmd.Process.Pid
		registerProcessGroupCleanup(t, cmd)
	})
	if _, state := ownerpkg.ProcessIdentity(pid); state != ownerpkg.Dead {
		t.Fatal("subtest cleanup left its process alive")
	}
}

func startJobCommand(t *testing.T, binary string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(binary, append([]string{"job"}, args...)...)
	log, err := os.CreateTemp(t.TempDir(), "job-log")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Signal(os.Interrupt)
		}
		_ = cmd.Wait()
		_ = log.Close()
	})
	return cmd
}

func TestJobsRealProcesses(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "dibs")
	if out, err := exec.Command("go", "build", "-o", binary, moduleRoot(t)).CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	developerConfig := t.TempDir()
	if err := os.MkdirAll(filepath.Join(developerConfig, "dibs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(developerConfig, "dibs", "config.json"), []byte(`{"cores":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", developerConfig)
	t.Setenv("DIBS_CONFIG", filepath.Join(developerConfig, "missing.json"))
	t.Run("command metadata excludes arguments", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("DIBS_CONFIG", "")
		dir := t.TempDir()
		t.Setenv("DIBS_STATE", dir)
		marker := filepath.Join(t.TempDir(), "started")
		secret := "private-argument-do-not-display"
		holder := startJobCommand(t, binary, "--", "sh", "-c", `touch "$1"; exec sleep 30`, "_", marker, secret)
		awaitJob(t, func() bool { _, err := os.Stat(marker); return err == nil })
		state, err := os.ReadFile(filepath.Join(dir, "state.json"))
		if err != nil {
			t.Fatal("could not read persisted lease state")
		}
		plain := childExit(t, exec.Command(binary, "list"), 0)
		structured := childExit(t, exec.Command(binary, "list", "--json"), 0)
		if strings.Contains(string(state), secret) || strings.Contains(plain, secret) || strings.Contains(structured, secret) {
			t.Fatal("job arguments leaked into lease metadata")
		}
		if !strings.Contains(string(state), `"resources": "sh"`) || !strings.Contains(plain, "sh") || !strings.Contains(structured, `"resources":"sh"`) {
			t.Fatal("executable basename missing from lease metadata")
		}
		holder.Process.Kill()
		holder.Wait()
		childExit(t, exec.Command(binary, "sweep", "--confirm"), 0)
	})
	t.Run("partial config runs default class", func(t *testing.T) {
		config := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(config, []byte(`{"classes":{"suite":{"cores":1,"memory":"1GiB","timeout":"1h"}}}`), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("DIBS_CONFIG", config)
		t.Setenv("DIBS_STATE", t.TempDir())
		childExit(t, exec.Command(binary, "job", "--", "true"), 0)
	})
	jobTestConfig(t)
	t.Run("exit codes and transparent argv", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DIBS_STATE", dir)
		for _, code := range []int{0, 1, 2, 37, 75, 124, 255} {
			childExit(t, exec.Command(binary, "job", "--", "sh", "-c", "exit "+strconv.Itoa(code)), code)
		}
		out := childExit(t, exec.Command(binary, "--json", "job", "--", "sh", "-c", `printf '%s' "$1"`, "_", "--timeout=child-flag"), 0)
		if out != "--timeout=child-flag" {
			t.Fatalf("child output/argv changed: %q", out)
		}
		childExit(t, exec.Command(binary, "job", "--", "sh", "-c", "kill -TERM $$"), 143)
		childExit(t, exec.Command(binary, "job", "--", "true"), 0)
		jobStore(t, dir, func(s *store) {
			if len(s.Leases) != 0 || len(s.Waiters) != 0 {
				t.Fatalf("finished jobs leaked state: %+v", s)
			}
		})
	})
	t.Run("usage and prestart errors", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "untouched")
		t.Setenv("DIBS_STATE", dir)
		for _, args := range [][]string{{"--cores", "0", "--", "true"}, {"--memory", "3GiB", "--", "true"}, {"--cores", "3", "--", "true"}, {"--timeout", "0s", "--", "true"}, {"--wait", "-1s", "--", "true"}, {"--class", "missing", "--", "true"}, {"--typo", "--", "true"}, {"true"}} {
			childExit(t, exec.Command(binary, append([]string{"job"}, args...)...), 2)
		}
		childExit(t, exec.Command(binary, "job", "--", "dibs-missing-child-executable"), 1)
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("invalid usage reached store")
		}
	})
	t.Run("startup keeps admission deadline", func(t *testing.T) {
		dir, bin := t.TempDir(), t.TempDir()
		t.Setenv("DIBS_STATE", dir)
		realPS, err := exec.LookPath("ps")
		if err != nil {
			t.Fatal(err)
		}
		// Delay the real external identity probe only after admission is durable.
		probe := "#!/bin/sh\nif [ -s \"$DIBS_STATE/state.json\" ]; then\n  touch \"$DIBS_STATE/probing\"\n  while [ ! -e \"$DIBS_STATE/resume\" ]; do sleep 0.01; done\nfi\nexec " + shellQuote(realPS) + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, "ps"), []byte(probe), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		marker := filepath.Join(dir, "child-started")
		cmd := startJobCommand(t, binary, "--timeout", "1s", "--", "sh", "-c", `touch "$1"`, "_", marker)
		awaitJob(t, func() bool { _, err := os.Stat(filepath.Join(dir, "probing")); return err == nil })
		data, err := os.ReadFile(filepath.Join(dir, "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		var state store
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		if len(state.Leases) != 1 {
			t.Fatalf("expected one admitted lease: %s", data)
		}
		for _, l := range state.Leases {
			time.Sleep(time.Until(l.Expires) + 20*time.Millisecond)
		}
		if err := os.WriteFile(filepath.Join(dir, "resume"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		err = cmd.Wait()
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			t.Fatalf("pre-start expiry should exit 1, got %v", err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("child executed after its admission deadline")
		}
		log, err := os.ReadFile(cmd.Stdout.(*os.File).Name())
		if err != nil || !strings.Contains(string(log), "job reservation expired before child start") {
			t.Fatalf("wrong pre-start failure: %s %v", log, err)
		}
	})
	t.Run("deadline flag class and grandchildren", func(t *testing.T) {
		for _, options := range [][]string{{"--timeout", "1s"}, {"--class", "short"}} {
			dir := t.TempDir()
			t.Setenv("DIBS_STATE", dir)
			pidfile := filepath.Join(t.TempDir(), "pid")
			script := `trap '' TERM; sh -c 'trap "" TERM; echo $$ > "$1"; exec sleep 30' _ "$1" & wait`
			args := append([]string{"job"}, options...)
			args = append(args, "--", "sh", "-c", script, "_", pidfile)
			started := time.Now()
			childExit(t, exec.Command(binary, args...), 124)
			if time.Since(started) > 7*time.Second {
				t.Fatal("deadline not enforced promptly")
			}
			data, err := os.ReadFile(pidfile)
			if err != nil {
				t.Fatal(err)
			}
			pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			if _, live := ownerpkg.ProcessIdentity(pid); live != ownerpkg.Dead {
				t.Fatal("deadline leaked grandchild")
			}
			jobStore(t, dir, func(s *store) {
				if len(s.Leases) != 0 {
					t.Fatal("timeout leaked reservation")
				}
			})
		}
	})
	t.Run("FIFO no backfill wait code and ps", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DIBS_STATE", dir)
		marker := filepath.Join(t.TempDir(), "started")
		holder := startJobCommand(t, binary, "--timeout", "30s", "--", "sh", "-c", `touch "$1"; exec sleep 30`, "_", marker)
		awaitJob(t, func() bool { _, err := os.Stat(marker); return err == nil })
		out := childExit(t, exec.Command(binary, "ps", "--json"), 0)
		var view struct {
			Leases []struct {
				ID     string
				Cores  int
				Memory string
				Class  string
			}
		}
		if err := json.Unmarshal([]byte(out), &view); err != nil || len(view.Leases) != 1 || view.Leases[0].Cores != 1 || view.Leases[0].Memory != "1073741824B" {
			t.Fatalf("missing ps reservation: %s %v", out, err)
		}
		childExit(t, exec.Command(binary, "extend", view.Leases[0].ID, "--ttl", "1h"), 1)
		order := filepath.Join(t.TempDir(), "order")
		big := startJobCommand(t, binary, "--cores", "2", "--memory", "2GiB", "--wait", "10s", "--", "sh", "-c", `echo big >> "$1"; sleep 0.1`, "_", order)
		awaitJob(t, func() bool { n := 0; jobStore(t, dir, func(s *store) { n = len(s.Waiters) }); return n == 1 })
		// One core is free, but the earlier 2-core waiter owns the next admission.
		zeroWaitMarker := filepath.Join(t.TempDir(), "zero-wait-started")
		childExit(t, exec.Command(binary, "job", "--wait", "0", "--", "touch", zeroWaitMarker), 75)
		if _, err := os.Stat(zeroWaitMarker); !os.IsNotExist(err) {
			t.Fatal("zero-wait child backfilled ahead of queued job")
		}
		childExit(t, exec.Command(binary, "job", "--wait", "100ms", "--", "sh", "-c", `echo bypass >> "$1"`, "_", order), 75)
		small := startJobCommand(t, binary, "--wait", "10s", "--", "sh", "-c", `echo small >> "$1"`, "_", order)
		awaitJob(t, func() bool { n := 0; jobStore(t, dir, func(s *store) { n = len(s.Waiters) }); return n == 2 })
		if _, err := os.Stat(order); !os.IsNotExist(err) {
			t.Fatal("later waiter backfilled")
		}
		childExit(t, exec.Command(binary, "release", view.Leases[0].ID, "--confirm"), 0)
		if err := holder.Wait(); err == nil {
			t.Fatal("released job remained alive")
		}
		if err := big.Wait(); err != nil {
			t.Fatal(err)
		}
		if err := small.Wait(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(order)
		if err != nil || string(data) != "big\nsmall\n" {
			t.Fatalf("non-FIFO execution: %q %v", data, err)
		}
	})
	t.Run("wait exhaustion does not start child", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DIBS_STATE", dir)
		marker := filepath.Join(t.TempDir(), "holder")
		holder := startJobCommand(t, binary, "--cores", "2", "--memory", "2GiB", "--timeout", "30s", "--", "sh", "-c", `touch "$1"; exec sleep 30`, "_", marker)
		awaitJob(t, func() bool { _, err := os.Stat(marker); return err == nil })
		childMarker := filepath.Join(t.TempDir(), "must-not-exist")
		for _, wait := range []string{"0", "100ms"} {
			childExit(t, exec.Command(binary, "job", "--wait", wait, "--", "touch", childMarker), 75)
		}
		if _, err := os.Stat(childMarker); !os.IsNotExist(err) {
			t.Fatal("unadmitted child ran")
		}
		holder.Process.Kill()
		holder.Wait()
		childExit(t, exec.Command(binary, "sweep", "--confirm"), 0)
	})
	t.Run("sweep owner death and stale waiter", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DIBS_STATE", dir)
		marker := filepath.Join(t.TempDir(), "holder")
		holder := startJobCommand(t, binary, "--cores", "2", "--memory", "2GiB", "--timeout", "30s", "--", "sh", "-c", `touch "$1"; exec sleep 30`, "_", marker)
		awaitJob(t, func() bool { _, err := os.Stat(marker); return err == nil })
		waiter := startJobCommand(t, binary, "--wait", "30s", "--", "true")
		awaitJob(t, func() bool { n := 0; jobStore(t, dir, func(s *store) { n = len(s.Waiters) }); return n == 1 })
		waiter.Process.Kill()
		waiter.Wait()
		holder.Process.Kill()
		holder.Wait()
		var pgid int
		jobStore(t, dir, func(s *store) {
			for _, l := range s.Leases {
				pgid, _ = strconv.Atoi(l.Reservation.Leader.ID)
			}
		})
		childExit(t, exec.Command(binary, "sweep", "--confirm"), 0)
		alive, err := groupAlive(context.Background(), pgid)
		if err != nil || alive {
			t.Fatal("sweep left orphan process group", err)
		}
		childExit(t, exec.Command(binary, "job", "--wait", "0", "--", "true"), 0)
		jobStore(t, dir, func(s *store) {
			if len(s.Leases)+len(s.Waiters) != 0 {
				t.Fatal("dead jobs/waiters retained budget")
			}
		})
	})
	t.Run("normal completion cleans descendants", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DIBS_STATE", dir)
		pidfile := filepath.Join(t.TempDir(), "child-pid")
		script := `sh -c 'echo $$ > "$1"; trap "" TERM; exec sleep 30' _ "$1" & while ! test -s "$1"; do sleep 0.01; done; exit 37`
		childExit(t, exec.Command(binary, "job", "--", "sh", "-c", script, "_", pidfile), 37)
		data, err := os.ReadFile(pidfile)
		if err != nil {
			t.Fatal(err)
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if _, live := ownerpkg.ProcessIdentity(pid); live != ownerpkg.Dead {
			t.Fatal("normal completion leaked descendant")
		}
		jobStore(t, dir, func(s *store) {
			if len(s.Leases) != 0 {
				t.Fatal("descendant cleanup leaked reservation")
			}
		})
	})
	t.Run("timeout cleanup failure retains reservation", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DIBS_STATE", dir)
		ps, err := exec.LookPath("ps")
		if err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		marker := filepath.Join(bin, "fail")
		script := "#!/bin/sh\nif test -e " + strconv.Quote(marker) + " && test \"$1\" = -A; then exit 1; fi\nexec " + strconv.Quote(ps) + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, "ps"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		originalPath := os.Getenv("PATH")
		t.Setenv("PATH", bin+string(os.PathListSeparator)+originalPath)
		pidfile := filepath.Join(t.TempDir(), "descendant")
		out := childExit(t, exec.Command(binary, "job", "--timeout", "1s", "--", "sh", "-c", `touch "$2"; sleep 30 </dev/null >/dev/null 2>&1 & echo $! > "$1"; wait`, "_", pidfile, marker), 124)
		if !strings.Contains(out, "cleanup incomplete") {
			t.Fatal("timeout cleanup failure was silent")
		}
		jobStore(t, dir, func(s *store) {
			if len(s.Leases) != 1 {
				t.Fatal("timeout cleanup failure released reservation")
			}
		})
		data, err := os.ReadFile(pidfile)
		if err != nil {
			t.Fatal("timeout child did not start")
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Setenv("PATH", originalPath)
		childExit(t, exec.Command(binary, "sweep", "--confirm"), 0)
	})
	t.Run("cleanup failure retains child code and lease", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DIBS_STATE", dir)
		ps, err := exec.LookPath("ps")
		if err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		marker := filepath.Join(bin, "fail")
		script := "#!/bin/sh\nif test -e " + strconv.Quote(marker) + "; then exit 1; fi\nexec " + strconv.Quote(ps) + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, "ps"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		out := childExit(t, exec.Command(binary, "job", "--", "sh", "-c", `touch "$1"; exit 37`, "_", marker), 37)
		if !strings.Contains(out, "cleanup incomplete") {
			t.Fatalf("cleanup failure was silent: %s", out)
		}
		jobStore(t, dir, func(s *store) {
			if len(s.Leases) != 1 {
				t.Fatal("cleanup failure lost reservation")
			}
		})
		if err := os.Remove(marker); err != nil {
			t.Fatal(err)
		}
		childExit(t, exec.Command(binary, "sweep", "--confirm"), 0)
	})
	t.Run("cancel cleans group", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DIBS_STATE", dir)
		marker := filepath.Join(t.TempDir(), "started")
		cmd := startJobCommand(t, binary, "--", "sh", "-c", `touch "$1"; exec sleep 30`, "_", marker)
		awaitJob(t, func() bool { _, err := os.Stat(marker); return err == nil })
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err == nil {
			t.Fatal("cancelled child reported success")
		}
		jobStore(t, dir, func(s *store) {
			if len(s.Leases) != 0 {
				t.Fatal("cancellation leaked budget")
			}
		})
	})
}

func TestDeadlineProbeSeesCompletedUnreapedChild(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 37")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	awaitJob(t, func() bool {
		completed, err := childExited(pid)
		return err == nil && completed
	})
	completed, err := childCompletion(make(chan error), pid)
	if err != nil || !completed {
		t.Fatal("deadline probe missed completed child")
	}
	if err := cmd.Wait(); err == nil || cmd.ProcessState.ExitCode() != 37 {
		t.Fatal("child's own completion status was not preserved")
	}
}

func TestDestroyDoesNotSignalGroupAfterLeaderReaped(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "child")
	cmd := exec.Command("sh", "-c", `sleep 30 </dev/null >/dev/null 2>&1 & echo $! > "$1"; read _; exit 0`, "_", pidfile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	release, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = release.Close() })
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	registerProcessGroupCleanup(t, cmd)
	leader, err := bindOwner("pid:"+strconv.Itoa(pid), 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	awaitJob(t, func() bool { _, err := os.Stat(pidfile); return err == nil })
	if err := release.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	lease := Lease{Machine: "local", ResourceRef: adapterpkg.ResourceRef{Adapter: "process"}, Reservation: &ReservationView{Leader: leader}}
	lease.ID = "orphaned-job"
	dir := t.TempDir()
	jobStore(t, dir, func(s *store) {
		s.Leases[lease.ID] = lease
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
		if _, err := releaseLease(context.Background(), s, lease.ID, "local", testAdapters()); err != nil {
			t.Fatalf("missing leader should permit lease release: %v", err)
		}
		if _, ok := s.Leases[lease.ID]; ok {
			t.Fatal("unverifiable group lease was retained")
		}
	})
	jobStore(t, dir, func(s *store) {
		if _, ok := s.Leases[lease.ID]; ok {
			t.Fatal("lease release was not durable")
		}
	})
	alive, err := groupAlive(context.Background(), pid)
	if err != nil || !alive {
		t.Fatal("release signaled an unverifiable group")
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func TestDestroyRejectsReusedZombieGroupLeader(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "child")
	cmd := exec.Command("sh", "-c", `sleep 30 & echo $! > "$1"; exit`, "_", pidfile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	registerProcessGroupCleanup(t, cmd)
	awaitJob(t, func() bool {
		_, err := os.Stat(pidfile)
		_, state := ownerpkg.ProcessIdentity(pid)
		return err == nil && state == ownerpkg.Dead
	})
	identity, state := ownerpkg.ProcessIdentity(pid)
	if state != ownerpkg.Dead || identity == "" {
		t.Fatal("zombie leader identity unavailable")
	}
	lease := Lease{Reservation: &ReservationView{Leader: ownerpkg.Reference{ID: strconv.Itoa(pid), Identity: "different-start"}}}
	if err := (processResource{}).Destroy(context.Background(), lease); err == nil {
		t.Fatal("destroy accepted a reused zombie group leader")
	}
	alive, err := groupAlive(context.Background(), pid)
	if err != nil || !alive {
		t.Fatal("destroy signaled a group with a mismatched leader identity")
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = cmd.Wait()
}

func TestDestroyRejectsReusedLeaderPID(t *testing.T) {
	identity, live := ownerpkg.ProcessIdentity(os.Getpid())
	if live != ownerpkg.Alive {
		t.Fatal("test process identity unavailable")
	}
	lease := Lease{Reservation: &ReservationView{Leader: ownerpkg.Reference{ID: strconv.Itoa(os.Getpid()), Identity: "not-the-current-start"}}}
	if err := (processResource{}).Destroy(context.Background(), lease); err == nil {
		t.Fatal("destroy accepted a reused process ID")
	}
	if _, live := ownerpkg.ProcessIdentity(os.Getpid()); live != ownerpkg.Alive {
		t.Fatal("destroy signaled the unrelated process")
	}
	if current, _ := ownerpkg.ProcessIdentity(os.Getpid()); current != identity {
		t.Fatal("test process identity changed")
	}
}

func TestExpiredWaiterCleanupAfterLockContention(t *testing.T) {
	dir := t.TempDir()
	owner, err := bindOwner("pid:"+strconv.Itoa(os.Getpid()), 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := newLease(owner, time.Minute, "", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	lease.Reservation = &ReservationView{Budget: budgetpkg.Amount{Cores: 2, Memory: 2}}
	result := make(chan error, 1)
	go func() {
		_, err := testManager().admitJob(context.Background(), dir, lease, budgetpkg.Amount{Cores: 1, Memory: 1}, 200*time.Millisecond)
		result <- err
	}()
	awaitJob(t, func() bool {
		var waiting bool
		jobStore(t, dir, func(s *store) { waiting = len(s.Waiters) == 1 })
		return waiting
	})
	lockCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	locked, err := testManager().openStore(lockCtx, dir)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	locked.Close()
	if err := <-result; !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("admission returned %v", err)
	}
	jobStore(t, dir, func(s *store) {
		if len(s.Waiters) != 0 {
			t.Fatalf("expired waiter remains after lock release: %+v", s.Waiters)
		}
	})
}

func TestAdmissionReclaimHasLiveSharedCleanupBudget(t *testing.T) {
	for _, count := range []int{1, 7} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			dir := t.TempDir()
			owner, err := bindOwner("pid:"+strconv.Itoa(os.Getpid()), 0, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			request, err := newLease(owner, time.Minute, "true", nil, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			request.ResourceRef.Adapter = "process"
			request.Reservation = &ReservationView{Budget: budgetpkg.Amount{Cores: 1, Memory: 1}, Class: "default"}
			for i := 0; i < count; i++ {
				ready := filepath.Join(t.TempDir(), "ready")
				script := `echo ready > "$1"; exec sleep 30`
				if count > 1 {
					script = `trap '' TERM; ` + script
				}
				cmd := exec.Command("sh", "-c", script, "_", ready)
				cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				registerProcessGroupCleanup(t, cmd)
				awaitJob(t, func() bool { _, err := os.Stat(ready); return err == nil })
				leader, err := bindOwner("pid:"+strconv.Itoa(cmd.Process.Pid), 0, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				stale, err := newLease(ownerpkg.Reference{Kind: "heartbeat", ID: filepath.Join(dir, "missing-owner"), Stale: time.Minute}, time.Minute, "sleep", nil, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				stale.ResourceRef.Adapter = "process"
				stale.Reservation = &ReservationView{Budget: budgetpkg.Amount{Cores: 1, Memory: 1}, Class: "default", Leader: leader}
				jobStore(t, dir, func(s *store) {
					s.Leases[stale.ID] = stale
					if err := s.Save(); err != nil {
						t.Fatal(err)
					}
				})
			}
			_, err = testManager().admitJob(context.Background(), dir, request, budgetpkg.Amount{Cores: 1, Memory: 1}, 0)
			if count == 1 {
				if err != nil {
					t.Fatalf("zero-wait request did not reclaim the dead owner's live job: %v", err)
				}
				jobStore(t, dir, func(s *store) {
					if len(s.Leases) != 1 || s.Leases[request.ID].ID != request.ID {
						t.Fatal("reclaimed reservation was not replaced by admitted request")
					}
				})
			} else {
				if !errors.Is(err, ErrNoCapacity) {
					t.Fatalf("cleanup budget restarted for each lease: %v", err)
				}
				jobStore(t, dir, func(s *store) {
					if len(s.Leases) == 0 || len(s.Leases) >= count {
						t.Fatal("shared cleanup budget must reclaim some jobs and retain unfinished cleanup")
					}
				})
			}
		})
	}
}

func TestJobQueuePrunesExpiredButKeepsUnknown(t *testing.T) {
	now := time.Now()
	owner := ownerpkg.Reference{Kind: "pid", ID: "not-a-pid", Identity: "unknown"}
	s := &store{Waiters: []waiter{{ID: "expired", Machine: "local", Owner: owner, Expires: now}, {ID: "unknown", Machine: "local", Owner: owner, Expires: now.Add(time.Hour)}, {ID: "foreign", Machine: "remote", Owner: owner, Expires: now}}}
	testManager().pruneWaiters(s, "local", now)
	if len(s.Waiters) != 2 || s.Waiters[0].ID != "unknown" || s.Waiters[1].ID != "foreign" {
		t.Fatalf("unsafe waiter pruning: %+v", s.Waiters)
	}
}

func TestJobHelperEOFDoesNotRun(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-run")
	binary := filepath.Join(t.TempDir(), "dibs")
	if out, err := exec.Command("go", "build", "-o", binary, moduleRoot(t)).CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "__job_exec", "/bin/sh", "-c", fmt.Sprintf("touch %s", strconv.Quote(marker)))
	cmd.ExtraFiles = []*os.File{read}
	write.Close()
	defer read.Close()
	childExit(t, cmd, 125)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("helper ran without durable admission")
	}
}
