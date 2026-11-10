# Contribute to berth

Install Go 1.26 or later and [just](https://just.systems), or run `nix develop`
for Go, just, and GoReleaser. Run `just` to list recipes.

Run `just fmt` to format Go files, then `just check` for formatting checks,
vet, race-enabled tests, and a build. Run `just deps-check` after dependency
changes. If `go.mod` or `go.sum` changes, rebuild with `nix build` and update
`package.nix`'s `vendorHash` to the hash Nix reports.

Run `just integration` and `just ssh-integration` with Docker and the tools
listed in [README.md](README.md#develop-and-verify). Missing prerequisites
fail these recipes rather than skip them.

Use `just release-check` to validate release configuration and
`just release-snapshot` to build archives without publishing. Release-please
owns `CHANGELOG.md` and `.release-please-manifest.json` after initial adoption.
Do not hand-edit generated release files.

Use conventional commit titles and raise pull requests through no-mistakes.
Installing berth with `go install github.com/Dematom1/berth@latest` requires
neither just nor Nix.
