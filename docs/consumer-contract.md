# Consumer contract

This page is for any tool that leases resources for work it starts and must get them back when the work ends: an agent supervisor, a coding agent session, a CI job, or a shell script. It defines the verbs, output fields, exit codes, and retry rules that version 1 of the contract keeps stable.

Agents read AXI: TOON on stdout, selected by `--axi`, `DIBS_AXI=1`, or a stdout that is not a terminal. Scripts read `--json`, which carries the same fields and never truncates. Each command's `--help` states its statuses, reasons, and exit codes, and is the contract of record. See [Output modes](output.md) for how the modes are selected and rendered.

Source: `internal/cli/contract.go` (versioned results and refusals), `internal/cli/cli.go` (`extend`, `release`, `--key`, `--pending`), `internal/cli/inventory.go` (`adopt`), `internal/lease/manager.go` and `internal/lease/lease.go` (renew and release), and `internal/lease/pending.go` (reconcile, reclaim, explain, and idempotency). Tests: `internal/lease/pending_test.go`, `internal/lease/lease_test.go`, `internal/cli/contract_test.go`, and `TestIntegrationPendingCrashRecovery` in `internal/cli/pending_integration_test.go` (build tag `integration`, run with `just integration`).

## Verbs

| Verb | Command | Use it when |
| --- | --- | --- |
| acquire | `dibs create --adapter T [--key K]` | dibs builds the resource |
| acquire, step 1 | `dibs create --adapter T --pending [--name HANDLE] [--key K]` | you build the resource yourself |
| acquire, step 2 (register) | `dibs adopt T HANDLE --pending ID` | the resource you built now exists |
| adopt | `dibs adopt T HANDLE --owner O --ttl D [--key K]` | the resource existed before any lease |
| reconcile | `dibs reconcile [ID] [--confirm]` | you restarted after a crash and may have built a resource without registering it |
| renew | `dibs extend ID --ttl D` | the work still needs the resource |
| heartbeat | touch the `heartbeat:<file>` owner file | the owner is a heartbeat; no dibs call is involved |
| release | `dibs release ID [--confirm]` | the work is done; omit `--confirm` to preview |
| reclaim | `dibs reclaim ID [--confirm]` | you believe the owner died and want dibs to prove it before destroying anything |
| list | `dibs list [--owner O]` | you need every lease, or the leases of one owner |
| explain | `dibs explain ID` | you need to know whether sweep or reclaim would act on a lease, and why |

`T` is an adapter name. `create --pending`, `adopt`, and `reconcile` work with every adapter that supports adoption: `kind`, `k3d`, and `compose` today. `create` without `--pending` also supports `none`, which records a metadata-only lease.

## Output

Every success and refusal from a contract surface carries `contract: 1`. A success carries `status`. A refusal carries `reason`. Responses that return a lease carry it under `lease`; release identifies its lease with `id`. Every response carries `help[]` with the next command to run.

A contract surface is `reconcile`, `reclaim`, `explain`, `extend`, and `release`, or an invocation that uses `--pending` or `--key`. This versions the existing `extend` refusals (now exit 3) and `release` status values (now `preview`, `released`, `already_done`); these are intentional contract changes. Other invocations keep their original output and exit codes.

AXI, for a retried register step:

```text
contract: 1
help[2]: Run dibs release 3f2a9c...0e1d to preview cleanup,"Run dibs --axi ps --full for all fields, complete text and all matching leases"
lease:
  id: 3f2a9c...0e1d
  key: "task-7:lab"
  liveness: alive
  owner: "heartbeat:/run/worker-7.beat"
  resource: "kind:lab-7"
  ...
status: already_done
```

AXI, for a refusal:

```text
code: COMMAND_FAILED
contract: 1
error: owner pid:4242 is alive; reclaim refused
exit_code: 3
help[1]: Wait for the owner to end, or have its owner run dibs release 3f2a9c...0e1d --confirm
reason: owner_alive
```

JSON has the same keys. The full lease record is under `record`, which AXI shows with `--full`.

```json
{"code":"COMMAND_FAILED","contract":1,"error":"owner pid:4242 is alive; reclaim refused","exit_code":3,"help":["Wait for the owner to end, or have its owner run dibs release 3f2a9c...0e1d --confirm"],"reason":"owner_alive"}
```

Lease views add `pending: true` while a pending lease waits for its resource, and `key` when the lease has an idempotency key. Both fields are absent otherwise.

### Quiet output

For `create --quiet` with `--pending` or `--key`, stdout contains only the lease ID and a newline on success, including an `already_done` retry. Exit codes carry the result; failures and refusals print no raw lease ID and retain their usual error output. Explicit `--axi` or `--json` cannot be combined with quiet contract creates; the command exits 2 before reading or writing state. Environment-selected AXI and pipe detection do not conflict. Plain `create --quiet` without `--pending` or `--key` keeps its existing behavior.

## Exit codes

| Code | Meaning | What to do |
| --- | --- | --- |
| `0` | Success; read `status` (quiet contract creates print only the lease ID) | Continue |
| `1` | dibs or a resource tool failed; records are retained | Retry later; read `error` |
| `2` | Usage error; nothing changed | Fix the command |
| `3` | Refused; nothing changed | Act on `reason` |

Exit 3 always means that dibs changed nothing. A consumer can retry a 1, but should not retry a 3 without changing something first.

## Statuses

| Command | Statuses |
| --- | --- |
| `create --pending` | `recorded`, `already_done` |
| `create --key` | `created`, `already_done` |
| `adopt --key` | `adopted`, `already_done` |
| `adopt --pending` | `bound`, `already_done` |
| `extend` | `renewed`, `reattached` |
| `release` | `preview`, `released`, `already_done` |
| `reconcile` | top level `preview` or `reconciled`; per lease in `results[]`: `bindable`, `bound`, `absent`, `ambiguous`, `conflict`, `unknown`, `already_done` |
| `reclaim` | `preview`, `reclaimed`, `already_done` |
| `explain` | `explained` |

`already_done` means that the call had already happened and dibs did nothing this time. The response still describes the lease, so a retry gets the same answer as the first call.

## Reasons

| Reason | Meaning | What to do |
| --- | --- | --- |
| `not_found` | No lease with that ID exists on this machine | Acquire a new lease with `dibs create`; a released lease cannot come back |
| `wrong_machine` | The lease belongs to another machine | Run the command on that machine |
| `not_pending` | The lease already covers a different resource | Run `dibs explain ID` |
| `already_leased` | Another lease owns or awaits the resource | Run `dibs list`; never lease one resource twice |
| `key_conflict` | The key names a lease for a different request | Repeat the original request, or use a new key |
| `handle_exists` | `create --pending --name` named a resource that already exists | Use `dibs adopt` for an existing resource |
| `handle_mismatch` | The handle differs from the one the pending lease recorded | Pass the recorded handle; `dibs explain ID` shows it |
| `resource_absent` | The resource to register or adopt does not exist, including a protected lease whose resource disappeared before late renewal | Create it, then retry after resolving the old lease |
| `resource_unknown` | A protected lease's resource presence could not be verified during late renewal | Retry when the resource tool is reachable; the lease is retained |
| `ambiguous` | More than one resource matches a pending lease | Register the right one with `dibs adopt T HANDLE --pending ID` |
| `owner_alive` | `reclaim` found the owner alive, even if the lease expired | Leave it, or release it as its owner |
| `owner_unknown` | `reclaim` could not verify the owner, or `extend` could not verify a live owner | Retry later; the lease is kept |
| `expired` | `extend` found an unprotected lease expired while its owner is alive | Release it and acquire again; do not create a second lease for a resource still covered |
| `owner_dead` | `extend` found the owner dead | Run `dibs reclaim ID --confirm` or `dibs sweep --confirm` |
| `job_deadline` | A job reservation cannot be extended | Start a new `dibs job` with `--timeout` |

## Idempotency

Pass `--key K` to `create` or `adopt` when a retry must not act twice. A key names one live lease on this machine. A retry returns `already_done` only when owner, adapter, request mode, and any pinned handle match. Pending creates also require the same Docker context, Docker host, and Compose file, including after the pending lease is bound. An `adopt` retry also requires the same resource name and Docker context or Docker host. The same key with a different request is refused with `key_conflict`. A key becomes free again when its lease is released.

Use a key that names the work and the role of the resource, such as `<task>:<attempt>:<role>`. Do not use a random value: after a crash, the restarted caller must be able to compute the same key again.

The other verbs are idempotent without a key:

- `adopt T HANDLE --pending ID` returns `already_done` when the lease already covers that handle.
- `reconcile ID` returns `already_done` for a lease that is no longer pending.
- `release ID --confirm` and `reclaim ID --confirm` return `already_done` when the lease is already gone. Repeating a successful `extend` renews the same lease again.

A keyed `create` that failed after it saved its lease returns `already_done` on retry, with the partly created resource still under that lease. Release the lease and acquire again when the first attempt reported exit 1.

## Crash-safe acquire

When you build the resource yourself, record the lease first. Then a crash at any point leaves a lease that dibs can finish.

1. Run `dibs create --adapter T --pending --owner O --ttl D --key K`. dibs records the lease, the scope where the resource will live (the selected Docker context and its endpoint, and the Compose file for `compose`), and the handle if you pass `--name`. With `--name`, dibs refuses with `handle_exists` when that handle is already in use.
2. Build the resource. Either give it the handle you pinned with `--name`, or, when your tool picks the handle, stamp it with the label `io.dibs.lease=<lease id>`.
3. Run `dibs adopt T HANDLE --pending ID`. dibs checks that the resource exists and that no other lease owns it, then captures its cleanup configuration under the same lease ID.

A resource matches a pending lease only by evidence recorded before it existed: the pinned handle or the `io.dibs.lease` label. Age, idleness, and name patterns never match. A resource that matches a pending lease is not an orphan, and plain `adopt` refuses it with `already_leased`.

After a crash, the restarted caller runs the same keyed `create --pending` and gets the same lease back. It then runs `dibs reconcile ID`:

- `bindable`: the resource exists. Run `dibs reconcile ID --confirm` (or `adopt --pending`) to register it. Do not build it again.
- `absent`: nothing was built. Build it now, then register it.
- `ambiguous`: several resources match. dibs binds none of them. Register the right one explicitly, and release the others.
- `conflict`: the match is under another lease. dibs binds nothing.
- `unknown`: discovery failed. Retry later.

`dibs reconcile` without an ID previews every pending lease on this machine, and `--confirm` binds each single unclaimed match.

Release and sweep finish a pending lease too. `release --confirm`, `reclaim --confirm`, and an eligible `sweep --confirm` first look for the lease's match. A single match is registered, destroyed, and verified gone. No match and no conflict means nothing was built, and dibs forgets the lease. An ambiguous match retains the lease: `release` exits 3 with `ambiguous`, `sweep` exits 1, and `reclaim` exits 3. A match already claimed by another lease retains this pending lease and refuses with `already_leased`; failed discovery retains the lease and exits 1.

If sweep forgets a pending lease while its caller is still building the resource, the later `adopt --pending` exits 3 with `not_found`. The caller still holds the resource it built and must destroy it or adopt it under a new lease.

## Renew and heartbeat

`dibs extend ID --ttl D` renews an unexpired lease whose owner is alive. For a lease created with `--protect-live-owner`, a late renewal reattaches only when its owner is alive and the pinned resource still exists; it reports `status: reattached`. Otherwise success reports `status: renewed`. Both succeed with exit 0 and may be repeated. Every extend refusal uses exit 3 and a stable reason: `not_found`, `wrong_machine`, `expired`, `owner_dead`, `owner_unknown`, or `job_deadline` for ordinary renewals, and `resource_absent` or `resource_unknown` for protected late renewals. When both expiry and owner status would refuse renewal, dead or unknown owner takes precedence. Exit 1 means the operation itself failed. Never answer a refused renewal by creating a second lease for a resource that the first lease still covers.

Protection is enabled per lease with `--protect-live-owner`, gated by `DIBS_LIVE_OWNER_PROTECTION=1` or user config `live_owner_protection: true`. Sweep and job admission spare alive and unknown owners only for protected leases; unprotected leases retain expiry-based reclamation. `reclaim` always requires proven owner death.

Renew at the latest when one third of the TTL remains. For a `heartbeat:<file>` owner, touch the file at most every staleness/3. The heartbeat is liveness, not renewal: expiry still reclaims a lease whose owner keeps touching its file.

## Reclaim

`dibs release ID` returns `preview` without `--confirm`, `released` after successful confirmed cleanup, and `already_done` when no lease exists; each is contract 1 and exit 0. A foreign-machine lease refuses with `wrong_machine` (exit 3). Ambiguous pending cleanup refuses with `ambiguous` (exit 3), while cleanup or discovery failures exit 1 and retain the record. This intentionally changes the previous human status strings to a stable enum.

`dibs reclaim ID` previews, and `--confirm` acts only when the owner check returns `dead` on this machine. An alive owner is refused with `owner_alive` even after the lease has expired, and unknown liveness is refused with `owner_unknown`. Reclaim destroys the resource, verifies it is gone, and then forgets the lease. A cleanup failure retains the lease and exits 1.

`reclaim` is the only command that requires proven owner death. `sweep --confirm` and job admission still reclaim expired leases even when the owner is alive or unknown, so consumers must renew before expiry. `dibs explain ID` reports both decisions without acting: `sweep_would_reclaim`, `reclaim_would_reclaim`, `liveness`, `expired`, `this_machine`, and a readable `why`.

## Map your lifecycle onto the verbs

| Lifecycle event | Call |
| --- | --- |
| Work starts and dibs can build the resource | `dibs create --adapter T --owner O --ttl D --key K` |
| Work starts and you build the resource | `dibs create --adapter T --pending --owner O --ttl D --key K`, build it, `dibs adopt T HANDLE --pending ID` |
| You take over a resource that already exists | `dibs adopt T HANDLE --owner O --ttl D --key K` |
| You observe that the work still needs the resource | `dibs extend ID --ttl D` |
| You restart after a crash | Repeat the keyed acquire, then `dibs reconcile ID` |
| Work ends | `dibs release ID --confirm` |
| You learn that a worker died | `dibs reclaim ID --confirm` |
| Periodic backstop | `dibs sweep --confirm` from a host timer |
| Audit | `dibs list --owner O`, `dibs explain ID` |

Choose an owner whose liveness is the work's own. Use the worker process (`pid:` or `session:`) or a heartbeat file that the worker itself touches. If the owner is the process that starts the work, or a supervisor that touches the heartbeat on the worker's behalf, then that process exiting reads as the work dying, and its leases become reclaimable.

Record the lease ID next to your own record of the work. Store the key you used, so that a restart can find the lease again without the ID.

### A supervisor that starts workers

```sh
key="$task:$attempt:lab"
id=$(dibs create --adapter kind --pending --name "lab-$task" \
  --owner "heartbeat:$state/$task.beat" --ttl 4h --key "$key" --quiet)
kind create cluster --name "lab-$task"
dibs adopt kind "lab-$task" --pending "$id"
# On each observation of the worker:
dibs extend "$id" --ttl 4h
# When the worker finishes:
dibs release "$id" --confirm
# After a supervisor restart, before building anything again:
dibs create --adapter kind --pending --name "lab-$task" \
  --owner "heartbeat:$state/$task.beat" --ttl 4h --key "$key" --quiet
dibs reconcile "$id" --confirm
```

### An agent session

The session's own shell owns the lease, so the lease becomes reclaimable when the session ends.

```sh
export DIBS_AXI=1
id=$(dibs create --adapter compose --file compose.yaml --owner "pid:$$" --ttl 2h \
  --key "session-$SESSION_ID:db" --quiet)
dibs explain "$id"
```

### A CI job

The job's runner process owns the lease, so a cancelled job leaves a lease that `reclaim` or `sweep` can finish.

```sh
id=$(dibs create --adapter k3d --owner "pid:$RUNNER_PID" --ttl 1h \
  --key "ci-$RUN_ID-$JOB:cluster" --quiet)
trap 'dibs release "$id" --confirm' EXIT
```

### A shell script that builds its own resource

```sh
# stack.yaml labels each service with io.dibs.lease=${DIBS_PENDING}
id=$(dibs create --adapter compose --pending --file stack.yaml --owner "pid:$$" --ttl 1h --quiet)
DIBS_PENDING="$id" docker compose -p "stack-$$" -f stack.yaml up -d
dibs reconcile "$id" --confirm
```

## Extension seams

New resource types come through the adapter contract described in [Adapters](adapters.md). A pending lease needs only the adoption seam: `Definition.Discover` rows with `name` as the handle and optional `labels`, where dibs reads `io.dibs.lease`, and `Prepare` with `PrepareRequest.Existing.Adopted`. An adapter whose tool assigns handles should report the caller's stamp in `labels`.

New owner kinds come through `owner.Definition{Name, Bind, Check}` registered in `cli.newOwners` behind their own opt-in. The contract commands consume only the `alive`, `dead`, or `unknown` that `Check` returns, so a new owner kind needs no change here.

## Invariants

- Exit 3 changes nothing. Every refusal returns before a state write or a resource call that acts. (`Refusal` in `internal/lease/pending.go`)
- A resource is never under two leases, and a pending lease is never bound twice. Binding runs under the state lock after the same ownership check that `adopt` runs. (`claimant`, `bindPending`; `TestPendingPinnedHandleLifecycle`)
- A pending lease matches only by its pinned handle or its `io.dibs.lease` label. (`matches`; `TestReconcileByLabel`)
- An ambiguous match or failed discovery never binds and never forgets. (`settlePending`; `TestReconcileAmbiguousConflictUnknown`)
- `reclaim` destroys only on proven owner death on this machine. (`Reclaim`; `TestReclaimRequiresProvenDeath`)
- A key names at most one live lease per machine. (`keyed`, `store.validate`; `TestIdempotencyKeys`)
- Invocations without a contract surface keep their exact output and exit codes. (`TestBaselineCommands`)

## Planned

- Terminal and pooled-worktree adapters, which plug into the adoption seam above.
- Contract surfaces for `--machine`. The new commands run on the local machine only.
