# CLI

`dibs` is one binary with no daemon. `main.go` sets up signal handling and the version string, then calls `Run` in `internal/cli/cli.go`. `Run` parses arguments, checks usage before it touches state, and dispatches each command. This page describes the dispatch rules, the commands, the legacy names, and the exit-code contract.

Source: `main.go` (entry point, version), `internal/cli/cli.go` (dispatch and the lease commands), `internal/cli/help.go` (help text and `exitCodes`), `internal/cli/flags.go` (flag parsing), `internal/cli/adapters.go` and `internal/cli/owners.go` (registration), `internal/compat/env.go` (legacy names), and `architecture_test.go` (package layering). Tests: `internal/cli/cli_test.go`, `internal/cli/output_modes_test.go`, `main_test.go`, `compat_test.go`, and `architecture_test.go`. Decision: [ADR 0007](adr/0007-output-modes-and-job-results.md).

## Entry point

`main` in `main.go` handles two cases:

- `dibs __job_exec <argv...>` is the internal job helper. It calls `leasepkg.Exec` and never reaches `Run`. See [Jobs](jobs.md#start).
- Everything else first warns about legacy `BERTH_*` names, then calls `Run` with a context that `SIGINT` and `SIGTERM` cancel.

`resolveVersion` in `main.go` sets `version` at startup:

1. A value set at link time with `-ldflags "-X main.version=..."`.
2. Otherwise, the module version from the Go build information, without its leading `v`.
3. Otherwise, `devel`, plus `-<first 12 characters of the VCS revision>` when known, plus `-dirty` when the tree was modified.

The SSH handshake compares this string exactly. See [Remote jobs over SSH](remote.md#protocol). Test: `TestResolveVersion`.

## Dispatch

`runWithOutput` in `internal/cli/cli.go` resolves the command in this order:

1. If the only argument is `--version`, `-v`, or `-V`, it prints `version` and exits 0.
2. It consumes any leading `--axi` and `--json` flags. Both together exit 2. See [Output modes](output.md#mode-selection).
3. If the next argument does not start with `-`, that argument is the command. With no command, the command is `list` in home view, which adds `bin` and `description` to the result.
4. It maps the aliases: `up` to `create`; `ls`, `ps`, and `who` to `list`; and `down` to `release`.
5. `help` and `help <command>` print help.
6. An unknown command exits 2.
7. `agent`, `setup`, `machine`, `job`, `adopt`, and `orphans` have their own parsers: `runAgent`, `runSetup`, `runMachine`, `runJob`, and `runInventory`.
8. The lease commands (`create`, `list`, `extend`, `release`, `sweep`, and `env`) share one `flag.FlagSet`. It registers only the flags that the command accepts.

`parseFlags` in `internal/cli/flags.go` moves positional arguments after the flags, so `extend <id> --ttl 1h` and `extend --ttl 1h <id>` mean the same thing. `job` does not use it: everything after the first `--` belongs to the child.

## Commands

| Command | Flags | Positional |
| --- | --- | --- |
| `create`, `up` | `--owner`, `--ttl` (default `8h`), `--stale` (default `15m`), `--resources`, `--label key=value` (repeatable), `--adapter none\|kind\|k3d\|compose` (default `none`), `--file` (Compose only, required for Compose), `--protect-live-owner` (needs the `live_owner_protection` opt-in, otherwise exit 2), `--quiet`, `-q`, `--key`, `--pending`, `--name` (with `--pending`; these three need the `consumer_contract` opt-in, otherwise exit 2) | none |
| `list`, `ls`, `ps`, `who` | `--owner`, `--machine` | none |
| `extend` | `--ttl` (required, positive) | lease ID |
| `release`, `down` | `--confirm` | lease ID |
| `sweep` | `--confirm`, `--machine` | none |
| `env` | none | lease ID |
| `job` | `--cores`, `--memory`, `--class` (default `default`), `--timeout`, `--wait` (default `30m`), `--machine` | `-- <cmd...>` |
| `adopt` | `--owner` (required), `--ttl` (required, positive), `--stale` (default `15m`), `--file` (Compose only), `--context`, `--protect-live-owner` (needs the `live_owner_protection` opt-in, otherwise exit 2), `--key`; or `--pending ID` alone, with optional `--file` (`--key` and `--pending` need the `consumer_contract` opt-in) | `<kind\|k3d\|compose> <name>` |
| `reconcile` | `--confirm` (needs the `consumer_contract` opt-in) | optional lease ID |
| `reclaim` | `--confirm` (needs the `consumer_contract` opt-in) | lease ID |
| `explain` | none (needs the `consumer_contract` opt-in) | lease ID |
| `orphans` | `--context` | none |
| `machine install` | `--file` (required) | machine name |
| `setup hooks` | `--remove` | none |
| `agent` | none | none (internal, see [Remote jobs over SSH](remote.md#agent-side)) |

Every command accepts `--help`. Every command except `agent` accepts `--axi` and `--json`. The lease commands also accept `--full`, which turns off AXI truncation. `list --owner` compares canonical owner strings: it normalizes PID spellings and makes heartbeat paths absolute, without checking liveness. Test: `TestCLIOwnerFilterUsesCanonicalIdentity`.

`dibs env <id>` prints `export KUBECONFIG=<quoted path>` for a kind or k3d lease. It exits 2 when the lease does not exist or has no kubeconfig. Test: `TestQuietAndKubeconfig`.

Behavior lives in the component docs:

- `create`, `list`, `extend`, `release`, and `sweep`: [Leases](leases.md).
- `adopt`, `orphans`, and the adapter flags: [Adapters](adapters.md).
- `--pending`, `--key`, `reconcile`, `reclaim`, `explain`, and the contract form of `extend` and `release`: [Consumer contract](consumer-contract.md).
- `job`: [Jobs](jobs.md).
- `agent`, `machine install`, and `--machine`: [Remote jobs over SSH](remote.md).
- `setup hooks`: [Agent hooks](hooks.md).

## Usage checks come first

`runWithOutput` rejects bad usage with exit 2 before it opens the state store or runs any resource tool. Bad usage includes:

- an unknown flag (including `--human`) or a missing flag value,
- `--axi` together with `--json`,
- a wrong number of positional arguments,
- a lease ID that is not 32 lowercase hex characters,
- a non-positive `--ttl`,
- an unknown adapter, or
- `--file` without `--adapter compose`, or `--adapter compose` without `--file`.

`adopt` and `orphans` apply the same rule to their own arguments before state access. Tests: `TestCLIRejectsUsageBeforeStateAccess`, `TestConflictingOutputFlags`, `TestInventoryUsageBeforeSideEffects`.

## Help text

`commandHelp` in `internal/cli/help.go` holds the help for each command. `help` adds three things:

- The `cadence` constant, for `create` and `extend`.
- Lines about output modes, for every command except `agent`.
- The `exitCodes` constant, for every command.

`emitHelp` prints the text as-is in human mode. In AXI and JSON it emits `usage` and `reference` fields. `TestCLIHomeHelpVersionAndTOON` asserts that the help contains the exit-code text, and that the `extend` help states the renewal cadence.

## Legacy names

The CLI accepts the berth names that existing installs use. The complete list is in [the index](README.md#intentional-berth-compatibility). The user-facing rules are:

- `DIBS_*` environment variables replace `BERTH_*`. A set `DIBS_*` variable wins, even when empty. Legacy names warn once on stderr, and never change stdout. Test: `TestRenamedEnvPrecedenceAndWarning`, `TestLegacyEnvCLIStreams`.
- Existing state directories, config files, and hook installations keep working. Test: `TestRenamedStateSelection`, `TestSetupMigratesLegacyHooks`.
- Existing lease records with `berth-` names still list and reclaim.

## Exit codes

The `exitCodes` constant in `internal/cli/help.go` is the contract. The README repeats it as the AXI exit-code table, and [ADR 0007](adr/0007-output-modes-and-job-results.md) records why.

| Code | Meaning |
| --- | --- |
| `0` | Success, including no-ops such as releasing an absent lease or an empty sweep |
| `1` | A dibs operation failed. Resource cleanup failures keep their lease. |
| `2` | Invalid command, argument, flag, or config. No child started. |
| Child's code | `dibs job`: the child ran to completion |
| `124` | `dibs job`: the deadline fired before the child was confirmed exited, or completion could not be verified at the deadline (the coreutils `timeout` convention) |
| `75` | `dibs job`: no slot freed within `--wait` (`EX_TEMPFAIL`). No child started. |
| `3` | `dibs extend` refused a lease with live-owner protection. `reason` is `owner_dead`, `owner_unknown`, `resource_absent`, or `resource_unknown`, and the lease is unchanged. The `extend` help states this. Unprotected leases keep exit 1. With the consumer contract on (`DIBS_CONSUMER_CONTRACT=1` or `"consumer_contract": true`), every refusal by `reconcile`, `reclaim`, `explain`, `extend`, `release`, or a `--pending`/`--key` invocation exits 3 with a stable `reason`, and nothing changes. |
| `76` | SSH transport or protocol failure after SSH started. The remote result is unknown. A remote child that exits 76 also returns 76, so the code alone does not prove a transport failure. Check stderr for the `SSH transport/protocol failed` line. |
| `128 + signal` | `dibs job`: a signal ended the child |
| `125` | `dibs job`: the helper exited before it ran the child, or the child's wait status was unavailable |
| `126` | `dibs job`: the helper could not `exec` the child |

Rules that the code enforces:

- `dibs job` uses dibs's own 1 and 2 only before a child starts. After start, the child's code wins, even when it is 1, 2, 75, 76, or 124.
- Cleanup errors after start go to stderr and never replace the child's code.
- A remote child's 255 passes through as 255. It is not an SSH failure.
- `dibs sweep --confirm` exits 1 when any eligible lease failed cleanup, and still prints the full result.
- The output mode never changes the exit code.

Tests: `TestJobsRealProcesses` subtests "exit codes and transparent argv", "wait exhaustion does not start child", and "deadline flag class and grandchildren", `TestOutputProcessesAndTTY`, the `TestSSHIntegration` subtests for 255, 75, and 76, and `TestCLIProtectedRenewRefusalsByOwner` for 3.

## Invariants

1. **The exit-code contract is stable.** Codes 0, 1, 2, 3, 75, 76, 124, 125, 126, and `128 + signal` keep their meanings. Changing one breaks every caller that branches on it.
2. **Usage errors happen before state access.** A usage error never reads or writes state, edits agent config, or runs a resource tool. Test: `TestCLIRejectsUsageBeforeStateAccess`.
3. **The help text matches the behavior.** A change to flags, defaults, or exit codes updates `commandHelp` or `exitCodes`, the README, and the matching doc in `docs/`.
4. **No prompts.** `release` and `sweep` are previews unless `--confirm` is set.
5. **New behavior is opt-in.** `AGENTS.md` requires a named setting in the user config or a `DIBS_*` variable, with no change when the setting is absent.
6. **Package layering is enforced.** `TestPackageGraph` fails on an unreviewed package or a forbidden import edge, and the root package holds only `main.go`.
