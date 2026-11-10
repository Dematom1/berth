package adapter

import "path/filepath"

// SnapshotMatches accepts a state-directory alias only when both roots resolve
// to the same ledger. Missing snapshot files are normal after failed creates.
func SnapshotMatches(path, expected string) bool {
	if path == expected {
		return true
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	expectedRoot := filepath.Dir(filepath.Dir(filepath.Dir(expected)))
	tail, err := filepath.Rel(expectedRoot, expected)
	if err != nil || path != filepath.Join(root, tail) {
		return false
	}
	original, oldErr := filepath.EvalSymlinks(root)
	current, newErr := filepath.EvalSymlinks(expectedRoot)
	return oldErr == nil && newErr == nil && original == current
}
