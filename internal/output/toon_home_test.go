package output

import (
	"bytes"
	"testing"
)

func TestTOONHomeEscaping(t *testing.T) {
	var rendered bytes.Buffer
	toonObject(&rendered, map[string]any{"leases": []map[string]any{{"id": "abc", "resources": "lab, \"db\"\nnext"}}}, "")
	if rendered.String() != "leases[1]{id,resources}:\n  abc,\"lab, \\\"db\\\"\\nnext\"\n" {
		t.Fatalf("TOON string escaping changed: %q", rendered.String())
	}
}
