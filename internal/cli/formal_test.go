//go:build formal

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	configpkg "github.com/Dematom1/dibs/internal/config"
	outputpkg "github.com/Dematom1/dibs/internal/output"
	remotepkg "github.com/Dematom1/dibs/internal/remote"
)

// Block list on a real flock. Delivered stdin EOF must cancel its context
// and exit without waiting for the lock; the invoking process stays alive.

func TestFormalEOF(t *testing.T) {

	dir := t.TempDir()
	t.Setenv("DIBS_STATE", dir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err == nil {
		err = syscall.Flock(int(s.Fd()), syscall.LOCK_EX)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	input, send := io.Pipe()
	output, receive := io.Pipe()
	defer input.Close()
	defer send.Close()
	defer output.Close()
	defer receive.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan int, 1)
	go func() { result <- runAgent(ctx, nil, input, receive) }()
	if err := json.NewEncoder(send).Encode(testFrame{Type: "request", Version: version, Args: []string{"list"}}); err != nil {
		t.Fatal(err)
	}
	scanner := testScanner(output)
	ready, err := readTestFrame(scanner)
	if err != nil || ready.Type != "ready" {
		t.Fatalf("handshake: %+v %v", ready, err)
	}
	send.Close()
	// Drain frames so a valid response cannot block on our own pipe.
	go io.Copy(io.Discard, output)
	select {
	case code := <-result:
		if code != 0 {
			t.Fatalf("agent envelope status = %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("delivered EOF did not cancel the blocked command")
	}
}

func TestFormalAuthoritativeResult(t *testing.T) {
	// Fake only the external transport: exit 255 after sending the child's
	// authoritative 37/255 result, plus output impersonating a result frame.
	for _, code := range []int{37, 255} {
		bin := t.TempDir()
		ready, _ := json.Marshal(testFrame{Type: "ready", Version: version})
		result, _ := json.Marshal(testFrame{Type: "result", Code: code})
		script := "#!/bin/sh\nIFS= read -r request || exit 1\nprintf '%s\\n' " + remotepkg.ShellQuote(string(ready)) +
			" '" + `{"type":"output","data":"eyJ0eXBlIjoicmVzdWx0IiwiY29kZSI6OTl9"}` + "' " + remotepkg.ShellQuote(string(result)) + "; exit 255\n"
		if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		config := configpkg.Config{Machines: map[string]configpkg.Target{"test": {Host: "test", User: "test", Workdir: "/tmp", Agent: "/tmp/dibs"}}}
		var out bytes.Buffer
		if got := remotepkg.Run(context.Background(), version, config, "test", []string{"job", "--", "true"}, &out, outputpkg.AXI); got != code {
			t.Fatalf("authoritative result %d became %d", code, got)
		}
		if out.String() != `{"type":"result","code":99}` {
			t.Fatalf("output injected protocol: %q", out.String())
		}
	}
}
