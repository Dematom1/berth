package lease

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

type Lease struct {
	ID          string                 `json:"id"`
	Machine     string                 `json:"machine"`
	Owner       ownerpkg.Reference     `json:"owner"`
	Created     time.Time              `json:"created"`
	Expires     time.Time              `json:"expires"`
	TTL         time.Duration          `json:"ttl"`
	Description string                 `json:"resources"`
	Labels      map[string]string      `json:"labels"`
	Key         string                 `json:"key,omitempty"`
	PendingFile string                 `json:"pending_file,omitempty"`
	ResourceRef adapterpkg.ResourceRef `json:"resource"`
	Reservation *ReservationView       `json:"job,omitempty"`
	// Opt-in: only proven owner death reclaims; expiry alone never does.
	ProtectLiveOwner bool `json:"protect_live_owner,omitempty"`
}

func newLease(owner ownerpkg.Reference, ttl time.Duration, resources string, labels map[string]string, now time.Time) (Lease, error) {
	if ttl <= 0 {
		return Lease{}, errors.New("--ttl must be positive")
	}
	host, err := machine()
	if err != nil {
		return Lease{}, err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return Lease{}, err
	}
	id := fmt.Sprintf("%x", token)
	return Lease{ID: id, Machine: host, Owner: owner, Created: now, Expires: now.Add(ttl), TTL: ttl, Description: resources, Labels: labels, ResourceRef: adapterpkg.ResourceRef{Adapter: "none"}}, nil
}

func reclaimable(l Lease, host string, now time.Time, check func(ownerpkg.Reference, time.Time) ownerpkg.Liveness) (bool, ownerpkg.Liveness) {
	if l.Machine != host {
		return false, ownerpkg.Unknown
	}
	live := check(l.Owner, now)
	if l.ProtectLiveOwner {
		return live == ownerpkg.Dead, live
	}
	return live == ownerpkg.Dead || !now.Before(l.Expires), live
}

// RenewRefusal is a protected lease's renew refusal; the lease is left unchanged.
type RenewRefusal struct {
	Reason string
	Cause  error
}

const (
	OwnerDead       = "owner_dead"
	OwnerUnknown    = "owner_unknown"
	ResourceAbsent  = "resource_absent"
	ResourceUnknown = "resource_unknown"
)

func (r *RenewRefusal) Error() string {
	message := map[string]string{
		OwnerDead:       "renew refused: the lease owner is dead; the lease is retained for reclaim",
		OwnerUnknown:    "renew refused: the lease owner cannot be verified; the lease is retained and not reclaimable",
		ResourceAbsent:  "renew refused: the expired lease's resource is gone; the lease is retained",
		ResourceUnknown: "renew refused: the expired lease's resource cannot be verified; the lease is retained",
	}[r.Reason]
	if r.Cause != nil {
		message += ": " + r.Cause.Error()
	}
	return message
}
func (r *RenewRefusal) Unwrap() error { return r.Cause }

const (
	Renewed    = "renewed"
	Reattached = "reattached"
)

// present reports whether a lease's pinned resource still exists; an error means unknown.
func extendLease(l *Lease, host string, ttl time.Duration, now time.Time, check func(ownerpkg.Reference, time.Time) ownerpkg.Liveness, present func(Lease) (bool, error)) (string, error) {
	if ttl <= 0 {
		return "", errors.New("--ttl must be positive")
	}
	if l.Machine != host {
		return "", errors.New("run dibs extend on the lease's machine")
	}
	if l.Reservation != nil {
		return "", errors.New("job deadlines cannot be extended; start a new dibs job with --timeout")
	}
	status := Renewed
	if !l.ProtectLiveOwner {
		if reclaim, live := reclaimable(*l, host, now, check); reclaim || live != ownerpkg.Alive {
			return "", errors.New("lease is expired, dead, or its owner cannot be verified; create a new lease")
		}
	} else {
		switch check(l.Owner, now) {
		case ownerpkg.Alive:
		case ownerpkg.Dead:
			return "", &RenewRefusal{Reason: OwnerDead}
		default:
			return "", &RenewRefusal{Reason: OwnerUnknown}
		}
		// A late renew reattaches only to the same, still-present resource; it never recreates one.
		if !now.Before(l.Expires) {
			found, err := present(*l)
			if err != nil {
				return "", &RenewRefusal{Reason: ResourceUnknown, Cause: err}
			}
			if !found {
				return "", &RenewRefusal{Reason: ResourceAbsent}
			}
			status = Reattached
		}
	}
	l.TTL, l.Expires = ttl, now.Add(ttl)
	return status, nil
}

// A cleanup failure keeps the durable intent; a later release or sweep retries it.
func releaseLease(ctx context.Context, s *store, id, host string, adapters adapterpkg.Registry) (bool, error) {
	l, ok := s.Leases[id]
	if !ok {
		return false, nil
	}
	if l.Machine != host {
		return false, errors.New("run dibs release on the lease's machine")
	}
	a, ok := adapters.Lookup(l.ResourceRef.Adapter)
	if !ok && l.ResourceRef.Adapter != "process" {
		return false, fmt.Errorf("unsupported resource adapter %q; keep the lease and use a compatible dibs version", l.ResourceRef.Adapter)
	}
	if l.ResourceRef.Pending {
		absent, err := settlePending(ctx, s, &l)
		if err != nil {
			return false, err
		}
		if absent {
			return forget(s, l)
		}
	}
	var cleanupErr error
	if l.ResourceRef.Adapter == "process" {
		cleanupErr = (processResource{}).Destroy(ctx, l)
	} else {
		cleanupErr = a.Adapter.Destroy(ctx, target(l))
	}
	if err := cleanupErr; err != nil {
		return false, err
	}
	return forget(s, l)
}

func forget(s *store, l Lease) (bool, error) {
	delete(s.Leases, l.ID)
	if err := s.Save(); err != nil {
		s.Leases[l.ID] = l
		return false, err
	}
	return true, nil
}

type SweepResult struct {
	Reclaimed []string          `json:"reclaimed"`
	Eligible  []string          `json:"eligible"`
	Unknown   []string          `json:"unknown"`
	Failures  map[string]string `json:"failures"`
}

func sweep(ctx context.Context, s *store, host string, now time.Time, confirm bool, check func(ownerpkg.Reference, time.Time) ownerpkg.Liveness, adapters adapterpkg.Registry) SweepResult {
	r := SweepResult{Reclaimed: []string{}, Eligible: []string{}, Unknown: []string{}, Failures: map[string]string{}}
	for _, id := range s.IDs() {
		l := s.Leases[id]
		eligible, live := reclaimable(l, host, now, check)
		if live == ownerpkg.Unknown {
			r.Unknown = append(r.Unknown, id)
		}
		if !eligible {
			continue
		}
		r.Eligible = append(r.Eligible, id)
		if confirm {
			if _, err := releaseLease(ctx, s, id, host, adapters); err != nil {
				r.Failures[id] = err.Error()
			} else {
				r.Reclaimed = append(r.Reclaimed, id)
			}
		}
	}
	return r
}
