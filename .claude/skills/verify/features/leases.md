# Leases and sweep

A user creates an owner-and-expiry claim, inspects it, renews it, and previews cleanup before deleting it. Owner death makes a lease reclaimable before expiry.

## Sub-features

- `home`: executable identity, empty home, and help.
- `plain`: create a labeled metadata lease and list it through every listing alias.
- `renew-release`: extend expiry, preview without mutation, confirm, and repeat release.
- `owner-death`: retain a live owner's lease, then reclaim after the owner exits.

## How to get to it (user POV)

Run `berth`, `berth --help`, `berth create` (alias `up`), `berth list` (aliases `ls`, `ps`, `who`), `berth extend <id> --ttl 30m`, `berth release <id>` (alias `down`), or `berth sweep`.

## Driving it with verify.py

Preconditions: the baseline tools in the index are installed. Docker is not needed.

- **Run:** `.claude/skills/verify/verify.py local --evidence "$PROOF/leases"`.
- **Create and inspect:** the helper runs `create --owner pid:<helper-pid> --ttl 5m --label demo=plain --json`. `list`, `ls`, `ps`, and `who` must show that ID.
- **Renew:** `extend <id> --ttl 30m --json` must move the recorded expiry forward.
- **Preview:** `release <id> --json` must leave the count at one. `release <id> --confirm --json` must reduce it to zero. Repeated `down <id> --confirm --json` must succeed.
- **Death:** start a real `sleep 60`, then `up --owner pid:<sleep-pid> --ttl 1h --json`. Confirmed sweep must retain it while alive. Terminate and reap that PID. Preview sweep must list the ID without removing it; confirmed sweep must remove it.
- **Proof:** inspect commands, listing bodies, sweep eligibility, and exit codes in `events.jsonl`. Require `result.json` to report success and cleanup.

## Gotchas

- PID owners need a live process whose start identity can be read. Do not use a PID from another machine.
- Expiry also permits reclaim with a live owner; "never reclaim live" applies only before expiry.
- `none` leases manage metadata, not external resources.
- Release is destructive only with `--confirm`; a preview exit of zero alone does not prove safety.
