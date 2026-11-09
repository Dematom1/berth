package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"time"
)

type Lease struct {
	ID        string            `json:"id"`
	Machine   string            `json:"machine"`
	Owner     Owner             `json:"owner"`
	Created   time.Time         `json:"created"`
	Expires   time.Time         `json:"expires"`
	TTL       time.Duration     `json:"ttl"`
	Resources string            `json:"resources"`
	Labels    map[string]string `json:"labels"`
	Resource  Resource          `json:"resource"`
}

type Resource struct {
	Adapter string `json:"adapter"`
	Name    string `json:"name,omitempty"`
	Context string `json:"docker_context,omitempty"`
	File    string `json:"file,omitempty"`
}

type Liveness string

const (
	Alive   Liveness = "alive"
	Dead    Liveness = "dead"
	Unknown Liveness = "unknown"
)

func machine() (string, error) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "", errors.New("cannot determine this machine's hostname")
	}
	return host, nil
}

func newLease(owner Owner, ttl time.Duration, resources string, labels map[string]string, now time.Time) (Lease, error) {
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
	return Lease{ID: id, Machine: host, Owner: owner, Created: now, Expires: now.Add(ttl), TTL: ttl, Resources: resources, Labels: labels, Resource: Resource{Adapter: "none"}}, nil
}

func reclaimable(l Lease, host string, now time.Time, check func(Owner, time.Time) Liveness) (bool, Liveness) {
	if l.Machine != host {
		return false, Unknown
	}
	live := check(l.Owner, now)
	return live == Dead || !now.Before(l.Expires), live
}

func extendLease(l *Lease, host string, ttl time.Duration, now time.Time, check func(Owner, time.Time) Liveness) error {
	if ttl <= 0 {
		return errors.New("--ttl must be positive")
	}
	if l.Machine != host {
		return errors.New("run berth extend on the lease's machine")
	}
	if reclaim, live := reclaimable(*l, host, now, check); reclaim || live != Alive {
		return errors.New("lease is expired, dead, or its owner cannot be verified; create a new lease")
	}
	l.TTL, l.Expires = ttl, now.Add(ttl)
	return nil
}

// A cleanup failure keeps the durable intent; a later release or sweep retries it.
func releaseLease(ctx context.Context, s *Store, id, host string, adapters map[string]Adapter) (bool, error) {
	l, ok := s.Leases[id]
	if !ok {
		return false, nil
	}
	if l.Machine != host {
		return false, errors.New("run berth release on the lease's machine")
	}
	a, ok := adapters[l.Resource.Adapter]
	if !ok {
		return false, fmt.Errorf("unsupported resource adapter %q; keep the lease and use a compatible berth version", l.Resource.Adapter)
	}
	if err := a.Destroy(ctx, l); err != nil {
		return false, err
	}
	delete(s.Leases, id)
	if err := s.Save(); err != nil {
		s.Leases[id] = l
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

func sweep(ctx context.Context, s *Store, host string, now time.Time, confirm bool, check func(Owner, time.Time) Liveness, adapters map[string]Adapter) SweepResult {
	r := SweepResult{Reclaimed: []string{}, Eligible: []string{}, Unknown: []string{}, Failures: map[string]string{}}
	for _, id := range s.IDs() {
		l := s.Leases[id]
		eligible, live := reclaimable(l, host, now, check)
		if live == Unknown {
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
