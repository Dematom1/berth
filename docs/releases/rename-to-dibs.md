# Rename release notes

Berth is renamed to dibs. The Go module is now `github.com/Dematom1/dibs`,
the binary is `dibs`, and the Nix package is `packages.<system>.dibs`.
Install with:

```sh
go install github.com/Dematom1/dibs@latest
dibs --version
```

GitHub redirects the old repository URLs after its rename. Go's module-path
check prevents the old `go install github.com/Dematom1/berth@latest` path from
providing a stub that prints a rename message in this same module. Use the new
install path. An old `berth` executable is still an old build.

## Compatibility

- `DIBS_*` replaces `BERTH_*`. The old environment variables are deprecated
  aliases for one minor release. A set `DIBS_*` value wins, even when empty.
  Alias use warns once on stderr without changing AXI or JSON stdout.
  Job children receive both lease-ID variables during this window.
- Config prefers `~/.config/dibs/config.json`, with read-only fallback to
  `~/.config/berth/config.json` when absent. `XDG_CONFIG_HOME` applies to both.
- Default state prefers the dibs name. When old state exists, dibs atomically
  publishes a directory alias to the old ledger rather than moving it or
  rewriting leases. Owners, deadlines, resource paths, and the shared lock
  remain unchanged. Retry after interruption is safe. Separate existing
  ledgers cause an error instead of silently hiding leases. Select each with
  `DIBS_STATE` to reclaim it. Explicit state paths are not moved.
- New resources use `dibs-` names and Compose labels use `io.dibs.*`.
  Existing leases with `berth-` names and `io.berth.*` snapshots still list
  and reclaim normally, including after the environment alias window ends.
  Cleanup verifies the recorded project or cluster,
  independent of attribution labels. Unleased resources remain untouched.
- `dibs setup hooks` replaces managed session hooks and old managed plugins.
  Custom hooks, settings, and unmanaged plugin files remain untouched.

Update sweep timers, agent briefs, remote binary paths, and scripts to use
`dibs` and `DIBS_*`. Install matching client and remote versions before SSH jobs.
Keep the old state directory and its alias until all leases are released.

The GitHub repository rename is managed separately through Terraform as a
rename, not deletion and recreation. No infrastructure is changed here.
