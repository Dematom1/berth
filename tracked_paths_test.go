package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"unicode"
)

// Local machine paths, agent scratchpads, and prompt glyphs must never ship in
// this public repository. One once landed via an icon-decorated `ls` capture.
func privatePathReason(path string) string {
	for _, marker := range []string{"/private/tmp", "/Users/", "scratchpad"} {
		if strings.Contains(path, marker) {
			return "contains " + marker
		}
	}
	for _, r := range path {
		switch {
		case r == unicode.ReplacementChar:
			return "is not valid UTF-8"
		case unicode.IsControl(r):
			return "contains a control character"
		case unicode.Is(unicode.Co, r):
			return "contains a private-use character"
		}
	}
	return ""
}

func TestPrivatePathReason(t *testing.T) {
	for _, path := range []string{
		" lock",
		"\U000F0001",
		"a\tb",
		"a\x1b[0mb",
		"a\nb",
		"\xff",
		"x/Users/me/lock",
		"x/private/tmp/lock",
		"docs/scratchpad/notes.md",
	} {
		if privatePathReason(path) == "" {
			t.Errorf("guard accepted %q", path)
		}
	}
	for _, path := range []string{"README.md", "internal/lease/store.go", "testdata/baseline/axi-10.golden", "docs/naïve résumé.md"} {
		if reason := privatePathReason(path); reason != "" {
			t.Errorf("guard rejected %q: %s", path, reason)
		}
	}
}

func TestTrackedPathsArePublic(t *testing.T) {
	out, err := exec.Command("git", "ls-files", "-z").Output()
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("git ls-files: %v", err)
		}
		t.Skipf("not a git checkout: %v", err)
	}
	for _, path := range bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0}) {
		if reason := privatePathReason(string(path)); reason != "" {
			t.Errorf("tracked path %q %s; remove it with git rm", path, reason)
		}
	}
}
