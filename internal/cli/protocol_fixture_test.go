package cli

import (
	"bufio"
	"encoding/json"
	"io"

	outputpkg "github.com/Dematom1/dibs/internal/output"
)

type testFrame struct {
	Type    string         `json:"type"`
	Version string         `json:"version,omitempty"`
	Args    []string       `json:"args,omitempty"`
	Data    []byte         `json:"data,omitempty"`
	Code    int            `json:"code,omitempty"`
	Error   string         `json:"error,omitempty"`
	Output  outputpkg.Mode `json:"output,omitempty"`
	Machine string         `json:"machine,omitempty"`
}

func testScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), 1<<20)
	return s
}
func readTestFrame(s *bufio.Scanner) (testFrame, error) {
	var f testFrame
	if !s.Scan() {
		if s.Err() != nil {
			return f, s.Err()
		}
		return f, io.EOF
	}
	err := json.Unmarshal(s.Bytes(), &f)
	return f, err
}
