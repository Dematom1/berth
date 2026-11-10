package main

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

func TestReleasePleaseDraftConfiguration(t *testing.T) {
	data, err := os.ReadFile("release-please-config.json")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Packages map[string]struct {
			ReleaseType      string `json:"release-type"`
			Draft            bool   `json:"draft"`
			ForceTagCreation bool   `json:"force-tag-creation"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	pkg := config.Packages["."]
	if pkg.ReleaseType != "go" || !pkg.Draft || !pkg.ForceTagCreation {
		t.Fatalf("release-please must create tagged Go draft releases: %+v", pkg)
	}
	data, err = os.ReadFile(".release-please-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]string
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(manifest["."]) {
		t.Fatalf("invalid release version: %q", manifest["."])
	}
}

func TestReleaseWorkflowLifecycle(t *testing.T) {
	jobs := object(workflow(t, "release")["jobs"])
	for _, name := range []string{"release-please", "build-and-upload", "finalize"} {
		require(t, jobs[name] != nil, "missing release lifecycle stage: "+name)
	}
	build := object(jobs["build-and-upload"])
	require(t, build["needs"] == "release-please", "asset build must depend on release creation")
	finalize := object(jobs["finalize"])
	needs := list(finalize["needs"])
	require(t, len(needs) == 2, "publication must wait for release creation and complete assets")
}
