package main

import (
	"os"
	"testing"
)

func TestNixWorkflowAndPackage(t *testing.T) {
	job := workflowJob(t, "nix", "nix")
	requireWorkflowText(t, job, "os: [ubuntu-24.04, macos-15]", "fetch-depth: 0",
		"DeterminateSystems/nix-installer-action@", "nix flake check --all-systems --no-build",
		"nix build --print-build-logs", "./result/bin/berth --version", ".release-please-manifest.json",
		"nix-build default.nix -A")
	for name, wants := range map[string][]string{
		"flake.nix":   {"aarch64-darwin", "aarch64-linux", "x86_64-linux", "./package.nix", ".release-please-manifest.json", "devShells", "pkgs.just"},
		"package.nix": {"buildGoModule", "-X main.version=${version}", "vendorHash = \"sha256-", `subPackages = [ "." ];`},
		"default.nix": {"./flake.lock", "flake-compat.locked.rev", "flake-compat.locked.narHash", "defaultNix"},
	} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		requireWorkflowText(t, string(data), wants...)
	}
}
