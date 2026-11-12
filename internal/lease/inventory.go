package lease

import (
	"context"
	"errors"
	"fmt"
	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	"os"
	"path/filepath"
	"time"
)

func (m *Manager) readInventoryStore(dir string) (*store, error) {
	s := &store{Version: 1, Leases: map[string]Lease{}, dir: dir, resources: m.options.Adapters, binary: m.options.Version}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err == nil {
		err = s.decode(data)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read lease state (kept unchanged): %w", err)
	}
	return s, nil
}
func resourceLeased(ctx context.Context, s *store, r adapterpkg.ResourceRef, run adapterpkg.CommandRunner) (bool, error) {
	id, err := claimant(ctx, s, r, nil, "", run)
	return id != "", err
}

type AdoptRequest struct {
	Owner            ownerpkg.Reference
	TTL              time.Duration
	Resource         adapterpkg.ResourceRef
	File             string
	ProtectLiveOwner bool
	Key              string
	// Pending binds Resource.Name to this pending lease instead of a new one.
	Pending string
}

// Adopt reports true when the request was already done and nothing changed.
func (m *Manager) Adopt(ctx context.Context, r AdoptRequest) (Lease, bool, error) {
	s, host, now, err := m.open(ctx)
	if err != nil {
		return Lease{}, false, err
	}
	defer s.Close()
	if r.Pending != "" {
		return m.bindPending(ctx, s, host, PendingRequest{ID: r.Pending, Adapter: r.Resource.Adapter, Handle: r.Resource.Name, File: r.File, Scope: r.Resource})
	}
	if l, done, err := keyed(s, host, r.Key, func(l Lease) bool {
		ref := l.ResourceRef
		return l.Owner.String() == r.Owner.String() && l.ProtectLiveOwner == r.ProtectLiveOwner && ref.Adapter == r.Resource.Adapter && ref.Name == r.Resource.Name && ref.DockerHost == r.Resource.DockerHost
	}); done || err != nil {
		return l, done, err
	}
	if m.checkOwner(r.Owner, now) != ownerpkg.Alive {
		return Lease{}, false, errors.New("owner became unavailable while waiting; refresh the owner and retry dibs adopt")
	}
	l, err := newLease(r.Owner, r.TTL, "adopted existing resource", nil, now)
	if err != nil {
		return Lease{}, false, err
	}
	l.Key = r.Key
	l.ProtectLiveOwner = r.ProtectLiveOwner
	d, ok := m.options.Adapters.Lookup(r.Resource.Adapter)
	if !ok || d.Discover == nil || !adapterpkg.ValidResourceName(r.Resource.Name) || d.RequiresFile != (r.File != "") {
		return Lease{}, false, errors.New("unsupported adoption target or missing Compose file")
	}
	leased := refuse("already_leased", "resource is already under a lease; inspect dibs ps")
	if other, err := claimant(ctx, s, r.Resource, nil, "", adapterpkg.RunCommand); err != nil {
		return Lease{}, false, err
	} else if other != "" {
		return Lease{}, false, leased
	}
	rows, err := d.Discover(ctx, r.Resource)
	if err != nil {
		return Lease{}, false, err
	}
	found := false
	for _, row := range rows {
		if row["name"] != r.Resource.Name {
			continue
		}
		found = true
		// A resource stamped for a pending lease belongs to that lease, not to a new one.
		if other, err := claimant(ctx, s, r.Resource, rowLabels(row), "", adapterpkg.RunCommand); err != nil {
			return Lease{}, false, err
		} else if other != "" {
			return Lease{}, false, leased
		}
	}
	if !found {
		return Lease{}, false, refuse("resource_absent", "resource does not exist; adoption refused")
	}
	r.Resource.Adopted = true
	l.ResourceRef, err = d.Prepare(ctx, adapterpkg.PrepareRequest{Metadata: target(l).Metadata, SnapshotDir: filepath.Join(s.dir, "resources", l.ID), File: r.File, Existing: r.Resource})
	if err != nil {
		return Lease{}, false, err
	}
	l.Labels = attribution(l)
	if s.Version < 2 {
		s.Version = 2
	}

	s.Leases[l.ID] = l
	if err := s.Save(); err != nil {
		return Lease{}, false, err
	}
	return detached(l), false, nil
}
func (m *Manager) Orphans(ctx context.Context, r adapterpkg.ResourceRef) ([]map[string]any, error) {
	rows := []map[string]any{}
	for _, name := range m.options.Adapters.CreateNames() {
		d, _ := m.options.Adapters.Lookup(name)
		if d.Discover == nil {
			continue
		}
		found, err := d.Discover(ctx, r)
		if err != nil {
			return nil, err
		}
		rows = append(rows, found...)
	}
	dir := m.options.Directory
	var err error
	if dir == "" {
		dir, err = resolveStateDir(false)
	}
	if err != nil {
		return nil, err
	}
	s, err := m.readInventoryStore(dir)
	if err != nil {
		return nil, err
	}
	unleased := []map[string]any{}
	for _, row := range rows {
		r.Adapter, r.Name = row["type"].(string), row["name"].(string)
		other, err := claimant(ctx, s, r, rowLabels(row), "", adapterpkg.RunCommand)
		if err != nil {
			return nil, err
		}
		if other == "" {
			unleased = append(unleased, row)
		}
	}
	return unleased, nil
}
