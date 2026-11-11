package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	remotepkg "github.com/Dematom1/berth/internal/remote"
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
	var releasePleaseAction, releaseConfig, appToken bool
	for _, raw := range list(please["steps"]) {
		step := object(raw)
		uses, _ := step["uses"].(string)
		releasePleaseAction = releasePleaseAction || strings.Contains(uses, "release-please-action")
		with := object(step["with"])
		if strings.HasPrefix(uses, "actions/create-github-app-token@") {
			appToken = step["id"] == "app" && with["app-id"] == "${{ secrets.RELEASE_APP_ID }}" && with["private-key"] == "${{ secrets.RELEASE_APP_PRIVATE_KEY }}" && with["repositories"] == "${{ github.event.repository.name }}"
		}
		if strings.Contains(uses, "release-please-action") {
			releaseConfig = with["config-file"] == "release-please-config.json" && with["manifest-file"] == ".release-please-manifest.json" && with["token"] == "${{ steps.app.outputs.token }}"
		}
	}
	require(t, releasePleaseAction && releaseConfig && appToken, "release creation must preserve App-authored PR identity and use the config and manifest")
	require(t, build["needs"] == "release-please", "asset build must depend on release creation")
	require(t, build["if"] == "needs.release-please.outputs.release_created == 'true'", "assets may build only for a created release")
	var taggedCheckout, uncachedGo bool
	for _, raw := range list(build["steps"]) {
		step := object(raw)
		if step["uses"] == "actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16" {
			uncachedGo = object(step["with"])["cache"] == false
		}
		if with := object(step["with"]); with["ref"] == "${{ needs.release-please.outputs.tag_name }}" {
			taggedCheckout = true
		}
	}
	require(t, taggedCheckout, "build must check out the release tag")
	require(t, uncachedGo, "release artifacts must not consume a shared Go cache")
	releaseScript := ""
	for _, raw := range list(build["steps"]) {
		step := object(raw)
		if step["name"] == "Upload complete asset set to draft" {
			releaseScript, _ = step["run"].(string)
		}
	}
	require(t, releaseScript != "", "release upload step must have an executable script")
	testReleaseUploadScript(t, releaseScript)
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
	require(t, condition == "!cancelled() && needs.release-please.result == 'success' && needs.build-and-upload.result == 'success' && needs.release-please.outputs.release_created == 'true'", "publication must require successful complete release")
	require(t, hasRun(finalize, `gh release edit "$TAG" --draft=false --prerelease=false --latest=true`), "complete draft must be published as latest")
}

func testReleaseUploadScript(t *testing.T, script string) {
	t.Helper()
	for _, tc := range []struct {
		name  string
		files int
		valid bool
	}{
		{"complete assets upload", 4, true},
		{"missing archive blocks upload", 3, false},
		{"corrupt checksum blocks upload", 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin, dist := filepath.Join(dir, "bin"), filepath.Join(dir, "dist")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dist, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := exec.LookPath("sha256sum"); err != nil {
				shasum, err := exec.LookPath("shasum")
				if err != nil {
					t.Fatal("release script test requires sha256sum or shasum")
				}
				shim := "#!/bin/sh\nexec " + remotepkg.ShellQuote(shasum) + " -a 256 \"$@\"\n"
				if err := os.WriteFile(filepath.Join(bin, "sha256sum"), []byte(shim), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			ghLog := filepath.Join(dir, "uploads")
			if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$UPLOAD_LOG\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			var sums strings.Builder
			for i := 0; i < tc.files; i++ {
				name := fmt.Sprintf("asset-%d.tar.gz", i)
				if err := os.WriteFile(filepath.Join(dist, name), []byte(name), 0o644); err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256([]byte(name)), name)
			}
			if err := os.WriteFile(filepath.Join(dist, "checksums.txt"), []byte(sums.String()), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.name == "corrupt checksum blocks upload" {
				if err := os.WriteFile(filepath.Join(dist, "checksums.txt"), []byte(strings.Repeat("0", 64)+"  asset-0.tar.gz\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("bash", "-e", "-c", script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "UPLOAD_LOG="+ghLog, "TAG=v1.2.3", "GH_REPO=example/berth", "GH_TOKEN=test")
			err := cmd.Run()
			if (err == nil) != tc.valid {
				t.Fatalf("script error = %v, want success %v", err, tc.valid)
			}
			_, statErr := os.Stat(ghLog)
			if (statErr == nil) != tc.valid {
				t.Fatalf("upload occurred = %v, want %v", statErr == nil, tc.valid)
			}
			if tc.valid {
				got, err := os.ReadFile(ghLog)
				if err != nil {
					t.Fatal(err)
				}
				want := "release\nupload\nv1.2.3\ndist/asset-0.tar.gz\ndist/asset-1.tar.gz\ndist/asset-2.tar.gz\ndist/asset-3.tar.gz\ndist/checksums.txt\n--clobber\n"
				if string(got) != want {
					t.Fatalf("gh upload arguments = %q, want %q", got, want)
				}
			}
		})
	}
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
