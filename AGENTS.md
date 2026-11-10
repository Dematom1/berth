# AGENTS.md

Read `VISION.md` first; it is the accept/resist test for every change.
`README.md` is the user contract: commands, exit codes, config, and safety rules.

## Rules for changes

- Dibs owns only the lease lifecycle and its guarantees; task-runner, repo-agent-runtime, secrets, and network-policy features are out of scope.
- New resource types and liveness sources go through the Adapter and Owner contracts; dibs ships one as a built-in, off until an opt-in setting enables it, only when it is widely used and CI-testable and was proposed as an issue first; anything dibs does not ship stays an exec file or plugin.
- Every adapter or owner must pass the conformance kit; every call runs under a timeout, and extensions never write dibs state.
- Record cleanup intent before creating a resource, and forget a lease only after destroy and verify-gone succeed.
- Never reclaim a resource that is not under a lease (adoption is an explicit `adopt`), never reclaim on unknown liveness alone (expiry still reclaims), and never act on another machine's leases.
- New behavior is opt-in: off until a named setting in the user `config.json` or a `DIBS_*` environment variable enables it (never a project file a leased job could edit), and unchanged when the setting is absent; a change to existing behavior needs manual maintainer review, while opt-in, non-breaking changes may merge once the automated review approves.
- Keep dibs one Go binary whose guarantees never require a resident process; the SSH agent lives only for its session.
- Treat the state schema, CLI, AXI output, and exit codes as stable contracts: version and migrate state, follow semver, and deprecate before removing.
- Every command serves humans and agents as first-class audiences: a readable default at a terminal, AXI output for agents (detected when stdout is not a terminal, or via `--axi`), and `--json` for scripts, with no prompts.
- Update the README in the same change as any user-visible behavior.

## Verify

- `make check` runs gofmt, vet, race tests, and build.
- `make integration` and `make ssh-integration` drive real kind, k3d, Compose, and SSH; they need Docker and fail rather than skip when a tool is missing.
  Install the missing tool or report the blocker; never skip a test.
- Changes reach `main` only through a PR raised via no-mistakes with a conventional PR title.
