# Contributing to berth

## Agree on the change first

Open a [bug report or feature request](https://github.com/Dematom1/berth/issues/new/choose) before a substantial fix or feature. Search existing issues and pull requests first. Include the version, reproduction or requested outcome, scope, and sanitized evidence.

Bug reports are welcome without prior approval. Before implementing a substantial change, wait for the maintainer to add `ready-for-pr` with a note on accepted scope and validation. Green checks do not oblige the maintainer to accept unagreed scope. Tiny documentation fixes with no behavior change can go straight to a pull request.

Report vulnerabilities privately as described in [SECURITY.md](SECURITY.md). Never include secrets, SSH keys, kubeconfigs, live credentials, or unredacted production output in public reports or test fixtures.

## Classify the contract effect

State the effect on the existing project contract in the issue and pull request:

- `restore` returns behavior or guarantees that regressed.
- `opt-in` adds behavior without changing existing defaults or guarantees.
- `new-default` changes default behavior or the existing contract. It requires an explicit maintainer decision before merge.
- `cannot-tell` means the evidence is insufficient. It does not authorize a merge.

## Raise the pull request through no-mistakes

Human-authored pull requests, including the maintainer's, must use [no-mistakes](https://github.com/kunchenguid/no-mistakes). Install its current release.

1. Fork berth and clone the parent repository, so `origin` points to `git@github.com:Dematom1/berth.git`.
2. Create a feature branch and make the change.
3. Run `no-mistakes init --fork-url git@github.com:<you>/berth.git`. If you have push access to the parent, use `no-mistakes init` instead.
4. Commit your change.
5. Run `no-mistakes axi run --intent "<the goal of your change>"` and respond to its gates. The pipeline validates the commits, pushes the branch, and opens the pull request. Do not push directly to `main`.

Use a conventional commit PR title, such as `fix: preserve lease ownership` or `docs: clarify job limits`. The shared title check accepts `feat`, `fix`, `chore`, `ci`, `docs`, `refactor`, `test`, `perf`, `build`, and `revert`, with an optional scope and breaking-change marker.

Keep the generated Pipeline, Intent, and evidence sections intact when you edit the PR body. Add the accepted issue or tiny-documentation exception, contract effect, commands and results, and anything you could not test.

The no-mistakes check verifies an attestation bound to the current head, including review, test, and document completion. Only verified release App PRs and Dependabot are exempt under the shared policy. Renovate is not exempt. Its updates need no-mistakes validation before merge, and automatic dependency merging is disabled until shared Go dependency verification is available.

## Run local checks

Use the Go version declared in `go.mod`:

```sh
make check
```

This checks formatting, runs `go vet` and race-enabled tests, and builds the CLI. CI also checks the GoReleaser config and cross-builds Linux amd64 and macOS arm64 binaries.

Run the real integrations when your environment has their prerequisites:

```sh
make ssh-integration
make integration
```

The SSH integration requires Docker, OpenSSH tools, and Go. The adapter integration requires Docker, kind, k3d, and Docker Compose. These tests create and remove real disposable resources. Missing prerequisites fail rather than skip. Never substitute production resources or credentials. Unit tests use temporary state and test helpers; retain that isolation when adding tests.

## Review and CI

The shared contribution checks read live PR metadata without executing contributed code. They check the title, no-mistakes attestation, release PR identity, and generated files. Do not hand-edit `CHANGELOG.md` or `.release-please-manifest.json`; release-please owns them after bootstrap.

The existing `main-required-ci` job runs the Go checks, cross-builds, SSH integration, and container-adapter integration. Fork workflow runs may require GitHub's maintainer approval. There is no separate `full-ci` label gate in berth.

CodeRabbit reviews are advisory. The maintainer decides which findings need action. Address the completed review for the current head together, check affected callers, rerun relevant tests, and send the repair through no-mistakes. If you disagree, explain why with evidence.

## Leave the merge to the maintainer

A contributor does not have standing authority to merge, close issues, or authorize CI. Merge requires accepted scope, green required checks on the current head, resolved review conversations, and an explicit decision for a `new-default` change.

Releases are separate. release-please uses conventional commits on `main` to maintain a Go release PR. A weekly workflow can merge only the verified release App's green release PR, subject to repository policy. The App-created `v*` tag triggers the existing GoReleaser workflow, which publishes the archives and checksums. No npm publishing is involved.

## License

Apache-2.0 is the project's selected license. Submit only work you have the right to contribute under that license.
