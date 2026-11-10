package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupMigratesLegacyHooks(t *testing.T) {
	home := hookHome(t)
	legacyPi := filepath.Join(home, ".pi", "agent", "extensions", "berth-axi.ts")
	legacyOpenCode := filepath.Join(home, ".config", "opencode", "plugins", "berth-axi.js")
	legacyClaude := filepath.Join(home, ".claude", "settings.json")
	legacyCodex := filepath.Join(home, ".codex", "config.toml")
	for path, content := range map[string]string{
		legacyPi:       "// berth-axi: managed by berth setup hooks\nold plugin\n",
		legacyOpenCode: "// user-managed legacy filename\n",
		legacyClaude:   `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"# berth-axi\nexport BERTH_AXI=1"},{"type":"command","command":"echo keep"}]}]}}`,
		legacyCodex:    "features.hooks = true # berth-axi was-false # user choice\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	invoke(t, 0, "setup", "hooks")
	files := hookFiles(t, home)
	if _, exists := files[legacyPi]; exists {
		t.Fatal("left the old managed Pi plugin installed")
	}
	if files[legacyOpenCode] != "// user-managed legacy filename\n" {
		t.Fatal("changed an unmanaged legacy plugin")
	}
	if strings.Contains(files[legacyClaude], "BERTH_AXI") || !strings.Contains(files[legacyClaude], "DIBS_AXI") || !strings.Contains(files[legacyClaude], "echo keep") {
		t.Fatal("session hook was not upgraded safely", files[legacyClaude])
	}
	if invoke(t, 0, "setup", "hooks")["status"] != "unchanged (no-op)" {
		t.Fatal("upgrade was not idempotent")
	}
	invoke(t, 0, "setup", "hooks", "--remove")
	if got := hookFiles(t, home)[legacyCodex]; got != "features.hooks = false # user choice\n" {
		t.Fatalf("legacy Codex toggle not restored: %q", got)
	}
}
