# berth

Owner-and-expiry leases for local stacks and clusters. One Go binary, no daemon.
This first slice supports leases, kind and k3d clusters, and Docker Compose
projects on macOS and Linux. Jobs, admission budgets, and SSH agents follow in later slices.

## Install

Requires Go 1.26 or later.

```sh
 go install github.com/Dematom1/berth@latest
 berth --version
```

From a checkout, `make install` installs to Go's binary directory. `make build`
writes `bin/berth`. Resource adapters also require `kind`, `k3d`, `docker`,
and Docker Compose, plus a running Docker engine.

## Start a leased test lab

Keep the shell or session that owns the lab alive.

```sh
 berth create --adapter kind --owner "pid:$$" --ttl 2h \
   --resources '2 CPUs, Kubernetes test lab' --label task=lab
 berth ps
 berth extend <id> --ttl 2h
 berth release <id>                  # preview
 berth release <id> --confirm        # delete cluster and release lease
```

Kind cluster names are `berth-<id>`. K3d names use the first 26 ID characters
to fit its 32-character limit. Both use the selected Docker context and private
kubeconfigs under `$BERTH_STATE/resources/<id>/`: `kubeconfig` for kind and `k3d`
for k3d. Creation and cleanup do not edit the caller's kubeconfig or current
context. The resources description is metadata, not a CPU or memory limit.
An orphaned lab such as `k8s-axi-lab` is prevented by creating the new cluster
through berth. This slice does not adopt or delete existing unleased clusters.

To create a Compose stack, pass its file.

```sh
 berth create --adapter compose --file compose.yaml --owner "pid:$$" --ttl 2h
 berth who --json
 berth sweep                        # preview dead or expired leases
 berth sweep --confirm               # reclaim them
```

Compose projects also use `berth-<id>`. Berth resolves the Compose file once,
saves a private config snapshot, and labels its services, volumes, and networks
with `io.berth.lease`, `io.berth.owner`, `io.berth.expires`, and `io.berth.machine`.
Cleanup does not require the original file, environment, or worktree. Labels
record creation-time expiry; the lease record is authoritative after `extend`.
Fixed `container_name` values and nonexternal volume or network names outside
the generated project are rejected. Mark shared volumes and networks `external`
to keep them out of cleanup. Release runs Compose down with `--volumes` and
`--remove-orphans`, so data in managed volumes is deleted.

Adapters tolerate already absent resources. Cleanup errors retain the
lease and exit 1. Berth verifies that managed resources are gone before removing
the record. A create that fails after saving its lease also retains the record
for release or sweep to clean up partial resources.

## Owners and renewal

`--owner` overrides `BERTH_OWNER`. Without either, berth uses the parent PID,
usually the invoking shell. A spawner can export `BERTH_OWNER` for all of its
workers. The supported owner kinds are:

- `pid:<pid>` follows a local process and records its start time to detect PID
  reuse. Process start identity comes from `ps` with one-second precision.
- `session:<holder-pid>` follows the local process holding a session, with the
  same start-time check. This is not a Unix session ID. A later SSH agent can
  use its session-holding process as the owner without querying a remote PID.
- `heartbeat:<file>` follows an existing regular file's modification time.
  `--stale` defaults to `15m`. A missing file or a stale heartbeat means dead;
  an unreadable file or a timestamp in the future means unknown. Create the
  file before creating the lease.

Touch a heartbeat file at most every staleness/3. Extend a lease at the latest
when one third of its TTL remains. A lease is reclaimable when its owner is
dead **or** when it has expired. A live owner that misses either cadence loses
its resources. Unknown liveness alone never permits reclaim, but expiry still
does. `extend` refuses expired leases and owners that are dead or unknown.

```sh
 touch /tmp/my-lab-heartbeat
 berth create --owner heartbeat:/tmp/my-lab-heartbeat --stale 15m --ttl 1h
 # The owner touches the file at least every 5m, and extends before 20m remain.
 berth extend <id> --ttl 1h
```

Durations use Go syntax such as `30m`, `2h`, or `1h30m`. Every lease has a positive
TTL, defaulting to `8h`. `extend --ttl` sets the expiry to now plus that duration.

## Command reference

| Command | Behavior |
| --- | --- |
| `berth` | Live home view with executable path, description, lease count, and hints |
| `berth create` or `berth up` | Create a lease, optionally with `--adapter kind`, `k3d`, or `compose` |
| `berth list`, `ls`, `ps`, or `who` | Owner, liveness, age, expiry, machine, and resource description for every lease |
| `berth extend <id> --ttl D` | Renew an unexpired lease with a live owner |
| `berth release <id>` or `down` | Preview cleanup; `--confirm` performs it |
| `berth sweep` | Preview reclaim of dead-owner or expired leases; `--confirm` performs it |

Every command accepts `--help` and `--json`. Output defaults to concise TOON-style
objects and tables. Label cells in text tables contain compact JSON, with `"{}"`
for empty labels. JSON listings keep labels as native objects and include full
records. Both formats have explicit empty states and structured errors on stdout. Resource
tool diagnostics go to stderr. There are no prompts. Unknown arguments and
flags fail with code 2 and help before state or resource operations run.

`create --adapter none` records a metadata-only lease. It does not manage or
delete an external resource. Labels are repeatable `--label key=value` metadata.
`list --owner KIND:ID` filters by canonical owner identity, resolving relative
heartbeat paths and numeric PID spellings without checking owner liveness.

### AXI exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success, including already absent releases and empty sweeps |
| `1` | Berth operation failed; cleanup records are retained on resource failure |
| `2` | Invalid command, argument, or flag; no child was started |
| Child's code | Reserved for `berth job` when its child runs to completion |
| `124` | Reserved for `berth job` when its deadline kills the child |
| `75` | Reserved for `berth job` when no slot frees within the wait, `EX_TEMPFAIL` |

The future `berth job` uses berth's own codes 1 and 2 only when no child was
started. Child completion passes through the child's code even when it overlaps
these numbers. `berth job --help` documents this contract now, but `berth job`
itself is not implemented and exits 2 without starting a child.

## Sweep from a host timer

Berth does not sweep by itself. Run `berth sweep --confirm` from cron, a systemd
timer, launchd, or your session teardown. Set the same absolute `BERTH_STATE`
and use an absolute executable path in the timer. Sweep frequency determines
how long a dead or expired lease can retain resources. Preview first to check
which resources will be deleted.

## State and safety

`BERTH_STATE` defaults to `$XDG_STATE_HOME/berth`, or
`~/.local/state/berth` when XDG is unset. State files and snapshots are private to
the invoking user. State contains resolved Compose values, which may include
secrets. Keep this directory on a local filesystem, not a network share.

A stable `lock` file serializes readers and writers through kernel `flock`.
Each state change writes and syncs a temporary file, renames it over `state.json`,
and syncs the directory. Berth saves cleanup intent **before** starting a
resource. If berth dies midway through create or delete, the next release or
eligible sweep retries the same unique resource. Kernel locks end on process
death. Corrupt or unsupported state is an error, never an empty lease list.

Each lease records the hostname of its authoritative machine. This slice never
acts on another machine's leases, even after expiry. It does not SSH to a host
or interpret a foreign PID as local. Run cleanup on the original machine with
its original state path. Do not share state between hosts or change the host's
name while leases exist.

Adapters pin the Docker context selected at creation. `DOCKER_HOST` overrides
are rejected; select a named context instead. Keep that context's endpoint
unchanged while leases exist. Berth never sweeps unleased resources. This slice
does not reconstruct leases after state loss; keep the state directory until
all managed resources are released. Small resource snapshots remain after
release for diagnosis and may be removed once their leases are absent.

Berth is a cooperation protocol, not a security boundary for programs with the
same Unix user. SSH session-as-liveness, machine-local CPU and memory admission
with owner room, job deadlines, and worktree transport belong to the next
slices. Kubernetes-backed machines are deferred. A resident scheduler is not
required for these leases; revisit it only when measured queue fairness or
push-event requirements cannot be met by CLI invocations and host timers.

## Develop and verify

```sh
 make check
 make integration
```

`make check` runs formatting checks, vet, race-enabled tests, and build. Unit
tests cover owner death, PID reuse, heartbeat staleness, expiry with a live
owner, unknown liveness, machine boundaries, retry, state corruption, locking,
and the CLI lifecycle. Real adapter tests are isolated behind the `integration` build tag and run with
`make integration`. They create unique kind, k3d, and Compose resources, then
verify cleanup. Missing adapter tools, Docker, or Compose fail the command rather
than skip; failed cleanup retains the state directory and reports its path.

CI runs on GitHub-hosted `ubuntu-24.04` and executes vet, unit tests, build,
cross-builds, and `make integration`. It installs pinned, checksum-verified
kind and k3d binaries; the integration target fails if Docker or any adapter
prerequisite is unavailable.
The live `main-required-ci` ruleset requires the checks `PR title` and
`PR must be raised via no-mistakes`; the workflow also provides the Go validation
check named `main-required-ci`.
