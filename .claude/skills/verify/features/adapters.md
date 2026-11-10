# Compose and clusters

A user creates an attributed stack or throwaway Kubernetes cluster, uses the service, and releases its resources without changing the caller's kubeconfig.

## Sub-features

- `compose-http`: nginx and Redis containers carry lease labels; nginx serves HTTP.
- `compose-cleanup`: preview preserves containers; confirmed release removes containers, networks, and volumes after the source file disappears.
- `kind-ready`: a real kind cluster has Ready nodes and a private kubeconfig.
- `k3d-ready`: a real k3d cluster has Ready nodes and a private kubeconfig.
- `cluster-cleanup`: release removes the cluster without changing the caller's kubeconfig.

## How to get to it (user POV)

Run `berth create --adapter compose --file <compose.yaml>`, `berth create --adapter kind`, or `berth create --adapter k3d`, with a live `--owner` and positive `--ttl`. Inspect via `berth ps`, Docker, HTTP, and `kubectl`; release via `berth release <id> --confirm`.

## Driving it with verify.py

Preconditions: Docker, Compose, kind, k3d, and kubectl all pass the skill's doctor checks. Images and package registries must be reachable.

- **Run:** `.claude/skills/verify/verify.py adapters --evidence "$PROOF/adapters"`.
- **Compose:** the helper creates a real nginx/Redis stack with a dynamic loopback HTTP port and a managed data volume. `docker ps` must return two lease-labeled containers. `docker inspect` must show owner, machine, expiry, and lease labels. An HTTP GET must return the nginx welcome page.
- **Release:** remove the original Compose file, preview release, and verify both containers still run. Confirm release, then query Docker by the lease label; no container, volume, or network may remain.
- **Clusters:** create each adapter, then run `kubectl --kubeconfig <state>/resources/<id>/kubeconfig get nodes -o json` for kind and the corresponding `.../<id>/k3d` path for k3d. Require nonempty nodes with Ready=True.
- **Isolation:** compare a disposable caller kubeconfig before and after create and release. Require byte-for-byte equality.
- **Deletion:** release through berth, then require the unique name to be absent from `kind get clusters` or `k3d cluster list -o json`. Require zero final leases.
- **Proof:** retain HTTP content, Docker inspections, node responses, commands, and cleanup observations in `events.jsonl`.

## Gotchas

- Do not use a shared fixed port or a developer's kubeconfig. The helper uses an ephemeral loopback port and its own sentinel file.
- Docker context selection remains the caller's choice. The helper does not switch contexts or restart the engine. `DOCKER_HOST` is removed inside the helper because berth requires a named context.
- Managed volumes are deleted. Never supply a real Compose file containing valuable data.
- Existing `just integration` additionally tests cleanup retry after external deletion; CI keeps that target too.
