# dibs component docs

These pages describe how each part of dibs works and the invariants that every change must keep. They are written for contributors and code reviewers. For installation and everyday use, read the [project README](../README.md).

Related records:

- [`GLOSSARY.md`](../GLOSSARY.md) defines the domain terms these pages use, such as lease ledger, verify-gone, waiter, reservation, and owner room.
- [`docs/adr/`](adr/) holds the architecture decisions. Each component page links the decisions it depends on.
- [`VISION.md`](../VISION.md) and [`AGENTS.md`](../AGENTS.md) set the scope and the rules for changes.
- [`verification/findings.md`](../verification/findings.md) lists what the formal models prove and the known limitations they found.
- [`docs/releases/rename-to-dibs.md`](releases/rename-to-dibs.md) records the berth to dibs rename and its compatibility window.

All production code lives under `internal/`. `main.go` holds only the entry point and version string. [`architecture_test.go`](../architecture_test.go) (`TestPackageGraph`) fails when a package is unreviewed or imports an edge it does not allow, so the layering on this page is enforced, not just described. Each page names the source files and tests it covers and cites functions by name rather than by line number.

| Component | Doc | Source | Tests |
| --- | --- | --- | --- |
| Leases, owner liveness, reclaim | [leases.md](leases.md) | `internal/lease/lease.go`, `internal/lease/manager.go`, `internal/owner/reference.go`, `internal/owner/registry.go` | `internal/lease/lease_test.go`, `internal/owner/reference_test.go`, `internal/cli/cli_test.go`, `internal/lease/formal_test.go` |
| State store and format | [state.md](state.md) | `internal/lease/store.go`, `internal/lease/machine.go`, `internal/atomicfile/write.go` | `internal/lease/lease_test.go`, `internal/lease/machine_migration_test.go`, `internal/lease/compat_test.go`, `internal/atomicfile/write_test.go` |
| Adapters, adoption, orphans | [adapters.md](adapters.md) | `internal/adapter/`, `internal/adapter/{none,kind,k3d,compose}/`, `internal/lease/inventory.go` | `internal/adapter/adapter_test.go`, `internal/lease/inventory_test.go`, `internal/cli/integration_test.go` |
| Jobs, admission, budgets, deadlines | [jobs.md](jobs.md) | `internal/job/command.go`, `internal/lease/job_*.go`, `internal/lease/job_wait_{darwin,linux}.go` | `internal/lease/job_test.go`, `internal/lease/formal_test.go` |
| Remote jobs over SSH | [remote.md](remote.md) | `internal/remote/`, `internal/cli/agent.go`, `internal/cli/machine.go` | `internal/remote/ssh_test.go`, `internal/remote/protocol_test.go`, `internal/remote/ssh_integration_test.go`, `internal/cli/ssh_test.go` |
| Output modes | [output.md](output.md) | `internal/output/` | `internal/output/output_test.go`, `internal/cli/output_modes_test.go`, `internal/cli/baseline_test.go` |
| Agent hooks | [hooks.md](hooks.md) | `internal/cli/hooks.go` | `internal/cli/hooks_test.go`, `internal/cli/hooks_compat_test.go` |
| Configuration and budget | [config.md](config.md) | `internal/config/config.go`, `internal/budget/amount.go` | `internal/config/config_test.go`, `internal/config/compat_test.go` |
| CLI, exit codes, legacy names | [cli.md](cli.md) | `main.go`, `internal/cli/cli.go`, `internal/cli/help.go`, `internal/compat/env.go` | `internal/cli/cli_test.go`, `compat_test.go`, `main_test.go` |

## Rules that span components

- **Never reclaim a live, unexpired owner.** Reclaim requires a dead owner or an expired lease. `unknown` liveness is never dead. See [Leases](leases.md#reclaim-rule).
- **A record is removed only after verify-gone.** Every `Destroy` verifies that the resource is gone before dibs deletes the lease, and a failure keeps the lease for retry. The process group has one documented exception. See [Adapters](adapters.md#contract) and [Jobs](jobs.md#process-group-cleanup).
- **Cleanup intent is durable first.** dibs saves the lease before it creates the resource. See [Leases](leases.md#create).
- **A host acts only on its own leases.** The machine is a stable OS identity, not a hostname. See [State](state.md#machine-identity).
- **AXI output is byte-stable, and JSON is complete.** See [Output modes](output.md#invariants).
- **Exit codes 124, 75, and 76 are a contract.** See [CLI](cli.md#exit-codes).

## Intentional berth compatibility

These are the only places non-test code still reads or writes the berth name. Every one exists so that a rename does not strand existing state, config, or agent sessions. Remove an item only when its alias window in the [rename record](releases/rename-to-dibs.md) has ended.

| Compatibility point | Where |
| --- | --- |
| `BERTH_*` environment aliases for `DIBS_*`, with a one-time stderr warning | `internal/compat/env.go` (`Env`, `WarnLegacyEnv`) |
| Config fallback to `$XDG_CONFIG_HOME/berth/config.json` when the dibs file is absent | `internal/config/config.go` (`Load`) |
| State directory alias `berth` to `dibs`, and the error when both exist | `internal/lease/store.go` (`migrateStateDir`) |
| `berth-` resource names still accepted for existing leases | `internal/adapter/{kind,k3d,compose}/` (`validate`) |
| `BERTH_LEASE` exported next to `DIBS_LEASE` to job children | `internal/lease/job_start.go` (`startJob`) |
| Legacy `# berth-axi` hook commands and `berth-axi` plugin files removed or rewritten | `internal/cli/hooks.go` |
| Legacy hostnames in lease records, and `DIBS_LEGACY_MACHINE` as the explicit attestation | `internal/lease/machine.go` (`migrateMachines`) |

## Keep these docs true

A change that alters behavior described here updates the matching page in the same pull request. Each invariant cites the code or test that enforces it. Behavior that is not built yet is listed under a **Planned** heading.
