---
status: accepted
---

# Separate resource adapters from owner checks

Berth owns the lease lifecycle, owner liveness, expiry and renewal, reclaim with proof, admission, and accounting and audit. Resource construction details belong to adapters. Task-running policy, scheduling daemons, secrets, and network policy are outside this core ownership. The existing local-job and SSH commands remain supported during a behavior-preserving restructure.

## Approved direction, not an implemented extension interface

The extension model approved on 2026-11-09 separates two small, versioned contracts. The resource Adapter contract provides create, idempotent destroy, exists or verify-gone, and describe. The Owner contract checks liveness and identity with alive, dead, and unknown outcomes. Adapters and owner integrations never mutate berth state. Berth retains authority over admission and record removal, requiring verify-gone before forgetting a managed resource.

The restructure direction places the contracts in `internal/adapter` and `internal/owner` with explicit registries. A conformance kit checks repeatable destroy, already absent resources, truthful verify-gone, honored timeouts, and required labels. Its planned commands are `berth adapter test <name>` and an owner equivalent. This keeps third-party integration from requiring changes throughout the lease lifecycle.

Additions follow the behavior-preserving restructure in separate slices. First comes the conformance kit and the explicit contracts, then an exec adapter and an exec owner driven by local files, then out-of-process plugins named `berth-adapter-<name>` and `berth-owner-<name>` on PATH with a small versioned JSON protocol over stdin and stdout and a community catalog. Calls have timeouts, path or checksum pinning is optional, and arguments and logs must not expose secrets.

Built-ins require widespread use and CI-testable behavior, with issue-first review. Kind, k3d, Compose, and Docker are the approved built-in category, not a claim that a standalone Docker adapter exists today. Other integrations use exec or cataloged plugins. State-schema migrations, a stable CLI and AXI contract, semver, and a deprecation policy are part of the approved stability direction.

## Current implementation limits

The current Go `Adapter` interface has only `Create` and `Destroy`, with verification inside built-in cleanup. Owner checks are concrete functions over a recorded `Owner`, not an extension registry. Exec integration, the plugin protocol, the conformance commands, and schema migrations are not implemented. The existing state format is version 1 and rejects unsupported versions. Label application currently described for managed resources is Compose-specific.

The [process cleanup exception](0002-reclaim-with-proof.md#existing-process-exception) must remain visible when designing the stronger contract. This decision records the approved destination. It does not authorize changing existing behavior in the docs or restructure slice.

Evidence for current behavior: [adapter interface and registry](../../adapter.go), [owner model](../../owner.go), [state validation](../../store.go), and [current supported commands](../../README.md#command-reference).
