---
status: accepted
---

# Renaming berth to dibs keeps every existing lease reclaimable

The project, module, binary, environment variables, config and state directories, resource names, and Compose labels move from berth to dibs. A rename must not orphan a resource: a lease that berth recorded stays a lease that dibs can release or sweep.

The default state directory moves once, by renaming `berth` to `dibs` under the berth store lock so no berth process is mid-operation, then leaving a regular file at the old path. An older berth binary therefore fails to open its state rather than starting a separate ledger that dibs would never reclaim. Resource files recorded under the old directory are mapped to the new one on load, and validation accepts both `berth-<id>` and `dibs-<id>` resource names. Cleanup never searched by label; it uses the recorded project or cluster name and the saved Compose snapshot, so `io.berth.*` labeled stacks are reclaimed through their leases. When both directories already exist, dibs leaves the berth one untouched and warns, because merging two ledgers is not worth its risk for a case that needs an old binary run after an upgrade.

`BERTH_*` environment variables and the berth config file remain deprecated fallbacks for one minor release, with a stderr warning; `DIBS_*` always wins when set. Recognition of berth state, names, and labels has no end date.

Evidence: [compatibility code](../../legacy.go), [compatibility tests](../../legacy_test.go), and [upgrade notes](../../README.md#upgrading-from-berth).
