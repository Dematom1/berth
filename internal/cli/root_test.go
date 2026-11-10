package cli

import (
	"path/filepath"
	"runtime"
	"testing"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok || !filepath.IsAbs(file) {
		t.Fatal("cannot locate absolute module root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}
