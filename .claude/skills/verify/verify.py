#!/usr/bin/env python3
"""Drive the real berth CLI; retain commands, output, exit codes, and observations."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[3]


class Driver:
    def __init__(self, evidence):
        self.evidence = evidence
        evidence.mkdir(parents=True, exist_ok=False)
        self.log = (evidence / "events.jsonl").open("w")
        self.scratch = Path(tempfile.mkdtemp(prefix="berth-verify-"))
        self.env = os.environ.copy()
        self.env.pop("BERTH_OWNER", None)
        self.env.pop("DOCKER_HOST", None)
        self.env.update(BERTH_STATE=str(self.scratch / "state"),
                        BERTH_CONFIG=str(self.scratch / "config.json"),
                        KUBECONFIG=str(self.scratch / "caller-kubeconfig"))
        self.config = self.scratch / "config.json"
        self.config.write_text(json.dumps({"cores": 3, "room_cores": 1,
                                          "memory": "3GiB", "room_memory": "1GiB"}))
        self.binary = str(self.scratch / "berth")
        self.children = []
        self.record(scratch=str(self.scratch), config=json.loads(self.config.read_text()),
                    state=self.env["BERTH_STATE"], kubeconfig=self.env["KUBECONFIG"])

    def record(self, **event):
        self.log.write(json.dumps(event) + "\n")
        self.log.flush()

    def run(self, *args, code=0, timeout=300, env=None):
        try:
            result = subprocess.run(args, cwd=ROOT, env=env or self.env,
                                    capture_output=True, text=True, timeout=timeout)
        except subprocess.TimeoutExpired as error:
            self.record(command=list(args), timeout=timeout,
                        stdout=(error.stdout or b"").decode(errors="replace"),
                        stderr=(error.stderr or b"").decode(errors="replace"))
            raise
        self.record(command=list(args), stdout=result.stdout, stderr=result.stderr,
                    exit=result.returncode)
        assert result.returncode == code, (args, result.returncode, result.stdout, result.stderr)
        return result.stdout

    def berth(self, *args, code=0):
        return json.loads(self.run(self.binary, *args, "--json", code=code))

    def observe(self, condition, message):
        self.record(observation=message, passed=bool(condition))
        assert condition, message

    def await_(self, predicate, message, seconds=15):
        until = time.monotonic() + seconds
        while time.monotonic() < until:
            if predicate():
                self.record(observation=message, passed=True)
                return
            time.sleep(0.1)
        raise AssertionError("timed out: " + message)

    def start(self, name, *args):
        output = (self.evidence / (name + ".log")).open("w")
        process = subprocess.Popen(args, cwd=ROOT, env=self.env, stdout=output, stderr=output)
        output.close()
        self.children.append((process, name, args))
        self.record(start=list(args), pid=process.pid, output=name + ".log")
        return process

    def finish(self, process, code=0):
        actual = process.wait(timeout=20)
        self.record(pid=process.pid, exit=actual)
        assert actual == code, (process.pid, actual, code)

    def count(self, expected):
        self.observe(self.berth("ps")["count"] == expected, f"lease count = {expected}")

    def create(self, *args):
        return self.berth("create", "--owner", f"pid:{os.getpid()}",
                          "--ttl", "5m", *args)["record"]["id"]

    def local(self):
        self.run(self.binary)
        self.run(self.binary, "--help")
        self.count(0)
        lease = self.create("--label", "demo=plain")
        for alias in ("list", "ls", "ps", "who"):
            listing = self.berth(alias)
            self.observe(listing["count"] == 1 and listing["leases"][0]["id"] == lease,
                         f"{alias} shows the created lease")
        before = self.berth("ps")["records"][0]["expires"]
        self.berth("extend", lease, "--ttl", "30m")
        after = self.berth("ps")["records"][0]["expires"]
        self.observe(after > before, "extend moves expiry forward")
        self.berth("release", lease)
        self.count(1)
        self.berth("release", lease, "--confirm")
        self.berth("down", lease, "--confirm")
        self.count(0)

        owner = self.start("owner", "sleep", "60")
        dead = self.berth("up", "--owner", f"pid:{owner.pid}", "--ttl", "1h")["record"]["id"]
        self.berth("sweep", "--confirm")
        self.count(1)
        owner.terminate()
        self.finish(owner, -15)
        self.observe(dead in self.berth("sweep")["eligible"], "dead owner eligible before expiry")
        self.count(1)
        self.berth("sweep", "--confirm")
        self.count(0)

        heartbeat = self.scratch / "heartbeat"
        heartbeat.touch()
        hb = self.berth("create", "--owner", f"heartbeat:{heartbeat}", "--stale", "1s",
                        "--ttl", "1h")["record"]["id"]
        time.sleep(1.2)
        self.observe(hb in self.berth("sweep")["eligible"], "stale heartbeat eligible")
        heartbeat.touch()
        self.berth("sweep", "--confirm")
        self.count(1)
        heartbeat.unlink()
        self.berth("sweep", "--confirm")
        self.count(0)

        self.run(self.binary, "job", "--", "sh", "-c", "echo working; echo done")
        self.run(self.binary, "job", "--", "sh", "-c", "exit 7", code=7)
        marker = self.scratch / "deadline-started"
        self.run(self.binary, "job", "--timeout", "500ms", "--", "sh", "-c",
                 'touch "$1"; exec sleep 30', "_", str(marker), code=124)
        self.observe(marker.exists(), "deadline child really started")
        never = self.scratch / "never-started"
        self.run(self.binary, "job", "--cores", "999", "--", "touch", str(never), code=2)
        self.observe(not never.exists(), "oversized request did not start child")
        self.queue(never)
        self.count(0)

    def queue(self, never):
        def job(name, cores):
            return self.start(name, self.binary, "job", "--cores", str(cores), "--memory", "1MiB",
                              "--wait", "20s", "--timeout", "30s", "--", "sh", "-c",
                              'echo "$1" >> "$2"; touch "$3"; while [ ! -e "$4" ]; do sleep .1; done',
                              "_", name, str(self.scratch / "order"),
                              str(self.scratch / (name + "-started")), str(self.scratch / (name + "-go")))

        def waiters():
            # Read-only observation of queue registration, not a state setter.
            return len(json.loads((self.scratch / "state/state.json").read_text()).get("waiters") or [])

        holder = job("holder", 1)
        self.await_(lambda: (self.scratch / "holder-started").exists(), "holder admitted")
        big = job("big", 2)
        self.await_(lambda: waiters() == 1, "large waiter registered first")
        small = job("small", 1)
        self.await_(lambda: waiters() == 2, "small waiter queued despite spare core")
        self.run(self.binary, "job", "--memory", "1MiB", "--wait", "0", "--",
                 "touch", str(never), code=75)
        self.observe(not never.exists() and not (self.scratch / "small-started").exists(),
                     "no backfill; exhausted waiter did not start")
        self.berth("ps")
        (self.scratch / "holder-go").touch()
        self.finish(holder)
        self.await_(lambda: (self.scratch / "big-started").exists(), "large waiter admitted next")
        self.observe(not (self.scratch / "small-started").exists(), "small waits behind running large job")
        (self.scratch / "big-go").touch()
        self.finish(big)
        self.await_(lambda: (self.scratch / "small-started").exists(), "small waiter admitted last")
        (self.scratch / "small-go").touch()
        self.finish(small)
        order = (self.scratch / "order").read_text().splitlines()
        self.record(order=order)
        self.observe(order == ["holder", "big", "small"], "FIFO execution order")

    def adapters(self):
        self.run("docker", "info")
        self.run("docker", "compose", "version")
        self.run("kind", "--version")
        self.run("k3d", "version")
        self.run("kubectl", "version", "--client")
        compose = self.scratch / "compose.yaml"
        compose.write_text('services:\n  web:\n    image: nginx:alpine\n    ports: ["127.0.0.1::80"]\n  cache:\n    image: redis:7-alpine\n    volumes: [data:/data]\nvolumes:\n  data: {}\n')
        lease = self.create("--adapter", "compose", "--file", str(compose))
        ids = self.run("docker", "ps", "-q", "--filter", "label=io.berth.lease=" + lease).split()
        self.observe(len(ids) == 2, "two attributed Compose containers")
        containers = json.loads(self.run("docker", "inspect", *ids))
        for container in containers:
            labels = container["Config"]["Labels"]
            self.observe(all(key in labels for key in ("io.berth.lease", "io.berth.owner",
                                                       "io.berth.expires", "io.berth.machine")),
                         "container has lease attribution labels")
        web = next(c for c in containers if c["Config"]["Labels"]["com.docker.compose.service"] == "web")
        address = self.run("docker", "port", web["Id"], "80/tcp").strip()
        def http_ready():
            try:
                with urllib.request.urlopen("http://" + address, timeout=2) as response:
                    body = response.read().decode()
                self.record(url="http://" + address, body=body)
                return "Welcome to nginx!" in body
            except OSError as error:
                self.record(url="http://" + address, waiting=str(error))
                return False

        self.await_(http_ready, "real Compose HTTP response")
        compose.unlink()
        self.berth("release", lease)  # Preview must leave the service running.
        self.observe(len(self.run("docker", "ps", "-q", "--filter", "label=io.berth.lease=" + lease).split()) == 2,
                     "release preview preserves containers")
        self.berth("release", lease, "--confirm")
        self.assert_no_resources(lease)

        caller = Path(self.env["KUBECONFIG"])
        original = 'apiVersion: v1\nkind: Config\nclusters: []\nusers: []\ncontexts: []\ncurrent-context: ""\n'
        caller.write_text(original)
        for adapter, config in (("kind", "kubeconfig"), ("k3d", "k3d")):
            lease = self.create("--adapter", adapter, "--resources", "verification cluster")
            kubeconfig = self.scratch / "state/resources" / lease / config
            self.run("kubectl", "--kubeconfig", str(kubeconfig), "wait", "--for=condition=Ready",
                     "nodes", "--all", "--timeout=120s")
            nodes = json.loads(self.run("kubectl", "--kubeconfig", str(kubeconfig), "get", "nodes", "-o", "json"))
            self.observe(bool(nodes["items"]) and all(any(c["type"] == "Ready" and c["status"] == "True"
                         for c in node["status"]["conditions"]) for node in nodes["items"]),
                         adapter + " has Ready nodes")
            self.berth("ps")
            self.observe(caller.read_text() == original, adapter + " leaves caller kubeconfig unchanged")
            self.berth("release", lease, "--confirm")
            clusters = (self.run("kind", "get", "clusters") if adapter == "kind" else
                        self.run("k3d", "cluster", "list", "-o", "json"))
            name = "berth-" + (lease if adapter == "kind" else lease[:26])
            self.observe(name not in clusters, adapter + " cluster absent after release")
            self.observe(caller.read_text() == original, adapter + " cleanup leaves caller kubeconfig unchanged")
        self.count(0)

    def assert_no_resources(self, lease):
        for args in (("ps", "-aq"), ("volume", "ls", "-q"), ("network", "ls", "-q")):
            out = self.run("docker", *args, "--filter", "label=io.berth.lease=" + lease)
            self.observe(not out.strip(), "managed " + " ".join(args) + " absent")

    def ssh(self):
        # Existing harness drives the built binary against real isolated OpenSSH.
        self.run("make", "ssh-integration", timeout=360)

    def cleanup(self):
        success = True
        for process, name, args in reversed(self.children):
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
            self.record(cleanup_pid=process.pid, name=name, exit=process.returncode)
        try:
            if Path(self.binary).exists():
                for lease in self.berth("ps")["leases"]:
                    self.berth("release", lease["id"], "--confirm")
                self.count(0)
        except Exception as error:
            success = False
            self.record(cleanup_error=str(error), retained_state=str(self.scratch))
        if success:
            shutil.rmtree(self.scratch)
        self.record(cleanup_complete=success, scratch=str(self.scratch))
        self.log.close()
        return success


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("flow", choices=("local", "adapters", "ssh", "all"))
    parser.add_argument("--evidence", required=True, type=Path, help="new absolute directory; never removed")
    args = parser.parse_args()
    if not args.evidence.is_absolute():
        parser.error("--evidence must be absolute")
    driver = Driver(args.evidence)
    passed = False
    try:
        driver.run("git", "rev-parse", "HEAD")
        driver.run("go", "build", "-o", driver.binary, ".")
        driver.run(driver.binary, "--version")
        driver.run(driver.binary, "ps", "--json")
        for flow in (("local", "adapters", "ssh") if args.flow == "all" else (args.flow,)):
            driver.record(flow=flow, status="started")
            getattr(driver, flow)()
            driver.record(flow=flow, status="passed")
        passed = True
    except BaseException as error:
        driver.record(failure=repr(error))
        raise
    finally:
        cleaned = driver.cleanup()
        (args.evidence / "result.json").write_text(json.dumps({"passed": passed, "cleaned": cleaned}) + "\n")
        print("evidence: " + str(args.evidence))
    assert cleaned, "cleanup failed; inspect retained_state in events.jsonl"


if __name__ == "__main__":
    main()
