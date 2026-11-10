package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"

	adapterpkg "github.com/Dematom1/berth/internal/adapter"
	leasepkg "github.com/Dematom1/berth/internal/lease"
	outputpkg "github.com/Dematom1/berth/internal/output"
	ownerpkg "github.com/Dematom1/berth/internal/owner"
	"strings"
	"testing"
	"time"
)

// Reproduce a lease written before a hostname change through the CLI, without
// changing this host's name or reading its real ledger.
func TestCLISweepAfterHostnameChange(t *testing.T) {
	for _, mode := range []outputpkg.Mode{outputpkg.Human, outputpkg.AXI, outputpkg.JSON} {
		t.Run(string(mode), func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("BERTH_STATE", dir)
			t.Setenv("BERTH_LEGACY_MACHINE", "")
			now := time.Now().UTC()
			old := "old-hostname.localdomain"
			expired := leasepkg.Lease{ID: strings.Repeat("a", 32), Machine: old, Owner: ownerpkg.Reference{Kind: "pid", ID: "4299161601", Identity: "unknown"}, Created: now.Add(-18 * time.Hour), Expires: now.Add(-16 * time.Hour), TTL: 2 * time.Hour, ResourceRef: adapterpkg.ResourceRef{Adapter: "none"}}
			pending := expired
			pending.ID = strings.Repeat("b", 32)
			pending.Expires = now.Add(time.Hour)
			foreign := expired
			foreign.ID = strings.Repeat("c", 32)
			foreign.Machine = "another-machine"
			data, err := json.Marshal(fixtureStore{Version: 1, Leases: map[string]leasepkg.Lease{expired.ID: expired, pending.ID: pending, foreign.ID: foreign}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "state.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			call := func(confirm bool) string {
				t.Helper()
				args := []string{"sweep"}
				if confirm {
					args = append(args, "--confirm")
				}
				var out bytes.Buffer
				if code := runWithOutput(context.Background(), args, &out, mode); code != 0 {
					t.Fatalf("sweep exit %d: %s", code, &out)
				}
				return out.String()
			}
			read := func() fixtureStore {
				t.Helper()
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var state fixtureStore
				if err := json.Unmarshal(data, &state); err != nil {
					t.Fatal(err)
				}
				return state
			}
			// With no evidence that the legacy name is local, both paths must refuse it.
			call(false)
			call(true)
			if len(read().Leases) != 3 {
				t.Fatal("unverified legacy machine was reclaimed")
			}
			// Explicit user attestation authorizes only this exact old hostname.
			t.Setenv("BERTH_LEGACY_MACHINE", old)
			preview := call(false)
			state := read()
			host := state.Leases[expired.ID].Machine
			if state.Version != 2 || !strings.HasPrefix(host, runtime.GOOS+":") || len(state.Leases) != 3 {
				t.Fatalf("preview must durably migrate identity, not reclaim: %+v; %s", state, preview)
			}
			if !strings.Contains(preview, expired.ID) {
				t.Fatalf("preview missing eligible lease: %s", preview)
			}
			// The alias is no longer needed, even with owner liveness still unknown.
			t.Setenv("BERTH_LEGACY_MACHINE", "")
			confirmed := call(true)
			state = read()
			if _, ok := state.Leases[expired.ID]; ok {
				t.Fatalf("expired unknown owner not reclaimed: %s", confirmed)
			}
			if len(state.Leases) != 2 || state.Leases[pending.ID].Machine != host || state.Leases[foreign.ID].Machine != foreign.Machine {
				t.Fatalf("unknown unexpired or foreign lease changed: %+v", state)
			}
			// Even an explicit alias must not rebind another machine's stable identity.
			foreign.Machine = "linux:0123456789abcdef0123456789abcdef"
			state.Leases[foreign.ID] = foreign
			data, _ = json.Marshal(state)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BERTH_LEGACY_MACHINE", foreign.Machine)
			call(true)
			if read().Leases[foreign.ID].Machine != foreign.Machine {
				t.Fatal("foreign stable identity was rebound")
			}
		})
	}
}
