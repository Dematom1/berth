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
    ("unknown", "internal/lease/lease.go", "live == ownerpkg.Dead || !now.Before(l.Expires)",
     "live == ownerpkg.Dead || live == ownerpkg.Unknown || !now.Before(l.Expires)", "^TestFormalLeaseDomain$"),
    ("lock", "internal/lease/store.go", "err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)",
     "err = nil", "^TestStoreDurabilityLockAndCorruption$"),
    ("budget", "internal/lease/job_admission.go", "return request.Cores <= capacity.Cores && request.Memory <= capacity.Memory",
     "return true", "^TestFormalBudgetDomain$"),
    ("fifo", "internal/lease/job_admission.go", "first = queued && w.ID == l.ID && now.Before(w.Expires)",
     "first = true", "^TestJobsRealProcesses$/^FIFO"),
    ("gate", "internal/lease/job_start.go", "if err != nil || n != 1 || b[0] != 1 {",
     "if n == 99 && err == nil && b[0] == 1 {", "^TestJobHelperEOFDoesNotRun$"),
    ("cleanup", "internal/lease/lease.go", "if err := cleanupErr; err != nil {\n\t\treturn false, err",
     "if err := cleanupErr; err != nil {\n\t\tdelete(s.Leases, id)\n\t\treturn false, err",
     "^TestSweepPreviewRetryAndIdempotence$"),
    ("deadline", "internal/lease/job_process.go", "return childExited(pid)", "return false, nil",
     "^TestDeadlineProbeSeesCompletedUnreapedChild$"),
    ("eof", "internal/remote/agent.go", "_, _ = readFrame(scanner)\n\t\tcancel()",
     "_, _ = readFrame(scanner)", "^TestFormalEOF$"),
    ("result", "internal/remote/ssh.go", "return f.Code", "return 76", "^TestFormalAuthoritativeResult$"),
]


def main():
    for name, file, old, new, test in MUTATIONS:
        with tempfile.TemporaryDirectory(prefix=f"berth-mutant-{name}-") as tmp:
            dest = Path(tmp)
            for source in [*ROOT.rglob("*.go"), *ROOT.glob("go.*"), *ROOT.rglob("*.golden")]:
                relative = source.relative_to(ROOT)
                if any(part.startswith(".") or part in {"bin", "build", "evidence"} for part in relative.parts):
                    continue
                target = dest / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(source, target)
            path = dest / file
            source = path.read_text()
            if source.count(old) != 1:
                raise SystemExit(f"{name}: mapping drift; re-read code before updating mutation")
            path.write_text(source.replace(old, new))
            result = subprocess.run(
                ["go", "test", "-tags", "formal", "-count=1", "-run", test, "-timeout", "30s",
                 "./internal/cli" if name in {"eof", "result"} else "./internal/lease"],
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
