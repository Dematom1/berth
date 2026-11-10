# Vision

`berth` exists so that agents can lease the resources their work needs, and every resource comes back when the work is done.
It serves AI agents, and the humans who share machines with them, and it turns "I need a kind cluster" or "I need four cores for this suite" into a lease with an owner, an expiry, and a guaranteed way back to zero.
It owns exactly one thing: the lease lifecycle and its guarantees.

## Berth owns the lease, not the thing

Berth owns the lease ledger and its state machine: owner, expiry, labels, and budget.
It owns owner liveness, judged alive, dead, or unknown against a recorded identity so a recycled PID or replaced file never passes as the original owner.
It owns expiry and extend; expiry is a hard backstop even for a live owner, and `extend` refuses dead, unknown, or expired owners.
It owns admission: per-machine core and memory budgets that always leave owner room, admitted FIFO by default.
It owns accounting and audit: who held what, for how long, and how it ended.
How a specific resource is built, scheduled, secured, or networked belongs to an adapter, an owner, or another tool, never to berth's core.

## Reclaim requires proof

A lease is reclaimable when its owner is dead or its lease has expired; unknown liveness alone never permits reclaim.
Berth records cleanup intent before anything is created, so a crash at any point leaves a lease that `release` or `sweep` can finish.
A lease record is forgotten only after destroy succeeds and verify-gone confirms the resource is absent; any failure keeps the lease and exits 1.
Release and sweep preview by default and act only with `--confirm`.
Berth reclaims only what is under a lease, and a host acts only on its own leases.
An existing resource enters berth only through an explicit `adopt` by a named owner, never because it looks old or idle.

## Two small contracts carry every extension

An Adapter implements create, idempotent destroy, exists or verify-gone, and describe.
An Owner implements check, returning alive, dead, or unknown with an identity.
Both contracts are small and versioned, and the conformance kit (`berth adapter test <name>` and its owner equivalent) proves idempotent destroy, tolerance of already-absent resources, truthful verify-gone, honored timeouts, and applied labels.
Extensions arrive in a fixed order of preference: the contracts first, then the exec adapter and exec owner from a local file or command, then out-of-process `berth-adapter-<name>` and `berth-owner-<name>` plugins on PATH speaking a small JSON protocol over stdin and stdout, listed in a community catalog.
Installing a plugin enables nothing by itself; like every opt-in, it stays off until a named setting turns it on.
An extension that cannot pass the conformance kit does not ship as conforming.

## Extensions cannot weaken the guarantees

Every adapter and owner call runs under a timeout.
Plugins never write berth state; berth alone records and forgets leases, and a plugin may receive only a read-only view of related leases through the protocol.
Secrets never appear in arguments, labels, logs, or that view.
A plugin that times out, crashes, or answers ambiguously yields unknown or a retained lease, never a reclaim.

## Growth is opt-in, contracts are stable

New behavior is opt-in by default: it ships off and turns on through a named config setting or environment variable, never by installing something.
Existing behavior changes only through a manual review by the maintainer.
Opt-in, non-breaking changes merge once the automated review process approves them.
Opt-in capabilities in vision include a resident sweeper, deadline-bounded backfill, owner preemption of lower-class jobs, cgroup or container enforcement of reservations, Kubernetes namespaces as machines through API calls, worktree sync into a per-lease remote directory, and interactive remote jobs.
Every guarantee holds without any of them: berth stays one Go binary that works per command, with sweep from a host timer or a caller and an SSH agent that lives exactly as long as its session.
A resource type is built in only when it is widely used and has a real integration test in CI, on a self-hosted runner where hosted runners cannot run it, as with macOS VMs; everything else is exec or a plugin, and a new built-in starts as an issue.
The state schema is versioned with migrations, the CLI, AXI, and exit-code contracts are stable, releases follow semver, and removals follow a published deprecation policy.
`berth job` exits with the child's code, 124 on deadline, 75 when no slot frees within the wait, 76 when SSH transport fails, 143 when preempted, and berth's own 1 and 2 only when no child started.

## Machines have roles

A machine's role decides how much of it berth may lease.
In the fleet that dogfoods berth, berth runs on the Mac, while the Lenovo is CI through runner-up and berth runs there idle-only, admitting work only when CI leaves the machine idle.
Each machine keeps its own ledger today; a shared ledger comes later.

## Humans and agents are both first-class

A human at a terminal gets a readable default, an agent gets AXI output automatically when stdout is not a terminal or explicitly via `--axi` or a harness hook, and scripts get `--json`.
Neither audience gets the degraded view, and an explicit flag always overrides detection.
Nothing prompts outside an explicitly interactive job, unknown flags fail with 2 before any state changes, and job child output passes through untouched.

## Future scope

Berth grows through new owners and adapters, delivered as exec, plugins, or earned built-ins; the lease core stays the same size.

## Non-goals

Berth is not a task runner, not a runtime between repositories and AI agents, not a scheduler daemon, not a Kubernetes operator, not a CI system, and not a security sandbox.
It does not manage secrets or network policy, build images, copy credentials to remote machines, or place work on a machine the caller did not name.
Conveniences such as named jobs in a project file sit on top of leases at most; they never become berth's identity.
Berth is a cooperation protocol among processes of the same user; enforced limits keep honest jobs honest and are not a security boundary.
It is a public, Apache-2.0 project for anyone running agents, dogfooded by the fleet that motivated it, and every slice ships green through real integration tests that fail rather than skip when a tool is missing.

A change aligns when it makes a lease harder to leak, reclaim more provable, admission more predictable, an extension easier to write and verify against the contracts, or the same answer clearer to both a human and an agent, and when new behavior arrives opt-in.
A change should be resisted when it reclaims by guess, forgets a lease before verify-gone, lets an extension write berth state, makes a resident process required, changes existing behavior without maintainer review, breaks a stable contract without deprecation, builds in a resource that has not earned it, serves one audience at the other's expense, or grows berth into a task runner, scheduler, sandbox, or orchestrator.
