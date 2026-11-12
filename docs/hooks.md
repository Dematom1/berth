# Agent hooks

`dibs setup hooks` makes coding agents get AXI output without passing `--axi` on every call. It installs a small, dibs-managed integration for Claude Code, Codex, Pi, and OpenCode that sets `DIBS_AXI=1` in the agent's shell environment. `dibs setup hooks --remove` takes the integration out again. This page describes which files the command edits and the rules that keep it from damaging user configuration.

Source: `internal/cli/hooks.go` (`runSetup`, `editSessionHook`, `managedPlugin`, `editCodexFeature`, and the legacy removal). Tests: `internal/cli/hooks_test.go` and `internal/cli/hooks_compat_test.go`. Output mode selection is in [Output modes](output.md#mode-selection).

## Files

`runSetup` resolves each agent's config directory from its usual environment variable, then falls back to a default under `$HOME`.

| Agent | File | Edit |
| --- | --- | --- |
| Claude Code | `$CLAUDE_CONFIG_DIR/settings.json` (default `~/.claude`) | Adds a `SessionStart` command hook (`editSessionHook`) |
| Codex | `$CODEX_HOME/hooks.json` (default `~/.codex`) | Adds the same `SessionStart` command hook |
| Codex | `$CODEX_HOME/config.toml` | Turns on `features.hooks` (`editCodexFeature`) |
| Pi | `$PI_CODING_AGENT_DIR/extensions/dibs-axi.ts` (default `~/.pi/agent`) | Writes a managed extension (`managedPlugin`) |
| OpenCode | `$XDG_CONFIG_HOME/opencode/plugins/dibs-axi.js` (default `~/.config`) | Writes a managed plugin |
| Pi, OpenCode | `berth-axi.ts` and `berth-axi.js` in the same directories | Removes the legacy managed plugin if berth wrote it (`removeLegacyPlugin`) |

The `SessionStart` command starts with the marker line `# dibs-axi`. It appends `export DIBS_AXI=1` to `$CODEX_ENV_FILE` or `$CLAUDE_ENV_FILE` when the agent provides one, and otherwise prints a reminder to use `--axi` or export `DIBS_AXI=1`. Pi and OpenCode plugins set `process.env.DIBS_AXI` or `output.env.DIBS_AXI` for the shell they start. Test: `TestSessionHookSetsEnvironment`.

## Edit rules

`runSetup` works in two phases:

1. It reads every target and computes every edit. Any read error, parse error, or refusal exits 1 before any file changes.
2. It writes each changed file with `atomicfile.Write`, or deletes a managed file on `--remove`. It reports the changed paths.

When no file changes, the status is `unchanged (no-op)`. Running setup again is safe. Test: `TestSetupHooksLifecycle`.

Usage errors touch nothing. The subcommand and flags are checked before `$HOME` is read. Test: `TestSetupUsageDoesNotTouchHome`.

`editSessionHook`:

- Requires the file to hold one JSON object, with `hooks` as an object and `SessionStart` as an array of groups that each hold a `hooks` array.
- Removes only hook commands that start with the `# dibs-axi` marker line, or the legacy `# berth-axi` marker, then adds one fresh copy unless `--remove` is set. The legacy marker is removed only so that a migration leaves exactly one dibs hook. Test: `TestSetupMigratesLegacyHooks`.
- Drops a group only when dibs's removal emptied it. Groups that were empty before stay.
- Returns the original bytes when the parsed content does not change. This keeps the user's formatting.

Test: `TestSessionHookDeduplicatesOnlyOwnedEntries`.

`managedPlugin` overwrites or deletes a plugin file only when the file starts with `// dibs-axi: managed by dibs setup hooks`. It refuses to replace any other file. `removeLegacyPlugin` deletes a berth-written plugin only when it starts with `// berth-axi: managed by berth setup hooks`, and leaves any other file alone. Test: `TestSetupRefusesMalformedAndUnmanagedFiles`.

`editCodexFeature`:

- Edits `config.toml` as text, line by line, so comments survive. It only adds, flips, or removes the `hooks` key in `[features]`, or the root `features.hooks` key.
- Marks its edits with a `# dibs-axi` comment. When it flips an existing `false`, the comment is `# dibs-axi was-false`, so `--remove` can restore `false`. A legacy `# berth-axi` comment is rewritten to `# dibs-axi`, so an older install migrates in place.
- Parses the TOML before and after the edit. It refuses the edit when anything other than `features.hooks` would change, when `features.hooks` is not a boolean, or when the key appears twice.
- Refuses an inline `features = { ... }` table.

Test: `TestCodexFeaturePreservesConfig`.

## Invariants

1. **Malformed or unmanaged user configuration is never replaced.** All edits are computed and validated before the first write. Test: `TestSetupRefusesMalformedAndUnmanagedFiles`.
2. **dibs removes only what it added.** Hook commands carry the `# dibs-axi` marker. Plugin files carry the managed header. Codex edits carry a `# dibs-axi` comment. The legacy markers are recognized only for migration.
3. **The Codex edit changes only `features.hooks`.** A before-and-after TOML comparison enforces this. Test: `TestCodexFeaturePreservesConfig`.
4. **Setup is idempotent.** A repeated run changes nothing and reports `unchanged (no-op)`. Test: `TestSetupHooksLifecycle`.
5. **Writes are atomic.** Every file goes through `atomicfile.Write`.
6. **Usage errors touch nothing.** A bad subcommand or flag exits 2 before dibs reads `$HOME`. Test: `TestSetupUsageDoesNotTouchHome`.
7. **Hooks only set `DIBS_AXI=1`.** They never run dibs, and they hold no executable path. Test: `TestSessionHookSetsEnvironment`.
