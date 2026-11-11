package lease

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	nonepkg "github.com/Dematom1/dibs/internal/adapter/none"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

var testNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func testLease(t *testing.T) Lease {
	t.Helper()
	l, err := newLease(ownerpkg.Reference{Kind: "pid", ID: "123", Identity: "test"}, time.Hour, "2 CPUs, lab", map[string]string{"task": "lab"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func testStore(t *testing.T) *store {
	t.Helper()
	s, err := testManager().openStore(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestReclaimAndExtend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		live    ownerpkg.Liveness
		elapsed time.Duration
		foreign bool
		want    bool
	}{
		{"live", ownerpkg.Alive, 0, false, false},
		{"dead owner", ownerpkg.Dead, 0, false, true},
		{"expired live owner", ownerpkg.Alive, time.Hour, false, true},
		{"unknown owner", ownerpkg.Unknown, 0, false, false},
		{"expired unknown owner", ownerpkg.Unknown, time.Hour, false, true},
		{"expired foreign machine", ownerpkg.Dead, time.Hour, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := testLease(t)
			host := l.Machine
			if tc.foreign {
				host = "elsewhere"
			}
			check := func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return tc.live }
			got, _ := reclaimable(l, host, testNow.Add(tc.elapsed), check)
			if got != tc.want {
				t.Fatalf("reclaimable = %v, want %v", got, tc.want)
			}
			err := extendLease(&l, host, 2*time.Hour, testNow.Add(tc.elapsed), check)
			canExtend := tc.live == ownerpkg.Alive && !tc.foreign && tc.elapsed < time.Hour
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

func (failingAdapter) Create(context.Context, adapterpkg.Target) error { return nil }

func (failingAdapter) Destroy(context.Context, adapterpkg.Target) error {
	return errors.New("unreachable engine")
}

func TestSweepPreviewRetryAndIdempotence(t *testing.T) {
	s := testStore(t)
	l := testLease(t)
	s.Leases[l.ID] = l
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	dead := func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return ownerpkg.Dead }
	r := sweep(context.Background(), s, l.Machine, testNow, false, dead, testAdapters())
	if len(r.Eligible) != 1 || len(s.Leases) != 1 || len(r.Reclaimed) != 0 {
		t.Fatalf("preview mutated state: %+v", r)
	}
	r = sweep(context.Background(), s, l.Machine, testNow, true, dead, adapterpkg.NewRegistry(adapterpkg.Definition{Name: "none", Adapter: failingAdapter{}, Prepare: nonepkg.New().Prepare, Validate: nonepkg.New().Validate, Env: nonepkg.New().Env}))
	if len(r.Failures) != 1 || len(s.Leases) != 1 {
		t.Fatalf("failed cleanup lost its lease: %+v", r)
	}
	r = sweep(context.Background(), s, l.Machine, testNow, true, dead, testAdapters())
	if len(r.Reclaimed) != 1 || len(s.Leases) != 0 {
		t.Fatalf("retry did not converge: %+v", r)
	}
	r = sweep(context.Background(), s, l.Machine, testNow, true, dead, testAdapters())
	if len(r.Reclaimed) != 0 || len(r.Failures) != 0 {
		t.Fatalf("second sweep not a no-op: %+v", r)
	}
	if changed, err := releaseLease(context.Background(), s, l.ID, l.Machine, testAdapters()); err != nil || changed {
		t.Fatalf("release absent = %v, %v", changed, err)
	}
}

func TestSweepDecisions(t *testing.T) {
	for _, tc := range []struct {
		live ownerpkg.Liveness
		now  time.Time
		want int
	}{
		{ownerpkg.Alive, testNow, 1}, {ownerpkg.Dead, testNow, 0}, {ownerpkg.Alive, testNow.Add(time.Hour), 0},
		{ownerpkg.Unknown, testNow, 1}, {ownerpkg.Unknown, testNow.Add(time.Hour), 0},
	} {
		s := testStore(t)
		l := testLease(t)
		s.Leases[l.ID] = l
		sweep(context.Background(), s, l.Machine, tc.now, true, func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return tc.live }, testAdapters())
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
	s, err := testManager().openStore(context.Background(), dir)
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
	s, err := testManager().openStore(context.Background(), dir)
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
	if second, err := testManager().openStore(ctx, dir); err == nil {
		second.Close()
		t.Fatal("concurrent writer acquired locked store")
	}
	s.Close()
	s, err = testManager().openStore(context.Background(), dir)
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
	if s, err := testManager().openStore(context.Background(), dir); err == nil {
		s.Close()
		t.Fatal("corrupt state accepted")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "{broken" {
		t.Fatal("corrupt state was overwritten")
	}
}

func TestStoreLockReleasedByProcessDeath(t *testing.T) {
	if os.Getenv("DIBS_TEST_HOLD_LOCK") != "" {
		s, err := testManager().openStore(context.Background(), os.Getenv("DIBS_TEST_HOLD_LOCK"))
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
	cmd.Env = append(os.Environ(), "DIBS_TEST_HOLD_LOCK="+dir)
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
	s, err := testManager().openStore(context.Background(), dir)
	if err != nil {
		t.Fatalf("kernel did not release dead holder's lock: %v", err)
	}
	s.Close()
}
