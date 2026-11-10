package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
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

func TestReleaseWorkflowPublishesOnlyCompletePrereleases(t *testing.T) {
	text := workflowText(t, "release")
	requireWorkflowText(t, text, "push:\n    branches: [main]", "group: release-${{ github.ref }}", "cancel-in-progress: false")
	if strings.Contains(text, "tags:") || strings.Contains(text, "pull_request:") {
		t.Fatal("release-please owns releases on main, not tags or PR code")
	}
	please := workflowJob(t, "release", "release-please")
	requireWorkflowText(t, please, "googleapis/release-please-action@", "config-file: release-please-config.json",
		"manifest-file: .release-please-manifest.json", "release_created:", "tag_name:")
	build := workflowJob(t, "release", "build-and-upload")
	requireWorkflowText(t, build, "needs: release-please", "if: needs.release-please.outputs.release_created == 'true'",
		"ref: ${{ needs.release-please.outputs.tag_name }}", "release --clean --skip=publish",
		`test "${#archives[@]}" -eq 4`, "sha256sum --check checksums.txt", "gh release upload",
		"dist/checksums.txt --clobber", "macOS Developer ID signing and verification (disabled)", "if: ${{ runner.os == 'macOS' && false }}")
	if strings.Index(build, "sha256sum --check") > strings.Index(build, "gh release upload") {
		t.Fatal("verify checksums before uploading")
	}
	finalize := workflowJob(t, "release", "finalize")
	requireWorkflowText(t, finalize, "needs: [release-please, build-and-upload]", "!cancelled() &&",
		"needs.release-please.result == 'success' &&", "needs.build-and-upload.result == 'success' &&",
		"needs.release-please.outputs.release_created == 'true'", "--draft=false --prerelease=true --latest=false")
	data, err := os.ReadFile(".goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	requireWorkflowText(t, string(data), "goos: [linux, darwin]", "goarch: [amd64, arm64]",
		"-X main.version={{.Version}}", "formats: [tar.gz]", "name_template: checksums.txt")
}
