package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"syscall"
	"time"
)

var leaseID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Store struct {
	Version int              `json:"version"`
	Leases  map[string]Lease `json:"leases"`
	Waiters []JobWaiter      `json:"waiters,omitempty"`
	dir     string
	lock    *os.File
}

func stateDir() (string, error) {
	if dir := os.Getenv("DIBS_STATE"); dir != "" {
		return filepath.Abs(dir)
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Abs(filepath.Join(base, "dibs"))
}

func openStore(ctx context.Context, dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	// ponytail: one host-wide lock covers slow adapters too; split only if measured contention matters.
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	s := &Store{Version: 1, Leases: map[string]Lease{}, dir: dir, lock: f}
	statePath := filepath.Join(dir, "state.json")
	if err := os.Chmod(statePath, 0600); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.Close()
		return nil, err
	}
	data, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err == nil {
		err = json.Unmarshal(data, s)
	}
	if err == nil {
		err = s.validate()
	}
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("cannot read lease state (kept unchanged): %w", err)
	}
	return s, nil
}

func (s *Store) validate() error {
	if s.Version != 1 || s.Leases == nil {
		return errors.New("unsupported or incomplete state format")
	}
	for _, w := range s.Waiters {
		if !leaseID.MatchString(w.ID) || w.Machine == "" || w.Expires.IsZero() || (w.Owner.Kind != "pid" && w.Owner.Kind != "session") || w.Owner.Start == "" {
			return errors.New("invalid queue record")
		}
	}
	for id, l := range s.Leases {
		if !leaseID.MatchString(id) || id != l.ID || l.Machine == "" || l.Created.IsZero() || l.Expires.IsZero() || l.TTL <= 0 {
			return errors.New("invalid lease record")
		}
		if l.Resource.Adapter == "process" {
			if l.Job == nil || l.Job.Budget.Cores <= 0 || l.Job.Budget.Memory <= 0 || l.Job.Class == "" {
				return errors.New("invalid job reservation")
			}
			if leader := l.Job.Leader; leader.ID != "" {
				pid, err := strconv.ParseInt(leader.ID, 10, 32)
				if err != nil || pid <= 1 || leader.Kind != "pid" || leader.Start == "" {
					return errors.New("invalid job group identity")
				}
			}
			continue
		}
		if l.Job != nil {
			return errors.New("job reservation requires process adapter")
		}
		if l.Resource.Adapter != "none" && l.Resource.Adapter != "kind" && l.Resource.Adapter != "k3d" && l.Resource.Adapter != "compose" {
			return errors.New("unsupported resource adapter")
		}
		resourceName := "dibs-" + id
		if l.Resource.Adapter == "k3d" {
			resourceName = "dibs-" + id[:26]
		}
		if l.Resource.Adapter != "none" && (l.Resource.Name != resourceName || l.Resource.Context == "") {
			return errors.New("invalid managed resource identity")
		}
		if l.Resource.Adapter == "k3d" && l.Resource.DockerHost == "" {
			return errors.New("k3d resource is missing its captured Docker endpoint")
		}
		if l.Resource.Adapter == "kind" || l.Resource.Adapter == "k3d" || l.Resource.Adapter == "compose" {
			name := "kubeconfig"
			if l.Resource.Adapter == "compose" {
				name = "compose.json"
			} else if l.Resource.Adapter == "k3d" {
				name = "k3d"
			}
			if l.Resource.File != filepath.Join(s.dir, "resources", id, name) {
				return errors.New("managed resource file must remain inside its original DIBS_STATE directory")
			}
		}
	}
	return nil
}

func (s *Store) IDs() []string {
	ids := make([]string, 0, len(s.Leases))
	for id := range s.Leases {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s *Store) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.dir, "state.json"), append(data, '\n'))
}

func (s *Store) Close() error { return s.lock.Close() }

var syncDirectory = func(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Sync the new file before rename, and the directory after rename. The lock file is never replaced.
func atomicWrite(path string, data []byte) error {
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
