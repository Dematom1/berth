package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSessionWaiterStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := parseOwner("session:"+strconv.Itoa(os.Getpid()), 0, time.Now())
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Waiters = []JobWaiter{{ID: "0123456789abcdef0123456789abcdef", Machine: "remote", Owner: owner, Expires: time.Now().Add(time.Minute)}}
	err = s.Save()
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err = openStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.Waiters) != 1 || s.Waiters[0].Owner != owner {
		t.Fatal("session waiter lost its owner identity")
	}
}

func TestShellQuote(t *testing.T) {
	for _, s := range []string{"", "simple", "a b", "'\"$`\\;\n", "$(touch /never-run)", "--flag", "世界"} {
		out, err := exec.Command("sh", "-c", "printf '%s' "+shellQuote(s)).Output()
		if err != nil || string(out) != s {
			t.Fatalf("quote %q: %q %v", s, out, err)
		}
	}
}

func TestSSHMachineValidation(t *testing.T) {
	valid := SSHMachine{Host: "lenovo", User: "worker", Workdir: "/home/worker/source 'quoted'", Agent: "/home/worker/bin/berth"}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for _, alter := range []func(*SSHMachine){
		func(m *SSHMachine) { m.Host = "-oProxyCommand=bad" }, func(m *SSHMachine) { m.Host = "a b" }, func(m *SSHMachine) { m.User = "" }, func(m *SSHMachine) { m.User = "-root" }, func(m *SSHMachine) { m.Workdir = "relative" }, func(m *SSHMachine) { m.Agent = "berth" }, func(m *SSHMachine) { m.Port = 65536 }, func(m *SSHMachine) { m.Agent = "/nul\x00" },
	} {
		bad := valid
		alter(&bad)
		if bad.validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	cmd := valid.command(context.Background(), "echo safe")
	joined := strings.Join(cmd.Args, " ")
	for _, required := range []string{"BatchMode=yes", "ServerAliveInterval=5", "ServerAliveCountMax=3", "-- lenovo echo safe"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %s in %v", required, cmd.Args)
		}
	}
}

func TestAgentProtocolRefusal(t *testing.T) {
	for _, tc := range []struct {
		request  string
		contains string
	}{
		{`{"type":"request","version":"old","args":["job","--","true"]}`, "version mismatch"},
		{`{"type":"request","version":` + fmt.Sprintf("%q", version) + `,"args":["machine"]}`, "unsupported"},
		{`{"type":"request","version":` + fmt.Sprintf("%q", version) + `,"args":["job","--machine=another","--","true"]}`, "nested"},
		{`{"type":"request","unknown":1}`, "invalid"},
		{`{"type":"request"} {}`, "invalid"},
	} {
		var out bytes.Buffer
		if code := runAgent(context.Background(), nil, strings.NewReader(tc.request+"\n"), &out); code != 1 {
			t.Fatalf("refusal code %d", code)
		}
		f, err := readFrame(frameScanner(&out))
		if err != nil || f.Type != "error" || !strings.Contains(f.Error, tc.contains) {
			t.Fatalf("refusal %+v %v", f, err)
		}
	}
	if _, err := readFrame(frameScanner(strings.NewReader(strings.Repeat("x", (1<<20)+1)))); err == nil {
		t.Fatal("unbounded frame accepted")
	}
}

func TestAgentOutputFrames(t *testing.T) {
	var out bytes.Buffer
	w := &frameWriter{enc: json.NewEncoder(&out)}
	data := append(bytes.Repeat([]byte{0, 255, '\n', '"'}, 20000), []byte(`{"type":"result","code":99}`)...)
	if n, err := w.Write(data); err != nil || n != len(data) {
		t.Fatalf("write %d %v", n, err)
	}
	scanner := frameScanner(&out)
	var decoded []byte
	for {
		f, err := readFrame(scanner)
		if err == io.EOF {
			break
		}
		if err != nil || f.Type != "output" {
			t.Fatalf("frame %+v %v", f, err)
		}
		decoded = append(decoded, f.Data...)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("binary output changed or injected a result")
	}
}
