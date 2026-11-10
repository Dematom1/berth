package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteRestoresOldStateWhenDirectorySyncFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old durable state"), 0600); err != nil {
		t.Fatal(err)
	}
	original := syncDirectory
	t.Cleanup(func() { syncDirectory = original })
	calls := 0
	syncDirectory = func(string) error {
		calls++
		if calls == 1 {
			return errors.New("injected directory sync failure")
		}
		return nil
	}
	if err := Write(path, []byte("new state")); err == nil {
		t.Fatal("directory sync failure reported success")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "old durable state" {
		t.Fatalf("failed write did not preserve prior durable state: %q, %v", got, err)
	}
}
