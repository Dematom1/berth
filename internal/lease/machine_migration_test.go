package lease

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenMigratedStoreWithoutHostname(t *testing.T) {
	original := machineHostname
	machineHostname = func() (string, error) { return "", errors.New("hostname unavailable") }
	t.Cleanup(func() { machineHostname = original })
	for _, tc := range []struct {
		name         string
		version      int
		legacyLease  bool
		legacyWaiter bool
		wantError    bool
	}{
		{"migrated v2", 2, false, false, false},
		{"legacy v2 lease", 2, true, false, true},
		{"legacy v2 waiter", 2, false, true, true},
		{"v1", 1, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			l := testLease(t)
			w := waiter{ID: l.ID, Machine: l.Machine, Owner: l.Owner, Expires: time.Now().Add(time.Hour)}
			if tc.legacyLease {
				l.Machine = "old-hostname"
			}
			if tc.legacyWaiter {
				w.Machine = "old-hostname"
			}
			data, err := json.Marshal(store{Version: tc.version, Leases: map[string]Lease{l.ID: l}, Waiters: []waiter{w}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "state.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			s, err := testManager().openStore(context.Background(), dir)
			if err == nil {
				s.Close()
			}
			if (err != nil) != tc.wantError {
				t.Fatalf("openStore error = %v, wantError %v", err, tc.wantError)
			}
			if err != nil && !strings.Contains(err.Error(), "hostname") {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(data) {
				t.Fatal("hostname failure changed the ledger")
			}
		})
	}
}

func TestMachineMigrationCurrentHostnameAndQueue(t *testing.T) {
	t.Setenv("BERTH_LEGACY_MACHINE", "")
	dir := t.TempDir()
	l := testLease(t)
	stable := l.Machine
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if stable == hostname || !strings.Contains(stable, ":") {
		t.Fatalf("new lease uses unstable identity: %s", stable)
	}
	l.Machine = hostname
	data, err := json.Marshal(store{Version: 1, Leases: map[string]Lease{l.ID: l}, Waiters: []waiter{{ID: l.ID, Machine: hostname, Owner: l.Owner, Expires: time.Now().Add(time.Hour)}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := testManager().openStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Version != 2 || s.Leases[l.ID].Machine != stable || s.Waiters[0].Machine != stable {
		t.Fatalf("legacy local lease/queue not migrated: %+v", s)
	}
}
