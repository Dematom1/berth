# Configuration

Berth reads one optional, strict JSON config file. It sets the job budget, owner room, job classes, and SSH machines. Lease commands without `--machine` never read it. This page describes where berth finds the file, the defaults it derives from the host, and every field.

Source: `config.go`, and `SSHMachine` in `ssh.go`. Tests: `TestJobBudgetAndConfig` and `TestPartialConfigKeepsDefaultClass` in `job_test.go`, and `TestSSHMachineValidation` and `TestRemoteConfigErrorHelp` in `ssh_test.go`.

## Who reads the config

`loadJobConfig` runs for:

- `berth job`, local or remote.
- `berth machine install`.
- `berth list --machine` and `berth sweep --machine`, to find the machine.

On an SSH machine, the remote berth reads its own config. The client's budget and classes never cross the wire. See [Remote jobs over SSH](remote.md).

## File location

`loadJobConfig` picks the path:

1. `$BERTH_CONFIG`. A missing file is an error.
2. `$XDG_CONFIG_HOME/berth/config.json`. A missing file means defaults.
3. `~/.config/berth/config.json`. A missing file means defaults.

Berth uses the XDG path on macOS too.

## Parsing rules

- The file must contain exactly one JSON object.
- Unknown fields are errors (`DisallowUnknownFields`).
- Berth decodes the file on top of the host defaults. An omitted top-level field keeps its default.
- The `default` class stays available unless the file defines a class named `default`.
- After decoding, `available` and every class must validate. Any failure is an error, and the command exits 2.

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

`JobConfig.available` returns the usable budget: `cores - room_cores` cores and `memory - room_memory` bytes.

A memory string is a non-negative integer with an optional unit: `GiB`, `MiB`, `KiB`, `GB`, `MB`, `KB`, or `B`. With no unit, the value is bytes. `memoryBytes` rejects overflow.

### Machines

| Field | Required | Rule |
| --- | --- | --- |
| `host` | yes | Hostname or SSH config alias. No leading `-`, and no whitespace, NUL, or `@`. |
| `user` | yes | Same character rules as `host` |
| `workdir` | yes | Absolute path on the remote. Remote jobs run here. |
| `agent` | yes | Absolute path of the berth binary on the remote |
| `port` | no | 1 to 65535 |
| `identity_file` | no | Passed as `ssh -i` with `IdentitiesOnly=yes` |

`configuredMachine` validates a machine only when a command uses it.

## Environment variables

| Variable | Read by | Effect |
| --- | --- | --- |
| `BERTH_CONFIG` | `loadJobConfig` | Config file path |
| `BERTH_STATE` | `stateDir` | State directory. See [State](state.md). |
| `BERTH_OWNER` | `run` (create) | Default `--owner` |
| `XDG_CONFIG_HOME`, `XDG_STATE_HOME` | `loadJobConfig`, `stateDir` | Base directories |
| `DOCKER_HOST` | `prepareResource` | Must be unset for managed adapters |
| `BERTH_LEASE` | Set by `startJob` | Lease ID, exported to the job child |

## Invariants

1. **The config is strict.** Unknown fields and trailing data are errors, never ignored.
2. **An explicit `BERTH_CONFIG` must exist.** Only the implicit path falls back to defaults.
3. **Owner room is always below the totals.** `available` refuses `room_cores >= cores` and `room_memory >= memory`.
4. **Config changes apply to new invocations only.** Existing reservations keep counting, and running jobs keep their deadline.
5. **Remote config is authoritative on the remote.** The client never sends budget or classes.
