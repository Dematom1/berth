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
	for _, raw := range list(object(job)["steps"]) {
		step := object(raw)
		run, _ := step["run"].(string)
		if strings.TrimSpace(run) == command && step["if"] == nil {
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
	pinned := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
	checkUse := func(value any) {
		t.Helper()
		uses, _ := value.(string)
		if uses != "" && !strings.HasPrefix(uses, "./") && !pinned.MatchString(uses) {
			t.Errorf("action or reusable workflow is not SHA-pinned: %q", uses)
		}
	}
	for _, path := range paths {
		doc := workflow(t, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		jobs := object(doc["jobs"])
		for _, raw := range jobs {
			checkUse(object(raw)["uses"])
			for _, step := range list(object(raw)["steps"]) {
				uses, _ := object(step)["uses"].(string)
				checkUse(uses)
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
		{check, "just release-check"}, {check, "just lint"}, {check, "just cross"}, {check, "just deps-check"}, {check, "just vulncheck"},
		{testJob, "just test"}, {testJob, "just build"}, {integration, "just integration"},
		{integration, ".claude/skills/verify/verify.py local --evidence \"$RUNNER_TEMP/verify/local\""}, {integration, ".claude/skills/verify/verify.py ssh --evidence \"$RUNNER_TEMP/verify/ssh\""}, {integration, ".claude/skills/verify/verify.py adapters --evidence \"$RUNNER_TEMP/verify/adapters\""},
	} {
		require(t, hasRun(pair.job, pair.command), "missing CI command: "+pair.command)
	}
	required := object(jobs["main-required-ci"])
	gotNeeds := map[any]bool{}
	for _, need := range list(required["needs"]) {
		gotNeeds[need] = true
	}
	require(t, len(gotNeeds) == 3 && gotNeeds["check"] && gotNeeds["test"] && gotNeeds["integration"], "required CI gate must depend on all CI jobs")
	require(t, required["if"] == "${{ always() }}" && hasRun(required, `jq -e 'all(.[]; .result == "success")' <<< "$RESULTS"`), "required CI gate must fail unless every job succeeds")
}

func TestFormalWorkflowInstallsJustBeforeUse(t *testing.T) {
	steps := list(object(object(workflow(t, "formal")["jobs"])["formal"])["steps"])
	setup, invocation := -1, -1
	for i, raw := range steps {
		step := object(raw)
		if step["uses"] == "extractions/setup-just@f8a3cce218d9f83db3a2ecd90e41ac3de6cdfd9b" {
			setup = i
		}
		if run, ok := step["run"].(string); ok && strings.Contains(run, "just formal") {
			invocation = i
		}
	}
	require(t, setup >= 0 && invocation > setup, "formal workflow must install just before invoking its recipe")
}

func TestNoMistakesRequiredWorkflow(t *testing.T) {
	doc, _ := contribution(t)
	pullRequest := object(object(doc["on"])["pull_request"])
	wantEvents := map[any]bool{"opened": true, "edited": true, "synchronize": true, "reopened": true, "ready_for_review": true}
	gotEvents := map[any]bool{}
	for _, event := range list(pullRequest["types"]) {
		gotEvents[event] = true
	}
	require(t, len(gotEvents) == len(wantEvents), "attestation must use the complete required PR event set")
	for event := range wantEvents {
		require(t, gotEvents[event], "attestation missing PR event: "+event.(string))
	}
	require(t, pullRequest["paths"] == nil && pullRequest["paths-ignore"] == nil, "attestation must run for every PR path")
	concurrency := object(doc["concurrency"])
	wantGroup := "contribution-${{ github.event.pull_request.number }}-${{ (github.event.action == 'opened' || github.event.action == 'edited') && github.run_id || 'head-change' }}"
	require(t, concurrency["group"] == wantGroup, "body-bearing events must retain their individual attestation runs")
}
