---
status: accepted
---

# Reclaim with proof before forgetting

Managed-resource cleanup destroys the recorded resource and verifies that it is gone before removing the lease record. A delete command's success is insufficient: partial creation, interrupted deletion, or a surviving resource must remain retryable. Cleanup uses the recorded identity and creation scope, tolerates already absent resources, and retains the lease on cleanup or verification failure.

The current kind, k3d, and Compose adapters perform verification inside `Destroy`. Kind and k3d check cluster absence. Compose checks project containers, volumes, and networks after `down --volumes --remove-orphans`, excluding declared external resources. `releaseLease` removes the record only after `Destroy` succeeds and restores its in-memory record if saving fails. A metadata-only lease has no external resource to verify.

## Existing process exception

The process adapter does not provide universal verify-gone proof. If its recorded leader is missing or reaped, it warns and permits release without signaling an unverifiable group. Descendants may survive. A present leader with unknown or mismatched identity instead retains the lease. With a matching identity, cleanup signals the group and checks for remaining non-zombie members. A reservation that never started has no process to clean up.

This exception is current behavior, covered by `TestDestroyDoesNotSignalGroupAfterLeaderReaped`, not a guarantee that descendants are absent. Process-group escape is also outside the cleanup guarantee. The approved [extension contract](0005-adapter-and-owner-extension-contracts.md) requires explicit verify-gone before forgetting. Reconciling that stronger contract with the process exception needs a separate behavior-change decision, not a restructure that silently changes cleanup semantics.

Evidence: [resource adapter cleanup](../../internal/adapter/compose/compose.go), [record removal](../../internal/lease/lease.go), [process cleanup](../../internal/lease/job_process.go), [process exception test](../../internal/lease/job_test.go), and [documented safety limits](../../README.md#run-a-local-job).
