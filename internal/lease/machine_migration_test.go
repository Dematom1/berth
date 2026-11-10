package lease

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
