# Configuration

dibs reads one optional, strict JSON config file. It sets the job budget, owner room, job classes, and SSH machines. This page describes where dibs finds the file, the defaults it derives from the host, and every field.

Source: `internal/config/config.go` (`Load`, `Available`, `Class`, `defaultJobConfig`, `Target`), `internal/budget/amount.go` (`ParseMemory`, `Amount`), and `internal/compat/env.go` (the `DIBS_CONFIG` and legacy `BERTH_CONFIG` lookup). Decision: [ADR 0004](adr/0004-fifo-admission-and-owner-room.md). Tests: `TestJobBudgetAndConfig` in `internal/config/config_test.go` and `internal/lease/job_test.go`, `TestPartialConfigKeepsDefaultClass` in `internal/config/config_test.go`, `TestRenamedConfigFallback` in `internal/config/compat_test.go`, `TestMemoryBytes` in `internal/budget/amount_test.go`, `TestSSHMachineValidation` and `TestRemoteConfigErrorHelp` in `internal/cli/ssh_test.go`, and `TestBudgetSummary` in `internal/output/budget_test.go`.

## Who reads the config

`Load` runs for:

- `dibs job`, local or remote.
- `dibs machine install`.
- `dibs list --machine` and `dibs sweep --machine`, to find the machine.
- A local `dibs list`, for its `budget` summary. A config error does not fail the listing. The summary then reads `jobs: budget unavailable (check DIBS_CONFIG)`.

`create`, `extend`, `release`, `sweep`, `env`, `adopt`, and `orphans` on this machine never read the config.

On an SSH machine, the remote dibs reads its own config. The client's budget and classes never cross the wire. See [Remote jobs over SSH](remote.md).

## File location

`Load` in `internal/config/config.go` picks the path:

1. `DIBS_CONFIG`, or the legacy `BERTH_CONFIG`. A missing file is an error. The explicit path is used exactly as given.
2. `$XDG_CONFIG_HOME/dibs/config.json`, or `~/.config/dibs/config.json`. A missing file means defaults.
3. If the dibs file is absent, the legacy `$XDG_CONFIG_HOME/berth/config.json` (or `~/.config/berth/config.json`) is read instead. A missing file there also means defaults.

dibs uses the XDG path on macOS too. The legacy fallback never moves or rewrites the old file.

## Parsing rules

- The file must contain exactly one JSON object. The first byte must be `{`.
- Unknown fields are errors (`DisallowUnknownFields`).
- dibs decodes the file on top of the host defaults. An omitted top-level field keeps its default.
- The `default` class stays available unless the file defines a class named `default`.
- After decoding, `Available` and every class must validate. Any failure is an error, and the command exits 2.

## Host defaults

`defaultJobConfig` derives the defaults from `runtime.NumCPU()` and the host's physical memory. `hostMemory` reads `sysctl -n hw.memsize` on macOS and `MemTotal` in `/proc/meminfo` on Linux.

| Field | Default |
| --- | --- |
| `cores` | Logical CPU count |
| `memory` | Physical memory in bytes |
| `room_cores` | `min(cores - 1, max(1, cores / 4))`, with integer division |
| `room_memory` | `min(memory / 2, max(512 MiB, memory / 4))` |
| `classes.default` | 1 core, `min(1 GiB, memory - room_memory)` bytes, timeout `1h` |

## Fields

| Field | Type | Rule |
| --- | --- | --- |
| `cores` | integer | Must be positive |
| `memory` | memory string | Must be positive |
| `room_cores` | integer | From 0 to `cores - 1` |
| `room_memory` | memory string | Less than `memory` |
| `classes.<name>.cores` | integer | Must be positive |
| `classes.<name>.memory` | memory string | Must be positive |
| `classes.<name>.timeout` | Go duration string | Must be positive, such as `30m` |
| `machines.<name>` | object | See [Machines](#machines) |

`Config.Available` returns the usable budget: `cores - room_cores` cores and `memory - room_memory` bytes. It refuses a budget with non-positive totals, or owner room that is not below the totals.

A memory string is a non-negative integer with an optional unit: `GiB`, `MiB`, `KiB`, `GB`, `MB`, `KB`, or `B`. With no unit, the value is bytes. `ParseMemory` rejects overflow.

### Machines

| Field | Required | Rule |
| --- | --- | --- |
| `host` | yes | Hostname or SSH config alias. No leading `-`, and no whitespace, NUL, or `@`. |
| `user` | yes | Same character rules as `host` |
| `workdir` | yes | Absolute path on the remote. Remote jobs run here. |
| `agent` | yes | Absolute path of the dibs binary on the remote |
| `port` | no | 1 to 65535 when set. Omitted or 0 means the SSH default. |
| `identity_file` | no | Passed as `ssh -i` with `IdentitiesOnly=yes` |

`Target.Validate` checks a machine only when a command uses it, through `LookupTarget`.

## Environment variables

| Variable | Read by | Effect |
| --- | --- | --- |
| `DIBS_CONFIG` (`BERTH_CONFIG`) | `Load` | Config file path |
| `DIBS_STATE` (`BERTH_STATE`) | `resolveStateDir` | State directory. See [State](state.md#state-directory). |
| `DIBS_OWNER` (`BERTH_OWNER`) | `run` in `internal/cli/cli.go` | Default `--owner` |
| `DIBS_AXI` (`BERTH_AXI`) | `Mode.Effective` | `1` selects AXI output when no `--axi` or `--json` flag is set. See [Output modes](output.md#mode-selection). |
| `DIBS_LEGACY_MACHINE` (`BERTH_LEGACY_MACHINE`) | `migrateMachines` | Same-user attestation for one legacy hostname. See [State](state.md#machine-identity). |
| `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `PI_CODING_AGENT_DIR` | `runSetup` | Agent config directories. See [Agent hooks](hooks.md). |
| `XDG_CONFIG_HOME`, `XDG_STATE_HOME` | `Load`, `runSetup`, `resolveStateDir` | Base directories |
| `DOCKER_HOST` | Docker-backed adapters | Must be unset. Managed adapters refuse it. |
| `DIBS_LEASE` (and `BERTH_LEASE`) | Set by `startJob` | Lease ID, exported to the job child |

The `DIBS_*` name wins whenever it is set, even when empty. `internal/compat/env.go` reads the legacy `BERTH_*` name only when the new one is unset. Each legacy name in use prints one warning on stderr.

## Invariants

1. **The config is strict.** Unknown fields and trailing data are errors, never ignored. Test: `TestJobBudgetAndConfig`.
2. **An explicit `DIBS_CONFIG` must exist.** Only the implicit path falls back to defaults. Test: `TestRenamedConfigFallback`.
3. **Owner room is always below the totals.** `Available` refuses `room_cores >= cores` and `room_memory >= memory`.
4. **Config changes apply to new invocations only.** Existing reservations keep counting, and running jobs keep their deadline.
5. **Remote config is authoritative on the remote.** The client never sends budget or classes. Test: `TestRemoteConfigErrorHelp`.

## Planned

The approved direction in [VISION.md](../VISION.md) makes every optional capability opt-in through a named setting in this user config. No such setting beyond the budget, classes, and machines above exists today.
