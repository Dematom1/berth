package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var syncDirectory = func(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Sync the new file before rename, and the directory after rename. The lock file is never replaced.
func Write(path string, data []byte) error {
	previous, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	existed := readErr == nil
	f, err := os.CreateTemp(filepath.Dir(path), ".dibs-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		var rollbackErr error
		if existed {
			var restore *os.File
			restore, rollbackErr = os.CreateTemp(filepath.Dir(path), ".dibs-rollback-*")
			if rollbackErr == nil {
				_, rollbackErr = restore.Write(previous)
				if rollbackErr == nil {
					rollbackErr = restore.Sync()
				}
				name := restore.Name()
				if closeErr := restore.Close(); rollbackErr == nil {
					rollbackErr = closeErr
				}
				if rollbackErr == nil {
					rollbackErr = os.Rename(name, path)
				}
				os.Remove(name)
			}
		} else {
			rollbackErr = os.Remove(path)
			if errors.Is(rollbackErr, os.ErrNotExist) {
				rollbackErr = nil
			}
		}
		if rollbackErr == nil {
			rollbackErr = syncDirectory(filepath.Dir(path))
		}
		if rollbackErr != nil {
			return fmt.Errorf("directory sync failed: %v; rollback failed: %w", err, rollbackErr)
		}
		return err
	}
	return nil
}
