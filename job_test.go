package main

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
)

func TestJobBudgetAndConfig(t *testing.T) {
	for _, tc := range []struct {
		cores      int
		memory     int64
		wantCores  int
		wantMemory int64
	}{
		{16, 32 * gib, 12, 24 * gib}, {1, gib, 1, gib / 2}, {4, 4 * gib, 3, 3 * gib},
	} {
		c := defaultJobConfig(tc.cores, tc.memory)
		got, err := c.available()
		if err != nil || got != (Budget{tc.wantCores, tc.wantMemory}) {
			t.Fatalf("owner room: %+v %v", got, err)
		}
	}
	for s, want := range map[string]int64{"1GiB": gib, "2MiB": 2 << 20, "1GB": 1000000000, "123": 123, "0B": 0} {
		if got, err := memoryBytes(s); err != nil || got != want {
			t.Fatalf("memory %s: %d %v", s, got, err)
		}
	}
	for _, s := range []string{"-1", "1.5GiB", "bogus", "9223372036854775807GiB"} {
		if _, err := memoryBytes(s); err == nil {
			t.Fatalf("accepted memory %q", s)
		}
	}
	c := defaultJobConfig(4, 4*gib)
	c.RoomCores = 4
	if _, err := c.available(); err == nil {
		t.Fatal("owner cores consumed entire budget")
	}
	c = defaultJobConfig(4, 4*gib)
	c.RoomMemory = "4GiB"
	if _, err := c.available(); err == nil {
		t.Fatal("owner memory consumed entire budget")
	}
	s := &Store{Leases: map[string]Lease{"one": {Machine: "local", Job: &JobResource{Budget: Budget{2, gib}}}, "foreign": {Machine: "remote", Job: &JobResource{Budget: Budget{100, 100 * gib}}}}}
	if !budgetFits(s, "local", Budget{3, 2 * gib}, Budget{1, gib}) {
		t.Fatal("exact budget fit refused")
	}
	for _, req := range []Budget{{2, gib}, {1, gib + 1}} {
		if budgetFits(s, "local", Budget{3, 2 * gib}, req) {
			t.Fatal("overbooked budget")
		}
	}
	stale := &Store{Leases: map[string]Lease{
		"large": {Machine: "local", Job: &JobResource{Budget: Budget{4, 3 * gib}}},
		"small": {Machine: "local", Job: &JobResource{Budget: Budget{1, gib}}},
	}}
	if budgetFits(stale, "local", Budget{3, 2 * gib}, Budget{1, 1}) {
		t.Fatal("reduced capacity freed resources held by stale reservations")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("BERTH_CONFIG", path)
	for _, data := range []string{`null`, `[]`, `{"classes":{"":{"cores":1,"memory":"1GiB","timeout":"1h"}}}`, `{"typo":1}`, `{"cores":0}`, `{"room_memory":"999999GiB"}`, `{"classes":{"suite":{"cores":1,"memory":"1MiB","timeout":"0s"}}}`, `{} {}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadJobConfig(); err == nil {
			t.Fatalf("accepted bad config %s", data)
		}
	}
}

func TestPartialConfigKeepsDefaultClass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"classes":{"suite":{"cores":1,"memory":"1GiB","timeout":"1h"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERTH_CONFIG", path)
	config, err := loadJobConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := config.class("default"); err != nil {
		t.Fatalf("default class lost: %v", err)
	}
	if _, _, err := config.class("suite"); err != nil {
		t.Fatalf("configured class lost: %v", err)
	}
}

func jobTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"cores":3,"memory":"4GiB","room_cores":1,"room_memory":"2GiB","classes":{"default":{"cores":1,"memory":"1GiB","timeout":"5s"},"short":{"cores":1,"memory":"1GiB","timeout":"200ms"}}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERTH_CONFIG", path)
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

func jobStore(t *testing.T, dir string, f func(*Store)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := openStore(ctx, dir)
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
	if _, state := processIdentity(pid); state != Dead {
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
	binary := filepath.Join(t.TempDir(), "berth")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	t.Run("command metadata excludes arguments", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("BERTH_STATE", dir)
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
		t.Setenv("BERTH_CONFIG", config)
		t.Setenv("BERTH_STATE", t.TempDir())
		childExit(t, exec.Command(binary, "job", "--", "true"), 0)
	})
	jobTestConfig(t)
	t.Run("exit codes and transparent argv", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("BERTH_STATE", dir)
		for _, code := range []int{0, 1, 2, 37, 75, 124, 255} {
			childExit(t, exec.Command(binary, "job", "--", "sh", "-c", "exit "+strconv.Itoa(code)), code)
		}
		out := childExit(t, exec.Command(binary, "--json", "job", "--", "sh", "-c", `printf '%s' "$1"`, "_", "--timeout=child-flag"), 0)
		if out != "--timeout=child-flag" {
			t.Fatalf("child output/argv changed: %q", out)
		}
		childExit(t, exec.Command(binary, "job", "--", "sh", "-c", "kill -TERM $$"), 143)
		childExit(t, exec.Command(binary, "job", "--", "true"), 0)
		jobStore(t, dir, func(s *Store) {
			if len(s.Leases) != 0 || len(s.Waiters) != 0 {
				t.Fatalf("finished jobs leaked state: %+v", s)
			}
		})
	})
	t.Run("usage and prestart errors", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "untouched")
		t.Setenv("BERTH_STATE", dir)
		for _, args := range [][]string{{"--cores", "0", "--", "true"}, {"--memory", "3GiB", "--", "true"}, {"--cores", "3", "--", "true"}, {"--timeout", "0s", "--", "true"}, {"--wait", "-1s", "--", "true"}, {"--class", "missing", "--", "true"}, {"--typo", "--", "true"}, {"true"}} {
			childExit(t, exec.Command(binary, append([]string{"job"}, args...)...), 2)
		}
		childExit(t, exec.Command(binary, "job", "--", "berth-missing-child-executable"), 1)
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("invalid usage reached store")
		}
	})
	t.Run("deadline flag class and grandchildren", func(t *testing.T) {
		for _, options := range [][]string{{"--timeout", "200ms"}, {"--class", "short"}} {
			dir := t.TempDir()
			t.Setenv("BERTH_STATE", dir)
			pidfile := filepath.Join(t.TempDir(), "pid")
			script := `trap '' TERM; sh -c 'trap "" TERM; echo $$ > "$1"; exec sleep 30' _ "$1" & wait`
			args := append([]string{"job"}, options...)
			args = append(args, "--", "sh", "-c", script, "_", pidfile)
			started := time.Now()
			childExit(t, exec.Command(binary, args...), 124)
			if time.Since(started) > 5*time.Second {
				t.Fatal("deadline not enforced promptly")
			}
			data, err := os.ReadFile(pidfile)
			if err != nil {
				t.Fatal(err)
			}
			pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			if _, live := processIdentity(pid); live != Dead {
				t.Fatal("deadline leaked grandchild")
			}
			jobStore(t, dir, func(s *Store) {
				if len(s.Leases) != 0 {
					t.Fatal("timeout leaked reservation")
				}
			})
		}
	})
	t.Run("FIFO no backfill wait code and ps", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("BERTH_STATE", dir)
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
		awaitJob(t, func() bool { n := 0; jobStore(t, dir, func(s *Store) { n = len(s.Waiters) }); return n == 1 })
		// One core is free, but the earlier 2-core waiter owns the next admission.
		zeroWaitMarker := filepath.Join(t.TempDir(), "zero-wait-started")
		childExit(t, exec.Command(binary, "job", "--wait", "0", "--", "touch", zeroWaitMarker), 75)
		if _, err := os.Stat(zeroWaitMarker); !os.IsNotExist(err) {
			t.Fatal("zero-wait child backfilled ahead of queued job")
		}
		childExit(t, exec.Command(binary, "job", "--wait", "100ms", "--", "sh", "-c", `echo bypass >> "$1"`, "_", order), 75)
		small := startJobCommand(t, binary, "--wait", "10s", "--", "sh", "-c", `echo small >> "$1"`, "_", order)
		awaitJob(t, func() bool { n := 0; jobStore(t, dir, func(s *Store) { n = len(s.Waiters) }); return n == 2 })
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
		t.Setenv("BERTH_STATE", dir)
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
		t.Setenv("BERTH_STATE", dir)
		marker := filepath.Join(t.TempDir(), "holder")
		holder := startJobCommand(t, binary, "--cores", "2", "--memory", "2GiB", "--timeout", "30s", "--", "sh", "-c", `touch "$1"; exec sleep 30`, "_", marker)
		awaitJob(t, func() bool { _, err := os.Stat(marker); return err == nil })
		waiter := startJobCommand(t, binary, "--wait", "30s", "--", "true")
		awaitJob(t, func() bool { n := 0; jobStore(t, dir, func(s *Store) { n = len(s.Waiters) }); return n == 1 })
		waiter.Process.Kill()
		waiter.Wait()
		holder.Process.Kill()
		holder.Wait()
		var pgid int
		jobStore(t, dir, func(s *Store) {
			for _, l := range s.Leases {
				pgid, _ = strconv.Atoi(l.Job.Leader.ID)
			}
		})
		childExit(t, exec.Command(binary, "sweep", "--confirm"), 0)
		alive, err := groupAlive(context.Background(), pgid)
		if err != nil || alive {
			t.Fatal("sweep left orphan process group", err)
		}
		childExit(t, exec.Command(binary, "job", "--wait", "0", "--", "true"), 0)
		jobStore(t, dir, func(s *Store) {
			if len(s.Leases)+len(s.Waiters) != 0 {
				t.Fatal("dead jobs/waiters retained budget")
			}
		})
	})
	t.Run("normal completion cleans descendants", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("BERTH_STATE", dir)
		pidfile := filepath.Join(t.TempDir(), "child-pid")
		script := `sh -c 'echo $$ > "$1"; trap "" TERM; exec sleep 30' _ "$1" & while ! test -s "$1"; do sleep 0.01; done; exit 37`
		childExit(t, exec.Command(binary, "job", "--", "sh", "-c", script, "_", pidfile), 37)
		data, err := os.ReadFile(pidfile)
		if err != nil {
			t.Fatal(err)
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if _, live := processIdentity(pid); live != Dead {
			t.Fatal("normal completion leaked descendant")
		}
		jobStore(t, dir, func(s *Store) {
			if len(s.Leases) != 0 {
				t.Fatal("descendant cleanup leaked reservation")
			}
		})
	})
	t.Run("timeout cleanup failure retains reservation", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("BERTH_STATE", dir)
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
		jobStore(t, dir, func(s *Store) {
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
		t.Setenv("BERTH_STATE", dir)
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
		jobStore(t, dir, func(s *Store) {
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
		t.Setenv("BERTH_STATE", dir)
		marker := filepath.Join(t.TempDir(), "started")
		cmd := startJobCommand(t, binary, "--", "sh", "-c", `touch "$1"; exec sleep 30`, "_", marker)
		awaitJob(t, func() bool { _, err := os.Stat(marker); return err == nil })
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err == nil {
			t.Fatal("cancelled child reported success")
		}
		jobStore(t, dir, func(s *Store) {
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
	cmd := exec.Command("sh", "-c", `sleep 30 </dev/null >/dev/null 2>&1 & echo $! > "$1"; exit`, "_", pidfile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	registerProcessGroupCleanup(t, cmd)
	leader, err := parseOwner("pid:"+strconv.Itoa(pid), 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	awaitJob(t, func() bool { _, err := os.Stat(pidfile); return err == nil })
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	lease := Lease{Job: &JobResource{Leader: leader}}
	if err := (processResource{}).Destroy(context.Background(), lease); err != nil {
		t.Fatalf("missing leader should not prevent release: %v", err)
	}
	alive, err := groupAlive(context.Background(), pid)
	if err != nil || !alive {
		t.Fatal("destroy signaled an unverifiable group")
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
		_, state := processIdentity(pid)
		return err == nil && state == Dead
	})
	identity, state := processIdentity(pid)
	if state != Dead || identity == "" {
		t.Fatal("zombie leader identity unavailable")
	}
	lease := Lease{Job: &JobResource{Leader: Owner{ID: strconv.Itoa(pid), Start: "different-start"}}}
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
	identity, live := processIdentity(os.Getpid())
	if live != Alive {
		t.Fatal("test process identity unavailable")
	}
	lease := Lease{Job: &JobResource{Leader: Owner{ID: strconv.Itoa(os.Getpid()), Start: "not-the-current-start"}}}
	if err := (processResource{}).Destroy(context.Background(), lease); err == nil {
		t.Fatal("destroy accepted a reused process ID")
	}
	if _, live := processIdentity(os.Getpid()); live != Alive {
		t.Fatal("destroy signaled the unrelated process")
	}
	if current, _ := processIdentity(os.Getpid()); current != identity {
		t.Fatal("test process identity changed")
	}
}

func TestExpiredWaiterCleanupAfterLockContention(t *testing.T) {
	dir := t.TempDir()
	owner, err := parseOwner("pid:"+strconv.Itoa(os.Getpid()), 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := newLease(owner, time.Minute, "", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	lease.Job = &JobResource{Budget: Budget{Cores: 2, Memory: 2}}
	result := make(chan error, 1)
	go func() {
		_, err := admitJob(context.Background(), dir, lease, Budget{Cores: 1, Memory: 1}, 200*time.Millisecond)
		result <- err
	}()
	awaitJob(t, func() bool {
		var waiting bool
		jobStore(t, dir, func(s *Store) { waiting = len(s.Waiters) == 1 })
		return waiting
	})
	lockCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	locked, err := openStore(lockCtx, dir)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	locked.Close()
	if err := <-result; !errors.Is(err, errNoCapacity) {
		t.Fatalf("admission returned %v", err)
	}
	jobStore(t, dir, func(s *Store) {
		if len(s.Waiters) != 0 {
			t.Fatalf("expired waiter remains after lock release: %+v", s.Waiters)
		}
	})
}

func TestJobQueuePrunesExpiredButKeepsUnknown(t *testing.T) {
	now := time.Now()
	owner := Owner{Kind: "pid", ID: "not-a-pid", Start: "unknown"}
	s := &Store{Waiters: []JobWaiter{{ID: "expired", Machine: "local", Owner: owner, Expires: now}, {ID: "unknown", Machine: "local", Owner: owner, Expires: now.Add(time.Hour)}, {ID: "foreign", Machine: "remote", Owner: owner, Expires: now}}}
	pruneWaiters(s, "local", now)
	if len(s.Waiters) != 2 || s.Waiters[0].ID != "unknown" || s.Waiters[1].ID != "foreign" {
		t.Fatalf("unsafe waiter pruning: %+v", s.Waiters)
	}
}

func TestJobHelperEOFDoesNotRun(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-run")
	binary := filepath.Join(t.TempDir(), "berth")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
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
