package lease

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
)

func TestRenamedInventoryAndAdoption(t *testing.T) {
	for _, driver := range []string{"kind", "k3d", "compose"} {
		for _, product := range []string{"berth", "dibs"} {
			t.Run(driver+"/"+product, func(t *testing.T) {
				name := product + "-existing-lab"
				baseRun := inventoryRunner(t, driver, name)
				run := func(ctx context.Context, env []string, tool string, args ...string) ([]byte, error) {
					out, err := baseRun(ctx, env, tool, args...)
					if err != nil || !strings.Contains(strings.Join(args, " "), "container inspect") {
						return out, err
					}
					var objects []map[string]any
					if err := json.Unmarshal(out, &objects); err != nil {
						t.Fatal(err)
					}
					labels := objects[0]["Config"].(map[string]any)["Labels"].(map[string]any)
					labels["io."+product+".lease"] = "old-attribution"
					return json.Marshal(objects)
				}
				m := inventoryManager(t, driver, run)
				r := adoptionRequest(t, driver)
				r.Resource.Name = name
				rows, err := m.Orphans(context.Background(), r.Resource)
				if err != nil || len(rows) != 1 || rows[0]["labels"].(map[string]string)["io."+product+".lease"] != "old-attribution" {
					t.Fatalf("lost legacy inventory: %v %v", rows, err)
				}
				l, _, err := m.Adopt(context.Background(), r)
				if err != nil || l.ResourceRef.Name != name || !l.ResourceRef.Adopted {
					t.Fatalf("adoption: %+v %v", l, err)
				}
				if rows, err := m.Orphans(context.Background(), r.Resource); err != nil || len(rows) != 0 {
					t.Fatalf("adopted resource shown as orphan: %v %v", rows, err)
				}
			})
		}
	}
}

func TestLegacyInventoryDefaultRemainsReadOnly(t *testing.T) {
	unsetEnv(t, "DIBS_STATE")
	unsetEnv(t, "BERTH_STATE")
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	legacy := filepath.Join(base, "berth")
	if err := os.Mkdir(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"version":1,"leases":{}}`)
	path := filepath.Join(legacy, "state.json")
	if err := os.WriteFile(path, original, 0400); err != nil {
		t.Fatal(err)
	}
	m := inventoryManager(t, "kind", inventoryRunner(t, "kind", "berth-orphan"))
	m.options.Directory = ""
	if rows, err := m.Orphans(context.Background(), adapterpkg.ResourceRef{Context: "test"}); err != nil || len(rows) != 1 {
		t.Fatalf("legacy inventory: %v %v", rows, err)
	}
	for _, path := range []string{filepath.Join(base, "dibs"), filepath.Join(legacy, "lock")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("inventory created %s: %v", path, err)
		}
	}
	got, err := os.ReadFile(path)
	info, statErr := os.Stat(path)
	if err != nil || statErr != nil || !bytes.Equal(got, original) || info.Mode().Perm() != 0400 {
		t.Fatal("inventory changed legacy state", err, statErr)
	}
}

func TestMachineMigrationThroughStateAlias(t *testing.T) {
	for _, version := range []int{1, 2} {
		for _, product := range []string{"BERTH", "DIBS"} {
			t.Run(product+string(rune('0'+version)), func(t *testing.T) {
				unsetEnv(t, "DIBS_STATE")
				unsetEnv(t, "BERTH_STATE")
				unsetEnv(t, "DIBS_LEGACY_MACHINE")
				unsetEnv(t, "BERTH_LEGACY_MACHINE")
				t.Setenv(product+"_LEGACY_MACHINE", "old-host")
				base := t.TempDir()
				t.Setenv("XDG_STATE_HOME", base)
				legacy := filepath.Join(base, "berth")
				s, err := testManager().openStore(context.Background(), legacy)
				if err != nil {
					t.Fatal(err)
				}
				l := testLease(t)
				stable := l.Machine
				l.Machine = "old-host"
				l.ResourceRef = adapterpkg.ResourceRef{Adapter: "kind", Name: "berth-" + l.ID, Context: "test", File: filepath.Join(legacy, "resources", l.ID, "kubeconfig")}
				s.Version, s.Leases[l.ID] = version, l
				s.Waiters = []waiter{{ID: l.ID, Machine: "old-host", Owner: l.Owner, Expires: time.Now().Add(time.Hour)}}
				if err := s.Save(); err != nil {
					t.Fatal(err)
				}
				s.Close()
				if _, err := stateDir(); err != nil {
					t.Fatal(err)
				}
				s, err = testManager().openStore(context.Background(), filepath.Join(base, "dibs"))
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				if s.Version != 2 || s.Leases[l.ID].Machine != stable || s.Waiters[0].Machine != stable || s.Leases[l.ID].ResourceRef.File != l.ResourceRef.File {
					t.Fatal("machine migration lost legacy resource identity")
				}
			})
		}
	}
}
