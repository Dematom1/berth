# Remote jobs over SSH

`berth job --machine <name>`, `berth list --machine <name>`, and `berth sweep --machine <name>` run on a configured SSH machine. The client starts `berth agent` on the remote over `ssh`, and the two sides exchange newline-delimited JSON frames. The remote berth is authoritative for config, budget, admission, state, deadlines, and cleanup. This page describes the transport, the protocol, and the agent.

Source: `ssh.go`. Tests: `ssh_test.go`, and `ssh_integration_test.go` (build tag `sshintegration`, run with `make ssh-integration`).

## Machine config

`machines` in the local config maps a name to an `SSHMachine`. See [Configuration](config.md#machines). `SSHMachine.validate` requires:

- `host` and `user`: non-empty, no leading `-`, and no whitespace, NUL, or `@`. `host` can be an SSH config alias.
- `workdir` and `agent`: absolute paths on the remote.
- `port`: 0 (unset) to 65535.
- No NUL in `workdir`, `agent`, or `identity_file`.

Test: `TestSSHMachineValidation`.

## SSH transport

`SSHMachine.command` always runs:

```sh
ssh -T -o BatchMode=yes -o ConnectTimeout=15 -o ServerAliveInterval=5 -o ServerAliveCountMax=3 \
	[-p <port>] [-i <identity_file> -o IdentitiesOnly=yes] -l <user> -- <host> <script>
```

Berth never disables host-key checking and never falls back to interactive authentication. SSH config aliases can add proxying, keys, and host-key policy.

Every value that berth puts into a remote shell script goes through `shellQuote`, which wraps the value in single quotes. Test: `TestShellQuote`.

## Client side

`runRemote` handles the client side:

1. It forwards only explicitly set flags, as `--name=value`, except `--machine`. It then adds `--json` when set and, for `job`, `--` and the child argv. The client never sends its own budget, classes, or defaults.
2. The remote script runs `<agent> --version` first. When the version differs from the client's `version`, the script prints one `error` frame and exits 0. This check also gives a clear refusal when the remote binary is too old to have the `agent` command.
3. When the versions match, the script runs `cd <workdir> && exec <agent> agent`.
4. The client sends a `request` frame and reads the first reply.
5. An `error` frame exits 1 with a hint to run `berth machine install`.
6. A `ready` frame with the client's exact version starts the session. Anything else is a protocol failure.
7. The client writes `output` frame data to stdout, byte for byte. Remote stderr passes through SSH to local stderr unframed.
8. A `result` frame with a code from 0 to 255 ends the session. The client returns that code, even if SSH fails during teardown.
9. Any other frame, a read error, or EOF before `result` kills `ssh` and returns 76. The remote result is then unknown, and the child may have run.

The client keeps SSH stdin open for the whole session. SSH stdin is only the owner liveness channel. Remote children get `/dev/null` as stdin.

## Protocol

The protocol is private to the SSH pipe. Users never see it.

- One JSON object per line. The reader accepts lines up to 1 MiB.
- The reader rejects unknown fields and any trailing data after the object.
- `data` is a byte slice that JSON encodes as base64, so child output cannot forge a frame.
- `frameWriter` splits output into frames of at most 32 KiB.
- Both sides require the exact same `version` string. No compatibility range exists.

| Type | Direction | Fields | Meaning |
| --- | --- | --- | --- |
| `request` | client to agent | `version`, `args` | The command to run |
| `ready` | agent to client | `version` | The agent accepted the request |
| `output` | agent to client | `data` | A chunk of the command's stdout |
| `result` | agent to client | `code` | The command's exit code. Always the last frame. |
| `error` | agent or version probe to client | `error` | Refusal. No child started. |

A `result` frame carries the child's code, so a child that exits 255 is never mistaken for an `ssh` failure, which also exits 255. Tests: `TestAgentOutputFrames`, and the `TestSSHIntegration` subtest "child codes including SSH reserved 255".

## Agent side

`runAgent` runs once per SSH session. It is not a daemon.

1. It reads one `request` frame. It refuses, with an `error` frame, a request that has the wrong type, a different version, no command, or a command other than `job`, `list`, or `sweep`.
2. It refuses a `--machine` flag before `--`, so remote machines cannot chain.
3. It sends `ready`.
4. A goroutine reads stdin. EOF, or any further frame, cancels the context. The agent treats the end of SSH stdin as owner death.
5. It runs `job` through `runJob` with owner kind `session`, so the agent process owns the reservation. It runs `list` and `sweep` through `run`. Command output goes out as `output` frames.
6. It sends `result` with the exit code.

When the context is cancelled, the job's process group is cleaned up and its reservation is released, including during admission. Test: `TestAgentProtocolRefusal`, and the `TestSSHIntegration` subtest "wait 75 and disconnect kills descendant and releases budget".

The remote uses its own config and state directory. `ps --machine` and `sweep --machine` read remote state. Each machine reclaims only its own leases, and no ledger spans hosts.

## Machine install

`berth machine install <name> --file <binary>` streams a binary to the remote over SSH stdin. The remote script:

1. Sets `umask 077` and creates the agent's directory.
2. Writes the upload to a temporary file next to the agent path.
3. Checks that the byte count equals the local file size.
4. Runs `chmod 700` and checks that `<tmp> --version` prints the client's version.
5. Moves the temporary file over the agent path with `mv -f`.

A size or version mismatch keeps the existing binary. Agents that are already running keep their executable. Test: `TestSSHIntegration` subtest "version checked installation and handshake".

## Invariants

1. **Exit code 76 means the remote result is unknown.** Return 76 only for transport or protocol failure after SSH started. Never return berth's own 1 or 2 once a child may have started. Test: `TestSSHIntegration` subtest "transport loss after child start returns 76".
2. **The `result` frame is authoritative.** No SSH exit status overwrites a received child code.
3. **SSH stdin is the liveness channel.** EOF on the agent's stdin cancels the job and releases its reservation. Remote children never read SSH stdin.
4. **Exact version match on both sides.** A mismatch refuses before any child starts.
5. **Host-key checking stays on.** `BatchMode=yes` stays, and berth adds no option that weakens host-key policy.
6. **Every remote shell value is quoted** with `shellQuote`.
7. **Remote state stays remote.** The client never forwards budget, classes, or state, and the agent refuses nested `--machine`.
8. **Frames cannot be forged by output.** Output bytes travel base64-encoded inside `output` frames, and the reader rejects unknown fields.
