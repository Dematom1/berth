package lease

import (
	"bytes"
	"context"
	"errors"
	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	nonepkg "github.com/Dematom1/dibs/internal/adapter/none"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	"os"
	"path/filepath"
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
	// An icon-decorated `ls -d` capture once created this tree in the working directory.
	for _, relative := range []string{"state", "\ue5ff " + filepath.Join(home, "st.Zpto")} {
		t.Setenv("DIBS_STATE", relative)
		if dir, err := stateDir(); err == nil {
			t.Fatalf("relative explicit state %q accepted as %q", relative, dir)
		}
	}
}

func TestStateRenameKeepsLegacyLeasesAndLock(t *testing.T) {
	unsetEnv(t, "DIBS_STATE")
	unsetEnv(t, "BERTH_STATE")
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	legacy := filepath.Join(base, "berth")
	s, err := testManager().openStore(context.Background(), legacy)
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
		l.ResourceRef = adapterpkg.ResourceRef{Adapter: adapter, Name: "berth-" + suffix, Context: "old-context", DockerHost: "unix:///old.sock", File: filepath.Join(legacy, "resources", l.ID, file)}
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
	if second, err := testManager().openStore(ctx, preferred); err == nil {
		second.Close()
		t.Fatal("migration split the state lock")
	}
	s.Close()
	s, err = testManager().openStore(context.Background(), preferred)
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
		if eligible, _ := reclaimable(l, l.Machine, testNow, func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return ownerpkg.Alive }); eligible {
			t.Fatal("migration lost live owner or TTL")
		}
	}
	host, err := machine()
	if err != nil {
		t.Fatal(err)
	}
	r := sweep(context.Background(), s, host, testNow.Add(time.Hour), true, func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return ownerpkg.Alive }, adapterpkg.NewRegistry(compatAdapter("kind"), compatAdapter("k3d"), compatAdapter("compose")))
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
	original := syncStateParent
	t.Cleanup(func() { syncStateParent = original })
	syncStateParent = func(string) error { return errors.New("injected crash after alias publication") }
	if _, err := migrateStateDir(base, true); err == nil {
		t.Fatal("failed sync reported success")
	}
	syncStateParent = original
	if dir, err := migrateStateDir(base, true); err != nil || dir != legacy {
		t.Fatalf("migration did not converge: %q, %v", dir, err)
	}
	preferred := filepath.Join(base, "dibs")
	if err := os.Remove(preferred); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(preferred, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateStateDir(base, true); err == nil {
		t.Fatal("separate existing ledgers silently hid old leases")
	}
}

func compatAdapter(name string) adapterpkg.Definition {
	d := nonepkg.New()
	d.Name = name
	return d
}
