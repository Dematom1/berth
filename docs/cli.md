# CLI

`berth` is one binary with no daemon. `main.go` sets up signal handling and the version string, then calls `run` in `cli.go`. `run` parses arguments, checks usage before it touches state, and dispatches each command. This page describes the dispatch rules, the commands, and the exit-code contract.

Source: `main.go`, `cli.go`. Tests: `cli_test.go`, `main_test.go`, and `output_modes_test.go`. Decision: [ADR 0007](adr/0007-output-modes-and-job-results.md).

## Entry point

`main` handles two cases:

- `berth __job_exec <argv...>` is the internal job helper. It calls `jobExec` and never reaches `run`. See [Jobs](jobs.md#start).
- Everything else calls `run` with a context that `SIGINT` and `SIGTERM` cancel.

`resolveVersion` sets `version` at startup:

1. A value set at link time with `-ldflags "-X main.version=..."`.
2. Otherwise, the module version from the Go build information, without its leading `v`.
3. Otherwise, `devel`, plus `-<first 12 characters of the VCS revision>` when known, plus `-dirty` when the tree was modified.

The SSH handshake compares this string exactly. See [Remote jobs over SSH](remote.md#protocol). Test: `TestResolveVersion`.

## Dispatch

`runWithOutput` resolves the command in this order:

1. If the only argument is `--version`, `-v`, or `-V`, it prints `version` and exits 0.
2. It consumes any leading `--axi` and `--json` flags. Both together exit 2. See [Output modes](output.md#mode-selection).
3. If the next argument does not start with `-`, that argument is the command. With no command, the command is `list` in home view, which adds `bin` and `description` to the result.
4. It maps the aliases: `up` to `create`; `ls`, `ps`, and `who` to `list`; and `down` to `release`.
5. `help` and `help <command>` print help.
6. An unknown command exits 2.
7. `agent`, `setup`, `machine`, and `job` have their own parsers: `runAgent`, `runSetup`, `runMachine`, and `runJob`.
8. The lease commands (`create`, `list`, `extend`, `release`, `sweep`, and `env`) share one `flag.FlagSet`. It registers only the flags that the command accepts.

`parseFlags` moves positional arguments after the flags, so `extend <id> --ttl 1h` and `extend --ttl 1h <id>` mean the same thing. `runJob` does not use it: everything after the first `--` belongs to the child.

## Commands

| Command | Flags | Positional |
| --- | --- | --- |
| `create`, `up` | `--owner`, `--ttl` (default `8h`), `--stale` (default `15m`), `--resources`, `--label key=value` (repeatable), `--adapter none\|kind\|k3d\|compose` (default `none`), `--file` (Compose only, required for Compose), `--quiet`, `-q` | none |
| `list`, `ls`, `ps`, `who` | `--owner`, `--machine` | none |
| `extend` | `--ttl` (required, positive) | lease ID |
| `release`, `down` | `--confirm` | lease ID |
| `sweep` | `--confirm`, `--machine` | none |
| `env` | none | lease ID |
| `job` | `--cores`, `--memory`, `--class` (default `default`), `--timeout`, `--wait` (default `30m`), `--machine` | `-- <cmd...>` |
| `machine install` | `--file` (required) | machine name |
| `setup hooks` | `--remove` | none |
| `agent` | none | none |

Every command accepts `--help`. Every command except `agent` accepts `--axi` and `--json`. The lease commands also accept `--full`, which turns off AXI truncation. `list --owner` compares canonical owner strings: it normalizes PID spellings and makes heartbeat paths absolute, without checking liveness. Test: `TestCLIOwnerFilterUsesCanonicalIdentity`.

`berth env <id>` prints `export KUBECONFIG=<quoted path>` for a kind or k3d lease. It exits 2 when the lease does not exist or has no kubeconfig. Test: `TestQuietAndKubeconfig`.

Behavior lives in the component docs:

- `create`, `list`, `extend`, `release`, and `sweep`: [Leases](leases.md).
- `job`: [Jobs](jobs.md).
- `agent`, `machine install`, and `--machine`: [Remote jobs over SSH](remote.md).
- `setup hooks`: [Agent hooks](hooks.md).

## Usage checks come first

`run` rejects bad usage with exit 2 before it opens the state store or runs any resource tool. Bad usage includes:

- an unknown flag (including `--human`) or a missing flag value,
- `--axi` together with `--json`,
- a wrong number of positional arguments,
- a lease ID that is not 32 lowercase hex characters,
- a non-positive `--ttl`,
- an unknown adapter, or
- `--file` without `--adapter compose`, or `--adapter compose` without `--file`.

Tests: `TestCLIRejectsUsageBeforeStateAccess`, `TestConflictingOutputFlags`.

## Help text

`commandHelp` holds the help for each command. `help` adds three things:

- The `cadence` constant, for `create` and `extend`.
- Lines about output modes, for every command except `agent`.
- The `exitCodes` constant, for every command.

`emitHelp` prints the text as-is in human mode. In AXI and JSON it emits `usage` and `reference` fields. `TestCLIHomeHelpVersionAndTOON` asserts that every command's help contains `Exit codes:`, that `job` help names 124, 75, and 76, and that `extend` help states the renewal cadence.

## Exit codes

The `exitCodes` constant in `cli.go` is the contract. The README repeats it as the AXI exit-code table, and [ADR 0007](adr/0007-output-modes-and-job-results.md) records why.

| Code | Meaning |
| --- | --- |
| `0` | Success, including no-ops such as releasing an absent lease or an empty sweep |
| `1` | A berth operation failed. Resource cleanup failures keep their lease. |
| `2` | Invalid command, argument, flag, or config. No child started. |
| Child's code | `berth job`: the child ran to completion |
| `124` | `berth job`: the deadline killed the child (the coreutils `timeout` convention) |
| `75` | `berth job`: no slot freed within `--wait` (`EX_TEMPFAIL`). No child started. |
| `76` | SSH transport or protocol failure after SSH started. The remote result is unknown. |
| `128 + signal` | `berth job`: a signal ended the child |
| `125` | `berth job`: the helper exited before it ran the child, or the child's wait status was unavailable |
| `126` | `berth job`: the helper could not `exec` the child |

Rules that the code enforces:

- `berth job` uses berth's own 1 and 2 only before a child starts. After start, the child's code wins, even when it is 1, 2, 75, 76, or 124.
- Cleanup errors after start go to stderr and never replace the child's code.
- A remote child's 255 passes through as 255. It is not an SSH failure.
- `berth sweep --confirm` exits 1 when any eligible lease failed cleanup, and still prints the full result.
- The output mode never changes the exit code.

Tests: `TestJobsRealProcesses` subtests "exit codes and transparent argv", "wait exhaustion does not start child", and "deadline flag class and grandchildren", `TestOutputProcessesAndTTY`, and the `TestSSHIntegration` subtests for 255, 75, and 76.

## Invariants

1. **The exit-code contract is stable.** Codes 0, 1, 2, 75, 76, 124, 125, 126, and `128 + signal` keep their meanings. Changing one breaks every caller that branches on it.
2. **Usage errors happen before state access.** A usage error never reads or writes state, edits agent config, or runs a resource tool.
3. **The help text matches the behavior.** A change to flags, defaults, or exit codes updates `commandHelp` or `exitCodes`, the README, and the matching doc in `docs/`.
4. **No prompts.** `release` and `sweep` are previews unless `--confirm` is set.
5. **New behavior is opt-in.** `AGENTS.md` requires a named setting in the user config or a `BERTH_*` variable, with no change when the setting is absent.
