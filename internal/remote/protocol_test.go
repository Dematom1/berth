package remote

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

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

func TestFrameLimit(t *testing.T) {
	if _, err := readFrame(frameScanner(strings.NewReader(strings.Repeat("x", (1<<20)+1)))); err == nil {
		t.Fatal("unbounded frame accepted")
	}
}
