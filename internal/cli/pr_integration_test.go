//go:build printegration

package cli

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

// Stable public pull requests read through the real GitHub API.
var (
	openPR   = ownerpkg.Reference{Kind: "pr", ID: "github.com/octocat/hello-world#32", Identity: "MDExOlB1bGxSZXF1ZXN0MTMzMzI3OA== MDEwOlJlcG9zaXRvcnkxMjk2MjY5"}
	closedPR = ownerpkg.Reference{Kind: "pr", ID: "github.com/octocat/hello-world#1", Identity: "MDExOlB1bGxSZXF1ZXN0MTQwOTAw MDEwOlJlcG9zaXRvcnkxMjk2MjY5"}
	mergedPR = ownerpkg.Reference{Kind: "pr", ID: "github.com/dematom1/dibs#28", Identity: "PR_kwDOVodKms8AAAABOPR3Lg R_kgDOVodKmg"}
)

func TestPROwnerIntegration(t *testing.T) {
	if _, err := exec.LookPath("gh"); err != nil {
		t.Fatalf("gh executable required for pr owner integration: %v", err)
	}
	dir := t.TempDir()
	t.Setenv("DIBS_STATE", dir)
	t.Setenv("DIBS_CONFIG", filepath.Join(dir, "absent.json"))
	t.Setenv("DIBS_PR_OWNER", "1")

	created := invoke(t, 0, "create", "--owner", "pr:https://github.com/octocat/Hello-World/pull/32", "--ttl", "1h")
	id := created["record"].(map[string]any)["id"].(string)
	invoke(t, 0, "create", "--owner", "pr:octocat/Hello-World#933", "--ttl", "1h")
	for _, closed := range []string{"pr:octocat/Hello-World#1", "pr:Dematom1/dibs#28"} {
		if msg, _ := invoke(t, 2, "create", "--owner", closed, "--ttl", "1h")["error"].(string); msg == "" {
			t.Fatalf("%s owned a new lease", closed)
		}
	}
	listing := invoke(t, 0, "list", "--owner", "pr:octocat/hello-world#32")
	if listing["count"] != float64(1) || listing["leases"].([]any)[0].(map[string]any)["liveness"] != "alive" {
		t.Fatalf("open PR lease not alive: %+v", listing)
	}
	invoke(t, 0, "extend", id, "--ttl", "2h")
	if r := invoke(t, 0, "sweep", "--confirm"); len(r["reclaimed"].([]any)) != 0 {
		t.Fatalf("swept open or draft PR leases: %+v", r)
	}

	// Repoint the open lease at a mismatched and then a closed PR so sweep observes each through the real API.
	for _, tc := range []struct {
		owner   ownerpkg.Reference
		reclaim bool
	}{
		{ownerpkg.Reference{Kind: "pr", ID: openPR.ID, Identity: "PR_replaced MDEwOlJlcG9zaXRvcnkxMjk2MjY5"}, false},
		{closedPR, true},
	} {
		s, err := openStore(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		l := s.Leases[id]
		l.Owner = tc.owner
		s.Leases[id] = l
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
		r := invoke(t, 0, "sweep", "--confirm")
		if got := len(r["reclaimed"].([]any)) == 1; got != tc.reclaim {
			t.Fatalf("%s reclaimed=%v, want %v: %+v", tc.owner.ID, got, tc.reclaim, r)
		}
	}
	registry := ownerpkg.NewRegistry(ownerpkg.PR())
	for _, tc := range []struct {
		owner ownerpkg.Reference
		want  ownerpkg.Liveness
	}{
		{openPR, ownerpkg.Alive}, {closedPR, ownerpkg.Dead}, {mergedPR, ownerpkg.Dead},
		{ownerpkg.Reference{Kind: "pr", ID: "github.com/octocat/hello-world#999999999", Identity: openPR.Identity}, ownerpkg.Unknown},
	} {
		if got := registry.Check(context.Background(), tc.owner, time.Now()).Liveness; got != tc.want {
			t.Fatalf("%s = %s, want %s", tc.owner.ID, got, tc.want)
		}
	}
}
