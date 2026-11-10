//go:build integration

package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	adapterpkg "github.com/Dematom1/berth/internal/adapter"
	leasepkg "github.com/Dematom1/berth/internal/lease"
)

func integrationReady(t *testing.T, driver string) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker executable required for %s integration: %v", driver, err)
	}
	if driver == "kind" || driver == "k3d" {
		if _, err := exec.LookPath(driver); err != nil {
			t.Fatalf("%s executable required for integration: %v", driver, err)
		}
	}
	if _, err := adapterpkg.RunCommand(context.Background(), nil, "docker", "info"); err != nil {
		t.Fatalf("Docker engine required for %s integration: %v", driver, err)
	}
	if driver == "compose" {
		if _, err := adapterpkg.RunCommand(context.Background(), nil, "docker", "compose", "version"); err != nil {
			t.Fatalf("Docker Compose required for integration: %v", err)
		}
	}
	dir, err := os.MkdirTemp("", "berth-integration-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERTH_STATE", dir)
	t.Setenv("BERTH_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
	t.Cleanup(func() {
		manager := testApplication().leases
		listing, err := manager.List(context.Background(), leasepkg.Filter{})
		if err != nil {
			t.Errorf("integration cleanup state at %s: %v", dir, err)
			return
		}
		for _, observed := range listing.Leases {
			id := observed.Lease.ID
			if _, err := manager.Release(context.Background(), leasepkg.ReleaseRequest{ID: id, Confirm: true}); err != nil {
				t.Errorf("integration cleanup retained %s at %s: %v", id, dir, err)
			}
		}
		listing, listErr := manager.List(context.Background(), leasepkg.Filter{})
		empty := listErr == nil && len(listing.Leases) == 0
		if empty {
			os.RemoveAll(dir)
		}
	})
}

// Remove the real resource while its lease remains, then let berth retry cleanup.
func removeIntegrationResource(t *testing.T, id string) {
	t.Helper()
	l, exists, err := testApplication().leases.Get(context.Background(), id)
	if err != nil || !exists {
		t.Fatalf("get lease: exists=%v error=%v", exists, err)
	}
	d, _ := newAdapters().Lookup(l.ResourceRef.Adapter)
	if err := d.Adapter.Destroy(context.Background(), adapterpkg.Target{Ref: l.ResourceRef}); err != nil {
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
	out, err := adapterpkg.RunCommand(context.Background(), []string{"KIND_EXPERIMENTAL_PROVIDER=docker"}, "kind", "get", "clusters")
	if err != nil || !strings.Contains(string(out), name) {
		t.Fatalf("leased cluster not running: %s, %v", out, err)
	}
	removeIntegrationResource(t, id)
	invoke(t, 0, "release", id, "--confirm")
	invoke(t, 0, "release", id, "--confirm")
	out, err = adapterpkg.RunCommand(context.Background(), []string{"KIND_EXPERIMENTAL_PROVIDER=docker"}, "kind", "get", "clusters")
	if err != nil || strings.Contains(string(out), name) {
		t.Fatalf("cluster survived release: %s, %v", out, err)
	}
	t.Logf("verified kind get clusters no longer lists %s", name)
}

func TestIntegrationK3d(t *testing.T) {
	integrationReady(t, "k3d")
	caller := filepath.Join(t.TempDir(), "caller-kubeconfig")
	original := []byte("apiVersion: v1\nkind: Config\nclusters: []\nusers: []\ncontexts: []\ncurrent-context: \"\"\n")
	if err := os.WriteFile(caller, original, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", caller)
	assertCallerUnchanged := func() {
		t.Helper()
		data, err := os.ReadFile(caller)
		if err != nil || string(data) != string(original) {
			t.Fatalf("k3d changed caller kubeconfig: %q, %v", data, err)
		}
	}
	created := invoke(t, 0, "create", "--adapter", "k3d", "--ttl", "15m", "--resources", "k3d adapter integration")
	l := created["record"].(map[string]any)
	id := l["id"].(string)
	name := l["resource"].(map[string]any)["name"].(string)
	t.Logf("created uniquely leased k3d cluster %s", name)
	assertCallerUnchanged()
	removeIntegrationResource(t, id)
	invoke(t, 0, "release", id, "--confirm")
	invoke(t, 0, "release", id, "--confirm")
	out, err := adapterpkg.RunCommand(context.Background(), nil, "k3d", "cluster", "list", "-o", "json")
	if err != nil || strings.Contains(string(out), name) {
		t.Fatalf("cluster survived release: %s, %v", out, err)
	}
	assertCallerUnchanged()
	t.Logf("verified k3d cluster list no longer lists %s; caller kubeconfig unchanged", name)
}

func TestIntegrationCompose(t *testing.T) {
	integrationReady(t, "compose")
	file := filepath.Join(t.TempDir(), "compose.yaml")
	config := "services:\n  test:\n    image: alpine:3.23\n    command: [sleep, '600']\n    volumes: [data:/data]\nvolumes:\n  data: {}\n"
	if err := os.WriteFile(file, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	created := invoke(t, 0, "create", "--adapter", "compose", "--file", file, "--ttl", "15m")
	id := created["record"].(map[string]any)["id"].(string)
	out, err := adapterpkg.RunCommand(context.Background(), nil, "docker", "ps", "-aq", "--filter", "label=io.berth.lease="+id)
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		t.Fatalf("stack not attributed to its lease: %s, %v", out, err)
	}
	// Reclaim must work after both the caller's config and the resources disappear.
	os.Remove(file)
	removeIntegrationResource(t, id)
	invoke(t, 0, "release", id, "--confirm")
	invoke(t, 0, "release", id, "--confirm")
	out, err = adapterpkg.RunCommand(context.Background(), nil, "docker", "ps", "-aq", "--filter", "label=io.berth.lease="+id)
	if err != nil || len(strings.TrimSpace(string(out))) != 0 {
		t.Fatalf("stack survived cleanup: %s, %v", out, err)
	}
}
