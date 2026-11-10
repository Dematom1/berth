package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuardGeneratedFilesWorkflow(t *testing.T) {
	doc := workflow(t, "guard-generated-files")
	job := object(object(doc["jobs"])["check"])
	steps := list(job["steps"])
	var script string
	for _, raw := range steps {
		if run, ok := object(raw)["run"].(string); ok {
			script = run
		}
	}
	if script == "" {
		t.Fatal("guard has no executable script")
	}
	for _, tc := range []struct {
		name, changed string
		bootstrap     bool
		wantOK        bool
	}{
		{"bootstrap", ".release-please-manifest.json", true, true},
		{"ordinary change", "source.go", false, true},
		{"manifest edit", ".release-please-manifest.json", false, false},
		{"changelog edit", "CHANGELOG.md", false, false},
		{"manifest deletion", ".release-please-manifest.json", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-c", "user.name=Policy test", "-c", "user.email=policy@example.invalid", "-c", "core.hooksPath=/dev/null", "-C", dir}, args...)...)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			write := func(path string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, path), []byte(tc.name), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			git("init", "--quiet")
			write("source.go")
			if !tc.bootstrap {
				write(".release-please-manifest.json")
				write("CHANGELOG.md")
			}
			git("add", ".")
			git("commit", "--quiet", "--no-gpg-sign", "-m", "base")
			base := git("rev-parse", "HEAD")
			if tc.name == "manifest deletion" {
				if err := os.Remove(filepath.Join(dir, tc.changed)); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(dir, tc.changed), []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
			git("add", "-A")
			git("commit", "--quiet", "--no-gpg-sign", "-m", "change")
			cmd := exec.Command("bash", "-c", script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "BASE_SHA="+base, "HEAD_SHA="+git("rev-parse", "HEAD"))
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.wantOK {
				t.Fatalf("guard result = %v, want success=%t\n%s", err, tc.wantOK, out)
			}
		})
	}
}
