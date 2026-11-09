package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestTOONQuotesKeysInEveryShape(t *testing.T) {
	key := "x\nerror: injected"
	for _, tc := range []struct {
		name string
		data map[string]any
		want string
	}{
		{"scalar", map[string]any{key: "value"}, `"x\nerror: injected": value`},
		{"object", map[string]any{key: map[string]any{"child": "value"}}, `"x\nerror: injected":`},
		{"labels", map[string]any{key: map[string]string{"child": "value"}}, `"x\nerror: injected":`},
		{"empty table", map[string]any{key: []map[string]any{}}, `"x\nerror: injected": []`},
		{"table", map[string]any{key: []map[string]any{{"child": "value"}}}, `"x\nerror: injected"[1]{child}:`},
		{"column", map[string]any{"rows": []map[string]any{{key: "value"}}}, `rows[1]{"x\nerror: injected"}:`},
		{"empty strings", map[string]any{key: []string{}}, `"x\nerror: injected": []`},
		{"strings", map[string]any{key: []string{"value"}}, `"x\nerror: injected"[1]: value`},
		{"space in key", map[string]any{"two words": "value"}, `"two words": value`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			emit(&out, false, tc.data)
			if !strings.Contains(out.String(), tc.want) || strings.Contains(out.String(), "\nerror: injected") {
				t.Fatalf("key changed TOON structure: %q; want %q", out.String(), tc.want)
			}
		})
	}
}
