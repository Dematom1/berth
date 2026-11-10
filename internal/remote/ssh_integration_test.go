//go:build sshintegration

package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	configpkg "github.com/Dematom1/berth/internal/config"
)

// A real OpenSSH server, real client, and cross-built berth binary. No missing
// prerequisite skips: this target must fail if Docker/SSH/Go are unavailable.

func TestSSHIntegration(t *testing.T) {
	for _, tool := range []string{"docker", "ssh", "ssh-keygen", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("install %s: %v", tool, err)
		}
	}
	dir := t.TempDir()
	run := func(name string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		t.Logf("command=%q output=%q error=%v", append([]string{name}, args...), out, err)
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, "client"))
	run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, "host"))
	binary := filepath.Join(dir, "berth")
	run("go", "build", "-ldflags", "-X main.version="+version, "-o", binary, moduleRoot(t))
	arch := runtime.GOARCH
	buildRemote := func(output, remoteVersion string) {
		t.Helper()
		cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+remoteVersion, "-o", output, moduleRoot(t))
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("remote build: %v %s", err, out)
		}
	}
	remoteBinary := filepath.Join(dir, "berth-linux")
	buildRemote(remoteBinary, version)
	buildRemote(filepath.Join(dir, "wrong-version"), "incompatible-test-version")
	// The fixture has two usable cores. Local config below has only one, proving
	// admission and class lookup use the remote machine, not client defaults.
	write(filepath.Join(dir, "remote-config.json"), `{"cores":3,"room_cores":1,"memory":"4GiB","room_memory":"2GiB","classes":{"default":{"cores":1,"memory":"1GiB","timeout":"20s"},"remote-short":{"cores":1,"memory":"1GiB","timeout":"1s"}}}`, 0600)
	name := "berth-ssh-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	script := `apk add --no-cache openssh-server procps >/dev/null
# Minimal Alpine has no machine-id; provision this disposable host once.
tr -d '-' </proc/sys/kernel/random/uuid >/etc/machine-id
mkdir -p /root/.ssh /root/.config/berth "/lab/work 'quoted'"
cp /fixture/client.pub /root/.ssh/authorized_keys
chmod 700 /root/.ssh; chmod 600 /root/.ssh/authorized_keys
cp /fixture/remote-config.json /root/.config/berth/config.json
echo 'root:unused-fixture-password' | chpasswd
exec /usr/sbin/sshd -D -e -h /fixture/host -o PermitRootLogin=prohibit-password -o PasswordAuthentication=no -o ClientAliveInterval=2 -o ClientAliveCountMax=2`
	run("docker", "run", "-d", "--rm", "--name", name, "-p", "127.0.0.1::22", "--mount", "type=bind,src="+dir+",dst=/fixture", "alpine:3.23", "sh", "-ec", script)
	t.Cleanup(func() {
		out, err := exec.Command("docker", "rm", "-f", name).CombinedOutput()
		if err != nil {
			t.Errorf("fixture cleanup: %v %s", err, out)
		}
	})
	address := run("docker", "port", name, "22/tcp")
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(filepath.Join(dir, "host.pub"))
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "known_hosts"), "[127.0.0.1]:"+port+" "+string(pub), 0600)
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	// Use an isolated, strict known-hosts file, never the developer's trust store.
	write(filepath.Join(dir, "ssh_config"), "Host *\n  UserKnownHostsFile "+filepath.Join(dir, "known_hosts")+"\n  StrictHostKeyChecking yes\n  LogLevel ERROR\n", 0600)
	wrapper := filepath.Join(dir, "tools")
	if err := os.Mkdir(wrapper, 0700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(wrapper, "ssh"), "#!/bin/sh\nexec "+ShellQuote(ssh)+" -F "+ShellQuote(filepath.Join(dir, "ssh_config"))+" \"$@\"\n", 0700)
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))
	m := configpkg.Target{Host: "127.0.0.1", User: "root", Workdir: "/lab/work 'quoted'", Agent: "/root/bin/berth 'quoted'", Port: portNumber, IdentityFile: filepath.Join(dir, "client")}
	ready := false
	until := time.Now().Add(time.Minute)
	for time.Now().Before(until) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := sshCommand(ctx, m, "true").Run()
		cancel()
		if err == nil {
			ready = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("sshd failed to start: %s", run("docker", "logs", name))
	}
	config := configpkg.Config{Cores: 1, RoomCores: 0, Memory: "1GiB", RoomMemory: "0", Machines: map[string]configpkg.Target{"fixture": m}}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "client-config.json"), string(encoded), 0600)
	t.Setenv("BERTH_CONFIG", filepath.Join(dir, "client-config.json"))
	t.Setenv("BERTH_STATE", filepath.Join(dir, "local-state-must-stay-absent"))
	command := func(args ...string) *exec.Cmd { return exec.Command(binary, args...) }
	job := func(args ...string) *exec.Cmd {
		return command(append([]string{"job", "--machine", "fixture"}, args...)...)
	}
	ps := func() map[string]any {
		t.Helper()
		out := childExit(t, command("ps", "--machine", "fixture", "--json"), 0)
		var result map[string]any
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("remote ps: %v %s", err, out)
		}
		return result
	}
	empty := func() {
		t.Helper()
		if ps()["count"] != float64(0) {
			t.Fatal("remote budget not released")
		}
	}
	childExit(t, command("machine", "install", "fixture", "--file", remoteBinary), 0)
	t.Run("child codes including SSH reserved 255", func(t *testing.T) {
		for _, code := range []int{0, 1, 2, 37, 75, 76, 124, 255} {
			childExit(t, job("--", "sh", "-c", fmt.Sprintf("exit %d", code)), code)
		}
		empty()
	})
	t.Run("safe argv binary stdout workdir and no stdin", func(t *testing.T) {
		argument := "'quoted' \"$`;\n$(touch /lab/injected) --machine=child"
		out := childExit(t, job("--", "sh", "-c", `printf '%s' "$1"; printf '\000\377'; pwd; test ! -e /lab/injected; test -z "$(cat)"`, "_", argument), 0)
		if out != argument+"\x00\xff"+m.Workdir+"\n" {
			t.Fatalf("changed remote output: %q", out)
		}
	})
	t.Run("remote budget owner room class and deadline", func(t *testing.T) {
		childExit(t, job("--cores", "2", "--memory", "2GiB", "--", "true"), 0)
		childExit(t, job("--cores", "3", "--", "true"), 2)
		childExit(t, job("--timeout", "1s", "--", "sleep", "30"), 124)
		childExit(t, job("--class", "remote-short", "--", "sleep", "30"), 124)
		empty()
	})
	t.Run("wait 75 and disconnect kills descendant and releases budget", func(t *testing.T) {
		marker := filepath.Join(dir, "started")
		holder := job("--cores", "2", "--memory", "2GiB", "--", "sh", "-c", `sleep 30 & echo "$!" > /fixture/started; wait`)
		log, err := os.Create(filepath.Join(dir, "holder.log"))
		if err != nil {
			t.Fatal(err)
		}
		defer log.Close()
		holder.Stdout, holder.Stderr = log, log
		if err := holder.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = holder.Process.Kill() })
		awaitJob(t, func() bool { data, _ := os.ReadFile(marker); return len(strings.TrimSpace(string(data))) > 0 })
		listed := ps()
		if listed["count"] != float64(1) {
			t.Fatalf("missing active remote lease: %+v", listed)
		}
		lease := listed["leases"].([]any)[0].(map[string]any)
		if !strings.HasPrefix(lease["owner"].(string), "session:") {
			t.Fatalf("remote lease does not name its session owner: %+v", lease)
		}
		queued := job("--wait", "30s", "--", "true")
		queued.Stdout, queued.Stderr = log, log
		if err := queued.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = queued.Process.Kill() })
		waiters := func() int {
			var state struct {
				Waiters []json.RawMessage `json:"waiters"`
			}
			data := run("docker", "exec", name, "cat", "/root/.local/state/berth/state.json")
			if err := json.Unmarshal([]byte(data), &state); err != nil {
				t.Fatal(err)
			}
			return len(state.Waiters)
		}
		awaitJob(t, func() bool { return waiters() == 1 })
		_ = queued.Process.Kill()
		_ = queued.Wait()
		awaitJob(t, func() bool { return waiters() == 0 })
		childExit(t, job("--wait", "200ms", "--", "true"), 75)
		childExit(t, command("sweep", "--machine", "fixture", "--confirm"), 0)
		if ps()["count"] != float64(1) {
			t.Fatal("remote sweep killed live session")
		}
		// SIGKILL the local client, not the remote child. Its pipe closes, the SSH
		// session EOF cancels the agent, and cleanup must happen without a sweep.
		if err := holder.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		_ = holder.Wait()
		awaitJob(t, func() bool { return ps()["count"] == float64(0) })
		data, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		pid := strings.TrimSpace(string(data))
		out := run("docker", "exec", name, "sh", "-c", "ps -p "+pid+" -o stat= || true")
		if out != "" && !strings.HasPrefix(out, "Z") {
			t.Fatalf("remote descendant survived session death: %s", out)
		}
		childExit(t, job("--cores", "2", "--memory", "2GiB", "--wait", "0", "--", "true"), 0)
	})
	t.Run("transport loss after child start returns 76", func(t *testing.T) {
		marker := filepath.Join(dir, "transport-started")
		client := job("--", "sh", "-c", "touch /fixture/transport-started; exec sleep 30")
		log, err := os.Create(filepath.Join(dir, "transport.log"))
		if err != nil {
			t.Fatal(err)
		}
		defer log.Close()
		client.Stdout, client.Stderr = log, log
		if err := client.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Process.Kill() })
		awaitJob(t, func() bool { _, err := os.Stat(marker); return err == nil })
		processes := run("ps", "-A", "-o", "ppid=", "-o", "pid=")
		transportPID := 0
		for _, line := range strings.Split(processes, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == strconv.Itoa(client.Process.Pid) {
				transportPID, _ = strconv.Atoi(fields[1])
			}
		}
		if transportPID <= 1 {
			t.Fatal("SSH child process not found")
		}
		transport, err := os.FindProcess(transportPID)
		if err != nil {
			t.Fatal(err)
		}
		if err := transport.Kill(); err != nil {
			t.Fatal(err)
		}
		if err := client.Wait(); err == nil || client.ProcessState.ExitCode() != 76 {
			t.Fatalf("transport loss replaced unknown child result: %v %v", client.ProcessState, err)
		}
		awaitJob(t, func() bool { return ps()["count"] == float64(0) })
	})
	t.Run("version checked installation and handshake", func(t *testing.T) {
		wrong := filepath.Join(dir, "wrong-version")
		if out := childExit(t, command("machine", "install", "fixture", "--file", wrong), 1); !strings.Contains(out, "installation failed") {
			t.Fatal(out)
		}
		childExit(t, job("--", "true"), 0) // refused upgrade retained good executable
		run("docker", "exec", name, "cp", "/fixture/wrong-version", m.Agent)
		out := childExit(t, job("--", "touch", "/lab/mismatch-must-not-run"), 1)
		if !strings.Contains(out, "version mismatch") {
			t.Fatalf("unclear mismatch: %s", out)
		}
		run("docker", "exec", name, "test", "!", "-e", "/lab/mismatch-must-not-run")
		childExit(t, command("machine", "install", "fixture", "--file", remoteBinary), 0)
	})
	t.Run("transport failure is distinct", func(t *testing.T) {
		run("docker", "exec", name, "mv", m.Agent, m.Agent+".saved")
		childExit(t, job("--", "true"), 76)
		run("docker", "exec", name, "mv", m.Agent+".saved", m.Agent)
	})
	empty()
	if _, err := os.Stat(os.Getenv("BERTH_STATE")); !os.IsNotExist(err) {
		t.Fatalf("remote invocation touched local state: %v", err)
	}
}
