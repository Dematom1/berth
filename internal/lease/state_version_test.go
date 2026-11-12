package lease

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func versionedManager(version string) *Manager {
	m := testManager()
	m.options.Version = version
	return m
}

func TestNewerStateRefusedWithUpgradeHint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	// A future schema may change field types; the refusal must not depend on them.
	data := []byte(`{"version":3,"writer_version":"9.0.0","leases":[]}` + "\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	m := versionedManager("0.2.3")
	_, openErr := m.openStore(context.Background(), dir)
	_, inventoryErr := m.readInventoryStore(dir)
	for _, err := range []error{openErr, inventoryErr} {
		if err == nil {
			t.Fatal("newer state was accepted")
		}
		for _, want := range []string{"kept unchanged", "state version 3 written by dibs 9.0.0", "state version 2, dibs 0.2.3", "upgrade dibs", "`which -a dibs berth`"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal %q lacks %q", err, want)
			}
		}
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(data) {
		t.Fatalf("newer state changed: %s %v", got, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":3,"leases":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.openStore(context.Background(), dir); err == nil || strings.Contains(err.Error(), "written by") || !strings.Contains(err.Error(), "state version 3 is newer") {
		t.Fatalf("refusal without recorded writer: %v", err)
	}
}

func TestStateWithWriterVersionLoadsAndRecordsWriter(t *testing.T) {
	dir := t.TempDir()
	l := testLease(t)
	data, err := json.Marshal(map[string]any{"version": 2, "writer_version": "0.2.2", "leases": map[string]Lease{l.ID: l}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := versionedManager("0.2.3").openStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 2 || s.Writer != "0.2.2" || s.Leases[l.ID].ID != l.ID {
		s.Close()
		t.Fatalf("v2 state with writer did not load: %+v", s)
	}
	err = s.Save()
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	data, err = os.ReadFile(filepath.Join(dir, "state.json"))
	if err == nil {
		err = json.Unmarshal(data, &saved)
	}
	if err != nil || saved["version"] != float64(2) || saved["writer_version"] != "0.2.3" {
		t.Fatalf("saved state = %s %v", data, err)
	}
}
