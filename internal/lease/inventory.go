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
	for _, l := range s.Leases {
		if l.ResourceRef.Adapter != r.Adapter || l.ResourceRef.Name != r.Name {
			continue
		}
		if l.ResourceRef.Context == r.Context || l.ResourceRef.DockerHost == r.DockerHost {
			return true, nil
		}
		other, err := adapterpkg.ResolveDockerContext(ctx, l.ResourceRef.Context, run)
		if err != nil {
			return false, errors.New("cannot rule out an existing lease in another Docker context")
		}
		if other.DockerHost == r.DockerHost {
			return true, nil
		}
	}
	return false, nil
}

type AdoptRequest struct {
	Owner    ownerpkg.Reference
	TTL      time.Duration
	Resource adapterpkg.ResourceRef
	File     string
}

func (m *Manager) Adopt(ctx context.Context, r AdoptRequest) (Lease, error) {
	s, _, now, err := m.open(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer s.Close()
	if m.checkOwner(r.Owner, now) != ownerpkg.Alive {
		return Lease{}, errors.New("owner became unavailable while waiting; refresh the owner and retry dibs adopt")
	}
	l, err := newLease(r.Owner, r.TTL, "adopted existing resource", nil, now)
	if err != nil {
		return Lease{}, err
	}
	d, ok := m.options.Adapters.Lookup(r.Resource.Adapter)
	if !ok || d.Discover == nil || !adapterpkg.ValidResourceName(r.Resource.Name) || d.RequiresFile != (r.File != "") {
		return Lease{}, errors.New("unsupported adoption target or missing Compose file")
	}
	leased, err := resourceLeased(ctx, s, r.Resource, adapterpkg.RunCommand)
	if err != nil {
		return Lease{}, err
	}
	if leased {
		return Lease{}, errors.New("resource is already under a lease; inspect dibs ps")
	}
	rows, err := d.Discover(ctx, r.Resource)
	if err != nil {
		return Lease{}, err
	}
	found := false
	for _, row := range rows {
		if row["name"] == r.Resource.Name {
			found = true
		}
	}
	if !found {
		return Lease{}, errors.New("resource does not exist; adoption refused")
	}
	r.Resource.Adopted = true
	l.ResourceRef, err = d.Prepare(ctx, adapterpkg.PrepareRequest{Metadata: target(l).Metadata, SnapshotDir: filepath.Join(s.dir, "resources", l.ID), File: r.File, Existing: r.Resource})
	if err != nil {
		return Lease{}, err
	}
	l.Labels = map[string]string{"io.dibs.lease": l.ID, "io.dibs.owner": l.Owner.String(), "io.dibs.expires": l.Expires.Format(time.RFC3339Nano), "io.dibs.machine": l.Machine}
	s.Version = stateVersion
	s.Leases[l.ID] = l
	if err := s.Save(); err != nil {
		return Lease{}, err
	}
	return detached(l), nil
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
		leased, err := resourceLeased(ctx, s, r, adapterpkg.RunCommand)
		if err != nil {
			return nil, err
		}
		if !leased {
			unleased = append(unleased, row)
		}
	}
	return unleased, nil
}
