package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func testLease(t *testing.T) Lease {
	t.Helper()
	l, err := newLease(Owner{Kind: "pid", ID: "123", Start: "test"}, time.Hour, "2 CPUs, lab", map[string]string{"task": "lab"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestReclaimAndExtend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		live    Liveness
		elapsed time.Duration
		foreign bool
		want    bool
	}{
		{"live", Alive, 0, false, false},
		{"dead owner", Dead, 0, false, true},
		{"expired live owner", Alive, time.Hour, false, true},
		{"unknown owner", Unknown, 0, false, false},
		{"expired unknown owner", Unknown, time.Hour, false, true},
		{"expired foreign machine", Dead, time.Hour, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := testLease(t)
			host := l.Machine
			if tc.foreign {
				host = "elsewhere"
			}
			check := func(Owner, time.Time) Liveness { return tc.live }
			got, _ := reclaimable(l, host, testNow.Add(tc.elapsed), check)
			if got != tc.want {
				t.Fatalf("reclaimable = %v, want %v", got, tc.want)
			}
			err := extendLease(&l, host, 2*time.Hour, testNow.Add(tc.elapsed), check)
			canExtend := tc.live == Alive && !tc.foreign && tc.elapsed < time.Hour
			if (err == nil) != canExtend {
				t.Fatalf("extend error = %v, canExtend = %v", err, canExtend)
			}
			if canExtend && !l.Expires.Equal(testNow.Add(2*time.Hour)) {
				t.Fatalf("expiry = %v", l.Expires)
			}
		})
	}
}

type failingAdapter struct{}

func (failingAdapter) Create(context.Context, Lease) error { return nil }
func (failingAdapter) Destroy(context.Context, Lease) error {
	return errors.New("unreachable engine")
}

func TestSweepPreviewRetryAndIdempotence(t *testing.T) {
	s := testStore(t)
	l := testLease(t)
	s.Leases[l.ID] = l
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	dead := func(Owner, time.Time) Liveness { return Dead }
	r := sweep(context.Background(), s, l.Machine, testNow, false, dead, adapters())
	if len(r.Eligible) != 1 || len(s.Leases) != 1 || len(r.Reclaimed) != 0 {
		t.Fatalf("preview mutated state: %+v", r)
	}
	r = sweep(context.Background(), s, l.Machine, testNow, true, dead, map[string]Adapter{"none": failingAdapter{}})
	if len(r.Failures) != 1 || len(s.Leases) != 1 {
		t.Fatalf("failed cleanup lost its lease: %+v", r)
	}
	r = sweep(context.Background(), s, l.Machine, testNow, true, dead, adapters())
	if len(r.Reclaimed) != 1 || len(s.Leases) != 0 {
		t.Fatalf("retry did not converge: %+v", r)
	}
	r = sweep(context.Background(), s, l.Machine, testNow, true, dead, adapters())
	if len(r.Reclaimed) != 0 || len(r.Failures) != 0 {
		t.Fatalf("second sweep not a no-op: %+v", r)
	}
	if changed, err := releaseLease(context.Background(), s, l.ID, l.Machine, adapters()); err != nil || changed {
		t.Fatalf("release absent = %v, %v", changed, err)
	}
}

func TestSweepDecisions(t *testing.T) {
	for _, tc := range []struct {
		live Liveness
		now  time.Time
		want int
	}{
		{Alive, testNow, 1}, {Dead, testNow, 0}, {Alive, testNow.Add(time.Hour), 0},
		{Unknown, testNow, 1}, {Unknown, testNow.Add(time.Hour), 0},
	} {
		s := testStore(t)
		l := testLease(t)
		s.Leases[l.ID] = l
		sweep(context.Background(), s, l.Machine, tc.now, true, func(Owner, time.Time) Liveness { return tc.live }, adapters())
		if len(s.Leases) != tc.want {
			t.Fatalf("live=%s time=%s: remaining=%d want=%d", tc.live, tc.now, len(s.Leases), tc.want)
		}
	}
}

func TestOpenStoreRestrictsExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state.json")
	if err := os.WriteFile(state, []byte(`{"version":1,"leases":{}}`), 0644); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, "lock")
	if err := os.WriteFile(lock, nil, 0644); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("directory permissions = %04o, want 0700", info.Mode().Perm())
	}
	for _, path := range []string{state, lock} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("existing store file was not preserved: %v", err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s permissions = %04o, want 0600", filepath.Base(path), info.Mode().Perm())
		}
	}
}

func TestStoreDurabilityLockAndCorruption(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	l := testLease(t)
	s.Leases[l.ID] = l
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if second, err := openStore(ctx, dir); err == nil {
		second.Close()
		t.Fatal("concurrent writer acquired locked store")
	}
	s.Close()
	s, err = openStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Leases) != 1 || s.Leases[l.ID].Labels["task"] != "lab" {
		t.Fatal("saved lease did not survive reopening")
	}
	s.Close()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := openStore(context.Background(), dir); err == nil {
		s.Close()
		t.Fatal("corrupt state accepted")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "{broken" {
		t.Fatal("corrupt state was overwritten")
	}
}

func TestOutOfRangePIDIsUnknownNotDead(t *testing.T) {
	for _, kind := range []string{"pid", "session"} {
		o := Owner{Kind: kind, ID: "4299161601", Start: "invalid persisted process"}
		if live := checkOwner(o, testNow); live != Unknown {
			t.Fatalf("%s PID outside pid_t range is %s, want unknown", kind, live)
		}
		if _, err := parseOwner(o.String(), time.Minute, testNow); err == nil {
			t.Fatalf("accepted out-of-range %s owner", kind)
		}
	}
}

func TestOwnerLiveness(t *testing.T) {
	now := time.Now()
	o, err := parseOwner("pid:"+strconv.Itoa(os.Getpid()), time.Minute, now)
	if err != nil || checkOwner(o, now) != Alive {
		t.Fatalf("current process: %+v, %v", o, err)
	}
	t.Setenv("TZ", "Pacific/Honolulu")
	if checkOwner(o, now) != Alive {
		t.Fatal("timezone change changed process identity")
	}
	o.Start = "different process start"
	if checkOwner(o, now) != Dead {
		t.Fatal("reused PID not detected")
	}
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	o, err = parseOwner("session:"+strconv.Itoa(child.Process.Pid), time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	child.Process.Kill()
	child.Wait()
	if checkOwner(o, now) != Dead {
		t.Fatal("dead session holder still alive")
	}
	file := filepath.Join(t.TempDir(), "heartbeat")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	now = time.Now()
	o, err = parseOwner("heartbeat:"+file, 3*time.Minute, now)
	if err != nil || checkOwner(o, now) != Alive {
		t.Fatalf("fresh heartbeat: %+v, %v", o, err)
	}
	if checkOwner(o, now.Add(3*time.Minute)) != Dead {
		t.Fatal("stale heartbeat did not die")
	}
	os.Chtimes(file, now.Add(time.Hour), now.Add(time.Hour))
	if checkOwner(o, now) != Unknown {
		t.Fatal("future heartbeat must be unknown")
	}
	os.Remove(file)
	if checkOwner(o, now) != Dead {
		t.Fatal("missing heartbeat did not die")
	}
	for _, bad := range []string{"pid:0", "pid:-1", "session:bad", "other:123", "heartbeat:"} {
		if _, err := parseOwner(bad, time.Minute, now); err == nil {
			t.Fatalf("accepted invalid owner %q", bad)
		}
	}
}

func TestStoreLockReleasedByProcessDeath(t *testing.T) {
	if os.Getenv("BERTH_TEST_HOLD_LOCK") != "" {
		s, err := openStore(context.Background(), os.Getenv("BERTH_TEST_HOLD_LOCK"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		os.Stdout.WriteString("ready\n")
		time.Sleep(time.Hour)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoreLockReleasedByProcessDeath$")
	dir := t.TempDir()
	cmd.Env = append(os.Environ(), "BERTH_TEST_HOLD_LOCK="+dir)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	ready := make([]byte, 6)
	if _, err := pipe.Read(ready); err != nil || !strings.Contains(string(ready), "ready") {
		t.Fatalf("child lock readiness: %q, %v", ready, err)
	}
	cmd.Process.Kill()
	cmd.Wait()
	s, err := openStore(context.Background(), dir)
	if err != nil {
		t.Fatalf("kernel did not release dead holder's lock: %v", err)
	}
	s.Close()
}

func TestAtomicWriteRestoresOldStateWhenDirectorySyncFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old durable state"), 0600); err != nil {
		t.Fatal(err)
	}
	original := syncDirectory
	t.Cleanup(func() { syncDirectory = original })
	calls := 0
	syncDirectory = func(string) error {
		calls++
		if calls == 1 {
			return errors.New("injected directory sync failure")
		}
		return nil
	}
	if err := atomicWrite(path, []byte("new state")); err == nil {
		t.Fatal("directory sync failure reported success")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "old durable state" {
		t.Fatalf("failed write did not preserve prior durable state: %q, %v", got, err)
	}
}
