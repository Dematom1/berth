package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	remotepkg "github.com/Dematom1/dibs/internal/remote"
)

func hookHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "PI_CODING_AGENT_DIR", "XDG_CONFIG_HOME", "CLAUDE_ENV_FILE", "CODEX_ENV_FILE"} {
		t.Setenv(name, "")
	}
	return home
}

func hookFiles(t *testing.T, home string) map[string]string {
	t.Helper()
	files := map[string]string{}
	if err := filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = string(data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestSetupHooksLifecycle(t *testing.T) {
	home := hookHome(t)
	claude := filepath.Join(home, ".claude", "settings.json")
	codex := filepath.Join(home, ".codex", "config.toml")
	for path, data := range map[string]string{
		claude: `{"model":"keep","hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"echo other"}]}],"Stop":[]}}`,
		codex:  "model = \"keep\"\n\n[features]\nhooks = false # user choice\nother = true\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	invoke(t, 0, "setup", "hooks")
	first := hookFiles(t, home)
	if len(first) != 5 {
		t.Fatalf("wrong destinations: %v", first)
	}
	for path, data := range first {
		if !strings.Contains(data, "dibs-axi") {
			t.Fatalf("missing marker: %s", path)
		}
	}
	if !strings.Contains(first[claude], "echo other") || !strings.Contains(first[claude], "keep") || !strings.Contains(first[codex], "other = true") {
		t.Fatal("lost user settings")
	}
	result := invoke(t, 0, "setup", "hooks")
	if result["status"] != "unchanged (no-op)" || len(result["changed"].([]any)) != 0 {
		t.Fatal(result)
	}
	second := hookFiles(t, home)
	for path, data := range first {
		if second[path] != data {
			t.Fatalf("non-idempotent %s", path)
		}
	}
	invoke(t, 0, "setup", "hooks", "--remove")
	removed := hookFiles(t, home)
	if len(removed) != 3 || strings.Contains(removed[claude], "dibs-axi") || !strings.Contains(removed[claude], "echo other") || strings.Contains(removed[codex], "dibs-axi") || !strings.Contains(removed[codex], "hooks = false # user choice") {
		t.Fatalf("bad removal: %v", removed)
	}
	if invoke(t, 0, "setup", "hooks", "--remove")["status"] != "unchanged (no-op)" {
		t.Fatal("remove not idempotent")
	}
}

func TestSetupFreshHomeAndOverrides(t *testing.T) {
	home := hookHome(t)
	for _, env := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "PI_CODING_AGENT_DIR", "XDG_CONFIG_HOME"} {
		t.Setenv(env, filepath.Join(home, env))
	}
	invoke(t, 0, "setup", "hooks")
	files := hookFiles(t, home)
	if len(files) != 5 {
		t.Fatal(files)
	}
	invoke(t, 0, "setup", "hooks", "--remove")
	if data, err := os.ReadFile(filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")); err != nil || len(data) != 0 {
		t.Fatal(string(data), err)
	}
}

func TestSetupRefusesMalformedAndUnmanagedFiles(t *testing.T) {
	for _, tc := range []struct{ path, content string }{
		{".claude/settings.json", "broken"}, {".claude/settings.json", "null"},
		{".claude/settings.json", `{"hooks": []}`},
		{".codex/hooks.json", `{"hooks":{"SessionStart":true}}`},
		{".codex/config.toml", "[broken"},
		{".codex/config.toml", "[features]\nhooks = 'yes'\n"},
		{".codex/config.toml", "text = '''\n[features]\nhooks = false\n'''\n"},
		{".pi/agent/extensions/dibs-axi.ts", "// user code"},
		{".config/opencode/plugins/dibs-axi.js", "// user code"},
	} {
		t.Run(tc.path+tc.content, func(t *testing.T) {
			home := hookHome(t)
			path := filepath.Join(home, tc.path)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			invoke(t, 1, "setup", "hooks")
			files := hookFiles(t, home)
			if len(files) != 1 || files[path] != tc.content {
				t.Fatalf("modified user files: %v", files)
			}
		})
	}
}

func TestSessionHookSetsEnvironment(t *testing.T) {
	home := hookHome(t)
	for _, env := range []string{"CLAUDE_ENV_FILE", "CODEX_ENV_FILE"} {
		path := filepath.Join(home, env+" ' env")
		t.Setenv(env, path)
		cmd := exec.Command("sh", "-c", sessionHook)
		if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
			t.Fatalf("hook: %s %v", out, err)
		}
		cmd = exec.Command("sh", "-c", ". "+remotepkg.ShellQuote(path)+"; printf '%s' \"$DIBS_AXI\"")
		if out, err := cmd.Output(); err != nil || string(out) != "1" {
			t.Fatalf("env not applied: %q %v", out, err)
		}
		t.Setenv(env, "")
	}
	out, err := exec.Command("sh", "-c", sessionHook).Output()
	if err != nil || !strings.Contains(string(out), "dibs --axi") {
		t.Fatalf("missing older-harness fallback: %q %v", out, err)
	}
}

func TestCodexFeaturePreservesConfig(t *testing.T) {
	for _, input := range []string{"", "model = \"test\"\n", "[features]\nother=true\n", "features.hooks = false\n", "[features]\nhooks = true\n", "[features]\nhooks=false # custom\n\n[other]\nx=1\n"} {
		installed, err := editCodexFeature([]byte(input), false)
		if err != nil {
			t.Fatal(err)
		}
		again, err := editCodexFeature(installed, false)
		if err != nil || !bytes.Equal(installed, again) {
			t.Fatalf("non-idempotent %q -> %q -> %q (%v)", input, installed, again, err)
		}
		removed, err := editCodexFeature(installed, true)
		if err != nil || string(removed) != input {
			t.Fatalf("didn't restore %q -> %q (%v)", input, removed, err)
		}
	}
}

func TestSessionHookDeduplicatesOnlyOwnedEntries(t *testing.T) {
	input := []byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"# dibs-axi\nold"},{"type":"command","command":"echo dibs-axi"}]}]}}`)
	data, err := editSessionHook(input, false)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := editSessionHook(data, false)
	if err != nil || !bytes.Equal(data, twice) {
		t.Fatal("not idempotent", err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("echo dibs-axi")) || bytes.Contains(data, []byte("old")) {
		t.Fatalf("bad ownership: %s", data)
	}
}

func TestSetupUsageDoesNotTouchHome(t *testing.T) {
	home := hookHome(t)
	for _, args := range [][]string{{"setup"}, {"setup", "hooks", "--bad"}, {"setup", "hooks", "extra"}} {
		var out bytes.Buffer
		if run(context.Background(), args, &out) != 2 {
			t.Fatal(out.String())
		}
	}
	if len(hookFiles(t, home)) != 0 {
		t.Fatal("usage error changed config")
	}
}
