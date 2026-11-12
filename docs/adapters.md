# Adapters, adoption, and orphans

An adapter creates and destroys the resource behind a lease. dibs ships four built-in adapters, `none`, `kind`, `k3d`, and `compose`, each registered explicitly in `internal/cli/adapters.go`. The job reservation uses a fifth, internal `process` path that is not in the registry. This page describes the adapter contract, each built-in adapter, how dibs pins the Docker endpoint for cleanup, and how existing resources enter dibs through `adopt` and are listed through `orphans`.

Source: `internal/adapter/adapter.go` (contract and registry), `internal/adapter/command.go` (external commands), `internal/adapter/docker.go` and `internal/adapter/inventory.go` (Docker context and discovery), `internal/adapter/{none,kind,k3d,compose}/` (built-ins), `internal/lease/inventory.go` (`Adopt`, `Orphans`), and `internal/cli/inventory.go` (the commands). Tests: `internal/adapter/adapter_test.go`, `internal/adapter/compose/inventory_test.go`, `internal/adapter/snapshot_test.go`, `internal/lease/inventory_test.go`, `internal/cli/inventory_test.go`, and `internal/cli/integration_test.go` (build tag `integration`, run with `just integration`). Decisions: [ADR 0002](adr/0002-reclaim-with-proof.md), [ADR 0005](adr/0005-adapter-and-owner-extension-contracts.md).

## Contract

`internal/adapter/adapter.go` defines the interface that every built-in implements:

```go
type Adapter interface {
	Create(context.Context, Target) error
	Destroy(context.Context, Target) error
}
```

A `Target` carries the `ResourceRef` captured at prepare time and the lease `Metadata`: lease ID, machine, owner, expiry, and labels. A `Definition` adds what the core needs around the adapter: `Name`, `CreateAllowed`, `RequiresFile`, and the functions `Prepare`, `Validate`, `Env`, and optionally `Discover` and `Exists`. `NewRegistry` panics on an incomplete or duplicate definition, so a broken adapter fails at startup, not on first use.

The callers in `internal/lease/manager.go` and `internal/lease/lease.go` enforce the contract:

- `Prepare` runs before the lease is saved, and captures the Docker context and endpoint and the private files. See [Docker endpoint pinning](#docker-context-pinning).
- `Create` runs only after the lease is saved. A failed `Create` keeps the lease.
- `Destroy` tolerates a resource that is partly created or already gone.
- `Destroy` returns nil only after it has verified that the resource is gone (verify-gone). `kind`, `k3d`, and `compose` delete the resource and then list what remains. `none` has nothing to verify.
- A `Destroy` error keeps the lease, and a later `release` or `sweep` retries it.
- `Exists` reports whether the pinned resource is still present: `true`, `false` for proven absent, or an error for unknown. Only a late renew of a protected lease calls it. See [Live-owner protection](leases.md#live-owner-protection). `none` always reports present, because it has no resource. `kind`, `k3d`, and `compose` use `DockerExists` in `internal/adapter/inventory.go`. It runs `DiscoverDocker` on the pinned context and, when the lease pins a `DockerHost`, first requires the context to still resolve to that endpoint. An adapter without `Exists` cannot reattach a protected lease after expiry.

`releaseLease` refuses a lease whose adapter is not in the registry and keeps the lease. An older binary therefore never drops a record it cannot clean up.

`adapter.go` notes that the verify-gone check currently lives inside each built-in `Destroy`. A separate `VerifyGone` contract belongs to the planned extension work. See [Planned](#planned).

## Running external tools

`RunCommand` in `internal/adapter/command.go` runs every external tool call for `kind`, `k3d`, and `compose`:

- Each call has a 5-minute timeout.
- Stdin is empty.
- Stderr is captured. On failure, dibs prints it to stderr and returns a generic error that tells the user to check the tools and retry `dibs release` or `dibs sweep --confirm`.

Tests inject a fake `CommandRunner` through each adapter's `New(run)` to check the exact argv and environment. Test: `TestAdapterCommandsAndSurvivors`.

## Docker context pinning

`DockerContext` in `internal/adapter/docker.go` runs before the lease is saved, for every Docker-backed adapter:

1. It refuses to run when `DOCKER_HOST` is set. Users select a named Docker context instead.
2. It records the current context from `docker context show`. An empty context is an error.

The `Prepare` function of each Docker-backed adapter then:

1. Creates `<state>/resources/<id>/` with mode `0700`.
2. Sets the resource name to `dibs-<lease id>`, or `dibs-<first 26 ID characters>` for k3d, which limits cluster names to 32 characters.
3. For k3d only, records the context's endpoint from `docker context inspect` as `docker_host`. kind and Compose record the context name and never an endpoint.
4. For Compose, writes the resolved config snapshot described under [Compose](#compose).

Later commands use the pinned context name, not the caller's current environment. kind and Compose resolve the endpoint from that named context on every call, so if the context is later pointed at another daemon, cleanup targets that daemon. k3d is the only adapter that pins the endpoint itself:

- `DockerEnv` (kind, Compose, and discovery) sets `DOCKER_HOST=` (empty), `DOCKER_CONTEXT=<recorded context name>`, `KIND_EXPERIMENTAL_PROVIDER=docker`, and `COMPOSE_INTERACTIVE_NO_CLI=1`.
- `k3dEnv` sets `DOCKER_CONTEXT=` (empty), `DOCKER_HOST=<pinned endpoint>`, `KIND_EXPERIMENTAL_PROVIDER=docker`, and `KUBECONFIG=<private file>`.

## Built-in adapters

### none

`none.New` does nothing on `Create` and `Destroy`. The lease is metadata only, and dibs never touches an external resource for it. It is the default for `dibs create`.

### kind

- `Create`: `kind create cluster --name <name> --kubeconfig <private file> --wait 120s`.
- `Destroy`: `kind delete cluster`, then `kind get clusters`. If the name is still listed, `Destroy` returns an error.
- The kubeconfig is private to the lease, so dibs never edits the caller's kubeconfig or current context. `dibs env <id>` prints the `KUBECONFIG` export.

Test: `TestIntegrationKind`.

### k3d

- `Create`: `k3d cluster create <name>` with `KUBECONFIG` set to the private file.
- `Destroy`: `k3d cluster delete <name>`, then `k3d cluster list -o json`. If the name is still listed, or the list cannot be parsed, `Destroy` returns an error.

Tests: `TestK3dKeepsCallerKubeconfig`, `TestIntegrationK3d`.

### Compose

`prepare` in `internal/adapter/compose/compose.go` resolves the user's Compose file once with `docker compose config --format json` and rewrites it:

- The config must define at least one service.
- A service with a fixed `container_name` is refused, unless the lease is adopted.
- A volume or network with a fixed `name` other than `<project>_<key>` is refused, unless it is marked `external`.
- Every non-external service, volume, and network gets the labels `io.dibs.lease`, `io.dibs.owner`, `io.dibs.expires`, and `io.dibs.machine`.
- The result is written atomically to `<state>/resources/<id>/compose.json`.

Cleanup reads only this snapshot. It does not need the original file, environment, or worktree. The `io.dibs.expires` label records the expiry at creation. After `extend`, the lease record is authoritative.

- `Create`: `docker --context <ctx> compose -p <name> -f compose.json up -d`.
- `Destroy`: `compose down --volumes --remove-orphans`. Then `docker ps -aq`, `docker volume ls -q`, and `docker network ls -q`, each filtered by `label=com.docker.compose.project=<name>`. Any output is an error.

`down --volumes` deletes data in managed volumes. External volumes and networks are outside cleanup.

Tests: `TestComposeSnapshotAndUnsafeNames`, `TestIntegrationCompose`.

### process

The `process` adapter backs job reservations. It is not in the registry. `processResource` in `internal/lease/job_process.go` handles it inside `releaseLease`. `Create` does nothing. `Destroy` terminates the job's process group after it verifies the leader's identity. If the leader is already gone and reaped, `Destroy` succeeds without signaling, and descendants may survive. [ADR 0002](adr/0002-reclaim-with-proof.md#existing-process-exception) records this exception. See [Jobs](jobs.md#process-group-cleanup).

## Adoption

`dibs adopt <kind|k3d|compose> <name> --owner KIND:ID --ttl D` takes an existing resource under a lease. `Manager.Adopt` in `internal/lease/inventory.go` does the following under the state lock:

1. The owner must be alive, and the TTL must be positive.
2. The adapter must support discovery, the name must be valid, and `--file` must be present exactly when the adapter requires it (Compose).
3. No existing lease may already own the resource. `claimant` compares adapter and name, then compares Docker endpoints: recorded endpoints when both exist, otherwise resolved context endpoints. Context names alone count only when both refs lack endpoints and name the same context. If an endpoint cannot be resolved, adoption fails closed. A pending lease also claims its pinned handle.
4. The resource must exist. Discovery finds it by its project or cluster label. A resource labeled `io.dibs.lease=<id>` for a pending lease belongs to that lease and is refused.
5. The adapter's `Prepare` captures its cleanup scope with `Adopted` set. kind and k3d write their kubeconfig from `kind get kubeconfig` or `k3d kubeconfig get`. Compose verifies adoption.
6. The lease gets `io.dibs.*` labels. State is saved as version 3 when it has an idempotency key or pending resource; otherwise it remains version 2. See [State](state.md#format-version-2).

With `--pending ID`, `bindPending` binds the named resource to an existing pending lease instead of creating a lease. The pending lease supplies the owner, TTL, key, and Docker scope. Binding keeps the lease ID, merges the `io.dibs.*` attribution into the lease labels, and writes the same captured cleanup configuration as adoption. `Manager.Reconcile` finds the resource for a pending lease by its pinned handle or its `io.dibs.lease` label and binds a single unclaimed match. See [Consumer contract](consumer-contract.md#crash-safe-acquire). Tests: `TestPendingPinnedHandleLifecycle`, `TestReconcileByLabel`, `TestIntegrationPendingCrashRecovery`.

Adoption does not recreate the resource and does not change its name. The adopted lease is then an ordinary lease: `dibs release <id> --confirm` runs the adapter's `Destroy` and deletes the resource. Adoption therefore carries the same destructive cleanup as a created lease.

For Compose, `verifyComposeAdoption` in `internal/adapter/compose/inventory.go` refuses an existing non-external volume or network that is not labeled with the project. Mark shared resources `external` before adoption. Tests: `TestAdoptRefusalsAndDurability`, `TestComposeAdoptionRequiresProjectLabels`, `TestIntegrationAdoptAndOrphans`.

## Orphans

`dibs orphans [--context NAME]` lists resources that no lease owns. `Manager.Orphans` runs discovery for every adapter that can create, then drops every resource that `claimant` matches, including resources that a pending lease pinned or labeled.

- Discovery (`DiscoverDocker`) finds containers (`-a`), and for Compose also volumes and networks, by their project or cluster label. It groups them by name and reports labels, creation evidence, and the Compose working directory with its status.
- The lease check reads a snapshot of `state.json` with `readInventoryStore`. That read takes no lock, writes nothing, and does not migrate state. Its answer is point-in-time evidence.
- Each row includes an `adopt` command template. The template uses placeholders for the owner and TTL, so the operator makes both choices.

Unleased does not mean unused. Age never authorizes cleanup. Test: `TestReadOnlyInventoryAndStateMigration`, and `TestInventoryOutputModes` for the output.

## Docker endpoint for legacy and adopted leases

`validate` in each Docker-backed adapter accepts the legacy `berth-<id>` name for existing leases, so leases created before the rename still list and reclaim. For k3d, the legacy name is `berth-` plus the first 26 characters. Adopted leases accept any valid resource name and require a captured `docker_host`. Test: `TestLegacyAndAdoptedSnapshotsThroughStateAlias`. Compose cleanup uses the recorded project, not label discovery. Test: `TestLegacyComposeLabelsCleanedByRecordedProject`.

## Invariants

1. **A record is removed only after verify-gone.** `Destroy` returns nil only after it lists the remaining resources and finds none. A new adapter must do the same. The only exception is the `process` adapter's reaped-leader case in ADR 0002, and a change to it needs its own decision.
2. **`Destroy` tolerates absence.** An already-absent or partly created resource is not a reason to keep failing, so retries converge. Test: `TestSweepPreviewRetryAndIdempotence`.
3. **Cleanup never depends on the caller's environment.** Adapters use the context name (and, for k3d, the endpoint) and private files recorded in the lease, never the current `DOCKER_HOST`, `DOCKER_CONTEXT`, `KUBECONFIG`, or the original Compose file. The recorded context name is not a pinned endpoint: changing what that context points to changes what cleanup targets. Test: `TestK3dKeepsCallerKubeconfig`.
4. **dibs touches only resources it named.** Names derive from the lease ID, or come from an adoption the operator made explicitly. Compose rejects fixed names that cleanup could share with another project. dibs never sweeps unleased resources. Test: `TestComposeSnapshotAndUnsafeNames`.
5. **Every external call is bounded.** `RunCommand` applies a 5-minute timeout.
6. **No secrets in arguments.** Resolved Compose values go to the private `compose.json` file, not to argv.
7. **Each adapter's file stays in its state directory.** `Validate` checks the file with `SnapshotMatches`, so cleanup cannot point at a file outside `<state>/resources/<id>/`.

## Planned

[ADR 0005](adr/0005-adapter-and-owner-extension-contracts.md) plans these changes. None exists in the code today.

- A versioned Adapter contract with `create`, an idempotent `destroy`, `exists` (verify-gone), and `describe`. Today, verify-gone lives inside each `Destroy`.
- A conformance kit, `dibs adapter test <name>`, that checks idempotent destroy, tolerance of already-absent resources, truthful verify-gone, honored timeouts, and applied labels. The command does not exist.
- An `exec` adapter defined in a local file, with create, destroy, and exists commands and a timeout.
- Out-of-process `dibs-adapter-<name>` plugins on `PATH` that speak a JSON protocol over stdin and stdout. Plugins never touch dibs state.
- Optional path or checksum pinning for plugins, and a community catalog.
- Label application is Compose-specific today. The planned contract applies required labels to every managed resource.
- Governance: a built-in adapter must be widely used and testable in CI. Everything else ships as `exec` or a plugin.
