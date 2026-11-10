package main

import "testing"

func TestNixWorkflowMatrixAndBuildChecks(t *testing.T) {
	job := object(object(workflow(t, "nix")["jobs"])["nix"])
	matrix := object(object(object(job["strategy"])["matrix"]))
	gotOS := map[any]bool{}
	for _, os := range list(matrix["os"]) {
		gotOS[os] = true
	}
	require(t, len(gotOS) == 2 && gotOS["ubuntu-24.04"] && gotOS["macos-15"], "Nix checks must cover Ubuntu and macOS")
	var installer, checkoutFull bool
	for _, raw := range list(job["steps"]) {
		step := object(raw)
		uses, _ := step["uses"].(string)
		installer = installer || uses == "DeterminateSystems/nix-installer-action@3138316df39ed29be04236d7ffc686fa525866aa"
		checkoutFull = checkoutFull || object(step["with"])["fetch-depth"] == 0
	}
	require(t, installer && checkoutFull, "Nix build requires installer and complete history for flake-compat")
	for _, command := range []string{"nix flake check --all-systems --no-build", "nix build --print-build-logs", "nix-build default.nix -A \"packages.$(nix eval --impure --raw --expr builtins.currentSystem).default\" --no-out-link"} {
		require(t, hasRun(job, command), "missing Nix verification command: "+command)
	}
	require(t, hasRun(job, `test "$(./result/bin/dibs --version)" = "$(jq -r '.["."]' .release-please-manifest.json)"`), "built version must equal the release manifest")
}
