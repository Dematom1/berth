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

// stateVersion is the newest state schema this binary reads and writes:
// 3 adds live-owner protection, 4 adds idempotency keys and pending leases,
// 5 adds lease policies and shell leases.
const stateVersion = 5

type store struct {
	Version int `json:"version"`
	// Writer names the binary version behind the last save so an older
	// binary can say which upgrade it needs. Additive; the schema stays 2.
	Writer    string           `json:"writer_version,omitempty"`
	Leases    map[string]Lease `json:"leases"`
	Waiters   []waiter         `json:"waiters,omitempty"`
	dir       string
	lock      *os.File
	resources adapterpkg.Registry
	binary    string
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
	s := &store{Version: stateVersion, Leases: map[string]Lease{}, dir: dir, lock: f, resources: m.options.Adapters, binary: m.options.Version}
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
		err = s.decode(data)
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

// decode refuses a newer schema before reading fields whose meaning it may change.
func (s *store) decode(data []byte) error {
	if err := refuseNewer(data, stateVersion, s.binary); err != nil {
		return err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return err
	}
	return s.validate()
}

// refuseNewer is the check every reader since writer_version applies with its own newest version.
func refuseNewer(data []byte, newest int, binary string) error {
	var header struct {
		Version int             `json:"version"`
		Writer  json.RawMessage `json:"writer_version"`
	}
	if json.Unmarshal(data, &header) != nil || header.Version <= newest {
		return nil
	}
	// writer_version is optional diagnostics; a malformed one must not mask the refusal.
	var recorded string
	writer := ""
	if json.Unmarshal(header.Writer, &recorded) == nil && recorded != "" {
		writer = " written by dibs " + recorded
	}
	return fmt.Errorf("state version %d%s is newer than this binary supports (state version %d, dibs %s); upgrade dibs, and run `which -a dibs berth` to find a stale binary earlier on PATH", header.Version, writer, newest, binary)
}

func (s *store) validate() error {
	if s.Version < 1 || s.Version > stateVersion || s.Leases == nil {
		return errors.New("unsupported or incomplete state format")
	}
	keys := map[string]bool{}
	for _, w := range s.Waiters {
		if !leaseID.MatchString(w.ID) || w.Machine == "" || w.Expires.IsZero() || (w.Owner.Kind != "pid" && w.Owner.Kind != "session") || w.Owner.Identity == "" {
			return errors.New("invalid queue record")
		}
	}
	for id, l := range s.Leases {
		if !leaseID.MatchString(id) || id != l.ID || l.Machine == "" || l.Created.IsZero() || l.Expires.IsZero() || l.TTL <= 0 {
			return errors.New("invalid lease record")
		}
		if l.ProtectLiveOwner && s.Version < 3 {
			return errors.New("invalid live-owner protection")
		}
		if (l.Policy != nil || l.DeadSince != nil || l.ResourceRef.Process != nil) && s.Version < 5 {
			return errors.New("invalid lease policy or shell record")
		}
		if l.DeadSince != nil && (l.Policy == nil || l.Policy.Grace == 0) {
			return errors.New("invalid owner death record")
		}
		if l.Policy != nil {
			if err := l.Policy.Validate(); err != nil {
				return fmt.Errorf("invalid recorded lease policy: %w", err)
			}
		}
		if l.KeyedBy != "" && l.KeyedBy != "create" && l.KeyedBy != "pending" && l.KeyedBy != "adopt" || l.KeyedBy != "" && l.Key == "" {
			return errors.New("invalid lease key mode")
		}
		if l.Key != "" {
			if s.Version < 4 || !ValidKey(l.Key) || keys[l.Machine+"\x00"+l.Key] {
				return errors.New("invalid lease key")
			}
			keys[l.Machine+"\x00"+l.Key] = true
		}
		if l.ResourceRef.Pending {
			d, ok := s.resources.Lookup(l.ResourceRef.Adapter)
			if s.Version < 4 || l.ResourceRef.Adopted || l.Reservation != nil || !ok || d.Discover == nil || (l.ResourceRef.Name != "" && !adapterpkg.ValidResourceName(l.ResourceRef.Name)) {
				return errors.New("invalid pending resource")
			}
			continue
		}
		if l.ResourceRef.Adapter == "process" {
			if l.ProtectLiveOwner || l.Reservation == nil || l.Reservation.Budget.Cores <= 0 || l.Reservation.Budget.Memory <= 0 || l.Reservation.Class == "" {
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
	s.settleVersion()
	s.Writer = s.binary
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicfilepkg.Write(filepath.Join(s.dir, "state.json"), append(data, '\n'))
}

func (s *store) Close() error { return s.lock.Close() }
