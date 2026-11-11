# Upgrade from berth to dibs

The module is `github.com/Dematom1/dibs`, the binary is `dibs`, and the Nix
package is `packages.<system>.dibs`.

```sh
go install github.com/Dematom1/dibs@latest
dibs --version
```

## Repository ordering

Rename the GitHub repository first through the separate infrastructure change
https://github.com/dematom-labs/infrastructure/pull/491, then merge the code
rename. Do not delete and recreate the repository. No infrastructure changes
are part of this code change.

GitHub redirects the old repository URLs after the repository rename. Current
main and existing releases still declare the berth module, so the old install
path continues to work during that interval. After the code rename, use the
new module path for renamed versions. Go validates module declarations: a
redirect cannot make `go install github.com/Dematom1/berth@latest` install a
version that declares `github.com/Dematom1/dibs`. No old-name forwarding binary
is included. An existing `berth` executable remains an old build.

## Compatibility

- `DIBS_*` replaces `BERTH_*`. Legacy aliases remain for one minor release.
  A set new variable wins, even when empty. Alias use warns once on stderr
  without values or changes to AXI or JSON stdout. Job children receive both
  lease-ID variables. Machine migration accepts `DIBS_LEGACY_MACHINE` with
  the same legacy fallback.
- Default config prefers dibs, falling back to berth only on absence.
  `XDG_CONFIG_HOME` applies to both. Explicit paths are unchanged. Nothing
  moves or rewrites the legacy config.
- Default state publishes a `dibs -> berth` directory alias atomically when
  legacy state exists. The ledger, lock inode, snapshots, owner identities,
  reservations, waiters, and TTLs stay in place. Retry after interruption is
  safe. Independent old and new directories cause an error; select each ledger
  with `DIBS_STATE` to reclaim it. Explicit state paths do not move.
- Schema v1 and v2 remain readable. The existing machine-identity migration
  still runs under the original lock, including through the alias. Its
  hostname attestation and foreign-machine restrictions remain unchanged.
  Old schema-v1-only binaries cannot read state upgraded to v2.
- New resources use `dibs-` names and `io.dibs.*` Compose labels. Existing
  leases with `berth-` names or `io.berth.*` snapshots still list and reclaim
  normally, including after the environment-alias window ends. Cleanup uses
  the recorded project or cluster, not attribution-label discovery.
- Adoption and read-only orphan inventory accept both name and label
  namespaces. Existing resource labels are preserved. Unleased resources
  remain untouched. Inventory neither publishes aliases nor migrates state.
- `dibs setup hooks` replaces managed session hooks and managed old plugins.
  Custom hooks, unrelated settings, and unmanaged plugin files stay intact.

Update sweep timers, agent briefs, remote binary paths, and scripts to use
`dibs` and `DIBS_*`. Install matching client and remote versions before SSH jobs.
Keep the old state directory and its alias until all leases are released.
