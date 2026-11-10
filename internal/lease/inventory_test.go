package lease

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/berth/internal/adapter"
	composepkg "github.com/Dematom1/berth/internal/adapter/compose"
	k3dpkg "github.com/Dematom1/berth/internal/adapter/k3d"
	kindpkg "github.com/Dematom1/berth/internal/adapter/kind"
	ownerpkg "github.com/Dematom1/berth/internal/owner"
)

func inventoryRunner(t *testing.T, driver, name string) adapterpkg.CommandRunner {
	t.Helper()
	return func(_ context.Context, _ []string, tool string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "context inspect"):
			return []byte("unix:///test.sock"), nil
		case strings.Contains(joined, "kubeconfig"):
			return []byte("apiVersion: v1\nkind: Config\n"), nil
		case strings.Contains(joined, "config --format json"):
			return []byte(`{"services":{"test":{"image":"alpine"}},"networks":{"default":{"name":"` + name + `_default"}}}`), nil
		case strings.Contains(joined, "container ls"):
			return []byte("abc"), nil
		case strings.Contains(joined, "container inspect"):
			label := map[string]string{"kind": "io.x-k8s.kind.cluster", "k3d": "k3d.cluster", "compose": "com.docker.compose.project"}[driver]
			return json.Marshal([]any{map[string]any{"Name": "test", "Created": "2026-01-01T00:00:00Z", "Config": map[string]any{"Labels": map[string]string{label: name, "custom": "preserved", "com.docker.compose.project.working_dir": "/missing-berth-test-dir", "com.docker.compose.project.config_files": "/missing-berth-test-dir/compose.yaml"}}}})
		case strings.Contains(joined, "volume ls"), strings.Contains(joined, "network ls"):
			return nil, nil
		}
		t.Fatalf("unexpected command: %s %v", tool, args)
		return nil, nil
	}
}
func inventoryManager(t *testing.T, driver string, run adapterpkg.CommandRunner) *Manager {
	t.Helper()
	d := map[string]adapterpkg.Definition{"kind": kindpkg.New(run), "k3d": k3dpkg.New(run), "compose": composepkg.New(run)}[driver]
	m := testManager()
	m.options.Directory = t.TempDir()
	m.options.Adapters = adapterpkg.NewRegistry(d)
	return m
}
func adoptionRequest(t *testing.T, driver string) AdoptRequest {
	t.Helper()
	o, err := bindOwner("pid:"+strconv.Itoa(os.Getpid()), time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	file := ""
	if driver == "compose" {
		file = "compose.yaml"
	}
	return AdoptRequest{Owner: o, TTL: time.Hour, Resource: adapterpkg.ResourceRef{Adapter: driver, Name: "existing-lab", Context: "test-context", DockerHost: "unix:///test.sock"}, File: file}
}
func TestAdoptRefusalsAndDurability(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	for _, driver := range []string{"kind", "k3d", "compose"} {
		t.Run(driver, func(t *testing.T) {
			ctx := context.Background()
			run := inventoryRunner(t, driver, "existing-lab")
			m := inventoryManager(t, driver, run)
			r := adoptionRequest(t, driver)
			// Discovery itself does not create state, and carries Docker evidence.
			rows, err := m.Orphans(ctx, r.Resource)
			if err != nil || len(rows) != 1 {
				t.Fatalf("inventory: %v %v", rows, err)
			}
			if rows[0]["labels"].(map[string]string)["custom"] != "preserved" || rows[0]["working_dir_status"] != "missing" {
				t.Fatal("lost discovery evidence")
			}
			if _, err := os.Stat(m.options.Directory + "/state.json"); !os.IsNotExist(err) {
				t.Fatal("inventory wrote state")
			}
			l, err := m.Adopt(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			if !l.ResourceRef.Adopted || l.ResourceRef.Name != r.Resource.Name || l.Labels["io.berth.lease"] != l.ID {
				t.Fatal("lost adopted identity")
			}
			disk, err := m.readInventoryStore(m.options.Directory)
			if err != nil || disk.Version != 2 || disk.Leases[l.ID].ResourceRef.Name != r.Resource.Name {
				t.Fatalf("not durable: %v %v", disk, err)
			}
			if _, err := m.Adopt(ctx, r); err == nil || !strings.Contains(err.Error(), "already under") {
				t.Fatalf("duplicate accepted: %v", err)
			}
			alias := r.Resource
			alias.Context = "same-engine-alias"
			if leased, err := resourceLeased(ctx, disk, alias, run); err != nil || !leased {
				t.Fatalf("alias bypass: %v %v", leased, err)
			}
			if rows, err := m.Orphans(ctx, r.Resource); err != nil || len(rows) != 0 {
				t.Fatalf("leased resource listed: %v %v", rows, err)
			}
			s, err := m.openStore(ctx, m.options.Directory)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			result := sweep(ctx, s, l.Machine, l.Expires.Add(time.Second), false, func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return ownerpkg.Unknown }, m.options.Adapters)
			if len(result.Eligible) != 1 || len(s.Leases) != 1 {
				t.Fatalf("preview changed adopted lease: %+v", result)
			}
		})
	}
	for _, failure := range []string{"missing", "unknown", "malformed", "incomplete"} {
		t.Run(failure, func(t *testing.T) {
			run := func(_ context.Context, _ []string, _ string, args ...string) ([]byte, error) {
				switch failure {
				case "missing":
					return nil, nil
				case "unknown":
					return nil, errors.New("engine unavailable")
				case "malformed":
					return []byte("bad json"), nil
				case "incomplete":
					if strings.Contains(strings.Join(args, " "), "inspect") {
						return []byte("[]"), nil
					}
					return []byte("abc"), nil
				}
				panic("unreachable")
			}
			m := inventoryManager(t, "kind", run)
			if _, err := m.Adopt(context.Background(), adoptionRequest(t, "kind")); err == nil {
				t.Fatal("unverified adoption accepted")
			}
			if _, err := os.Stat(m.options.Directory + "/state.json"); !os.IsNotExist(err) {
				t.Fatal("refusal wrote state")
			}
		})
	}
}
func TestReadOnlyInventoryAndStateMigration(t *testing.T) {
	m := testManager()
	dir := filepath.Join(t.TempDir(), "absent")
	if s, err := m.readInventoryStore(dir); err != nil || len(s.Leases) != 0 {
		t.Fatalf("empty snapshot: %v %v", s, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("snapshot created directory")
	}
	dir = t.TempDir()
	path := filepath.Join(dir, "state.json")
	original := []byte(`{"version":1,"leases":{}}`)
	if err := os.WriteFile(path, original, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := m.readInventoryStore(dir); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	data, _ := os.ReadFile(path)
	if info.Mode().Perm() != 0400 || string(data) != string(original) {
		t.Fatal("inventory mutated state")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":999,"leases":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.readInventoryStore(dir); err == nil {
		t.Fatal("unknown schema accepted")
	}
	s := testStore(t)
	l := testLease(t)
	l.ResourceRef = adapterpkg.ResourceRef{Adapter: "kind", Name: "existing-lab", Context: "test", DockerHost: "unix:///test.sock", Adopted: true, File: filepath.Join(s.dir, "resources", l.ID, "kubeconfig")}
	s.Leases[l.ID] = l
	s.Version = 1
	if err := s.validate(); err == nil {
		t.Fatal("version 1 accepted adopted identity")
	}
	s.Version = 2
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	l.ResourceRef.Adopted = false
	s.Leases[l.ID] = l
	if err := s.validate(); err == nil {
		t.Fatal("generated naming restriction removed")
	}
}
