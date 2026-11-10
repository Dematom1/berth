# Add a resource adapter

Start with an issue and agree on scope and validation under [CONTRIBUTING.md](CONTRIBUTING.md). New built-ins must meet the [vision](VISION.md). This guide describes the current internal interface, not a public extension API.

1. Add `internal/adapter/<name>` with a private implementation and a `New` constructor that returns `adapter.Definition`. Use [kind](internal/adapter/kind/kind.go) as an example.
2. Implement `Create` and `Destroy` against `adapter.Target`. `Create` runs only after the lease is durable. `Destroy` must tolerate partial creation and already-absent resources. Keep absence verification inside `Destroy`; an error retains the cleanup record.
3. Bundle preparation, stored-reference validation, file policy, and environment projection in the definition. Preparation captures cleanup scope and private snapshots before creation. Validation must work without the source file or an engine call. Do not read or write the lease ledger.
4. Add one ordinary import and constructor entry in [`cli.newAdapters`](internal/cli/adapters.go). Do not use `init`, blank imports, or another operational allowlist. The CLI derives create choices from the registry.
5. Retain exact command, environment, snapshot, survivor, and tool-failure tests. Add real isolated integration coverage that fails when prerequisites are missing. Run `just check`, `just integration`, `just ssh-integration`, and the published verification flows.

The lifecycle owner is [`internal/lease`](internal/lease). It owns locking, durable state, admission, start and finish, and record removal. The job facade never writes state. Process reservations are private lifecycle records, not external adapters.

Owner integrations have a separate registry in [`cli.newOwners`](internal/cli/owners.go). References, raw observations, and pinned-identity decisions live in [`internal/owner`](internal/owner).

Versioned extension contracts, a separate `VerifyGone` coordinator, public conformance commands, exec adapters, and plugins are later work. The current kind and k3d adapters do not promise Compose's attribution labels. Do not claim stronger guarantees from a package move.
