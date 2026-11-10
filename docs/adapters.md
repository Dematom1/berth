# Adapters

An adapter creates and destroys the resource behind a lease. Berth ships five built-in adapters and no plugin mechanism yet. This page describes the adapter contract, each built-in adapter, and how berth pins the Docker endpoint for cleanup.

Source: `adapter.go`, plus `processResource` in `job.go`. Tests: `adapter_test.go`, and `integration_test.go` (build tag `integration`, run with `make integration`). Decisions: [ADR 0002](adr/0002-reclaim-with-proof.md), [ADR 0005](adr/0005-adapter-and-owner-extension-contracts.md).

## Contract

`adapter.go` defines the interface:

```go
type Adapter interface {
	Create(context.Context, Lease) error
	Destroy(context.Context, Lease) error
}
```

The comment on the interface states the contract, and the callers in `cli.go` and `lease.go` enforce it:

- `Create` runs only after the lease is saved. A failed `Create` keeps the lease.
- `Destroy` tolerates a resource that is partly created or already gone.
- `Destroy` returns nil only after it has verified that the resource is gone (verify-gone). The `kind`, `k3d`, and `compose` adapters delete the resource and then list what remains. `none` has nothing to verify. `process` has one documented exception, described under [process](#process).
- A `Destroy` error keeps the lease, and a later `release` or `sweep` retries it.

`adapters()` returns the registry: `none`, `kind`, `k3d`, `compose`, and `process`. `berth create --adapter` accepts `none`, `kind`, `k3d`, and `compose`. Only `berth job` creates `process` leases.

`releaseLease` refuses a lease whose adapter is not in the registry and keeps the lease. An older binary therefore never drops a record it cannot clean up.

## Running external tools

`runCommand` runs every external tool call for `kind`, `k3d`, and `compose`:

- Each call has a 5-minute timeout.
- Stdin is empty.
- Stderr is captured. On failure, berth prints it to stderr and returns a generic error that tells the user to retry `berth release` or `berth sweep --confirm`.

Tests inject a fake `commandRunner` to check the exact argv and environment. Test: `TestAdapterCommandsAndSurvivors`.

## Docker endpoint pinning

`prepareResource` runs before the lease is saved, for every adapter except `none`:

1. It refuses to run when `DOCKER_HOST` is set. Users select a named Docker context instead.
2. It records the current context from `docker context show`. An empty context is an error.
3. It creates `<state>/resources/<id>/` with mode `0700`.
4. It sets the resource name to `berth-<id>`, or `berth-<first 26 ID characters>` for k3d, which limits cluster names to 32 characters.
5. For k3d, it records the context's endpoint from `docker context inspect` as `docker_host`.
6. For Compose, it writes the resolved config snapshot described under [Compose](#compose).

Later commands run with the pinned endpoint, not the caller's current environment:

- `dockerEnv` (kind, Compose) sets `DOCKER_HOST=` and `DOCKER_CONTEXT=<pinned context>`.
- `k3dEnv` sets `DOCKER_CONTEXT=`, `DOCKER_HOST=<pinned endpoint>`, and `KUBECONFIG=<private file>`.

## Built-in adapters

### none

`noResource` does nothing on `Create` and `Destroy`. The lease is metadata only, and berth never touches an external resource for it.

### kind

- `Create`: `kind create cluster --name <name> --kubeconfig <private file> --wait 120s`.
- `Destroy`: `kind delete cluster`, then `kind get clusters`. If the name is still listed, `Destroy` returns an error.
- The kubeconfig is private to the lease, so berth never edits the caller's kubeconfig or current context.

Test: `TestIntegrationKind`.

### k3d

- `Create`: `k3d cluster create <name>` with `KUBECONFIG` set to the private file.
- `Destroy`: `k3d cluster delete <name>`, then `k3d cluster list -o json`. If the name is still listed, or the list cannot be parsed, `Destroy` returns an error.

Tests: `TestK3dKeepsCallerKubeconfig`, `TestIntegrationK3d`.

### Compose

`prepareResource` resolves the user's Compose file once with `docker compose config --format json` and rewrites it:

- The config must define at least one service.
- A service with a fixed `container_name` is refused.
- A volume or network with a fixed `name` other than `<project>_<key>` is refused, unless it is marked `external`.
- Every non-external service, volume, and network gets the labels `io.berth.lease`, `io.berth.owner`, `io.berth.expires`, and `io.berth.machine`.
- The result is written atomically to `<state>/resources/<id>/compose.json`.

Cleanup reads only this snapshot. It does not need the original file, environment, or worktree. The `io.berth.expires` label records the expiry at creation. After `extend`, the lease record is authoritative.

- `Create`: `docker --context <ctx> compose -p <name> -f compose.json up -d`.
- `Destroy`: `compose down --volumes --remove-orphans`. Then `docker ps -aq`, `docker volume ls -q`, and `docker network ls -q`, each filtered by `label=com.docker.compose.project=<name>`. Any output is an error.

`down --volumes` deletes data in managed volumes. External volumes and networks are outside cleanup.

Tests: `TestComposeSnapshotAndUnsafeNames`, `TestIntegrationCompose`.

### process

`processResource` backs job reservations. `Create` does nothing. `Destroy` terminates the job's process group after it verifies the leader's identity. If the leader is already gone and reaped, `Destroy` succeeds without signaling, and descendants may survive. [ADR 0002](adr/0002-reclaim-with-proof.md#existing-process-exception) records this exception. See [Jobs](jobs.md#process-group-cleanup).

## Invariants

1. **A record is removed only after verify-gone.** `Destroy` returns nil only after it lists the remaining resources and finds none. A new adapter must do the same. The only exception is the `process` adapter's reaped-leader case in ADR 0002, and a change to it needs its own decision.
2. **`Destroy` is idempotent.** An already-absent resource is success, so retries converge. Test: `TestSweepPreviewRetryAndIdempotence`.
3. **Cleanup never depends on the caller's environment.** Adapters use the pinned context, endpoint, and private files recorded in the lease, never the current `DOCKER_HOST`, `DOCKER_CONTEXT`, `KUBECONFIG`, or the original Compose file.
4. **Berth touches only resources it named.** Names derive from the lease ID. Compose rejects fixed names that cleanup could share with another project. Berth never sweeps unleased resources.
5. **Every external call is bounded.** `runCommand` applies a 5-minute timeout.
6. **No secrets in arguments.** Resolved Compose values go to the private `compose.json` file, not to argv.

## Planned

The captain-approved scope (2026-11-09) and [ADR 0005](adr/0005-adapter-and-owner-extension-contracts.md) plan these changes. None exists in the code today. ADR 0005 places the contracts in `internal/adapter` and `internal/owner`, which do not exist yet.

- A versioned Adapter contract with `create`, an idempotent `destroy`, `exists` (verify-gone), and `describe`. Today, verify-gone lives inside each `Destroy`.
- A conformance kit, `berth adapter test <name>`, that checks idempotent destroy, tolerance of already-absent resources, truthful verify-gone, honored timeouts, and applied labels.
- An `exec` adapter defined in a local file, such as `berth create --adapter exec --file thing.berth.toml`, with create, destroy, and exists commands and a timeout.
- Out-of-process `berth-adapter-<name>` plugins on `PATH` that speak a JSON protocol over stdin and stdout. Plugins never touch berth state.
- Optional path or checksum pinning for plugins, and a community catalog.
- Governance: a built-in adapter must be widely used and testable in CI (kind, k3d, Compose, and Docker qualify). Everything else ships as `exec` or a plugin.
