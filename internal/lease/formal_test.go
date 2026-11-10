//go:build formal

package lease

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	budgetpkg "github.com/Dematom1/berth/internal/budget"
	ownerpkg "github.com/Dematom1/berth/internal/owner"
)

// Executable bindings, not a proof of the Go binary. Independent truth-table
// oracle: dead is reclaimable; unknown needs expiry; foreign is never eligible.
// Alive+expired is a KNOWN counterexample F1, not the requested safety promise.

func TestFormalLeaseDomain(t *testing.T) {
	for _, live := range []ownerpkg.Liveness{ownerpkg.Alive, ownerpkg.Dead, ownerpkg.Unknown} {
		for _, local := range []bool{false, true} {
			for _, expired := range []bool{false, true} {
				l := testLease(t)
				host := "foreign"
				if local {
					host = l.Machine
				}
				now := testNow
				if expired {
					now = l.Expires
				}
				check := func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return live }
				want := false
				if local {
					switch live {
					case ownerpkg.Dead:
						want = true
					case ownerpkg.Alive, ownerpkg.Unknown:
						want = expired
					}
				}
				got, _ := reclaimable(l, host, now, check)
				if got != want {
					t.Fatalf("local=%v expired=%v live=%s: reclaim=%v want=%v", local, expired, live, got, want)
				}
				for _, job := range []bool{false, true} {
					for _, ttl := range []time.Duration{-1, 0, time.Minute} {
						copy := l
						if job {
							copy.Reservation = &ReservationView{}
						}
						err := extendLease(&copy, host, ttl, now, check)
						allowed := local && live == ownerpkg.Alive && !expired && !job && ttl > 0
						if (err == nil) != allowed {
							t.Fatalf("extend local=%v expired=%v live=%s job=%v ttl=%v: %v", local, expired, live, job, ttl, err)
						}
						if !allowed && !copy.Expires.Equal(l.Expires) {
							t.Fatal("rejected extension changed deadline")
						}
					}
				}
			}
		}
	}
}

func TestFormalBudgetDomain(t *testing.T) {
	// Exhaust all two-reservation and positive-request sizes 1..2 against
	// capacities 1..3, independently summing rather than saturating/subtracting.
	requests := []budgetpkg.Amount{{Cores: 1, Memory: 1}, {Cores: 1, Memory: 2}, {Cores: 2, Memory: 1}, {Cores: 2, Memory: 2}}
	for _, a := range requests {
		for _, b := range requests {
			for _, request := range requests {
				for cores := 1; cores <= 3; cores++ {
					for memory := int64(1); memory <= 3; memory++ {
						s := &store{Leases: map[string]Lease{
							"a":       {Machine: "local", Reservation: &ReservationView{Budget: a}},
							"b":       {Machine: "local", Reservation: &ReservationView{Budget: b}},
							"foreign": {Machine: "elsewhere", Reservation: &ReservationView{Budget: budgetpkg.Amount{Cores: 99, Memory: 99}}},
						}}
						want := a.Cores+b.Cores+request.Cores <= cores && a.Memory+b.Memory+request.Memory <= memory
						if got := budgetFits(s, "local", budgetpkg.Amount{Cores: cores, Memory: memory}, request); got != want {
							t.Fatalf("a=%v b=%v request=%v capacity=%v: %v want %v", a, b, request, budgetpkg.Amount{Cores: cores, Memory: memory}, got, want)
						}
					}
				}
			}
		}
	}
}

func TestFormalConcurrentAdmission(t *testing.T) {
	dir := t.TempDir()
	owner, err := bindOwner("pid:"+strconv.Itoa(os.Getpid()), 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 3)
	var wg sync.WaitGroup
	for range 3 {
		l, err := newLease(owner, time.Minute, "", nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		l.ResourceRef.Adapter = "process"
		l.Reservation = &ReservationView{Budget: budgetpkg.Amount{Cores: 1, Memory: 1}, Class: "default"}
		wg.Go(func() {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := testManager().admitJob(ctx, dir, l, budgetpkg.Amount{Cores: 2, Memory: 2}, time.Second)
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	admitted := 0
	for err := range results {
		if err == nil {
			admitted++
		} else if !errors.Is(err, ErrNoCapacity) {
			t.Fatal(err)
		}
	}
	if admitted != 2 {
		t.Fatalf("capacity 2 admitted %d unit requests", admitted)
	}
	jobStore(t, dir, func(s *store) {
		if len(s.Leases) != 2 {
			t.Fatalf("lost reservation: %d", len(s.Leases))
		}
	})
}
