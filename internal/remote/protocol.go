package remote

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"

	outputpkg "github.com/Dematom1/dibs/internal/output"
)

// Newline-delimited JSON is private to the SSH pipe, never user-facing. Byte
// slices use JSON base64 so arbitrary child output cannot impersonate a result.
// A result frame distinguishes child exit 255 from an SSH transport failure.
type agentFrame struct {
	Type    string         `json:"type"`
	Version string         `json:"version,omitempty"`
	Args    []string       `json:"args,omitempty"`
	Data    []byte         `json:"data,omitempty"`
	Code    int            `json:"code,omitempty"`
	Error   string         `json:"error,omitempty"`
	Output  outputpkg.Mode `json:"output,omitempty"`
	Machine string         `json:"machine,omitempty"`
}

func frameScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), 1<<20)
	return s
}

func readFrame(s *bufio.Scanner) (agentFrame, error) {
	var f agentFrame
	if !s.Scan() {
		if s.Err() != nil {
			return f, s.Err()
		}
		return f, io.EOF
	}
	dec := json.NewDecoder(strings.NewReader(s.Text()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return f, errors.New("invalid trailing protocol data")
	}
	return f, nil
}

type frameWriter struct{ enc *json.Encoder }

func (w *frameWriter) send(f agentFrame) error { return w.enc.Encode(f) }

func (w *frameWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		size := min(len(p), 32<<10)
		if err := w.send(agentFrame{Type: "output", Data: p[:size]}); err != nil {
			return n, err
		}
		n += size
		p = p[size:]
	}
	return n, nil
}
