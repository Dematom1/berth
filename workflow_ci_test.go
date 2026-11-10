package main

import (
	"os"
	"path/filepath"
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
					if len(pin) != 40 {
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
	testJob := object(jobs["test"])
	matrix := object(object(object(testJob["strategy"])["matrix"]))
	require(t, len(list(matrix["os"])) == 2, "race tests must cover both hosted OSes")
	require(t, testJob["runs-on"] == "${{ matrix.os }}", "test job must run on each matrix OS")
	for _, name := range []string{"check", "test", "integration", "main-required-ci"} {
		require(t, jobs[name] != nil, "missing required CI job: "+name)
	}
}

func TestNoMistakesRequiredWorkflow(t *testing.T) {
	doc := workflow(t, "no-mistakes-required")
	jobs := object(doc["jobs"])
	require(t, jobs["check"] != nil, "missing attestation check")
	require(t, object(doc["permissions"])["contents"] == "read", "PR workflow must have read-only contents permission")
}
