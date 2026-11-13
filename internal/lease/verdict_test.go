package lease

import (
	"context"
	"errors"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	nonepkg "github.com/Dematom1/dibs/internal/adapter/none"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	policypkg "github.com/Dematom1/dibs/internal/policy"
)

// observed is a "none" resource that reports the given activity.
func observed(a adapterpkg.Activity, err error) adapterpkg.Registry {
	d := nonepkg.New()
	d.Activity = func(context.Context, adapterpkg.Target) (adapterpkg.Activity, error) { return a, err }
	return adapterpkg.NewRegistry(d)
}

func TestDecideAppliesPolicy(t *testing.T) {
	hour := time.Hour
	timed := &policypkg.Policy{Row: "none", Binding: policypkg.Time, TTL: hour, MaxLifetime: 3 * hour, Action: policypkg.Destroy}
	owner := &policypkg.Policy{Row: "none", Binding: policypkg.Owner, TTL: hour, Action: policypkg.Destroy}
	hybrid := &policypkg.Policy{Row: "none", Binding: policypkg.Hybrid, TTL: hour, Idle: 30 * time.Minute, MaxLifetime: 10 * hour, Action: policypkg.Destroy}
	hybridReclaim := *hybrid
	hybridReclaim.ReclaimLiveOwner = true
	withGrace := func(p *policypkg.Policy, g time.Duration) *policypkg.Policy { c := *p; c.Grace = g; return &c }
	report := func(p *policypkg.Policy) *policypkg.Policy { c := *p; c.Action = policypkg.Report; return &c }
	idle := func(d time.Duration) adapterpkg.Activity { return adapterpkg.Activity{Idle: d, IdleKnown: true} }
	busy := adapterpkg.Activity{Busy: "shell 1 has a foreground job"}
	for _, tc := range []struct {
		name     string
		policy   *policypkg.Policy
		live     ownerpkg.Liveness
		elapsed  time.Duration
		activity adapterpkg.Activity
		err      error
		trigger  string
		hold     string
		due      time.Duration // from creation; 0 when none
	}{
		{"time: live, unexpired", timed, ownerpkg.Alive, 0, idle(0), nil, "", HoldOwnerAlive, hour},
		{"time: expired with live owner", timed, ownerpkg.Alive, hour, idle(0), nil, TriggerExpired, "", 0},
		{"time: unknown owner unexpired", timed, ownerpkg.Unknown, 0, idle(0), nil, "", HoldOwnerUnknown, hour},
		{"owner: expiry ignored", owner, ownerpkg.Alive, 100 * hour, idle(0), nil, "", HoldOwnerAlive, 0},
		{"owner: unknown never reclaims", owner, ownerpkg.Unknown, 100 * hour, idle(0), nil, "", HoldOwnerUnknown, 0},
		{"owner: death reclaims", owner, ownerpkg.Dead, 0, idle(0), nil, TriggerOwnerDead, "", 0},
		{"hybrid: active shell held", hybrid, ownerpkg.Alive, 2 * hour, idle(10 * time.Minute), nil, "", HoldOwnerAlive, 2*hour + 20*time.Minute},
		{"hybrid: idle past timeout holds live owner", hybrid, ownerpkg.Alive, 2 * hour, idle(31 * time.Minute), nil, TriggerIdle, HoldLiveOwner, 0},
		{"hybrid: idle allowed for live owner", &hybridReclaim, ownerpkg.Alive, 2 * hour, idle(31 * time.Minute), nil, TriggerIdle, "", 0},
		{"hybrid: idle never allowed for unknown owner", &hybridReclaim, ownerpkg.Unknown, 2 * hour, idle(31 * time.Minute), nil, TriggerIdle, HoldLiveOwner, 0},
		{"hybrid: idle unknown waits for max", hybrid, ownerpkg.Alive, 2 * hour, adapterpkg.Activity{}, nil, "", HoldOwnerAlive, 10 * hour},
		{"hybrid: max lifetime holds live owner", hybrid, ownerpkg.Alive, 10 * hour, idle(0), nil, TriggerMaxLifetime, HoldLiveOwner, 0},
		{"hybrid: max lifetime allowed for live owner", &hybridReclaim, ownerpkg.Alive, 10 * hour, idle(0), nil, TriggerMaxLifetime, "", 0},
		{"hybrid: max lifetime never allowed for unknown owner", &hybridReclaim, ownerpkg.Unknown, 10 * hour, idle(0), nil, TriggerMaxLifetime, HoldLiveOwner, 0},
		{"hybrid: owner hold takes precedence over unreadable activity", hybrid, ownerpkg.Alive, 10 * hour, adapterpkg.Activity{}, errors.New("ps failed"), TriggerMaxLifetime, HoldLiveOwner, 0},
		{"busy holds a fired trigger", owner, ownerpkg.Dead, 0, busy, nil, TriggerOwnerDead, HoldResourceBusy, 0},
		{"busy is never idle", hybrid, ownerpkg.Alive, 2 * hour, busy, nil, "", HoldOwnerAlive, 10 * hour},
		{"unreadable activity holds", owner, ownerpkg.Dead, 0, adapterpkg.Activity{}, errors.New("ps failed"), TriggerOwnerDead, HoldResourceUnknown, 0},
		{"grace delays expiry", withGrace(timed, 30*time.Minute), ownerpkg.Alive, hour + 10*time.Minute, idle(0), nil, TriggerExpired, HoldGrace, hour + 30*time.Minute},
		{"grace passed", withGrace(timed, 30*time.Minute), ownerpkg.Alive, 2 * hour, idle(0), nil, TriggerExpired, "", 0},
		{"grace starts at first death", withGrace(owner, time.Minute), ownerpkg.Dead, 0, idle(0), nil, TriggerOwnerDead, HoldGrace, time.Minute},
		{"report never acts", report(owner), ownerpkg.Dead, 0, idle(0), nil, TriggerOwnerDead, HoldReportOnly, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := testLease(t)
			l.Policy = tc.policy
			now := l.Created.Add(tc.elapsed)
			v := decide(context.Background(), l, l.Machine, now, func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return tc.live }, observed(tc.activity, tc.err))
			if v.Trigger != tc.trigger || v.Hold != tc.hold || v.Reclaim != (tc.hold == "") {
				t.Fatalf("verdict = %+v, want trigger %q hold %q", v, tc.trigger, tc.hold)
			}
			if want := l.Created.Add(tc.due); (tc.due == 0) != v.Due.IsZero() || (tc.due != 0 && !v.Due.Equal(want)) {
				t.Fatalf("due = %v, want %v", v.Due, tc.due)
			}
		})
	}
	l := testLease(t)
	l.Policy = owner
	if v := decide(context.Background(), l, "elsewhere", testNow, func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return ownerpkg.Dead }, observed(idle(0), nil)); v.Reclaim || v.Hold != HoldWrongMachine {
		t.Fatalf("foreign verdict = %+v", v)
	}
	shell := testLease(t)
	shell.ResourceRef.Adapter = "shell"
	shell.ResourceRef.Process = &adapterpkg.ProcessRef{PID: 123, Start: "test"}
	for _, tc := range []struct {
		live ownerpkg.Liveness
		want bool
	}{{ownerpkg.Alive, false}, {ownerpkg.Unknown, false}, {ownerpkg.Dead, true}} {
		shell.Expires = shell.Created.Add(-time.Hour)
		check := func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return tc.live }
		eligible, _ := reclaimable(shell, shell.Machine, shell.Created, check)
		if eligible != tc.want {
			t.Fatalf("legacy shell eligible for TTL reclaim with owner %s: %v", tc.live, eligible)
		}
		v := decide(context.Background(), shell, shell.Machine, shell.Created, check, observed(idle(0), nil))
		if v.Reclaim != tc.want {
			t.Fatalf("legacy shell live=%s: %+v", tc.live, v)
		}
	}
	// Protection outranks any policy limit: only proven death reclaims.
	l.Policy, l.ProtectLiveOwner = hybrid, true
	if v := decide(context.Background(), l, l.Machine, l.Created.Add(100*hour), func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return ownerpkg.Alive }, observed(idle(100*hour), nil)); v.Reclaim {
		t.Fatalf("protected verdict = %+v", v)
	}
}

func TestExtendHonorsPolicy(t *testing.T) {
	alive := func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return ownerpkg.Alive }
	present := func(Lease) (bool, error) { return true, nil }
	l := testLease(t)
	l.Policy = &policypkg.Policy{Binding: policypkg.Time, TTL: time.Hour, MaxLifetime: 3 * time.Hour, Action: policypkg.Destroy}
	if _, err := extendLease(&l, l.Machine, 4*time.Hour, l.Created.Add(50*time.Minute), alive, present); err != nil || !l.Expires.Equal(l.Created.Add(3*time.Hour)) {
		t.Fatalf("extend past max lifetime: expires %v, %v", l.Expires, err)
	}
	// A hybrid lease outlives its expiry, so only the cap refuses a renewal.
	h := testLease(t)
	h.Policy = &policypkg.Policy{Binding: policypkg.Hybrid, TTL: time.Hour, MaxLifetime: 3 * time.Hour, Action: policypkg.Destroy}
	if _, err := extendLease(&h, h.Machine, time.Hour, h.Created.Add(3*time.Hour), alive, present); reason(err) != MaxLifetime || !h.Expires.Equal(h.Created.Add(time.Hour)) {
		t.Fatalf("extend at max lifetime = %v", err)
	}
	// An owner-bound lease renews after expiry like a protected one.
	owned := testLease(t)
	owned.Policy = &policypkg.Policy{Binding: policypkg.Owner, TTL: time.Hour, Action: policypkg.Destroy}
	if status, err := extendLease(&owned, owned.Machine, time.Hour, owned.Expires.Add(time.Hour), alive, present); err != nil || status != Reattached {
		t.Fatalf("owner-bound extend after expiry = %q, %v", status, err)
	}
}

func TestSweepHoldsAndGraceClock(t *testing.T) {
	ctx := context.Background()
	m := testManager()
	m.options.Directory = t.TempDir()
	s, err := m.openStore(ctx, m.options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l := testLease(t)
	l.Policy = &policypkg.Policy{Row: "none", Binding: policypkg.Owner, TTL: time.Hour, Grace: time.Minute, Action: policypkg.Destroy}
	s.Leases[l.ID] = l
	if err := s.Save(); err != nil || s.Version != 5 {
		t.Fatalf("policy lease saved as version %d: %v", s.Version, err)
	}
	dead := func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return ownerpkg.Dead }
	if r := sweep(ctx, s, l.Machine, testNow, false, dead, testAdapters()); r.Held[l.ID] != HoldGrace || s.Leases[l.ID].DeadSince != nil {
		t.Fatalf("preview started the grace clock or acted: %+v %+v", r, s.Leases[l.ID])
	}
	if r := sweep(ctx, s, l.Machine, testNow, true, dead, testAdapters()); r.Held[l.ID] != HoldGrace || s.Leases[l.ID].DeadSince == nil {
		t.Fatalf("first confirmed sweep: %+v", r)
	}
	if r := sweep(ctx, s, l.Machine, testNow.Add(30*time.Second), true, dead, testAdapters()); len(r.Reclaimed) != 0 {
		t.Fatalf("reclaimed inside grace: %+v", r)
	}
	if r := sweep(ctx, s, l.Machine, testNow.Add(time.Minute), true, dead, testAdapters()); len(r.Reclaimed) != 1 {
		t.Fatalf("not reclaimed after grace: %+v", r)
	}
	if err := s.Save(); err != nil || s.Version != 2 {
		t.Fatalf("empty state saved as version %d: %v", s.Version, err)
	}
	// A recovered owner clears the clock, so a later death gets a full grace period.
	again := testLease(t)
	again.Policy = l.Policy
	stamp := testNow
	again.DeadSince = &stamp
	s.Leases[again.ID] = again
	sweep(ctx, s, again.Machine, testNow.Add(time.Hour), true, func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return ownerpkg.Alive }, testAdapters())
	if s.Leases[again.ID].DeadSince != nil {
		t.Fatal("live owner kept a stale death stamp")
	}
}

func TestPolicyStateNeedsVersionFive(t *testing.T) {
	m := testManager()
	m.options.Directory = t.TempDir()
	s, err := m.openStore(context.Background(), m.options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l := testLease(t)
	l.Policy = &policypkg.Policy{Binding: policypkg.Time, TTL: time.Hour, Action: policypkg.Destroy}
	s.Leases[l.ID] = l
	s.Version = 4
	if s.validate() == nil {
		t.Fatal("version 4 accepted a policy")
	}
	s.Version = 5
	stamp := testNow
	l.DeadSince = &stamp
	s.Leases[l.ID] = l
	if s.validate() == nil {
		t.Fatal("death stamp without a grace period accepted")
	}
}

// A confirmed sweep never touches another machine's death observation, and a
// grace refusal from reclaim writes nothing, as every refusal must.
func TestGraceClockWritesOnlyLocalSweeps(t *testing.T) {
	ctx := context.Background()
	m := testManager()
	m.options.Owners = ownerpkg.NewRegistry(ownerpkg.PID(), ownerpkg.Session(), ownerpkg.Heartbeat())
	m.options.Directory = t.TempDir()
	s, err := m.openStore(ctx, m.options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	grace := &policypkg.Policy{Row: "none", Binding: policypkg.Owner, TTL: time.Hour, Grace: time.Hour, Action: policypkg.Destroy}
	foreign := testLease(t)
	foreign.Machine, foreign.Policy = "linux:other", grace
	stamp := testNow
	foreign.DeadSince = &stamp
	s.Leases[foreign.ID] = foreign
	alive := func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return ownerpkg.Alive }
	host := testLease(t).Machine
	sweep(ctx, s, host, testNow.Add(time.Minute), true, alive, testAdapters())
	if got := s.Leases[foreign.ID].DeadSince; got == nil || !got.Equal(stamp) {
		t.Fatalf("sweep changed a foreign lease's death stamp: %v", got)
	}
	delete(s.Leases, foreign.ID)

	// The owner is a PID that cannot exist, so reclaim sees it dead.
	local := testLease(t)
	local.Owner, local.Policy = ownerpkg.Reference{Kind: "pid", ID: "2147483640", Identity: "gone"}, grace
	s.Leases[local.ID] = local
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for _, confirm := range []bool{false, true} {
		if _, err := m.Reclaim(ctx, local.ID, confirm); reason(err) != HoldGrace {
			t.Fatalf("reclaim confirm=%v inside grace = %v", confirm, err)
		}
	}
	if l, _, err := m.Get(ctx, local.ID); err != nil || l.DeadSince != nil {
		t.Fatalf("grace refusal wrote a death stamp: %+v %v", l.DeadSince, err)
	}
}

func TestStateRejectsInvalidRecordedPolicy(t *testing.T) {
	m := testManager()
	m.options.Directory = t.TempDir()
	s, err := m.openStore(context.Background(), m.options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Version = 5
	for name, p := range map[string]policypkg.Policy{
		"unknown action":        {Row: "none", Binding: policypkg.Time, TTL: time.Hour, Action: "reportt"},
		"unknown binding":       {Row: "none", Binding: "forever", TTL: time.Hour, Action: policypkg.Destroy},
		"idle on a time lease":  {Row: "none", Binding: policypkg.Time, TTL: time.Hour, Idle: time.Minute, Action: policypkg.Destroy},
		"negative renew":        {Row: "none", Binding: policypkg.Time, TTL: time.Hour, Renew: -time.Minute, Action: policypkg.Destroy},
		"negative max lifetime": {Row: "none", Binding: policypkg.Time, TTL: time.Hour, MaxLifetime: -time.Minute, Action: policypkg.Destroy},
		"negative grace":        {Row: "none", Binding: policypkg.Time, TTL: time.Hour, Grace: -time.Minute, Action: policypkg.Destroy},
		"negative idle":         {Row: "none", Binding: policypkg.Hybrid, TTL: time.Hour, Idle: -time.Minute, MaxLifetime: time.Hour, Action: policypkg.Destroy},
	} {
		l := testLease(t)
		l.Policy = &p
		s.Leases = map[string]Lease{l.ID: l}
		if err := s.validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
