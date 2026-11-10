package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuardGeneratedFilesWorkflow(t *testing.T) {
	text := workflowText(t, "guard-generated-files")
	requireWorkflowText(t, text, "types: [opened, synchronize, reopened]", "fetch-depth: 0",
		"github.event.pull_request.user.login != 'github-actions[bot]' &&",
		"github.event.pull_request.user.login != 'release-please[bot]'")
	if strings.Contains(text, "paths-ignore:") {
		t.Fatal("generated-only PRs must still run the guard")
	}
	job := workflowJob(t, "guard-generated-files", "check")
	_, script, ok := strings.Cut(job, "        run: |\n")
	if !ok {
		t.Fatal("guard has no shell script")
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
