---
status: accepted
---

# Live-owner protection

[ADR 0001](0001-lease-lifecycle-and-ttl.md) makes expiry a hard backstop: on its machine, an expired lease is reclaimable even when its owner is alive or unknown. That stays the default. It bounds leaks when an ownership signal goes stale.

The backstop is wrong for a caller that renews on behalf of a long-lived worker. If that renewing supervisor is down for longer than one TTL, the next sweep destroys the resource of a worker that is still running. The supervisor can then only create a second lease while the worker still uses the first resource. A missed renewal becomes proof that an active owner may be reclaimed.

A caller may therefore pass `--protect-live-owner` on `create` or `adopt`, once the machine opts in with `"live_owner_protection": true` in the user config or `DIBS_LIVE_OWNER_PROTECTION=1`, like other new behavior. While the setting is off the flag fails with code 2. The recorded lease, not the setting, carries protection afterwards, so turning the setting off never unprotects a live worker's lease. The lease records `protect_live_owner: true` and amends ADR 0001 for that lease only:

- `reclaimable` requires proven owner death. Expiry alone never reclaims, and unknown liveness never reclaims, expired or not. Sweep and job admission share this rule.
- `extend` on a live owner succeeds before expiry as before. After expiry it reattaches the same lease to the same resource when the adapter's `Exists` proves the pinned resource present. A late renew never creates a lease.
- A renew refused for owner death, unknown owner, or resource presence exits 3 with a stable reason (`owner_dead`, `owner_unknown`, `resource_absent`, `resource_unknown`) and leaves the record unchanged. The TTL, machine, and job checks keep exit 1, and unprotected leases keep exit 1 and their message.
- Explicit `release --confirm` is unchanged. The usual caller is the live owner releasing its own resource, so requiring death would forbid self-release. Dibs authenticates no caller, so an owner-match check would add ceremony without a boundary. Protection guards automatic reclaim by guess, and explicit release is not a guess.
- A ledger that holds a protected lease is saved as format 3. Older binaries reject format 3 instead of silently ignoring the field and reclaiming at expiry. The ledger returns to format 2 once no protected lease remains.

Protection holds only when the owner is the worker's own liveness source. If the owner is the supervisor's PID, or the supervisor touches the heartbeat, a supervisor outage reads as owner death. A protected lease whose owner stays unknown forever is never reclaimed automatically. It stays visible in `dibs ps` until an explicit release. The caller accepted that trade when it opted in.

Job reservations cannot be protected. Their deadlines never move.

Evidence: [reclaim and renew](../../internal/lease/lease.go), [format 3](../../internal/lease/store.go), [presence checks](../../internal/adapter/inventory.go), `TestProtectedReclaimAndRenew`, `TestProtectedSweepSurvivesSupervisorOutage`, `TestCLIProtectedLeaseSurvivesExpiryAndReattaches`, the `ProtectedSafe` property in [the lease model](../../verification/Lease.tla), and [the scope issue](https://github.com/Dematom1/dibs/issues/41).
