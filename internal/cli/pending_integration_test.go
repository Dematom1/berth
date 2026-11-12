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
	"testing"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
)

func integrationPresent(t *testing.T, d adapterpkg.Definition, ref adapterpkg.ResourceRef) bool {
	t.Helper()
	rows, err := d.Discover(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row["name"] == ref.Name {
			return true
		}
	}
	return false
}

// The caller builds the resource itself and crashes before registering it.
// Reconcile must bind it, reclaim must refuse a live owner, and reclaim after
// the owner dies must destroy it and verify it gone.
func TestIntegrationPendingCrashRecovery(t *testing.T) {
	for _, tc := range []struct{ driver, match string }{{"kind", "name"}, {"k3d", "name"}, {"compose", "label"}} {
		t.Run(tc.driver+"-by-"+tc.match, func(t *testing.T) {
			integrationReady(t, tc.driver)
			t.Setenv("DOCKER_HOST", "")
			ctx := context.Background()
			owner := exec.Command("sleep", "600")
			if err := owner.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { owner.Process.Kill(); owner.Wait() })
			ownerRef := "pid:" + strconv.Itoa(owner.Process.Pid)
			d, _ := newAdapters().Lookup(tc.driver)
			file := ""
			if tc.driver == "compose" {
				file = filepath.Join(t.TempDir(), "compose.yaml")
				if err := os.WriteFile(file, []byte("services:\n  test:\n    image: alpine:3.23\n    command: [sleep, '600']\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var token [16]byte
			if _, err := rand.Read(token[:]); err != nil {
				t.Fatal(err)
			}
			key := fmt.Sprintf("it-%x", token[:4])
			args := []string{"create", "--adapter", tc.driver, "--pending", "--owner", ownerRef, "--ttl", "1h", "--key", key}
			if file != "" {
				args = append(args, "--file", file)
			}
			// The pinned-name case names the resource as the fixture will; the label case
			// lets the fixture stamp io.dibs.lease with the pending lease's id.
			fixtureID := fmt.Sprintf("%x", token)
			fixtureRef, err := d.Prepare(ctx, adapterpkg.PrepareRequest{Metadata: adapterpkg.Metadata{LeaseID: fixtureID}, SnapshotDir: filepath.Join(t.TempDir(), "probe"), File: file})
			if err != nil {
				t.Fatal(err)
			}
			if tc.match == "name" {
				args = append(args, "--name", fixtureRef.Name)
			}
			recorded := invoke(t, 0, args...)
			id := recorded["lease"].(map[string]any)["id"].(string)
			if recorded["status"] != "recorded" || recorded["lease"].(map[string]any)["pending"] != true {
				t.Fatalf("pending not recorded: %v", recorded)
			}
			if tc.match == "label" {
				fixtureRef, err = d.Prepare(ctx, adapterpkg.PrepareRequest{Metadata: adapterpkg.Metadata{LeaseID: id}, SnapshotDir: filepath.Join(t.TempDir(), "fixture"), File: file})
				if err != nil {
					t.Fatal(err)
				}
			}
			fixture := adapterpkg.Target{Ref: fixtureRef}
			t.Cleanup(func() {
				if err := d.Adapter.Destroy(ctx, fixture); err != nil {
					t.Errorf("fixture cleanup failed: %v", err)
				}
			})
			if err := d.Adapter.Create(ctx, fixture); err != nil {
				t.Fatal(err)
			}
			// Crash here: the caller never ran dibs adopt --pending. Its restart retries
			// the same keyed acquire and gets the same lease back.
			if retry := invoke(t, 0, args...); retry["status"] != "already_done" || retry["lease"].(map[string]any)["id"] != id {
				t.Fatalf("keyed retry made a second lease: %v", retry)
			}
			preview := invoke(t, 0, "reconcile", id)["results"].([]any)[0].(map[string]any)
			if preview["status"] != "bindable" || preview["matches"].([]any)[0] != fixtureRef.Name {
				t.Fatalf("reconcile preview: %v", preview)
			}
			if tc.driver == "k3d" {
				// The ordinary register step, as the caller would run it without a crash.
				if bound := invoke(t, 0, "adopt", tc.driver, fixtureRef.Name, "--pending", id); bound["status"] != "bound" {
					t.Fatalf("adopt --pending: %v", bound)
				}
				if again := invoke(t, 0, "adopt", tc.driver, fixtureRef.Name, "--pending", id); again["status"] != "already_done" {
					t.Fatalf("adopt --pending retry: %v", again)
				}
			} else if bound := invoke(t, 0, "reconcile", id, "--confirm")["results"].([]any)[0].(map[string]any); bound["status"] != "bound" {
				t.Fatalf("reconcile: %v", bound)
			}
			if again := invoke(t, 0, "reconcile", id, "--confirm")["results"].([]any)[0].(map[string]any); again["status"] != "already_done" {
				t.Fatalf("reconcile retry: %v", again)
			}
			if refused := invoke(t, 3, "reclaim", id, "--confirm"); refused["reason"] != "owner_alive" {
				t.Fatalf("live owner: %v", refused)
			}
			if !integrationPresent(t, d, fixtureRef) {
				t.Fatal("refused reclaim removed the resource")
			}
			owner.Process.Kill()
			owner.Wait()
			if r := invoke(t, 0, "reclaim", id, "--confirm"); r["status"] != "reclaimed" {
				t.Fatalf("reclaim: %v", r)
			}
			if integrationPresent(t, d, fixtureRef) {
				t.Fatal("reclaimed resource survived")
			}
			path := "reconcile"
			if tc.driver == "k3d" {
				path = "adopt --pending"
			}
			t.Logf("verified %s pending lease, crash before register, keyed retry, %s matched by %s, live-owner refusal, and reclaim after owner death", tc.driver, path, tc.match)
		})
	}
	t.Run("never-created", func(t *testing.T) {
		integrationReady(t, "kind")
		t.Setenv("DOCKER_HOST", "")
		id := invoke(t, 0, "create", "--adapter", "kind", "--pending", "--name", "dibs-it-never-built", "--ttl", "1h")["lease"].(map[string]any)["id"].(string)
		if r := invoke(t, 0, "reconcile", id)["results"].([]any)[0].(map[string]any); r["status"] != "absent" {
			t.Fatalf("reconcile: %v", r)
		}
		if r := invoke(t, 0, "release", id, "--confirm"); r["status"] != "released" {
			t.Fatalf("release: %v", r)
		}
	})
}
