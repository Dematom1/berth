//go:build integration

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
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker executable required for %s integration: %v", driver, err)
	}
	if driver == "kind" || driver == "k3d" {
		if _, err := exec.LookPath(driver); err != nil {
			t.Fatalf("%s executable required for integration: %v", driver, err)
		}
	}
	if _, err := runCommand(context.Background(), nil, "docker", "info"); err != nil {
		t.Fatalf("Docker engine required for %s integration: %v", driver, err)
	}
	if driver == "compose" {
		if _, err := runCommand(context.Background(), nil, "docker", "compose", "version"); err != nil {
			t.Fatalf("Docker Compose required for integration: %v", err)
		}
	}
	dir, err := os.MkdirTemp("", "dibs-integration-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_STATE", dir)
	t.Setenv("DIBS_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
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

// Remove the real resource while its lease remains, then let dibs retry cleanup.
func removeIntegrationResource(t *testing.T, id string) {
	t.Helper()
	s, err := openStore(context.Background(), os.Getenv("DIBS_STATE"))
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
	out, err := runCommand(context.Background(), nil, "k3d", "cluster", "list", "-o", "json")
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
	out, err := runCommand(context.Background(), nil, "docker", "ps", "-aq", "--filter", "label=io.dibs.lease="+id)
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		t.Fatalf("stack not attributed to its lease: %s, %v", out, err)
	}
	// Reclaim must work after both the caller's config and the resources disappear.
	os.Remove(file)
	removeIntegrationResource(t, id)
	invoke(t, 0, "release", id, "--confirm")
	invoke(t, 0, "release", id, "--confirm")
	out, err = runCommand(context.Background(), nil, "docker", "ps", "-aq", "--filter", "label=io.dibs.lease="+id)
	if err != nil || len(strings.TrimSpace(string(out))) != 0 {
		t.Fatalf("stack survived cleanup: %s, %v", out, err)
	}
}

// A stack berth v0.1.0 started stays reclaimable after dibs migrates its state.
func TestIntegrationLegacyCompose(t *testing.T) {
	integrationReady(t, "compose")
	unsetenv(t, "DIBS_STATE", "BERTH_STATE")
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	out, err := runCommand(context.Background(), nil, "docker", "context", "show")
	if err != nil {
		t.Fatal(err)
	}
	host, err := machine()
	if err != nil {
		t.Fatal(err)
	}
	old, err := openStore(context.Background(), filepath.Join(base, legacyName))
	if err != nil {
		t.Fatal(err)
	}
	l := legacyComposeLease(t, old, host)
	l.Resource.Context = strings.TrimSpace(string(out))
	old.Leases[l.ID] = l
	err = old.Save()
	old.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := (composeAdapter{runCommand}).Create(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	running := func() string {
		out, err := runCommand(context.Background(), nil, "docker", "ps", "-aq", "--filter", "label=io.berth.lease="+l.ID)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() {
		l.Resource.File = legacyResourceFile(filepath.Join(base, "dibs"), l.Resource.File)
		if running() != "" {
			(composeAdapter{runCommand}).Destroy(context.Background(), l)
		}
	})
	if running() == "" {
		t.Fatal("legacy stack not labeled io.berth.lease")
	}
	invoke(t, 0, "release", l.ID, "--confirm")
	if left := running(); left != "" {
		t.Fatalf("legacy stack survived cleanup: %s", left)
	}
}
