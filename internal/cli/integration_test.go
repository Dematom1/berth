//go:build integration

package cli

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
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
	dir, err := os.MkdirTemp("", "dibs-integration-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_STATE", dir)
	t.Setenv("DIBS_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
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

// Remove the real resource while its lease remains, then let dibs retry cleanup.
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

func TestIntegrationAdoptAndOrphans(t *testing.T) {
	for _, driver := range []string{"kind", "k3d", "compose"} {
		t.Run(driver, func(t *testing.T) {
			integrationReady(t, driver)
			t.Setenv("DOCKER_HOST", "")
			ctx := context.Background()
			var token [16]byte
			if _, err := rand.Read(token[:]); err != nil {
				t.Fatal(err)
			}
			fixtureID := fmt.Sprintf("%x", token)
			d, _ := newAdapters().Lookup(driver)
			file := ""
			if driver == "compose" {
				file = filepath.Join(t.TempDir(), "compose.yaml")
				if err := os.WriteFile(file, []byte("services:\n  test:\n    image: alpine:3.23\n    command: [sleep, '600']\n    volumes: [data:/data]\nvolumes:\n  data: {}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ref, err := d.Prepare(ctx, adapterpkg.PrepareRequest{Metadata: adapterpkg.Metadata{LeaseID: fixtureID}, SnapshotDir: filepath.Join(t.TempDir(), "snapshot"), File: file})
			if err != nil {
				t.Fatal(err)
			}
			fixture := adapterpkg.Target{Ref: ref}
			t.Cleanup(func() {
				if err := d.Adapter.Destroy(ctx, fixture); err != nil {
					t.Errorf("fixture cleanup failed: %v", err)
				}
			})
			if err := d.Adapter.Create(ctx, fixture); err != nil {
				t.Fatal(err)
			}
			name := ref.Name
			find := func(result map[string]any) bool {
				for _, item := range result["orphans"].([]any) {
					row := item.(map[string]any)
					if row["type"] == driver && row["name"] == name {
						if !strings.Contains(row["adopt"].(string), name) || row["evidence"] == nil {
							t.Fatal("orphan lacks evidence or adoption command")
						}
						return true
					}
				}
				return false
			}
			if !find(invoke(t, 0, "orphans")) {
				t.Fatal("unleased fixture missing from inventory")
			}
			if _, err := os.Stat(filepath.Join(os.Getenv("DIBS_STATE"), "state.json")); !os.IsNotExist(err) {
				t.Fatal("orphan view wrote lease state")
			}
			args := []string{"adopt", driver, name, "--owner", os.Getenv("DIBS_OWNER"), "--ttl", "1ms", "--context", ref.Context}
			if file != "" {
				args = append(args, "--file", file)
			}
			adopted := invoke(t, 0, args...)
			id := adopted["record"].(map[string]any)["id"].(string)
			if id == fixtureID {
				t.Fatal("adoption reused fixture identity")
			}
			if find(invoke(t, 0, "orphans")) {
				t.Fatal("leased resource still listed as unleased")
			}
			invoke(t, 1, args...)
			if file != "" {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
			time.Sleep(2 * time.Millisecond)
			preview := invoke(t, 0, "sweep")
			if len(preview["eligible"].([]any)) != 1 || len(preview["reclaimed"].([]any)) != 0 {
				t.Fatal("sweep preview did not preserve adopted resource")
			}
			rows, err := d.Discover(ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			present := false
			for _, row := range rows {
				if row["name"] == name {
					present = true
				}
			}
			if !present {
				t.Fatal("preview deleted the resource")
			}
			result := invoke(t, 0, "sweep", "--confirm")
			if len(result["reclaimed"].([]any)) != 1 {
				t.Fatal("expired adopted resource not reclaimed")
			}
			if find(invoke(t, 0, "orphans")) {
				t.Fatal("adopted fixture survived sweep")
			}
			invoke(t, 0, "release", id, "--confirm")
			t.Logf("verified %s orphan discovery, explicit adoption, preview, expiry sweep and verify-gone", driver)
		})
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
	out, err := adapterpkg.RunCommand(context.Background(), nil, "docker", "ps", "-aq", "--filter", "label=io.dibs.lease="+id)
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		t.Fatalf("stack not attributed to its lease: %s, %v", out, err)
	}
	// Reclaim must work after both the caller's config and the resources disappear.
	os.Remove(file)
	removeIntegrationResource(t, id)
	invoke(t, 0, "release", id, "--confirm")
	invoke(t, 0, "release", id, "--confirm")
	out, err = adapterpkg.RunCommand(context.Background(), nil, "docker", "ps", "-aq", "--filter", "label=io.dibs.lease="+id)
	if err != nil || len(strings.TrimSpace(string(out))) != 0 {
		t.Fatalf("stack survived cleanup: %s, %v", out, err)
	}
}

// A supervisor outage lets a protected lease expire: sweep keeps the live
// owner's real resource, a late renew reattaches, and a vanished resource refuses.
func TestIntegrationProtectedLeaseReattach(t *testing.T) {
	for _, driver := range []string{"kind", "k3d", "compose"} {
		t.Run(driver, func(t *testing.T) {
			integrationReady(t, driver)
			enableProtection(t)
			t.Setenv("DOCKER_HOST", "")
			args := []string{"create", "--adapter", driver, "--ttl", "1s", "--protect-live-owner"}
			if driver == "compose" {
				file := filepath.Join(t.TempDir(), "compose.yaml")
				if err := os.WriteFile(file, []byte("services:\n  test:\n    image: alpine:3.23\n    command: [sleep, '600']\n"), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--file", file)
			}
			id := invoke(t, 0, args...)["record"].(map[string]any)["id"].(string)
			expire := func() {
				l, _, err := testApplication().leases.Get(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				time.Sleep(time.Until(l.Expires) + 100*time.Millisecond)
			}
			expire()
			if r := invoke(t, 0, "sweep", "--confirm"); len(r["reclaimed"].([]any)) != 0 {
				t.Fatalf("sweep reclaimed a live owner's protected lease: %+v", r)
			}
			if r := invoke(t, 0, "extend", id, "--ttl", "1s"); r["status"] != "reattached" {
				t.Fatalf("late renew of a present resource: %+v", r)
			}
			removeIntegrationResource(t, id)
			expire()
			if r := invoke(t, 3, "extend", id, "--ttl", "1h"); r["reason"] != "resource_absent" {
				t.Fatalf("late renew of a removed resource: %+v", r)
			}
			invoke(t, 0, "release", id, "--confirm")
		})
	}
}
