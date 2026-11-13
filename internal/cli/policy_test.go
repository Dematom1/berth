package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	outputpkg "github.com/Dematom1/dibs/internal/output"
	policypkg "github.com/Dematom1/dibs/internal/policy"
)

func jsonRun(t *testing.T, want int, args ...string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	if code := run(context.Background(), append([]string{"--json"}, args...), &out); code != want {
		t.Fatalf("dibs %s = %d, want %d: %s", strings.Join(args, " "), code, want, out.String())
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("dibs %s: %v: %s", strings.Join(args, " "), err, out.String())
	}
	return result
}

// Each new capability is off until its own setting names it, and refuses before touching state.
func TestShellExecAndPolicyOptIns(t *testing.T) {
	owner := "pid:" + strconv.Itoa(os.Getpid())
	for name, tc := range map[string]struct {
		args    []string
		setting string
	}{
		"shell adapter":      {[]string{"adopt", "shell", strconv.Itoa(os.Getppid()), "--owner", owner, "--ttl", "1h"}, "DIBS_SHELL_ADAPTER=1"},
		"exec owner":         {[]string{"create", "--owner", "exec:true"}, "DIBS_EXEC_OWNER=1"},
		"create policy flag": {[]string{"create", "--owner", owner, "--idle", "5m"}, "DIBS_LEASE_POLICIES=1"},
		"adopt policy flag":  {[]string{"adopt", "kind", "lab", "--owner", owner, "--ttl", "1h", "--max-lifetime", "2h"}, "DIBS_LEASE_POLICIES=1"},
		"adopt without ttl":  {[]string{"adopt", "kind", "lab", "--owner", owner}, "positive --ttl"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "untouched")
			t.Setenv("DIBS_STATE", dir)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			result := jsonRun(t, 2, tc.args...)
			if !strings.Contains(result["error"].(string), tc.setting) {
				t.Fatalf("error %q does not name %s", result["error"], tc.setting)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refusal touched state")
			}
		})
	}
	t.Setenv("DIBS_SHELL_ADAPTER", "1")
	t.Setenv("DIBS_STATE", filepath.Join(t.TempDir(), "state"))
	if result := jsonRun(t, 2, "adopt", "shell", "123", "--context", "x", "--owner", owner, "--ttl", "1h"); !strings.Contains(result["error"].(string), "--context only for Docker") {
		t.Fatalf("shell with a Docker context = %v", result)
	}
}

func TestPolicyTableOnCreateAndExplain(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("DIBS_STATE", filepath.Join(t.TempDir(), "state"))
	t.Setenv("DIBS_CONSUMER_CONTRACT", "1")
	owner := "pid:" + strconv.Itoa(os.Getpid())
	created := jsonRun(t, 0, "create", "--owner", owner)
	id := created["record"].(map[string]any)["id"].(string)
	explained := jsonRun(t, 0, "explain", id)
	if _, ok := explained["policy"]; ok {
		t.Fatalf("explain grew policy fields with policies off: %v", explained)
	}
	if created["record"].(map[string]any)["policy"] != nil {
		t.Fatal("lease recorded a policy with policies off")
	}

	t.Setenv("DIBS_LEASE_POLICIES", "1")
	write := func(body string) {
		if err := os.MkdirAll(filepath.Join(config, "dibs"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(config, "dibs", "config.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"policies": {"none": {"binding": "time", "ttl": "2h", "renew": "30m", "max_lifetime": "3h"}}}`)
	created = jsonRun(t, 0, "create", "--owner", owner)
	record := created["record"].(map[string]any)
	if record["ttl"].(float64) != 2*3600e9 || record["policy"].(map[string]any)["row"] != "none" {
		t.Fatalf("policy create = %v", record)
	}
	explained = jsonRun(t, 0, "explain", record["id"].(string))
	policy := explained["policy"].(map[string]any)
	if policy["binding"] != "time" || policy["max_lifetime"] != "3h0m0s" || policy["source"] != "lease" || explained["held_because"] != "owner_alive" || explained["remaining"] == "" || explained["renew_by"] == "" {
		t.Fatalf("policy explain = %v", explained)
	}
	// A lease from before the table keeps its TTL rule and says so.
	if legacy := jsonRun(t, 0, "explain", id)["policy"].(map[string]any); legacy["source"] != "legacy" || legacy["binding"] != "time" {
		t.Fatalf("legacy explain = %v", legacy)
	}
	if result := jsonRun(t, 2, "create", "--owner", owner, "--ttl", "4h"); !strings.Contains(result["error"].(string), "exceeds") {
		t.Fatalf("ttl past max lifetime = %v", result)
	}
	write(`{"policies": {"none": {"binding": "owner", "max_lifetime": "1h"}}}`)
	if result := jsonRun(t, 2, "create", "--owner", owner); !strings.Contains(result["error"].(string), "use hybrid") {
		t.Fatalf("invalid policy row = %v", result)
	}
	write(`{"policies": {"none": {"binding": "time", "ttl": "1h", "typo": true}}}`)
	if result := jsonRun(t, 2, "create", "--owner", owner); !strings.Contains(result["error"].(string), "unknown field") {
		t.Fatalf("unknown policy field = %v", result)
	}
}

type policyShell struct{ destroyed *bool }

func (p policyShell) Create(context.Context, adapterpkg.Target) error { return nil }
func (p policyShell) Destroy(context.Context, adapterpkg.Target) error {
	*p.destroyed = true
	return nil
}

func TestSweepProtectsLiveOwnerShellUnlessPolicyAllowsIt(t *testing.T) {
	for _, allowLive := range []bool{false, true} {
		t.Run(strconv.FormatBool(allowLive), func(t *testing.T) {
			destroyed := false
			ref := adapterpkg.ResourceRef{Adapter: "shell", Name: "424242", Adopted: true, Process: &adapterpkg.ProcessRef{PID: 424242, Start: "shell-start"}}
			definition := adapterpkg.Definition{
				Name: "shell", Adapter: policyShell{&destroyed},
				Discover: func(_ context.Context, r adapterpkg.ResourceRef) ([]map[string]any, error) {
					return []map[string]any{{"name": r.Name}}, nil
				},
				Prepare: func(_ context.Context, r adapterpkg.PrepareRequest) (adapterpkg.ResourceRef, error) {
					return r.Existing, nil
				},
				Validate: func(string, adapterpkg.Metadata, adapterpkg.ResourceRef) error { return nil },
				Env:      func(adapterpkg.ResourceRef) adapterpkg.Environment { return adapterpkg.Environment{} },
				Activity: func(context.Context, adapterpkg.Target) (adapterpkg.Activity, error) {
					return adapterpkg.Activity{Idle: time.Hour, IdleKnown: true}, nil
				},
			}
			resources := adapterpkg.NewRegistry(definition)
			owners := newOwners()
			owner, err := owners.Bind(context.Background(), "pid:"+strconv.Itoa(os.Getpid()), 0, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			manager := leasepkg.New(leasepkg.Options{Directory: t.TempDir(), Adapters: resources, Owners: owners})
			policy := &policypkg.Policy{Row: "shell", Binding: policypkg.Hybrid, TTL: time.Hour, Idle: time.Nanosecond, Action: policypkg.Destroy, ReclaimLiveOwner: allowLive}
			l, _, err := manager.Adopt(context.Background(), leasepkg.AdoptRequest{Owner: owner, TTL: time.Hour, Resource: ref, Policy: policy})
			if err != nil {
				t.Fatal(err)
			}
			app := application{version: version, resources: resources, owners: owners, leases: manager}
			var out bytes.Buffer
			if code := app.runWithOutput(context.Background(), []string{"sweep", "--confirm"}, &out, outputpkg.JSON); code != 0 {
				t.Fatalf("sweep = %d: %s", code, out.String())
			}
			if destroyed != allowLive {
				t.Fatalf("destroyed=%v, policy permission=%v", destroyed, allowLive)
			}
			if !allowLive {
				var result map[string]any
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				held := result["held"].(map[string]any)
				if held[l.ID] != leasepkg.HoldLiveOwner {
					t.Fatalf("held = %v", held)
				}
			}
		})
	}
}
