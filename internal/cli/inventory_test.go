package cli

import (
	"bytes"
	"context"
	"errors"
	outputpkg "github.com/Dematom1/dibs/internal/output"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInventoryUsageBeforeSideEffects(t *testing.T) {
	for _, args := range [][]string{
		{"adopt", "kind", "lab", "--owner", "bad", "--ttl", "1h"},
		{"adopt", "kind", "lab", "--ttl", "1h"},
		{"adopt", "kind", "lab", "--owner", "pid:1", "--ttl", "0"},
		{"adopt", "kind", "--bad", "lab"},
		{"adopt", "compose", "lab", "--owner", "pid:1", "--ttl", "1h"},
		{"adopt", "kind", "-unsafe", "--owner", "pid:1", "--ttl", "1h"},
		{"orphans", "--confirm"}, {"orphans", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "untouched")
			t.Setenv("DIBS_STATE", dir)
			t.Setenv("PATH", "")
			var out bytes.Buffer
			if code := run(context.Background(), args, &out); code != 2 {
				t.Fatalf("code %d: %s", code, out.String())
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("usage error touched state")
			}
		})
	}
}

func TestInventoryOutputModes(t *testing.T) {
	row := map[string]any{"type": "compose", "name": "lab", "docker_context": "test", "config_files": "/tmp/compose.yaml", "evidence": map[string]any{"container:test": map[string]any{"created": "2026-01-01T00:00:00Z", "labels": map[string]string{"custom": "preserved"}}}}
	row["adopt"] = adoptionCommand(row)
	for _, mode := range []outputpkg.Mode{outputpkg.Human, outputpkg.AXI, outputpkg.JSON} {
		var out bytes.Buffer
		outputpkg.Emit(&out, mode, map[string]any{"count": 1, "orphans": []map[string]any{row}})
		if !strings.Contains(out.String(), "dibs adopt compose") || !strings.Contains(out.String(), "preserved") {
			t.Fatalf("missing evidence/command in %s: %s", mode, out.String())
		}
		out.Reset()
		outputpkg.Emit(&out, mode, map[string]any{"count": 0, "orphans": []map[string]any{}})
		if out.Len() == 0 {
			t.Fatal("ambiguous empty output")
		}
	}
}
