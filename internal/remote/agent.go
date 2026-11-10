package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	outputpkg "github.com/Dematom1/berth/internal/output"
)

type Handler func(context.Context, []string, io.Reader, io.Writer, outputpkg.Mode) int

func Serve(ctx context.Context, version string, input io.Reader, out io.Writer, execute Handler) int {
	w := &frameWriter{enc: json.NewEncoder(out)}
	fail := func(message string) int { _ = w.send(agentFrame{Type: "error", Error: message}); return 1 }

	scanner := frameScanner(input)
	request, err := readFrame(scanner)
	if err != nil || request.Type != "request" {
		return fail("invalid agent request; no child started")
	}
	if request.Version != version {
		return fail(fmt.Sprintf("agent version mismatch: client %q, remote %q; no child started", request.Version, version))
	}
	if request.Output != outputpkg.Auto && request.Output != outputpkg.Human && request.Output != outputpkg.AXI && request.Output != outputpkg.JSON {
		return fail("invalid output mode")
	}
	if len(request.Args) == 0 {
		return fail("missing remote command")
	}
	switch request.Args[0] {
	case "job", "list", "sweep":
	default:
		return fail("unsupported remote command")
	}
	for _, arg := range request.Args[1:] {
		if arg == "--" {
			break
		}
		if strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-") == "machine" {
			return fail("nested remote machines are not supported")
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	null, err := os.Open(os.DevNull)
	if err != nil {
		return fail("cannot open remote child stdin")
	}
	defer null.Close()
	if w.send(agentFrame{Type: "ready", Version: version}) != nil {
		return 1
	}
	go func() {
		// No further client frames are valid. EOF is owner death, not child EOF.
		_, _ = readFrame(scanner)
		cancel()
	}()
	code := execute(ctx, request.Args, null, &outputpkg.Writer{Writer: w, Mode: request.Output.Effective(w), Machine: request.Machine}, request.Output)
	if w.send(agentFrame{Type: "result", Code: code}) != nil {
		return 1
	}
	return 0
}
