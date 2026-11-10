package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Compatibility with the pre-rename name "berth". Leases, labels, and the
// tombstone stay recognized for good; the environment and config aliases go in
// the next minor release.
const (
	legacyName      = "berth"
	legacyEnvPrefix = "BERTH_"
)

var (
	warnings io.Writer = os.Stderr
	warned   sync.Map
)

func deprecated(message string) {
	if _, seen := warned.LoadOrStore(message, true); !seen {
		fmt.Fprintln(warnings, "dibs: deprecated:", message)
	}
}

// getenv reads DIBS_<name>. The legacy BERTH_<name> applies only while
// DIBS_<name> is unset, so a set DIBS_<name> (even empty) always wins.
func getenv(name string) string {
	if value, ok := os.LookupEnv("DIBS_" + name); ok {
		return value
	}
	value := os.Getenv(legacyEnvPrefix + name)
	if value != "" {
		deprecated(legacyEnvPrefix + name + " is renamed to DIBS_" + name + "; the old name stops working in the next minor release")
	}
	return value
}

// readLegacyConfig reads the pre-rename config file when the dibs one is absent.
func readLegacyConfig(base, current string) ([]byte, error) {
	legacy := filepath.Join(base, legacyName, "config.json")
	data, err := os.ReadFile(legacy)
	if err == nil {
		deprecated("reading " + legacy + "; move it to " + current)
	}
	return data, err
}

// migrateLegacyState moves base/berth to base/dibs under the berth store lock,
// so no berth process is mid-operation, then leaves a regular file at the old
// path. An older berth binary then fails to open its state instead of starting
// an empty ledger that dibs would never reclaim. openStore rewrites resource
// paths recorded under the old directory.
func migrateLegacyState(ctx context.Context, base string) error {
	legacy, dir := filepath.Join(base, legacyName), filepath.Join(base, "dibs")
	pending := func() (bool, error) {
		if info, err := os.Lstat(legacy); err != nil || !info.IsDir() {
			return false, nil
		}
		_, err := os.Lstat(dir)
		if err == nil {
			deprecated(legacy + " was not migrated because " + dir + " exists; dibs does not manage leases recorded only in " + legacy)
			return false, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		return true, nil
	}
	if ok, err := pending(); !ok {
		return err
	}
	old, err := openStore(ctx, legacy)
	if err != nil {
		if ok, _ := pending(); !ok {
			return nil // A concurrent dibs finished the migration first.
		}
		return fmt.Errorf("cannot migrate berth state from %s (kept unchanged): %w", legacy, err)
	}
	defer old.Close()
	if ok, err := pending(); !ok {
		return err
	}
	if err := os.Rename(legacy, dir); err != nil {
		return fmt.Errorf("cannot migrate berth state from %s (kept unchanged): %w", legacy, err)
	}
	// ponytail: a crash between the rename and this write leaves no tombstone; an
	// older berth run after that starts a separate ledger, which dibs then reports.
	f, err := os.OpenFile(legacy, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		_, err = fmt.Fprintf(f, "berth was renamed to dibs. This state moved to %s.\nInstall dibs (go install github.com/Dematom1/dibs@latest) to manage these leases.\n", dir)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = syncDirectory(base)
	}
	if err != nil {
		return fmt.Errorf("migrated berth state to %s but cannot leave a notice at %s: %w", dir, legacy, err)
	}
	fmt.Fprintf(warnings, "dibs: migrated berth state from %s to %s\n", legacy, dir)
	return nil
}

// legacyResourceFile maps a resource file recorded under the sibling berth
// directory to the same file under dir, where migration moved it.
func legacyResourceFile(dir, file string) string {
	legacy := filepath.Join(filepath.Dir(dir), legacyName) + string(filepath.Separator)
	if rest, ok := strings.CutPrefix(file, legacy); ok && strings.HasPrefix(rest, "resources"+string(filepath.Separator)) {
		return filepath.Join(dir, rest)
	}
	return file
}
