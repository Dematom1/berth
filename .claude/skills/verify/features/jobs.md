# Jobs and FIFO admission

A user reserves a machine budget, runs a child, receives its result, and waits in arrival order when capacity is unavailable.

## Sub-features

- `job-result`: output passthrough and exit 0 or child exit 7.
- `job-deadline`: a started child exceeds its deadline and returns 124.
- `job-invalid`: oversized admission returns 2 without starting the child.
- `job-wait`: exhausted admission returns 75 without starting the child.
- `job-fifo`: a smaller request cannot backfill ahead of an earlier large waiter.

## How to get to it (user POV)

Run `berth job [--cores N --memory M --timeout D --wait W] -- <command...>` and inspect active reservations with `berth ps`. SSH transport code 76 is covered in [SSH machines](ssh.md).

## Driving it with verify.py

Preconditions: baseline tools are available. The helper writes an isolated config with three total cores, one owner-room core, and two GiB of usable memory.

- **Run:** `.claude/skills/verify/verify.py local --evidence "$PROOF/jobs"`.
- **Results:** `job -- sh -c 'echo working; echo done'` returns 0. `job -- sh -c 'exit 7'` returns 7.
- **Deadline:** a child writes a marker then sleeps for 30 seconds under `--timeout 5s`. Require marker existence and exit 124.
- **Invalid:** `job --cores 999 -- touch <marker>` returns 2. Require the marker to remain absent.
- **FIFO:** start a one-core holder, queue a two-core request, then a one-core request. Observe registration in the read-only state snapshot, not shell launch order. The smaller job must not start despite one free core.
- **Exhaustion:** `job --memory 1MiB --wait 0 -- touch <marker>` returns 75, and no marker is created.
- **Admission:** release each child through its own gate file. Require execution order `holder`, `big`, `small`, no overlap with the fully reserved large job, and zero final leases.
- **Proof:** retain background logs, recorded PIDs and exit codes, CLI listings, the observed order, and `result.json`.

## Gotchas

- Queue order is state-lock acquisition order, not launch time. The helper waits for each registration before launching the next contender.
- Assertions use observed child-start markers, not elapsed-time speed estimates.
- Reservations are cooperative; these checks do not prove cgroup enforcement or prevent process-group escape.
- Local exit codes 1, 125, 126, and signal passthrough have existing unit coverage but are not separate demo journeys here.
