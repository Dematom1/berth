package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func workflow(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(".github", "workflows", name+".yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func list(v any) []any            { a, _ := v.([]any); return a }
func require(t *testing.T, ok bool, message string) {
	t.Helper()
	if !ok {
		t.Error(message)
	}
}

func runs(job any) []string {
	var out []string
	for _, step := range list(object(job)["steps"]) {
		if run, ok := object(step)["run"].(string); ok {
			out = append(out, run)
		}
	}
	return out
}

func hasRun(job any, command string) bool {
	for _, run := range runs(job) {
		if strings.Contains(run, command) {
			return true
		}
	}
	return false
}

func TestWorkflowActionPinsAndTrustBoundaries(t *testing.T) {
	paths, err := filepath.Glob(".github/workflows/*.y*ml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("workflows: %v, %v", paths, err)
	}
	for _, path := range paths {
		doc := workflow(t, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		jobs := object(doc["jobs"])
		for _, raw := range jobs {
			for _, step := range list(object(raw)["steps"]) {
				uses, _ := object(step)["uses"].(string)
				if uses == "" {
					continue
				}
				if !strings.HasPrefix(uses, "./") && !strings.Contains(uses, "@") {
					t.Errorf("unversioned action %q", uses)
				}
				if !strings.HasPrefix(uses, "./") {
					pin := strings.SplitN(uses, "@", 2)[1]
					if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(pin) {
						t.Errorf("action is not SHA-pinned: %q", uses)
					}
				}
				with := object(object(step)["with"])
				if strings.Contains(uses, "actions/checkout") {
					require(t, with["persist-credentials"] == false, "checkout credentials must not persist")
				}
				if strings.Contains(uses, "actions/setup-go") {
					require(t, with["go-version-file"] == "go.mod", "Go version must come from go.mod")
				}
			}
		}
	}
}

func TestCIWorkflowRequiredCoverage(t *testing.T) {
	jobs := object(workflow(t, "ci")["jobs"])
	check, testJob, integration := jobs["check"], jobs["test"], jobs["integration"]
	matrix := object(object(object(testJob)["strategy"])["matrix"])
	gotOS := map[any]bool{}
	for _, os := range list(matrix["os"]) {
		gotOS[os] = true
	}
	require(t, gotOS["ubuntu-24.04"] && gotOS["macos-15"] && len(gotOS) == 2, "race tests must cover Ubuntu 24.04 and macOS 15")
	require(t, object(testJob)["runs-on"] == "${{ matrix.os }}", "test job must run on each matrix OS")
	for _, pair := range []struct {
		job     any
		command string
	}{
		{check, "just lint"}, {check, "just cross"}, {check, "just deps-check"}, {check, "just vulncheck"},
		{testJob, "just test"}, {testJob, "just build"}, {integration, "just integration"}, {integration, "just ssh-integration"},
	} {
		require(t, hasRun(pair.job, pair.command), "missing CI command: "+pair.command)
	}
	required := object(jobs["main-required-ci"])
	gotNeeds := map[any]bool{}
	for _, need := range list(required["needs"]) {
		gotNeeds[need] = true
	}
	require(t, len(gotNeeds) == 3 && gotNeeds["check"] && gotNeeds["test"] && gotNeeds["integration"], "required CI gate must depend on all CI jobs")
	require(t, required["if"] == "${{ always() }}" && hasRun(required, `all(.[]; .result == "success")`), "required CI gate must fail unless every job succeeds")
}

func TestNoMistakesRequiredWorkflow(t *testing.T) {
	doc := workflow(t, "no-mistakes-required")
	jobs := object(doc["jobs"])
	require(t, jobs["check"] != nil, "missing attestation check")
	require(t, object(doc["permissions"])["contents"] == "read", "PR workflow must have read-only contents permission")
	pullRequest := object(object(doc["on"])["pull_request"])
	require(t, len(list(pullRequest["types"])) == 5, "attestation must rerun on PR edits and readiness changes")
	job := object(jobs["check"])
	require(t, strings.Contains(job["if"].(string), "release-please[bot]"), "release automation must be exempt from PR attestation")
	var guardAction bool
	for _, raw := range list(job["steps"]) {
		uses, _ := object(raw)["uses"].(string)
		guardAction = guardAction || strings.Contains(uses, "require-no-mistakes@")
	}
	require(t, guardAction, "attestation must invoke the no-mistakes guard")
}
