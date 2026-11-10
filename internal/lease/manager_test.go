package lease

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	budgetpkg "github.com/Dematom1/dibs/internal/budget"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

func TestManagerListingAccountsForHiddenLocalReservations(t *testing.T) {
	dir := t.TempDir()
	m := testManager()
	m.options.Directory = dir
	s, err := m.openStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	host, err := machine()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i, fixture := range []struct {
		machine, owner string
		cores          int
		memory         int64
	}{{host, "visible", 2, 1 << 30}, {host, "hidden", 7, 2 << 30}, {"foreign-" + host, "visible", 99, 99 << 30}} {
		l := testLease(t)
		l.ID = []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccccccccccc"}[i]
		l.Machine = fixture.machine
		l.Owner = ownerpkg.Reference{Kind: "heartbeat", ID: filepath.Join(dir, fixture.owner), Stale: time.Minute}
		l.Created = now
		l.Expires = now.Add(time.Hour)
		l.ResourceRef = adapterpkg.ResourceRef{Adapter: "process"}
		l.Reservation = &ReservationView{Budget: budgetpkg.Amount{Cores: fixture.cores, Memory: fixture.memory}, Class: "suite"}
		s.Leases[l.ID] = l
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	listing, err := m.List(context.Background(), Filter{Owner: "heartbeat:" + filepath.Join(dir, "visible")})
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Leases) != 2 || listing.Reserved != (budgetpkg.Amount{Cores: 9, Memory: 3 << 30}) {
		t.Fatalf("filter changed accounting: %+v", listing)
	}
	l := listing.Leases[0].Lease
	l.Labels["task"] = "changed"
	l.Reservation.Class = "changed"
	reread, exists, err := m.Get(context.Background(), l.ID)
	if err != nil || !exists {
		t.Fatalf("get: %v %v", exists, err)
	}
	if reread.Labels["task"] != "lab" || reread.Reservation.Class != "suite" {
		t.Fatal("returned snapshots altered durable state")
	}
}

func TestManagerConstructionDoesNotOpenState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	m := New(Options{Directory: dir})
	if m == nil {
		t.Fatal("nil manager")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("New touched state: %v", err)
	}
}
