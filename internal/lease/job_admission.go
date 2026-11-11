package lease

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	budgetpkg "github.com/Dematom1/dibs/internal/budget"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

type ReservationView struct {
	Budget budgetpkg.Amount   `json:"budget"`
	Class  string             `json:"class"`
	Leader ownerpkg.Reference `json:"leader"`
}

type waiter struct {
	ID      string             `json:"id"`
	Machine string             `json:"machine"`
	Owner   ownerpkg.Reference `json:"owner"`
	Expires time.Time          `json:"expires"`
}

func budgetFits(s *store, host string, capacity, request budgetpkg.Amount) bool {
	for _, l := range s.Leases {
		if l.Machine != host || l.Reservation == nil {
			continue
		}
		capacity.Cores = max(0, capacity.Cores-l.Reservation.Budget.Cores)
		capacity.Memory = max(0, capacity.Memory-l.Reservation.Budget.Memory)
	}
	return request.Cores <= capacity.Cores && request.Memory <= capacity.Memory
}

func (m *Manager) pruneWaiters(s *store, host string, now time.Time) {
	// ponytail: linear queue scans poll owner PIDs; add event-driven wakeups only if queue size makes this costly.
	kept := s.Waiters[:0]
	for _, w := range s.Waiters {
		if w.Machine != host || (now.Before(w.Expires) && m.checkOwner(w.Owner, now) != ownerpkg.Dead) {
			kept = append(kept, w)
		}
	}
	s.Waiters = kept
}

func removeWaiter(s *store, id string) {
	for i, w := range s.Waiters {
		if w.ID == id {
			s.Waiters = append(s.Waiters[:i], s.Waiters[i+1:]...)
			return
		}
	}
}

var ErrNoCapacity = errors.New("no slot freed within --wait")

func (m *Manager) admitJob(ctx context.Context, dir string, l Lease, capacity budgetpkg.Amount, wait time.Duration) (Lease, error) {
	deadline := time.Now().Add(wait)
	lockCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	queued := false
	defer func() {
		if !queued {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if s, err := m.openStore(cleanupCtx, dir); err == nil {
			defer s.Close()
			removeWaiter(s, l.ID)
			if err := s.Save(); err != nil {
				fmt.Fprintln(os.Stderr, "dibs: waiter cleanup failed; it expires automatically:", err)
			}
		}
	}()
	for {
		s, err := m.openStore(lockCtx, dir)
		if err != nil {
			if ctx.Err() == nil && lockCtx.Err() != nil {
				return l, ErrNoCapacity
			}
			return l, err
		}
		now := time.Now().UTC()
		previousCount := len(s.Waiters)
		m.pruneWaiters(s, l.Machine, now)
		changed := len(s.Waiters) != previousCount
		if ctx.Err() != nil || (wait > 0 && !now.Before(deadline)) {
			s.Close()
			if ctx.Err() != nil {
				return l, ctx.Err()
			}
			return l, ErrNoCapacity
		}
		if !queued && wait > 0 {
			s.Waiters = append(s.Waiters, waiter{l.ID, l.Machine, l.Owner, deadline})
			queued, changed = true, true
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(ctx, 5*time.Second)
		for _, id := range s.IDs() {
			if cleanupCtx.Err() != nil {
				break
			}
			existing := s.Leases[id]
			if existing.Reservation != nil {
				if eligible, _ := reclaimable(existing, l.Machine, now, m.checkOwner); eligible {
					if _, err := releaseLease(cleanupCtx, s, id, l.Machine, m.options.Adapters); err != nil {
						fmt.Fprintln(os.Stderr, "dibs: retaining job reservation after cleanup failure:", err)
					}
				}
			}
		}
		cancelCleanup()
		now = time.Now().UTC()
		if ctx.Err() != nil || (wait > 0 && !now.Before(deadline)) {
			s.Close()
			if ctx.Err() != nil {
				return l, ctx.Err()
			}
			return l, ErrNoCapacity
		}
		first := true
		for _, w := range s.Waiters {
			if w.Machine == l.Machine {
				first = queued && w.ID == l.ID && now.Before(w.Expires)
				break
			}
		}
		if first && budgetFits(s, l.Machine, capacity, l.Reservation.Budget) {
			removeWaiter(s, l.ID)
			l.Created, l.Expires = now, now.Add(l.TTL)
			s.Leases[l.ID] = l
			err = s.Save()
			s.Close()
			if err == nil {
				queued = false
			}
			return l, err
		}
		if changed {
			err = s.Save()
		}
		s.Close()
		if err != nil {
			return l, err
		}
		if wait == 0 {
			return l, ErrNoCapacity
		}
		select {
		case <-lockCtx.Done():
			if ctx.Err() != nil {
				return l, ctx.Err()
			}
			return l, ErrNoCapacity
		case <-time.After(50 * time.Millisecond):
		}
	}
}
