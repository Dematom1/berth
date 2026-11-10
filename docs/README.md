# berth component docs

These pages describe how each part of berth works and the invariants that every change must keep. They are written for contributors and code reviewers. For installation and everyday use, read the [project README](../README.md).

All Go source is in `package main` at the repository root. Each doc names the source files and tests it covers, and cites functions by name rather than by line number.

| Component | Doc | Source | Tests |
| --- | --- | --- | --- |
| Leases, owner liveness, reclaim | [leases.md](leases.md) | `lease.go`, `owner.go` | `lease_test.go`, `cli_test.go` |
| State store and format | [state.md](state.md) | `store.go` | `lease_test.go`, `ssh_test.go` |
| Adapters | [adapters.md](adapters.md) | `adapter.go` | `adapter_test.go`, `integration_test.go` |
| Jobs, admission, budgets, deadlines | [jobs.md](jobs.md) | `job.go`, `job_wait_darwin.go`, `job_wait_linux.go` | `job_test.go` |
| Remote jobs over SSH | [remote.md](remote.md) | `ssh.go` | `ssh_test.go`, `ssh_integration_test.go` |
| Output formats | [output.md](output.md) | `output.go` | `output_test.go`, `cli_test.go` |
| Configuration | [config.md](config.md) | `config.go` | `job_test.go`, `ssh_test.go` |
| CLI and exit codes | [cli.md](cli.md) | `main.go`, `cli.go` | `cli_test.go` |

## Rules that span components

- **Never reclaim a live, unexpired owner.** Reclaim requires a dead owner or an expired lease. `unknown` liveness is never dead. See [Leases](leases.md#reclaim-rule).
- **A record is removed only after verify-gone.** Every `Destroy` verifies that the resource is gone before berth deletes the lease. A failure keeps the lease for retry. See [Adapters](adapters.md#contract).
- **Cleanup intent is durable first.** Berth saves the lease before it creates the resource. See [Leases](leases.md#create).
- **A host acts only on its own leases.** See [Leases](leases.md#reclaim-rule).
- **AXI output is byte-stable.** See [Output formats](output.md#invariants).
- **Exit codes 124, 75, and 76 are a contract.** See [CLI](cli.md#exit-codes).

## Keep these docs true

A change that alters behavior described here updates the matching doc in the same pull request. Each invariant cites the code or test that enforces it. Behavior that is not built yet is listed under a **Planned** heading.
