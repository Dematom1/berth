package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	budgetpkg "github.com/Dematom1/dibs/internal/budget"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	outputpkg "github.com/Dematom1/dibs/internal/output"
)

func TestContractUsageBeforeSideEffects(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, args := range [][]string{
		{"create", "--key", "-bad"},
		{"create", "--key", strings.Repeat("k", 201)},
		{"create", "--name", "lab"},
		{"create", "--pending"},
		{"create", "--pending", "--adapter", "compose"},
		{"create", "--pending", "--adapter", "kind", "--file", "x"},
		{"create", "--pending", "--adapter", "kind", "--name", "-x"},
		{"adopt", "kind", "lab", "--owner", "pid:1", "--ttl", "1h", "--key", "bad key"},
		{"adopt", "kind", "lab", "--pending", "nothex"},
		{"adopt", "kind", "lab", "--pending", id, "--owner", "pid:1"},
		{"adopt", "kind", "lab", "--pending", id, "--ttl", "1h"},
		{"adopt", "kind", "lab", "--pending", id, "--context", "x"},
		{"adopt", "kind", "lab", "--pending", id, "--key", "k"},
		{"adopt", "kind", "lab", "--pending", id, "--file", "x"},
		{"adopt", "kind", "--pending", id},
		{"reconcile", "extra", "args"}, {"reconcile", "nothex"}, {"reconcile", "--typo"},
		{"reclaim"}, {"reclaim", "nothex"}, {"reclaim", id, id},
		{"explain"}, {"explain", id, "--confirm"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "untouched")
			t.Setenv("DIBS_STATE", dir)
			t.Setenv("PATH", "")
			var out bytes.Buffer
			if code := run(context.Background(), append([]string{"--json"}, args...), &out); code != 2 {
				t.Fatalf("code %d: %s", code, out.String())
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("usage error touched state")
			}
		})
	}
}

func TestContractKeysReclaimExplain(t *testing.T) {
	t.Setenv("DIBS_STATE", t.TempDir())
	self := "pid:" + strconv.Itoa(os.Getpid())
	created := invoke(t, 0, "create", "--owner", self, "--key", "job-1:meta")
	id := created["lease"].(map[string]any)["id"].(string)
	if created["contract"] != float64(1) || created["status"] != "created" || created["lease"].(map[string]any)["key"] != "job-1:meta" {
		t.Fatalf("keyed create: %v", created)
	}
	retry := invoke(t, 0, "create", "--owner", self, "--key", "job-1:meta")
	if retry["status"] != "already_done" || retry["lease"].(map[string]any)["id"] != id {
		t.Fatalf("retry: %v", retry)
	}
	var quiet bytes.Buffer
	if code := run(context.Background(), []string{"create", "--owner", self, "--key", "job-1:meta", "--quiet"}, &quiet); code != 0 || quiet.String() != id+"\n" {
		t.Fatalf("quiet retry: %d %q", code, quiet.String())
	}
	conflict := invoke(t, 3, "create", "--owner", "pid:"+strconv.Itoa(os.Getppid()), "--key", "job-1:meta")
	if conflict["reason"] != "key_conflict" || conflict["exit_code"] != float64(3) || conflict["contract"] != float64(1) {
		t.Fatalf("conflict: %v", conflict)
	}
	alive := invoke(t, 3, "reclaim", id, "--confirm")
	if alive["reason"] != "owner_alive" || !strings.Contains(alive["help"].([]any)[0].(string), "dibs release "+id) {
		t.Fatalf("live owner reclaimed: %v", alive)
	}
	explained := invoke(t, 0, "explain", id)
	if explained["status"] != "explained" || explained["liveness"] != "alive" || explained["reclaim_would_reclaim"] != false || explained["sweep_would_reclaim"] != false {
		t.Fatalf("explain: %v", explained)
	}

	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	owner := "pid:" + strconv.Itoa(child.Process.Pid)
	doomed := invoke(t, 0, "create", "--owner", owner)["lease"].(map[string]any)["id"].(string)
	child.Process.Kill()
	child.Wait()
	if e := invoke(t, 0, "explain", doomed); e["liveness"] != "dead" || e["reclaim_would_reclaim"] != true {
		t.Fatalf("explain dead: %v", e)
	}
	if p := invoke(t, 0, "reclaim", doomed); p["status"] != "preview" {
		t.Fatalf("preview: %v", p)
	}
	if r := invoke(t, 0, "reclaim", doomed, "--confirm"); r["status"] != "reclaimed" || r["liveness"] != "dead" {
		t.Fatalf("reclaim: %v", r)
	}
	if r := invoke(t, 0, "reclaim", doomed, "--confirm"); r["status"] != "already_done" {
		t.Fatalf("retried reclaim: %v", r)
	}
	if missing := invoke(t, 3, "explain", doomed); missing["reason"] != "not_found" {
		t.Fatalf("explain missing: %v", missing)
	}
	if empty := invoke(t, 0, "reconcile"); empty["count"] != float64(0) || empty["status"] != "preview" {
		t.Fatalf("reconcile: %v", empty)
	}
	if missing := invoke(t, 3, "reconcile", doomed); missing["reason"] != "not_found" {
		t.Fatalf("reconcile missing: %v", missing)
	}
	if done := invoke(t, 0, "reconcile", id, "--confirm"); done["results"].([]any)[0].(map[string]any)["status"] != "already_done" {
		t.Fatalf("reconcile non-pending: %v", done)
	}
	invoke(t, 0, "release", id, "--confirm")
	if fresh := invoke(t, 0, "create", "--owner", self, "--key", "job-1:meta"); fresh["status"] != "created" || fresh["lease"].(map[string]any)["id"] == id {
		t.Fatalf("released key not reusable: %v", fresh)
	}
}

func TestQuietContractCreate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_STATE", dir)
	self := "pid:" + strconv.Itoa(os.Getpid())
	var out bytes.Buffer
	args := []string{"create", "--owner", self, "--key", "quiet:contract", "--quiet"}
	if code := run(context.Background(), args, &out); code != 0 {
		t.Fatalf("created: %d %q", code, out.String())
	}
	id := strings.TrimSpace(out.String())
	if id == "" || out.String() != id+"\n" {
		t.Fatalf("create output %q", out.String())
	}
	out.Reset()
	if code := run(context.Background(), args, &out); code != 0 || out.String() != id+"\n" {
		t.Fatalf("already_done: %d %q", code, out.String())
	}
	out.Reset()
	conflictArgs := []string{"create", "--owner", "pid:" + strconv.Itoa(os.Getppid()), "--key", "quiet:contract", "--quiet"}
	if code := run(context.Background(), conflictArgs, &out); code != 3 || out.String() == id+"\n" {
		t.Fatalf("refusal: %d %q", code, out.String())
	}

	for _, args := range [][]string{
		{"--axi", "create", "--adapter", "kind", "--pending", "--key", "quiet:axi", "--owner", self, "--quiet"},
		{"create", "--adapter", "kind", "--pending", "--key", "quiet:json", "--owner", self, "--quiet", "--json"},
	} {
		state := filepath.Join(t.TempDir(), "untouched")
		t.Setenv("DIBS_STATE", state)
		out.Reset()
		if code := run(context.Background(), args, &out); code != 2 {
			t.Fatalf("explicit mode accepted: %d %q", code, out.String())
		}
		if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("usage error touched state: %v", err)
		}
	}
}

func TestRenewReleaseContractModes(t *testing.T) {
	for _, mode := range []outputpkg.Mode{outputpkg.AXI, outputpkg.JSON} {
		t.Run(string(mode), func(t *testing.T) {
			t.Setenv("DIBS_STATE", t.TempDir())
			t.Setenv("DIBS_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
			created := invoke(t, 0, "create", "--ttl", "1h")
			id := created["record"].(map[string]any)["id"].(string)
			invokeMode := func(args ...string) (int, string, map[string]any) {
				var out bytes.Buffer
				code := runWithOutput(context.Background(), args, &out, mode)
				result := map[string]any{}
				if mode == outputpkg.JSON {
					if err := json.Unmarshal(out.Bytes(), &result); err != nil {
						t.Fatalf("invalid JSON: %q: %v", out.String(), err)
					}
				} else if !strings.Contains(out.String(), "contract: 1") {
					t.Fatalf("missing AXI contract: %q", out.String())
				}
				return code, out.String(), result
			}
			assertField := func(code int, text string, got map[string]any, field, value string) {
				if code != 0 || (mode == outputpkg.JSON && (got["contract"] != float64(1) || got[field] != value)) || (mode == outputpkg.AXI && !strings.Contains(text, field+": "+value)) {
					t.Fatalf("%s: %d %q %+v", field, code, text, got)
				}
			}
			code, text, got := invokeMode("extend", id, "--ttl", "1h")
			assertField(code, text, got, "status", "renewed")
			code, text, got = invokeMode("extend", id, "--ttl", "1h")
			assertField(code, text, got, "status", "renewed")
			code, text, got = invokeMode("release", id)
			assertField(code, text, got, "status", "preview")
			code, text, got = invokeMode("release", id, "--confirm")
			assertField(code, text, got, "status", "released")
			code, text, got = invokeMode("release", id, "--confirm")
			assertField(code, text, got, "status", "already_done")
			missing := strings.Repeat("a", 32)
			code, text, got = invokeMode("extend", missing, "--ttl", "1h")
			if code != 3 || (mode == outputpkg.JSON && got["reason"] != "not_found") || (mode == outputpkg.AXI && !strings.Contains(text, "reason: not_found")) {
				t.Fatalf("missing renewal: %d %q %+v", code, text, got)
			}
			code, text, got = invokeMode("release", missing, "--confirm")
			assertField(code, text, got, "status", "already_done")

			mutateLease := func(id string, change func(*leasepkg.Lease)) {
				s, err := openStore(context.Background(), os.Getenv("DIBS_STATE"))
				if err != nil {
					t.Fatal(err)
				}
				l := s.Leases[id]
				change(&l)
				s.Leases[id] = l
				if err := s.Save(); err != nil {
					t.Fatal(err)
				}
			}
			for _, tc := range []struct {
				name, reason string
				change       func(*leasepkg.Lease)
			}{
				{"wrong-machine", "wrong_machine", func(l *leasepkg.Lease) { l.Machine = "another-machine" }},
				{"expired", "expired", func(l *leasepkg.Lease) { l.Expires = time.Now().Add(-time.Second) }},
				{"owner-dead", "owner_dead", func(l *leasepkg.Lease) { l.Owner.Identity = "different-start" }},
				{"owner-unknown", "owner_unknown", func(l *leasepkg.Lease) { l.Owner.Kind = "missing-owner-kind" }},
				{"job-deadline", "job_deadline", func(l *leasepkg.Lease) {
					l.ResourceRef.Adapter = "process"
					l.Reservation = &leasepkg.ReservationView{Budget: budgetpkg.Amount{Cores: 1, Memory: 1}, Class: "test"}
				}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					seed := invoke(t, 0, "create", "--ttl", "1h")
					seedID := seed["record"].(map[string]any)["id"].(string)
					mutateLease(seedID, tc.change)
					code, text, result := invokeMode("extend", seedID, "--ttl", "1h")
					if code != 3 || (mode == outputpkg.JSON && (result["contract"] != float64(1) || result["reason"] != tc.reason)) || (mode == outputpkg.AXI && !strings.Contains(text, "reason: "+tc.reason)) {
						t.Fatalf("refusal: %d %q %+v", code, text, result)
					}
					if tc.name == "wrong-machine" {
						code, text, result = invokeMode("release", seedID, "--confirm")
						if code != 3 || (mode == outputpkg.JSON && result["reason"] != "wrong_machine") || (mode == outputpkg.AXI && !strings.Contains(text, "reason: wrong_machine")) {
							t.Fatalf("release wrong machine: %d %q %+v", code, text, result)
						}
					}
				})
			}
			app := testApplication()
			for _, mode := range []outputpkg.Mode{outputpkg.AXI, outputpkg.JSON} {
				var refusal bytes.Buffer
				if code := app.contractOperationError(&refusal, mode, "release", id, &leasepkg.Refusal{Reason: "ambiguous", Message: "multiple resources match"}); code != 3 {
					t.Fatalf("ambiguous release: %d %s", code, refusal.String())
				}
				if mode == outputpkg.JSON {
					var result map[string]any
					if err := json.Unmarshal(refusal.Bytes(), &result); err != nil || result["reason"] != "ambiguous" || result["contract"] != float64(1) {
						t.Fatalf("ambiguous JSON refusal: %s %v", refusal.String(), err)
					}
				} else if !strings.Contains(refusal.String(), "reason: ambiguous") {
					t.Fatalf("ambiguous AXI refusal: %s", refusal.String())
				}
			}
		})
	}
}

func TestContractOutputModes(t *testing.T) {
	t.Setenv("DIBS_STATE", t.TempDir())
	missing := strings.Repeat("a", 32)
	for _, tc := range []struct {
		name string
		mode outputpkg.Mode
		args []string
		code int
	}{
		{"explain-missing-axi", outputpkg.AXI, []string{"explain", missing}, 3},
		{"explain-missing-json", outputpkg.JSON, []string{"explain", missing}, 3},
		{"explain-missing-human", outputpkg.Human, []string{"explain", missing}, 3},
		{"reclaim-absent-axi", outputpkg.AXI, []string{"reclaim", missing, "--confirm"}, 0},
		{"reclaim-absent-json", outputpkg.JSON, []string{"reclaim", missing, "--confirm"}, 0},
		{"reconcile-empty-axi", outputpkg.AXI, []string{"reconcile"}, 0},
		{"reconcile-empty-human", outputpkg.Human, []string{"reconcile"}, 0},
		{"help-reconcile-axi", outputpkg.AXI, []string{"reconcile", "--help"}, 0},
		{"help-reclaim-axi", outputpkg.AXI, []string{"reclaim", "--help"}, 0},
		{"help-explain-axi", outputpkg.AXI, []string{"explain", "--help"}, 0},
		{"help-adopt-axi", outputpkg.AXI, []string{"adopt", "--help"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := runWithOutput(context.Background(), tc.args, &out, tc.mode); code != tc.code {
				t.Fatalf("code %d: %s", code, out.String())
			}
			snapshot(t, tc.name, out.String())
		})
	}
}
