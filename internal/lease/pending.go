package lease

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

// LeaseLabel is the label a caller stamps on a resource it builds for a pending
// lease when the tool, not the caller, assigns the resource's handle.
const LeaseLabel = "io.dibs.lease"

// Refusal is a safety refusal that changed nothing. Reason is a stable contract value.
type Refusal struct{ Reason, Message string }

func (r *Refusal) Error() string { return r.Message }

func refuse(reason, message string) error { return &Refusal{reason, message} }

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+=-]{0,199}$`)

func ValidKey(key string) bool { return keyPattern.MatchString(key) }

// Version 3 marks records older binaries must not misread; it lasts only while one exists.
func (s *store) settleVersion() {
	for _, l := range s.Leases {
		if l.Key != "" || l.ResourceRef.Pending {
			s.Version = 3
			return
		}
	}
	if s.Version == 3 {
		s.Version = 2
	}
}

// keyed returns the live lease that already answers an idempotency key, or a
// key_conflict refusal when the key names a different request.
func keyed(s *store, host, key string, same func(Lease) bool) (Lease, bool, error) {
	if key == "" {
		return Lease{}, false, nil
	}
	for _, id := range s.IDs() {
		l := s.Leases[id]
		if l.Key != key || l.Machine != host {
			continue
		}
		if !same(l) {
			return Lease{}, false, refuse("key_conflict", "key "+key+" already names lease "+l.ID+" for a different request")
		}
		return detached(l), true, nil
	}
	return Lease{}, false, nil
}

func rowLabels(row map[string]any) map[string]string {
	switch labels := row["labels"].(type) {
	case map[string]string:
		return labels
	case map[string]any:
		out := map[string]string{}
		for k, v := range labels {
			if text, ok := v.(string); ok {
				out[k] = text
			}
		}
		return out
	}
	return nil
}

func sameEngine(ctx context.Context, a, b adapterpkg.ResourceRef, run adapterpkg.CommandRunner) (bool, error) {
	if a.Context == b.Context || a.DockerHost == b.DockerHost {
		return true, nil
	}
	other, err := adapterpkg.ResolveDockerContext(ctx, a.Context, run)
	if err != nil {
		return false, errors.New("cannot rule out an existing lease in another Docker context")
	}
	return other.DockerHost == b.DockerHost, nil
}

// claimant names the lease, other than except, that owns or awaits this resource.
// A pending lease claims the handle it pinned and anything stamped with its ID.
func claimant(ctx context.Context, s *store, r adapterpkg.ResourceRef, labels map[string]string, except string, run adapterpkg.CommandRunner) (string, error) {
	for _, id := range s.IDs() {
		l := s.Leases[id]
		if id == except || l.ResourceRef.Adapter != r.Adapter {
			continue
		}
		if l.ResourceRef.Pending && labels[LeaseLabel] == id {
			return id, nil
		}
		if l.ResourceRef.Name != r.Name {
			continue
		}
		same, err := sameEngine(ctx, l.ResourceRef, r, run)
		if err != nil {
			return "", err
		}
		if same {
			return id, nil
		}
	}
	return "", nil
}

// matches finds resources built for a pending lease from evidence recorded before
// they existed: the pinned handle or the lease's label. Matches under another
// lease are conflicts, never candidates.
func matches(ctx context.Context, s *store, l Lease, run adapterpkg.CommandRunner) (free, conflicts []string, err error) {
	d, ok := s.resources.Lookup(l.ResourceRef.Adapter)
	if !ok || d.Discover == nil {
		return nil, nil, fmt.Errorf("adapter %q cannot discover resources; keep the lease and use a compatible dibs version", l.ResourceRef.Adapter)
	}
	rows, err := d.Discover(ctx, l.ResourceRef)
	if err != nil {
		return nil, nil, err
	}
	for _, row := range rows {
		name, _ := row["name"].(string)
		labels := rowLabels(row)
		if !(l.ResourceRef.Name != "" && name == l.ResourceRef.Name) && labels[LeaseLabel] != l.ID {
			continue
		}
		ref := l.ResourceRef
		ref.Name = name
		other, err := claimant(ctx, s, ref, labels, l.ID, run)
		if err != nil {
			return nil, nil, err
		}
		if other != "" {
			conflicts = append(conflicts, name)
		} else {
			free = append(free, name)
		}
	}
	return free, conflicts, nil
}

// bind turns a pending lease into an adopted lease of handle and saves it.
func bind(ctx context.Context, s *store, l *Lease, handle, file string) error {
	d, ok := s.resources.Lookup(l.ResourceRef.Adapter)
	if !ok {
		return errors.New("unsupported resource adapter")
	}
	existing := l.ResourceRef
	existing.Name, existing.Pending, existing.Adopted, existing.File = handle, false, true, ""
	ref, err := d.Prepare(ctx, adapterpkg.PrepareRequest{Metadata: target(*l).Metadata, SnapshotDir: filepath.Join(s.dir, "resources", l.ID), File: file, Existing: existing})
	if err != nil {
		return err
	}
	bound := *l
	bound.ResourceRef = ref
	bound.Labels = map[string]string{}
	for k, v := range l.Labels {
		bound.Labels[k] = v
	}
	for k, v := range attribution(bound) {
		bound.Labels[k] = v
	}
	s.Leases[l.ID] = bound
	if err := s.Save(); err != nil {
		s.Leases[l.ID] = *l
		return err
	}
	*l = bound
	return nil
}

func attribution(l Lease) map[string]string {
	return map[string]string{LeaseLabel: l.ID, "io.dibs.owner": l.Owner.String(), "io.dibs.expires": l.Expires.Format(time.RFC3339Nano), "io.dibs.machine": l.Machine}
}

// settlePending binds a pending lease's single match before cleanup, or reports
// that nothing was ever created. Any doubt retains the lease.
func settlePending(ctx context.Context, s *store, l *Lease) (absent bool, err error) {
	free, _, err := matches(ctx, s, *l, adapterpkg.RunCommand)
	if err != nil {
		return false, err
	}
	switch len(free) {
	case 0:
		return true, nil
	case 1:
		return false, bind(ctx, s, l, free[0], l.ResourceRef.File)
	}
	return false, refuse("ambiguous", fmt.Sprintf("pending lease %s matches %d resources; release each explicitly", l.ID, len(free)))
}

type PendingRequest struct {
	ID     string
	Handle string
	File   string
}

func (m *Manager) bindPending(ctx context.Context, s *store, host string, r PendingRequest) (Lease, bool, error) {
	l, ok := s.Leases[r.ID]
	if !ok {
		return Lease{}, false, refuse("not_found", "pending lease "+r.ID+" does not exist on this machine")
	}
	if l.Machine != host {
		return Lease{}, false, refuse("wrong_machine", "run dibs adopt on the lease's machine")
	}
	if !l.ResourceRef.Pending {
		if l.ResourceRef.Name == r.Handle {
			return detached(l), true, nil
		}
		return Lease{}, false, refuse("not_pending", "lease "+r.ID+" already covers "+l.ResourceRef.Adapter+":"+l.ResourceRef.Name)
	}
	if l.ResourceRef.Name != "" && l.ResourceRef.Name != r.Handle {
		return Lease{}, false, refuse("handle_mismatch", "pending lease "+r.ID+" was recorded for "+l.ResourceRef.Name)
	}
	d, _ := s.resources.Lookup(l.ResourceRef.Adapter)
	rows, err := d.Discover(ctx, l.ResourceRef)
	if err != nil {
		return Lease{}, false, err
	}
	var labels map[string]string
	found := false
	for _, row := range rows {
		if row["name"] == r.Handle {
			found, labels = true, rowLabels(row)
		}
	}
	if !found {
		return Lease{}, false, refuse("resource_absent", "resource "+r.Handle+" does not exist; create it, then retry dibs adopt")
	}
	ref := l.ResourceRef
	ref.Name = r.Handle
	other, err := claimant(ctx, s, ref, labels, l.ID, adapterpkg.RunCommand)
	if err != nil {
		return Lease{}, false, err
	}
	if other != "" {
		return Lease{}, false, refuse("already_leased", "resource is already under lease "+other+"; inspect dibs ps")
	}
	file := r.File
	if file == "" {
		file = l.ResourceRef.File
	}
	if err := bind(ctx, s, &l, r.Handle, file); err != nil {
		return Lease{}, false, err
	}
	return detached(l), false, nil
}

type ReconcileRow struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"`
	Adapter   string   `json:"resource_adapter"`
	Name      string   `json:"resource_name"`
	Matches   []string `json:"matches"`
	Conflicts []string `json:"conflicts"`
	Error     string   `json:"error,omitempty"`
}

// Reconcile resolves pending leases left by a crash between create and register.
// It binds only a single, unclaimed match and only with confirm.
func (m *Manager) Reconcile(ctx context.Context, id string, confirm bool) ([]ReconcileRow, error) {
	s, host, _, err := m.open(ctx)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ids := []string{}
	if id != "" {
		l, ok := s.Leases[id]
		if !ok {
			return nil, refuse("not_found", "lease "+id+" does not exist on this machine")
		}
		if l.Machine != host {
			return nil, refuse("wrong_machine", "run dibs reconcile on the lease's machine")
		}
		if !l.ResourceRef.Pending {
			return []ReconcileRow{{ID: id, Status: "already_done", Adapter: l.ResourceRef.Adapter, Name: l.ResourceRef.Name, Matches: []string{}, Conflicts: []string{}}}, nil
		}
		ids = append(ids, id)
	} else {
		for _, candidate := range s.IDs() {
			if l := s.Leases[candidate]; l.ResourceRef.Pending && l.Machine == host {
				ids = append(ids, candidate)
			}
		}
	}
	rows := []ReconcileRow{}
	for _, id := range ids {
		l := s.Leases[id]
		row := ReconcileRow{ID: id, Adapter: l.ResourceRef.Adapter, Name: l.ResourceRef.Name, Matches: []string{}, Conflicts: []string{}}
		free, conflicts, err := matches(ctx, s, l, adapterpkg.RunCommand)
		row.Matches, row.Conflicts = append(row.Matches, free...), append(row.Conflicts, conflicts...)
		switch {
		case err != nil:
			row.Status, row.Error = "unknown", err.Error()
		case len(free) > 1:
			row.Status = "ambiguous"
		case len(free) == 1 && !confirm:
			row.Status = "bindable"
		case len(free) == 1:
			if err := bind(ctx, s, &l, free[0], l.ResourceRef.File); err != nil {
				row.Status, row.Error = "unknown", err.Error()
			} else {
				row.Status, row.Name = "bound", free[0]
			}
		case len(conflicts) > 0:
			row.Status = "conflict"
		default:
			row.Status = "absent"
		}
		rows = append(rows, row)
	}
	return rows, nil
}

type ReclaimResult struct {
	ID       string
	Status   string
	Liveness ownerpkg.Liveness
}

// Reclaim destroys a lease's resource only when its owner is proven dead.
func (m *Manager) Reclaim(ctx context.Context, id string, confirm bool) (ReclaimResult, error) {
	s, host, now, err := m.open(ctx)
	if err != nil {
		return ReclaimResult{}, err
	}
	defer s.Close()
	l, ok := s.Leases[id]
	if !ok {
		return ReclaimResult{ID: id, Status: "already_done", Liveness: ownerpkg.Unknown}, nil
	}
	if l.Machine != host {
		return ReclaimResult{}, refuse("wrong_machine", "run dibs reclaim on the lease's machine")
	}
	live := m.checkOwner(l.Owner, now)
	switch live {
	case ownerpkg.Alive:
		return ReclaimResult{}, refuse("owner_alive", "owner "+l.Owner.String()+" is alive; reclaim refused")
	case ownerpkg.Unknown:
		return ReclaimResult{}, refuse("owner_unknown", "owner "+l.Owner.String()+" cannot be verified; reclaim refused")
	}
	if !confirm {
		return ReclaimResult{ID: id, Status: "preview", Liveness: live}, nil
	}
	if _, err := releaseLease(ctx, s, id, host, m.options.Adapters); err != nil {
		return ReclaimResult{}, err
	}
	return ReclaimResult{ID: id, Status: "reclaimed", Liveness: live}, nil
}

type Explanation struct {
	At          time.Time
	Lease       Lease
	Liveness    ownerpkg.Liveness
	Expired     bool
	ThisMachine bool
	Sweep       bool
	Reclaim     bool
	Reason      string
}

// Explain reports, without acting, whether sweep or reclaim would act on a lease.
func (m *Manager) Explain(ctx context.Context, id string) (Explanation, error) {
	s, host, now, err := m.open(ctx)
	if err != nil {
		return Explanation{}, err
	}
	defer s.Close()
	l, ok := s.Leases[id]
	if !ok {
		return Explanation{}, refuse("not_found", "lease "+id+" does not exist on this machine")
	}
	e := Explanation{At: now, Lease: detached(l), Expired: !now.Before(l.Expires), ThisMachine: l.Machine == host}
	e.Sweep, e.Liveness = reclaimable(l, host, now, m.checkOwner)
	e.Reclaim = e.ThisMachine && e.Liveness == ownerpkg.Dead
	switch {
	case !e.ThisMachine:
		e.Reason = "lease belongs to another machine; only that machine may reclaim it"
	case e.Liveness == ownerpkg.Dead:
		e.Reason = "owner is dead; sweep and reclaim would destroy the resource"
	case e.Sweep:
		e.Reason = "lease expired; sweep would destroy the resource, reclaim would refuse (owner " + string(e.Liveness) + ")"
	case e.Liveness == ownerpkg.Alive:
		e.Reason = "owner is alive and lease is unexpired; nothing would reclaim it"
	default:
		e.Reason = "owner liveness is unknown and lease is unexpired; nothing would reclaim it"
	}
	if l.ResourceRef.Pending {
		e.Reason += "; the resource is pending registration"
	}
	return e, nil
}
