package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func integrationReady(t *testing.T, driver string) {
	t.Helper()
	if os.Getenv("BERTH_INTEGRATION") != "1" {
		t.Skip("set BERTH_INTEGRATION=1 to create and reclaim isolated Docker resources")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker unavailable")
	}
	if driver == "kind" {
		if _, err := exec.LookPath("kind"); err != nil {
			t.Skip("kind unavailable")
		}
	}
	if _, err := runCommand(context.Background(), nil, "docker", "info"); err != nil {
		t.Skip("Docker engine unavailable")
	}
	dir, err := os.MkdirTemp("", "berth-integration-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERTH_STATE", dir)
	t.Setenv("BERTH_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
	t.Cleanup(func() {
		s, err := openStore(context.Background(), dir)
		if err != nil {
			t.Errorf("integration cleanup state at %s: %v", dir, err)
			return
		}
		host, _ := machine()
		for _, id := range s.IDs() {
			if _, err := releaseLease(context.Background(), s, id, host, adapters()); err != nil {
				t.Errorf("integration cleanup retained %s at %s: %v", id, dir, err)
			}
		}
		empty := len(s.Leases) == 0
		s.Close()
		if empty {
			os.RemoveAll(dir)
		}
	})
}

// Remove the real resource while its lease remains, then let berth retry cleanup.
func removeIntegrationResource(t *testing.T, id string) {
	t.Helper()
	s, err := openStore(context.Background(), os.Getenv("BERTH_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	l := s.Leases[id]
	s.Close()
	if err := adapters()[l.Resource.Adapter].Destroy(context.Background(), l); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationKind(t *testing.T) {
	integrationReady(t, "kind")
	created := invoke(t, 0, "create", "--adapter", "kind", "--ttl", "15m", "--resources", "kind adapter integration")
	l := created["record"].(map[string]any)
	id := l["id"].(string)
	resource := l["resource"].(map[string]any)
	name := resource["name"].(string)
	t.Logf("created uniquely leased kind cluster %s", name)
	out, err := runCommand(context.Background(), []string{"KIND_EXPERIMENTAL_PROVIDER=docker"}, "kind", "get", "clusters")
	if err != nil || !strings.Contains(string(out), name) {
		t.Fatalf("leased cluster not running: %s, %v", out, err)
	}
	removeIntegrationResource(t, id)
	invoke(t, 0, "release", id, "--confirm")
	invoke(t, 0, "release", id, "--confirm")
	out, err = runCommand(context.Background(), []string{"KIND_EXPERIMENTAL_PROVIDER=docker"}, "kind", "get", "clusters")
	if err != nil || strings.Contains(string(out), name) {
		t.Fatalf("cluster survived release: %s, %v", out, err)
	}
	t.Logf("verified kind get clusters no longer lists %s", name)
}

func TestIntegrationCompose(t *testing.T) {
	integrationReady(t, "compose")
	if _, err := runCommand(context.Background(), nil, "docker", "compose", "version"); err != nil {
		t.Skip("Docker Compose unavailable")
	}
	file := filepath.Join(t.TempDir(), "compose.yaml")
	config := "services:\n  test:\n    image: alpine:3.23\n    command: [sleep, '600']\n    volumes: [data:/data]\nvolumes:\n  data: {}\n"
	if err := os.WriteFile(file, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	created := invoke(t, 0, "create", "--adapter", "compose", "--file", file, "--ttl", "15m")
	id := created["record"].(map[string]any)["id"].(string)
	out, err := runCommand(context.Background(), nil, "docker", "ps", "-aq", "--filter", "label=io.berth.lease="+id)
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		t.Fatalf("stack not attributed to its lease: %s, %v", out, err)
	}
	// Reclaim must work after both the caller's config and the resources disappear.
	os.Remove(file)
	removeIntegrationResource(t, id)
	invoke(t, 0, "release", id, "--confirm")
	invoke(t, 0, "release", id, "--confirm")
	out, err = runCommand(context.Background(), nil, "docker", "ps", "-aq", "--filter", "label=io.berth.lease="+id)
	if err != nil || len(strings.TrimSpace(string(out))) != 0 {
		t.Fatalf("stack survived cleanup: %s, %v", out, err)
	}
}
