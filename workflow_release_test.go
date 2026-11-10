package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
	doc := workflow(t, "release")
	push := object(object(doc["on"])["push"])
	require(t, len(list(push["branches"])) == 1 && list(push["branches"])[0] == "main", "release workflow must only run on main pushes")
	jobs := object(doc["jobs"])
	please, build, finalize := object(jobs["release-please"]), object(jobs["build-and-upload"]), object(jobs["finalize"])
	require(t, please["outputs"] != nil && please["permissions"] != nil, "release-please must expose release metadata and write permissions")
	var releasePleaseAction, releaseConfig bool
	for _, raw := range list(please["steps"]) {
		step := object(raw)
		uses, _ := step["uses"].(string)
		releasePleaseAction = releasePleaseAction || strings.Contains(uses, "release-please-action")
		with := object(step["with"])
		releaseConfig = releaseConfig || with["config-file"] == "release-please-config.json" && with["manifest-file"] == ".release-please-manifest.json"
	}
	require(t, releasePleaseAction && releaseConfig, "release creation must use release-please config and manifest")
	require(t, build["needs"] == "release-please", "asset build must depend on release creation")
	require(t, build["if"] == "needs.release-please.outputs.release_created == 'true'", "assets may build only for a created release")
	var taggedCheckout bool
	var buildCommands []string
	for _, raw := range list(build["steps"]) {
		step := object(raw)
		if with := object(step["with"]); with["ref"] == "${{ needs.release-please.outputs.tag_name }}" {
			taggedCheckout = true
		}
		if run, ok := step["run"].(string); ok {
			buildCommands = append(buildCommands, run)
		}
	}
	require(t, taggedCheckout, "build must check out the release tag")
	commands := strings.Join(buildCommands, "\n")
	for _, want := range []string{"test \"${#archives[@]}\" -eq 4", "sha256sum --check checksums.txt", "gh release upload"} {
		require(t, strings.Contains(commands, want), "missing release asset operation: "+want)
	}
	require(t, strings.Index(commands, "sha256sum --check") >= 0 && strings.Index(commands, "sha256sum --check") < strings.Index(commands, "gh release upload"), "checksums must be verified before upload")
	var skipPublish bool
	for _, raw := range list(build["steps"]) {
		with := object(object(raw)["with"])
		if args, ok := with["args"].(string); ok && strings.Contains(args, "--skip=publish") {
			skipPublish = true
		}
	}
	require(t, skipPublish, "GoReleaser must upload only after the complete draft asset set exists")
	needs := list(finalize["needs"])
	gotNeeds := map[any]bool{}
	for _, need := range needs {
		gotNeeds[need] = true
	}
	condition, _ := finalize["if"].(string)
	require(t, len(gotNeeds) == 2 && gotNeeds["release-please"] && gotNeeds["build-and-upload"], "publication must depend on release creation and assets")
	require(t, strings.Contains(condition, "!cancelled()") && strings.Contains(condition, "needs.release-please.result == 'success'") && strings.Contains(condition, "needs.build-and-upload.result == 'success'") && strings.Contains(condition, "release_created == 'true'"), "publication must require successful complete release")
	finalRuns := strings.Join(runs(finalize), "\n")
	require(t, strings.Contains(finalRuns, "--draft=false --prerelease=true --latest=false"), "complete draft must be published as prerelease, not latest")
}

func TestGoReleaserArtifactContract(t *testing.T) {
	data, err := os.ReadFile(".goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	build := object(list(cfg["builds"])[0])
	gotOS, gotArch := map[any]bool{}, map[any]bool{}
	for _, v := range list(build["goos"]) {
		gotOS[v] = true
	}
	for _, v := range list(build["goarch"]) {
		gotArch[v] = true
	}
	require(t, gotOS["linux"] && gotOS["darwin"] && len(gotOS) == 2, "release must build Linux and macOS")
	require(t, gotArch["amd64"] && gotArch["arm64"] && len(gotArch) == 2, "release must build both supported architectures")
	ldflags := list(build["ldflags"])
	require(t, len(ldflags) > 0 && strings.Contains(ldflags[0].(string), "main.version={{.Version}}"), "binary version must be stamped from release tag")
	archive := object(list(cfg["archives"])[0])
	require(t, len(list(archive["formats"])) == 1 && list(archive["formats"])[0] == "tar.gz", "release artifacts must be archives")
	require(t, object(cfg["checksum"])["name_template"] == "checksums.txt", "release must publish checksums")
}
