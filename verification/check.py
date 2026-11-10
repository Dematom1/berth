#!/usr/bin/env python3
"""Bounded TLC checks; expected counterexamples are findings, not proofs."""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parent
VERSION = "1.7.4"
SHA256 = "936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88"


def main():
    if sys.argv[1:] not in ([], ["--strict"]):
        raise SystemExit("usage: python3 verification/check.py [--strict]")
    logs = Path(os.environ.get("FORMAL_LOG_DIR") or tempfile.mkdtemp(prefix="dibs-formal-")).resolve()
    logs.mkdir(parents=True, exist_ok=True)
    jar = Path(os.environ.get("TLA2TOOLS_JAR", logs / "tla2tools.jar")).resolve()
    if not jar.exists():
        urllib.request.urlretrieve(
            f"https://github.com/tlaplus/tlaplus/releases/download/v{VERSION}/tla2tools.jar", jar
        )
    if hashlib.sha256(jar.read_bytes()).hexdigest() != SHA256:
        raise SystemExit("tla2tools.jar checksum mismatch")
    java = os.environ.get("JAVA", shutil.which("java"))
    if not java:
        raise SystemExit("Java 17+ required (CI uses Java 21)")

    for model in ROOT.glob("*.tla"):
        shutil.copyfile(model, logs / model.name)

    def check(name, module, constants, invariants, violation=None, temporal=False):
        config = logs / f"{name}.cfg"
        config.write_text(
            "SPECIFICATION Spec\nCHECK_DEADLOCK FALSE\nCONSTANTS\n"
            + "\n".join(f"  {k} = {'TRUE' if v else 'FALSE'}" for k, v in constants.items())
            + ("\nPROPERTIES\n  " if temporal else "\nINVARIANTS\n  ")
            + "\n  ".join(invariants) + "\n"
        )
        result = subprocess.run(
            [java, "-Xmx1g", "-cp", str(jar), "tlc2.TLC", "-workers", "1",
             "-metadir", str(logs / f"{name}-states"), "-config", config.name, module],
            cwd=logs, capture_output=True, text=True, timeout=120,
        )
        output = result.stdout + result.stderr
        (logs / f"{name}.log").write_text(output)
        if violation:
            if temporal:
                ok = result.returncode == 13 and "Temporal properties were violated." in output
            else:
                ok = result.returncode == 12 and f"Invariant {violation} is violated." in output
        else:
            ok = result.returncode == 0 and "Model checking completed. No error has been found." in output
        if not ok:
            print(output, file=sys.stderr)
            raise SystemExit(f"FAIL {name}: unexpected TLC outcome; see {logs}")
        print(f"{'COUNTEREXAMPLE' if violation else 'PASS'} {name}", flush=True)

    lease = {"UnsafeUnknown": False}
    admission = {"BypassFIFO": False, "IgnoreBudget": False}
    job = dict.fromkeys(["SkipDurableGate", "DropOnFailure", "OverwriteResult", "WrongDeadline", "IgnoreEOF"], False)
    check("lease", "Lease", lease, ["ForeignSafe", "UnknownSafe", "FailureRetains"])
    check("admission", "Admission", admission, ["BudgetSafe", "NoBackfill", "QueueUnique"])
    check("job-session", "JobSession", job,
          ["DurableBeforeExec", "FailureRetains", "CleanupBeforeDelete", "CompletionWins", "ReceivedResult"])
    check("ssh-eof-liveness", "JobSession", job, ["EOFEventuallyCancels"], temporal=True)
    check("mutant-eof", "JobSession", {**job, "IgnoreEOF": True},
          ["EOFEventuallyCancels"], "EOFEventuallyCancels", temporal=True)
    check("live-owner-before-expiry", "Lease", lease, ["NeverReclaimLive"])
    # Never hide a new failure behind a known one: each property runs separately.
    for name, module, constants, invariant in [
        ("F2-orphan-group", "JobSession", job, "NoUnrecordedGroup"),
        ("F3-late-start", "JobSession", job, "NoLateStart"),
    ]:
        check(name, module, constants, [invariant], invariant)
    # Sensitivity checks: each passing safety family must reject a broken model.
    for name, module, constants, flag, invariant in [
        ("unknown", "Lease", lease, "UnsafeUnknown", "UnknownSafe"),
        ("fifo", "Admission", admission, "BypassFIFO", "NoBackfill"),
        ("budget", "Admission", admission, "IgnoreBudget", "BudgetSafe"),
        ("durable", "JobSession", job, "SkipDurableGate", "DurableBeforeExec"),
        ("cleanup", "JobSession", job, "DropOnFailure", "CleanupBeforeDelete"),
        ("deadline", "JobSession", job, "WrongDeadline", "CompletionWins"),
        ("result", "JobSession", job, "OverwriteResult", "ReceivedResult"),
    ]:
        check(f"mutant-{name}", module, {**constants, flag: True}, [invariant], invariant)
    print(f"TLC logs and counterexample traces: {logs}")
    print("NOT VERIFIED: F2 cleanup without orphan; F3 no start after deadline.")
    print("Known counterexamples reproduced; no product fixes applied. See verification/findings.md.")
    return 1 if "--strict" in sys.argv else 0


if __name__ == "__main__":
    raise SystemExit(main())
