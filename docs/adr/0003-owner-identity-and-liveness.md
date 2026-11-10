---
status: accepted
---

# Owner identity and three-state liveness

Owner checks return alive, dead, or unknown rather than a boolean, so inspection failure does not become permission to destroy an unexpired resource. PID and session owners bind a process number to its recorded start identity. A reused PID is dead relative to the recorded owner, even if the replacement process is alive.

PID identity currently uses `ps` start time with one-second precision, not a collision-free native birth token. Missing processes and zombies are dead. Inspection failure is unknown. Heartbeat owners use an existing regular file's freshness: missing or stale means dead, while unreadable, nonregular, or future-dated means unknown. Heartbeat identity follows the canonical path, not a pinned inode.

Creation requires a verified live owner. Renewal also requires alive, while [expiry still permits reclaim](0001-lease-lifecycle-and-ttl.md) for an unknown owner. Lease creation defaults to the parent PID after `--owner` and `DIBS_OWNER`; jobs instead use their own dibs process, or the SSH agent's session holder. List filtering compares canonical owner selectors without requiring a fresh liveness check to resolve the selector.

Evidence: [owner parsing](../../internal/owner/reference.go), [registered identity checks](../../internal/owner/registry.go), [lease eligibility](../../internal/lease/lease.go), [owner precedence and list filtering](../../internal/cli/cli.go), and [owner tests](../../internal/owner/reference_test.go).
