package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func workflowText(t *testing.T, name string) string {
	t.Helper()
	if filepath.Ext(name) == "" {
		name += ".yml"
	}
	data, err := os.ReadFile(filepath.Join(".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func workflowJob(t *testing.T, workflow, name string) string {
	t.Helper()
	text := workflowText(t, workflow)
	header := "\n  " + name + ":\n"
	_, rest, ok := strings.Cut(text, header)
	if !ok {
		t.Fatalf("%s has no %s job", workflow, name)
	}
	if next := regexp.MustCompile(`(?m)^  [a-zA-Z0-9_-]+:\s*$`).FindStringIndex(rest); next != nil {
		rest = rest[:next[0]]
	}
	return rest
}

func requireWorkflowText(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("workflow missing %q", want)
		}
	}
}

func TestWorkflowActionPinsAndTrustBoundaries(t *testing.T) {
	paths, err := filepath.Glob(".github/workflows/*.y*ml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("workflows: %v, %v", paths, err)
	}
	uses := regexp.MustCompile(`(?m)^\s*(?:-\s*)?uses:\s*(\S+)`)
	pinned := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			text := workflowText(t, filepath.Base(path))
			for _, match := range uses.FindAllStringSubmatch(text, -1) {
				if !strings.HasPrefix(match[1], "./") && !pinned.MatchString(match[1]) {
					t.Errorf("action must use an immutable SHA: %s", match[1])
				}
			}
			for _, forbidden := range []string{"pull_request_target:", "continue-on-error: true", "secrets: inherit"} {
				if strings.Contains(text, forbidden) {
					t.Errorf("workflow must not contain %q", forbidden)
				}
			}
			for _, step := range strings.Split(text, "\n      - ") {
				if strings.Contains(step, "uses: actions/checkout@") {
					requireWorkflowText(t, step, "persist-credentials: false")
				}
				if strings.Contains(step, "uses: actions/setup-go@") {
					requireWorkflowText(t, step, "go-version-file: go.mod")
				}
			}
			switch name {
			case "ci", "nix", "no-mistakes-required", "guard-generated-files":
				requireWorkflowText(t, text, "contents: read")
				if strings.Contains(text, ": write") || strings.Contains(text, "secrets.") {
					t.Error("PR workflows must stay read-only without secrets")
				}
			}
		})
	}
}

func TestCIWorkflowRequiredCoverage(t *testing.T) {
	check := workflowJob(t, "ci", "check")
	requireWorkflowText(t, check, "just lint", "just deps-check", "just vulncheck", "extractions/setup-just@")
	test := workflowJob(t, "ci", "test")
	requireWorkflowText(t, test, "fail-fast: false", "os: [ubuntu-24.04, macos-15]", "runs-on: ${{ matrix.os }}",
		"just test", "just build", "extractions/setup-just@")
	integration := workflowJob(t, "ci", "integration")
	requireWorkflowText(t, integration, "runs-on: ubuntu-24.04", "kind-linux-amd64", "k3d-linux-amd64",
		"sha256sum --check", "docker compose version", "just integration", "just ssh-integration")
	required := workflowJob(t, "ci", "main-required-ci")
	requireWorkflowText(t, required, "needs: [check, test, integration]", "if: ${{ always() }}",
		"RESULTS: ${{ toJSON(needs) }}", `jq -e 'all(.[]; .result == "success")'`)
	data, err := os.ReadFile("justfile")
	if err != nil {
		t.Fatal(err)
	}
	requireWorkflowText(t, string(data), "gofmt -l .", "exit 1", "go vet ./...", "go mod verify", "go mod tidy",
		"git diff --exit-code -- go.mod go.sum", "golang.org/x/vuln/cmd/govulncheck@", "go test -race -timeout 10m ./...",
		"lint: fmt-check vet", "check: lint test", "go install .", "release-check:", "release-snapshot:")
	if _, err := os.Stat("Makefile"); !os.IsNotExist(err) {
		t.Error("justfile replaces Makefile; do not keep two recipe owners")
	}
}

func TestNoMistakesRequiredWorkflow(t *testing.T) {
	text := workflowText(t, "no-mistakes-required")
	requireWorkflowText(t, text, "pull_request:", "types: [opened, edited, synchronize, reopened, ready_for_review]",
		"pull-requests: read", "github.run_id || 'head-change'", "name: PR must be raised via no-mistakes")
	job := workflowJob(t, "no-mistakes-required", "check")
	requireWorkflowText(t, job,
		"github.event.pull_request.user.login != 'github-actions[bot]' &&",
		"github.event.pull_request.user.login != 'dependabot[bot]' &&",
		"github.event.pull_request.user.login != 'release-please[bot]'",
		"kunchenguid/no-mistakes/.github/actions/require-no-mistakes@f6441c96c352a18b9cadcaef6b6c7017e9ac3970")
	for _, forbidden := range []string{"actions/checkout@", "exempt-authors:", "paths-ignore:"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("attestation gate must not contain %q", forbidden)
		}
	}
}
