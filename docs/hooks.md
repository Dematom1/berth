# Agent hooks

`berth setup hooks` makes coding agents get AXI output without passing `--axi` on every call. It installs a small, berth-managed integration for Claude Code, Codex, Pi, and OpenCode that sets `BERTH_AXI=1` in the agent's shell environment. `berth setup hooks --remove` takes the integration out again. This page describes which files the command edits and the rules that keep it from damaging user configuration.

Source: `hooks.go`. Tests: `hooks_test.go`. Output mode selection is in [Output modes](output.md#mode-selection).

## Files

`runSetup` resolves each agent's config directory from its usual environment variable, then falls back to a default under `$HOME`.

| Agent | File | Edit |
| --- | --- | --- |
| Claude Code | `$CLAUDE_CONFIG_DIR/settings.json` (default `~/.claude`) | Adds a `SessionStart` command hook (`editSessionHook`) |
| Codex | `$CODEX_HOME/hooks.json` (default `~/.codex`) | Adds the same `SessionStart` command hook |
| Codex | `$CODEX_HOME/config.toml` | Turns on `features.hooks` (`editCodexFeature`) |
| Pi | `$PI_CODING_AGENT_DIR/extensions/berth-axi.ts` (default `~/.pi/agent`) | Writes a managed extension (`managedPlugin`) |
| OpenCode | `$XDG_CONFIG_HOME/opencode/plugins/berth-axi.js` (default `~/.config`) | Writes a managed plugin |

The `SessionStart` command starts with the marker line `# berth-axi`. It appends `export BERTH_AXI=1` to `$CODEX_ENV_FILE` or `$CLAUDE_ENV_FILE` when the agent provides one, and otherwise prints a reminder to use `--axi`. Test: `TestSessionHookSetsEnvironment`.

## Edit rules

`runSetup` works in two phases:

1. It reads every target and computes every edit. Any read error, parse error, or refusal exits 1 before any file changes.
2. It writes each changed file with `atomicWrite`, or deletes a managed file on `--remove`. It reports the changed paths.

When no file changes, the status is `unchanged (no-op)`. Running setup again is safe. Test: `TestSetupHooksLifecycle`.

`editSessionHook`:

- Requires the file to hold one JSON object, with `hooks` as an object and `SessionStart` as an array of groups that each hold a `hooks` array.
- Removes only hook commands that start with the `# berth-axi` marker line, then adds one fresh copy unless `--remove` is set.
- Drops a group only when berth's removal emptied it. Groups that were empty before stay.
- Returns the original bytes when the parsed content does not change. This keeps the user's formatting.

Test: `TestSessionHookDeduplicatesOnlyOwnedEntries`.

`managedPlugin` overwrites or deletes a plugin file only when the file starts with `// berth-axi: managed by berth setup hooks`. It refuses to replace any other file. Test: `TestSetupRefusesMalformedAndUnmanagedFiles`.

`editCodexFeature`:

- Edits `config.toml` as text, line by line, so comments survive. It only adds, flips, or removes the `hooks` key in `[features]`, or the root `features.hooks` key.
- Marks its edits with a `# berth-axi` comment. When it flips an existing `false`, the comment is `# berth-axi was-false`, so `--remove` can restore `false`.
- Parses the TOML before and after the edit. It refuses the edit when anything other than `features.hooks` would change, when `features.hooks` is not a boolean, or when the key appears twice.
- Refuses an inline `features = { ... }` table.

Test: `TestCodexFeaturePreservesConfig`.

## Invariants

1. **Malformed or unmanaged user configuration is never replaced.** All edits are computed and validated before the first write.
2. **Berth removes only what it added.** Hook commands carry the `# berth-axi` marker. Plugin files carry the managed header. Codex edits carry a `# berth-axi` comment.
3. **The Codex edit changes only `features.hooks`.** A before-and-after TOML comparison enforces this.
4. **Setup is idempotent.** A repeated run changes nothing and reports `unchanged (no-op)`.
5. **Writes are atomic.** Every file goes through `atomicWrite`.
6. **Usage errors touch nothing.** A bad subcommand or flag exits 2 before berth reads `$HOME`. Test: `TestSetupUsageDoesNotTouchHome`.
7. **Hooks only set `BERTH_AXI=1`.** They never run berth, and they hold no executable path.
