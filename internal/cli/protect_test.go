package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	configpkg "github.com/Dematom1/dibs/internal/config"
	outputpkg "github.com/Dematom1/dibs/internal/output"
)

// enableProtection opts this test into --protect-live-owner without reading the user config.
func enableProtection(t *testing.T) {
	t.Helper()
	t.Setenv("DIBS_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	t.Setenv("DIBS_LIVE_OWNER_PROTECTION", "1")
}

func TestLiveOwnerProtectionOptIn(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DIBS_CONFIG", config)
	t.Setenv("DIBS_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
	for _, tc := range []struct {
		name, config, env string
		setEnv, want      bool
	}{
		{name: "absent", want: false},
		{name: "config off", config: `{"live_owner_protection":false}`, want: false},
		{name: "config on", config: `{"live_owner_protection":true}`, want: true},
		{name: "env on", env: "1", setEnv: true, want: true},
		{name: "env off overrides config", config: `{"live_owner_protection":true}`, env: "0", setEnv: true, want: false},
		{name: "invalid env stays off", config: `{"live_owner_protection":true}`, env: "yes", setEnv: true, want: false},
		{name: "invalid config stays off", config: `{"live_owner_protection":true`, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DIBS_STATE", t.TempDir())
			os.Remove(config)
			if tc.config != "" {
				if err := os.WriteFile(config, []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("DIBS_LIVE_OWNER_PROTECTION", tc.env)
			if !tc.setEnv {
				os.Unsetenv("DIBS_LIVE_OWNER_PROTECTION")
			}
			if tc.want {
				if r := invoke(t, 0, "create", "--protect-live-owner"); r["record"].(map[string]any)["protect_live_owner"] != true {
					t.Fatalf("enabled protection not recorded: %+v", r)
				}
				return
			}
			for _, args := range [][]string{
				{"create", "--protect-live-owner"},
				{"adopt", "kind", "my-lab", "--owner", "pid:" + strconv.Itoa(os.Getpid()), "--ttl", "1h", "--protect-live-owner"},
			} {
				r := invoke(t, 2, args...)
				if msg, _ := r["error"].(string); !strings.Contains(msg, "DIBS_LIVE_OWNER_PROTECTION=1") || !strings.Contains(msg, `"live_owner_protection": true`) {
					t.Fatalf("%s while off = %+v", args[0], r)
				}
				code, out := runMode(t, "axi", args...)
				if code != 2 || !strings.Contains(out, "DIBS_LIVE_OWNER_PROTECTION=1") {
					t.Fatalf("AXI %s while off = %d:\n%s", args[0], code, out)
				}
			}
			if r := invoke(t, 0, "ps"); r["count"] != float64(0) {
				t.Fatalf("refused protection wrote a lease: %+v", r)
			}
			if r := invoke(t, 0, "create"); r["record"].(map[string]any)["protect_live_owner"] != nil {
				t.Fatalf("unprotected lease shows protection: %+v", r)
			}
		})
	}
}

func TestConfigAcceptsLiveOwnerProtection(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DIBS_CONFIG", config)
	if err := os.WriteFile(config, []byte(`{"live_owner_protection":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if c, err := configpkg.Load(); err != nil || !c.LiveOwnerProtection {
		t.Fatalf("strict config load rejected live_owner_protection: %+v, %v", c, err)
	}
}

func runMode(t *testing.T, mode string, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := run(context.Background(), append([]string{"--" + mode}, args...), &out)
	return code, out.String()
}

// A supervisor outage lets a protected lease expire; sweep keeps it and a late renew reattaches.
func TestCLIProtectedLeaseSurvivesExpiryAndReattaches(t *testing.T) {
	enableProtection(t)
	dir := t.TempDir()
	t.Setenv("DIBS_STATE", dir)
	t.Setenv("DIBS_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
	created := invoke(t, 0, "create", "--ttl", "1ns", "--protect-live-owner")
	record := created["record"].(map[string]any)
	id := record["id"].(string)
	if record["protect_live_owner"] != true || created["lease"].(map[string]any)["protect_live_owner"] != true {
		t.Fatalf("protection not reported: %+v", created)
	}
	if r := invoke(t, 0, "sweep", "--confirm"); len(r["eligible"].([]any)) != 0 || len(r["reclaimed"].([]any)) != 0 {
		t.Fatalf("expired protected lease with a live owner was reclaimed: %+v", r)
	}
	if invoke(t, 0, "ps")["count"] != float64(1) {
		t.Fatal("protected lease lost")
	}
	if r := invoke(t, 0, "extend", id, "--ttl", "1h"); r["status"] != "reattached" || r["help"] == nil {
		t.Fatalf("late renew did not reattach: %+v", r)
	}
	code, out := runMode(t, "axi", "extend", id, "--ttl", "1h")
	if code != 0 || !strings.Contains(out, "status: renewed") || !strings.Contains(out, "protect_live_owner: true") || !strings.Contains(out, "dibs extend "+id+" --ttl 1h0m0s to renew again") {
		t.Fatalf("AXI renew = %d:\n%s", code, out)
	}
	code, out = runMode(t, "axi", "ps")
	if code != 0 || !strings.Contains(out, "protect_live_owner") || !strings.Contains(out, ",true,") {
		t.Fatalf("AXI ps lacks protection:\n%s", out)
	}
	var human bytes.Buffer
	if code := runWithOutput(context.Background(), []string{"ps"}, &human, outputpkg.Human); code != 0 || !strings.Contains(human.String(), "alive, protected") {
		t.Fatalf("human ps lacks protection:\n%s", human.String())
	}
	state, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil || !strings.Contains(string(state), `"version": 3`) {
		t.Fatalf("protected ledger not format 3: %s", state)
	}
	invoke(t, 0, "release", id, "--confirm")
	if state, _ := os.ReadFile(filepath.Join(dir, "state.json")); !strings.Contains(string(state), `"version": 2`) {
		t.Fatalf("empty ledger stayed format 3: %s", state)
	}
}

func TestCLIUnprotectedLateRenewUnchanged(t *testing.T) {
	t.Setenv("DIBS_STATE", t.TempDir())
	t.Setenv("DIBS_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
	id := invoke(t, 0, "create", "--ttl", "1ns")["record"].(map[string]any)["id"].(string)
	if r := invoke(t, 1, "extend", id, "--ttl", "1h"); r["reason"] != nil || r["exit_code"] != float64(1) {
		t.Fatalf("unprotected late renew changed: %+v", r)
	}
}

func TestCLIProtectedRenewRefusalsByOwner(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		breakOwner   func(string) error
	}{
		{"dead", "owner_dead", os.Remove},
		{"unknown", "owner_unknown", func(path string) error {
			future := time.Now().Add(time.Hour)
			return os.Chtimes(path, future, future)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableProtection(t)
			dir := t.TempDir()
			t.Setenv("DIBS_STATE", dir)
			beat := filepath.Join(dir, "beat")
			if err := os.WriteFile(beat, nil, 0600); err != nil {
				t.Fatal(err)
			}
			id := invoke(t, 0, "create", "--owner", "heartbeat:"+beat, "--ttl", "1h", "--protect-live-owner")["record"].(map[string]any)["id"].(string)
			if err := tc.breakOwner(beat); err != nil {
				t.Fatal(err)
			}
			r := invoke(t, 3, "extend", id, "--ttl", "1h")
			if r["reason"] != tc.reason || r["exit_code"] != float64(3) || r["code"] != "COMMAND_FAILED" || r["id"] != id || len(r["help"].([]any)) != 1 {
				t.Fatalf("JSON refusal = %+v", r)
			}
			code, out := runMode(t, "axi", "extend", id, "--ttl", "1h")
			if code != 3 || !strings.Contains(out, "reason: "+tc.reason) || !strings.Contains(out, "exit_code: 3") || !strings.Contains(out, "help[1]:") {
				t.Fatalf("AXI refusal = %d:\n%s", code, out)
			}
			sweep := invoke(t, 0, "sweep", "--confirm")
			if reclaimed := len(sweep["reclaimed"].([]any)); (tc.reason == "owner_dead") != (reclaimed == 1) {
				t.Fatalf("sweep after %s reclaimed %d", tc.reason, reclaimed)
			}
		})
	}
}

// Fake kind and docker report the pinned cluster as present, absent, or unreachable.
func TestCLIProtectedLateRenewChecksPinnedResource(t *testing.T) {
	enableProtection(t)
	dir := t.TempDir()
	t.Setenv("DIBS_STATE", dir)
	t.Setenv("DIBS_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
	t.Setenv("DOCKER_HOST", "")
	bin := t.TempDir()
	scripts := map[string]string{
		"kind": `#!/bin/sh
case "$1" in
create) printf '%s' "$4" > "$DIBS_STATE/cluster" ;;
esac
exit 0
`,
		"docker": `#!/bin/sh
mode=$(cat "$DIBS_STATE/docker-mode" 2>/dev/null)
case "$*" in
"context show") printf 'test-context\n'; exit 0 ;;
esac
[ "$mode" = fail ] && exit 1
case "$*" in
*"container ls"*) [ "$mode" = present ] && printf 'c1\n'; exit 0 ;;
*"container inspect"*) printf '[{"Name":"/c1","Config":{"Labels":{"io.x-k8s.kind.cluster":"%s"}},"State":{"Status":"running"}}]' "$(cat "$DIBS_STATE/cluster")"; exit 0 ;;
esac
exit 1
`,
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	mode := func(m string) {
		if err := os.WriteFile(filepath.Join(dir, "docker-mode"), []byte(m), 0600); err != nil {
			t.Fatal(err)
		}
	}
	id := invoke(t, 0, "create", "--adapter", "kind", "--ttl", "1ns", "--protect-live-owner")["record"].(map[string]any)["id"].(string)
	for _, step := range []struct{ mode, reason string }{{"fail", "resource_unknown"}, {"absent", "resource_absent"}} {
		mode(step.mode)
		if r := invoke(t, 3, "extend", id, "--ttl", "1ns"); r["reason"] != step.reason {
			t.Fatalf("docker %s: %+v", step.mode, r)
		}
		code, out := runMode(t, "axi", "extend", id, "--ttl", "1ns")
		if code != 3 || !strings.Contains(out, "reason: "+step.reason) {
			t.Fatalf("AXI docker %s = %d:\n%s", step.mode, code, out)
		}
	}
	mode("present")
	if r := invoke(t, 0, "extend", id, "--ttl", "1h"); r["status"] != "reattached" {
		t.Fatalf("present resource did not reattach: %+v", r)
	}
	invoke(t, 0, "release", id, "--confirm")
}
