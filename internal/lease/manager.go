package lease

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	budgetpkg "github.com/Dematom1/dibs/internal/budget"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

type Options struct {
	Directory string
	Adapters  adapterpkg.Registry
	Owners    ownerpkg.Registry
	// Version is recorded in saved state as its writer.
	Version string
}
type Manager struct{ options Options }

func New(options Options) *Manager { return &Manager{options: options} }
func ValidID(value string) bool    { return leaseID.MatchString(value) }

type FailureStage uint8

const (
	ResolveDirectory FailureStage = iota
	ReadLedger
	ResolveMachine
	Transition
)

type Failure struct {
	Stage FailureStage
	Cause error
}

func (f *Failure) Error() string { return f.Cause.Error() }
func (f *Failure) Unwrap() error { return f.Cause }
func (m *Manager) directory() (string, error) {
	if m.options.Directory != "" {
		return m.options.Directory, nil
	}
	return stateDir()
}
func (m *Manager) open(ctx context.Context) (*store, string, time.Time, error) {
	dir, err := m.directory()
	if err != nil {
		return nil, "", time.Time{}, &Failure{ResolveDirectory, err}
	}
	s, err := m.openStore(ctx, dir)
	if err != nil {
		return nil, "", time.Time{}, &Failure{ReadLedger, err}
	}
	host, err := machine()
	if err != nil {
		s.Close()
		return nil, "", time.Time{}, &Failure{ResolveMachine, err}
	}
	return s, host, time.Now().UTC(), nil
}
func (m *Manager) checkOwner(reference ownerpkg.Reference, now time.Time) ownerpkg.Liveness {
	return m.options.Owners.Check(context.Background(), reference, now).Liveness
}
func target(l Lease) adapterpkg.Target {
	labels := map[string]string(nil)
	if l.Labels != nil {
		labels = make(map[string]string, len(l.Labels))
		for k, v := range l.Labels {
			labels[k] = v
		}
	}
	return adapterpkg.Target{Ref: l.ResourceRef, Metadata: adapterpkg.Metadata{LeaseID: l.ID, Machine: l.Machine, Owner: l.Owner.String(), Expires: l.Expires, Labels: labels}}
}
func detached(l Lease) Lease {
	l.Labels = target(l).Metadata.Labels
	if l.Reservation != nil {
		job := *l.Reservation
		l.Reservation = &job
	}
	return l
}

type CreateRequest struct {
	Owner       ownerpkg.Reference
	TTL         time.Duration
	Description string
	Labels      map[string]string
	Adapter     string
	File        string
	// Key makes a retry return the lease it already created instead of a second one.
	Key string
	// Pending records the lease before the caller creates the resource in Scope,
	// optionally pinning its handle as Name.
	Pending bool
	Name    string
	Scope   adapterpkg.ResourceRef
}

// Create reports true when Key named an existing lease and nothing was done.
func (m *Manager) Create(ctx context.Context, r CreateRequest) (Lease, bool, error) {
	s, host, now, err := m.open(ctx)
	if err != nil {
		return Lease{}, false, err
	}
	defer s.Close()
	if l, done, err := keyed(s, host, r.Key, func(l Lease) bool {
		ref := l.ResourceRef
		if l.Owner.String() != r.Owner.String() || ref.Adapter != r.Adapter || (ref.Pending || ref.Adopted) != r.Pending || (r.Name != "" && ref.Name != r.Name) {
			return false
		}
		if !r.Pending {
			return true
		}
		return ref.Context == r.Scope.Context && ref.DockerHost == r.Scope.DockerHost && l.PendingFile == r.File
	}); done || err != nil {
		return l, done, err
	}
	if m.checkOwner(r.Owner, now) != ownerpkg.Alive {
		return Lease{}, false, errors.New("owner became stale or unavailable while waiting; refresh the owner and retry dibs create")
	}
	l, err := newLease(r.Owner, r.TTL, r.Description, r.Labels, now)
	if err != nil {
		return Lease{}, false, err
	}
	l.Key = r.Key
	d, ok := m.options.Adapters.Lookup(r.Adapter)
	if !ok {
		return Lease{}, false, errors.New("unsupported resource adapter")
	}
	if r.Pending {
		return m.recordPending(ctx, s, l, d, r)
	}
	l.ResourceRef, err = d.Prepare(ctx, adapterpkg.PrepareRequest{Metadata: target(l).Metadata, SnapshotDir: filepath.Join(s.dir, "resources", l.ID), File: r.File})
	if err != nil {
		return Lease{}, false, err
	}
	s.Leases[l.ID] = l
	if err := s.Save(); err != nil {
		return Lease{}, false, err
	}
	if err := d.Adapter.Create(ctx, target(l)); err != nil {
		return Lease{}, false, fmt.Errorf("creation incomplete; lease %s retained for cleanup: %w", l.ID, err)
	}
	return detached(l), false, nil
}

// A pinned handle must not exist yet: an existing resource enters only through adopt.
func (m *Manager) recordPending(ctx context.Context, s *store, l Lease, d adapterpkg.Definition, r CreateRequest) (Lease, bool, error) {
	if d.Discover == nil || (r.Name != "" && !adapterpkg.ValidResourceName(r.Name)) {
		return Lease{}, false, errors.New("unsupported pending resource adapter or handle")
	}
	l.PendingFile = r.File
	l.ResourceRef = r.Scope
	l.ResourceRef.Adapter, l.ResourceRef.Name, l.ResourceRef.File, l.ResourceRef.Pending, l.ResourceRef.Adopted = r.Adapter, r.Name, r.File, true, false
	if r.Name != "" {
		if other, err := claimant(ctx, s, l.ResourceRef, nil, l.ID, adapterpkg.RunCommand); err != nil {
			return Lease{}, false, err
		} else if other != "" {
			return Lease{}, false, refuse("already_leased", "handle "+r.Name+" is already claimed by lease "+other)
		}
		rows, err := d.Discover(ctx, l.ResourceRef)
		if err != nil {
			return Lease{}, false, err
		}
		for _, row := range rows {
			if row["name"] == r.Name {
				return Lease{}, false, refuse("handle_exists", "resource "+r.Name+" already exists; use dibs adopt for an existing resource")
			}
		}
	}
	s.Leases[l.ID] = l
	if err := s.Save(); err != nil {
		return Lease{}, false, err
	}
	return detached(l), false, nil
}

type Filter struct{ Owner string }
type ObservedLease struct {
	Lease    Lease
	Liveness ownerpkg.Liveness
}
type Listing struct {
	Leases   []ObservedLease
	At       time.Time
	Reserved budgetpkg.Amount
}

func (m *Manager) List(ctx context.Context, f Filter) (Listing, error) {
	s, host, now, err := m.open(ctx)
	if err != nil {
		return Listing{}, err
	}
	defer s.Close()
	result := Listing{Leases: []ObservedLease{}, At: now}
	selector := ownerpkg.CanonicalSelector(f.Owner)
	for _, id := range s.IDs() {
		l := s.Leases[id]
		if l.Machine == host && l.Reservation != nil {
			result.Reserved.Cores += l.Reservation.Budget.Cores
			result.Reserved.Memory += l.Reservation.Budget.Memory
		}
		if selector != "" && l.Owner.String() != selector {
			continue
		}
		_, live := reclaimable(l, host, now, m.checkOwner)
		result.Leases = append(result.Leases, ObservedLease{detached(l), live})
	}
	return result, nil
}
func (m *Manager) Get(ctx context.Context, id string) (Lease, bool, error) {
	s, _, _, err := m.open(ctx)
	if err != nil {
		return Lease{}, false, err
	}
	defer s.Close()
	l, ok := s.Leases[id]
	return detached(l), ok, nil
}
func (m *Manager) Extend(ctx context.Context, id string, ttl time.Duration) (Lease, error) {
	s, host, now, err := m.open(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer s.Close()
	l, ok := s.Leases[id]
	if !ok {
		return Lease{}, errors.New("lease does not exist; use dibs create for a new lease")
	}
	if err := extendLease(&l, host, ttl, now, m.checkOwner); err != nil {
		return Lease{}, err
	}
	s.Leases[id] = l
	err = s.Save()
	return detached(l), err
}

type ReleaseRequest struct {
	ID      string
	Confirm bool
}
type ReleaseResult struct {
	ID       string
	Exists   bool
	Released bool
}

func (m *Manager) Release(ctx context.Context, r ReleaseRequest) (ReleaseResult, error) {
	s, host, _, err := m.open(ctx)
	if err != nil {
		return ReleaseResult{}, err
	}
	defer s.Close()
	_, exists := s.Leases[r.ID]
	result := ReleaseResult{ID: r.ID, Exists: exists}
	if exists && r.Confirm {
		result.Released, err = releaseLease(ctx, s, r.ID, host, m.options.Adapters)
	}
	return result, err
}

type SweepRequest struct{ Confirm bool }

func (m *Manager) Sweep(ctx context.Context, r SweepRequest) (SweepResult, error) {
	s, host, now, err := m.open(ctx)
	if err != nil {
		return SweepResult{}, err
	}
	defer s.Close()
	return sweep(ctx, s, host, now, r.Confirm, m.checkOwner, m.options.Adapters), nil
}

type AdmissionRequest struct {
	Owner       ownerpkg.Reference
	Capacity    budgetpkg.Amount
	Request     budgetpkg.Amount
	Class       string
	TTL         time.Duration
	Wait        time.Duration
	Description string
}
type Reservation struct {
	lease     Lease
	directory string
}

func (m *Manager) Admit(ctx context.Context, r AdmissionRequest) (Reservation, error) {
	l, err := newLease(r.Owner, r.TTL, r.Description, nil, time.Now().UTC())
	if err != nil {
		return Reservation{}, &Failure{Transition, err}
	}
	l.ResourceRef.Adapter = "process"
	l.Reservation = &ReservationView{Budget: r.Request, Class: r.Class}
	dir, err := m.directory()
	if err != nil {
		return Reservation{}, &Failure{ResolveDirectory, err}
	}
	l, err = m.admitJob(ctx, dir, l, r.Capacity, r.Wait)
	return Reservation{lease: l, directory: dir}, err
}

type JobResult struct {
	Code    int
	Started bool
	Err     error
}
