package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"

	configpkg "github.com/Dematom1/berth/internal/config"
	outputpkg "github.com/Dematom1/berth/internal/output"
	remotepkg "github.com/Dematom1/berth/internal/remote"
)

func TestRemoteConfigErrorHelp(t *testing.T) {
	for _, command := range []string{"job", "list", "sweep"} {
		var out bytes.Buffer
		code := remotepkg.Run(context.Background(), version, configpkg.Config{}, "missing", []string{command}, &out, outputpkg.AXI)
		if code != 2 || !strings.Contains(out.String(), "Run berth "+command+" --help") {
			t.Fatalf("%s config error: %d %s", command, code, out.String())
		}
	}
}

func TestShellQuote(t *testing.T) {
	for _, s := range []string{"", "simple", "a b", "'\"$`\\;\n", "$(touch /never-run)", "--flag", "世界"} {
		out, err := exec.Command("sh", "-c", "printf '%s' "+remotepkg.ShellQuote(s)).Output()
		if err != nil || string(out) != s {
			t.Fatalf("quote %q: %q %v", s, out, err)
		}
	}
}

func TestSSHMachineValidation(t *testing.T) {
	valid := configpkg.Target{Host: "lenovo", User: "worker", Workdir: "/home/worker/source 'quoted'", Agent: "/home/worker/bin/berth"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, alter := range []func(*configpkg.Target){
		func(m *configpkg.Target) { m.Host = "-oProxyCommand=bad" }, func(m *configpkg.Target) { m.Host = "a b" }, func(m *configpkg.Target) { m.User = "" }, func(m *configpkg.Target) { m.User = "-root" }, func(m *configpkg.Target) { m.Workdir = "relative" }, func(m *configpkg.Target) { m.Agent = "berth" }, func(m *configpkg.Target) { m.Port = 65536 }, func(m *configpkg.Target) { m.Agent = "/nul\x00" },
	} {
		bad := valid
		alter(&bad)
		if bad.Validate() == nil {
			t.Fatalf("accepted %+v", bad)
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
		{`{"type":"request","version":` + fmt.Sprintf("%q", version) + `,"args":["list"],"output":"unknown"}`, "invalid output mode"},
		{`{"type":"request"} {}`, "invalid"},
	} {
		var out bytes.Buffer
		if code := runAgent(context.Background(), nil, strings.NewReader(tc.request+"\n"), &out); code != 1 {
			t.Fatalf("refusal code %d", code)
		}
		f, err := readTestFrame(testScanner(&out))
		if err != nil || f.Type != "error" || !strings.Contains(f.Error, tc.contains) {
			t.Fatalf("refusal %+v %v", f, err)
		}
	}
}

func TestAgentRequestedOutput(t *testing.T) {
	t.Setenv("BERTH_STATE", t.TempDir())
	t.Setenv("BERTH_AXI", "1")
	fixedBudget(t)
	for _, mode := range []outputpkg.Mode{outputpkg.Human, outputpkg.AXI, outputpkg.JSON} {
		request, _ := json.Marshal(testFrame{Type: "request", Version: version, Args: []string{"list"}, Output: mode})
		reader, writer := io.Pipe()
		var wire bytes.Buffer
		code := runAgent(context.Background(), nil, io.MultiReader(bytes.NewReader(append(request, '\n')), reader), &wire)
		writer.Close()
		reader.Close()
		if code != 0 {
			t.Fatal(code, wire.String())
		}
		scanner := testScanner(&wire)
		var rendered bytes.Buffer
		for {
			frame, err := readTestFrame(scanner)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if frame.Type == "output" {
				rendered.Write(frame.Data)
			}
		}
		snapshot(t, "ps-"+string(mode), rendered.String())
	}
}
