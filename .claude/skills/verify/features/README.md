# Berth verification map

This map is the maintained source for the demo's user journeys. Run the matching helper flow from the checkout root. Each invocation needs a new absolute `--evidence` directory outside the checkout.

## Preconditions

- Go 1.26+, Python 3, and a POSIX shell are available.
- Adapter journeys also need a running Docker engine, Compose, kind, k3d, and kubectl.
- SSH journeys need Docker, SSH, ssh-keygen, and network access to Alpine packages.
- The helper creates its own state, budget config, resource names, and kubeconfig. Never reuse a user's state or change their Docker context.
- Require the helper's version probe and initial empty listing to succeed.

## Proof conventions

Keep `events.jsonl`, background logs, and `result.json`. Require both success and cleanup. A mutation needs a second observation, not just a successful exit. Unknown prerequisites or failed cleanup mean failure; never mark an unrun entry point as verified. Commands and aliases listed as manual extensions below are not automatically covered unless the flow says so.

## Features

- [Leases and sweep](leases.md): home/help, plain lease, renewal, preview, release, and owner death.
- [Heartbeat owners](heartbeat.md): staleness, refresh, and missing heartbeat cleanup.
- [Jobs and FIFO admission](jobs.md): 0, child 7, 124, 75, 2; concurrent no-backfill.
- [Compose and clusters](adapters.md): HTTP, attribution, Ready nodes, private kubeconfigs, and deletion.
- [SSH machines](ssh.md): installation, remote-authoritative admission, session death, binary output, and 76.

The scripts shorten demo waits and use dynamic loopback ports rather than port 8099. They keep the same real user paths and assert the same outcomes. The fixed test budget has two usable cores, independent of the host's capacity; reservations are not OS limits.
