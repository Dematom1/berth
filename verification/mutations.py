#!/usr/bin/env python3
"""Prove real-code checks reject breaks, only in disposable temp copies."""
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parent.parent
# Exact edits are mutation mechanics, never evidence of correctness. Each
# oracle executes the mutated binary/function and asserts observable behavior.
MUTATIONS = [
    ("unknown", "lease.go", "live == Dead || !now.Before(l.Expires)",
     "live == Dead || live == Unknown || !now.Before(l.Expires)", "^TestFormalLeaseDomain$"),
    ("lock", "store.go", "err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)",
     "err = nil", "^TestStoreDurabilityLockAndCorruption$"),
    ("budget", "job.go", "return request.Cores <= capacity.Cores && request.Memory <= capacity.Memory",
     "return true", "^TestFormalBudgetDomain$"),
    ("fifo", "job.go", "first = queued && w.ID == l.ID && now.Before(w.Expires)",
     "first = true", "^TestJobsRealProcesses$/^FIFO"),
    ("gate", "job.go", "if err != nil || n != 1 || b[0] != 1 {",
     "if n == 99 && err == nil && b[0] == 1 {", "^TestJobHelperEOFDoesNotRun$"),
    ("cleanup", "lease.go", "if err := a.Destroy(ctx, l); err != nil {",
     "if err := a.Destroy(ctx, l); err != nil {\n\t\tdelete(s.Leases, id)",
     "^TestSweepPreviewRetryAndIdempotence$"),
    ("deadline", "job.go", "return childExited(pid)", "return false, nil",
     "^TestDeadlineProbeSeesCompletedUnreapedChild$"),
    ("eof", "ssh.go", "_, _ = readFrame(scanner)\n\t\tcancel()",
     "_, _ = readFrame(scanner)", "^TestFormalEOF$"),
    ("result", "ssh.go", "return f.Code", "return 76", "^TestFormalAuthoritativeResult$"),
    ("orphan", "job.go", "if alive {\n\t\t\treturn errOrphanSuspected",
     "if alive {\n\t\t\treturn nil", "^TestDestroyDoesNotSignalGroupAfterLeaderReaped$"),
    ("late-start", "job.go", "if !time.Now().Before(deadline) {",
     "if false && !time.Now().Before(deadline) {", "^TestJobHelperRefusesExpiredDeadline$"),
]


def main():
    for name, file, old, new, test in MUTATIONS:
        with tempfile.TemporaryDirectory(prefix=f"berth-mutant-{name}-") as tmp:
            dest = Path(tmp)
            for source in [*ROOT.glob("*.go"), ROOT / "go.mod", ROOT / "go.sum"]:
                shutil.copyfile(source, dest / source.name)
            path = dest / file
            source = path.read_text()
            if source.count(old) != 1:
                raise SystemExit(f"{name}: mapping drift; re-read code before updating mutation")
            path.write_text(source.replace(old, new))
            result = subprocess.run(
                ["go", "test", "-tags", "formal", "-count=1", "-run", test, "-timeout", "30s", "."],
                cwd=dest, capture_output=True, text=True, timeout=60,
            )
            output = result.stdout + result.stderr
            # A syntax/build/tool failure is not evidence the behavior was caught.
            if result.returncode != 1 or "--- FAIL: Test" not in output or "[build failed]" in output:
                print(output)
                raise SystemExit(f"FAIL {name}: mutation was not behaviorally rejected")
            print(f"REJECTED real-code mutant {name}", flush=True)


if __name__ == "__main__":
    main()
