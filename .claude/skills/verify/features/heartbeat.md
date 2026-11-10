# Heartbeat owners

A user keeps a lease alive by refreshing an existing regular file. Stale or missing heartbeats allow sweep before lease expiry.

## Sub-features

- `heartbeat-stale`: preview identifies a stale heartbeat without deleting the lease.
- `heartbeat-refresh`: touching the heartbeat protects the unexpired lease again.
- `heartbeat-missing`: removing the file permits confirmed sweep.

## How to get to it (user POV)

Create a file with `touch`, then run `dibs create --owner heartbeat:<absolute-file> --stale 1s --ttl 1h`. Use `dibs sweep` and `dibs sweep --confirm` to inspect and reclaim.

## Driving it with verify.py

Preconditions: the baseline tools in the index are installed; the helper creates the heartbeat file itself.

- **Run:** `.claude/skills/verify/verify.py local --evidence "$PROOF/heartbeat"`.
- **Stale:** create the lease, wait 1.2 seconds, and preview sweep. The ID must be eligible despite its one-hour expiry.
- **Refresh:** touch the file, then confirm sweep. `ps --json` must still show one lease.
- **Missing:** unlink the file, then confirm sweep. `ps --json` must show zero leases.
- **Proof:** retain the sweep responses and count assertions in `events.jsonl`, plus `result.json`.

## Gotchas

- The demo refreshes the file before confirmed sweep: the lease is no longer eligible at that point. Missing-file cleanup completes this journey.
- A future timestamp means unknown, not dead. This mapped journey covers stale, refreshed, and missing files; future timestamps are not claimed as covered here.
- Production cadence is at most staleness/3, with renewal before one third of TTL remains. Short waits here are verification scaffolding.
