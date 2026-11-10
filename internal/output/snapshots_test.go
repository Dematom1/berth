package output

import (
	"os"
	"path/filepath"
	"testing"
)

func snapshot(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "output", name+".golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s output changed\nwant:\n%s\ngot:\n%s", name, want, got)
	}
}

func fixedBudget(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"cores":14,"memory":"32GiB","room_cores":3,"room_memory":"8GiB","classes":{"default":{"cores":1,"memory":"1GiB","timeout":"1h"}}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERTH_CONFIG", path)
}
