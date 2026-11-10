package lease

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

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	atomicfilepkg "github.com/Dematom1/dibs/internal/atomicfile"
	"github.com/Dematom1/dibs/internal/compat"
)

var leaseID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type store struct {
	Version   int              `json:"version"`
	Leases    map[string]Lease `json:"leases"`
	Waiters   []waiter         `json:"waiters,omitempty"`
	dir       string
	lock      *os.File
	resources adapterpkg.Registry
}

func stateDir() (string, error) { return resolveStateDir(true) }

func resolveStateDir(publish bool) (string, error) {
	if dir := compat.Env("STATE"); dir != "" {
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
	return migrateStateDir(base, publish)
}

var syncStateParent = func(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Keep the ledger, lock inode and absolute snapshot paths in place, including
// while an old berth process holds the lock. Inventory never publishes aliases.
func migrateStateDir(base string, publish bool) (string, error) {
	base, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	preferred, legacy := filepath.Join(base, "dibs"), filepath.Join(base, "berth")
	oldReal, err := filepath.EvalSymlinks(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return preferred, nil
	}
	if err != nil {
		return "", err
	}
	newReal, err := filepath.EvalSymlinks(preferred)
	if errors.Is(err, os.ErrNotExist) {
		if !publish {
			return legacy, nil
		}
		if err := os.Symlink(legacy, preferred); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		newReal, err = filepath.EvalSymlinks(preferred)
	}
	if err != nil {
		return "", err
	}
	if newReal != oldReal {
		return "", errors.New("both dibs and berth state directories exist; keep both and select the ledger explicitly with DIBS_STATE")
	}
	if publish {
		if err := syncStateParent(base); err != nil {
			return "", err
		}
	}
	return legacy, nil
}

func (m *Manager) openStore(ctx context.Context, dir string) (*store, error) {
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
	s := &store{Version: 2, Leases: map[string]Lease{}, dir: dir, lock: f, resources: m.options.Adapters}
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
		s.Version = 0
		err = json.Unmarshal(data, s)
	}
	if err == nil {
		err = s.validate()
	}
	if err == nil {
		err = s.migrateMachines()
	}
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("cannot read lease state (kept unchanged): %w", err)
	}
	return s, nil
}

func (s *store) validate() error {
	if (s.Version != 1 && s.Version != 2) || s.Leases == nil {
		return errors.New("unsupported or incomplete state format")
	}
	for _, w := range s.Waiters {
		if !leaseID.MatchString(w.ID) || w.Machine == "" || w.Expires.IsZero() || (w.Owner.Kind != "pid" && w.Owner.Kind != "session") || w.Owner.Identity == "" {
			return errors.New("invalid queue record")
		}
	}
	for id, l := range s.Leases {
		if !leaseID.MatchString(id) || id != l.ID || l.Machine == "" || l.Created.IsZero() || l.Expires.IsZero() || l.TTL <= 0 {
			return errors.New("invalid lease record")
		}
		if l.ResourceRef.Adapter == "process" {
			if l.Reservation == nil || l.Reservation.Budget.Cores <= 0 || l.Reservation.Budget.Memory <= 0 || l.Reservation.Class == "" {
				return errors.New("invalid job reservation")
			}
			if leader := l.Reservation.Leader; leader.ID != "" {
				pid, err := strconv.ParseInt(leader.ID, 10, 32)
				if err != nil || pid <= 1 || leader.Kind != "pid" || leader.Identity == "" {
					return errors.New("invalid job group identity")
				}
			}
			continue
		}
		if l.Reservation != nil {
			return errors.New("job reservation requires process adapter")
		}
		if l.ResourceRef.Adopted && s.Version < 2 {
			return errors.New("invalid adopted resource identity")
		}
		if err := s.resources.Validate(filepath.Join(s.dir, "resources", id), target(l)); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) IDs() []string {
	ids := make([]string, 0, len(s.Leases))
	for id := range s.Leases {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s *store) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicfilepkg.Write(filepath.Join(s.dir, "state.json"), append(data, '\n'))
}

func (s *store) Close() error { return s.lock.Close() }
