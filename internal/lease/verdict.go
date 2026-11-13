package lease

import (
	"context"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	policypkg "github.com/Dematom1/dibs/internal/policy"
)

// Reclaim triggers, and the reasons a lease is held, are stable contract values.
const (
	TriggerOwnerDead   = "owner_dead"
	TriggerExpired     = "expired"
	TriggerMaxLifetime = "max_lifetime"
	TriggerIdle        = "idle"

	HoldWrongMachine    = "wrong_machine"
	HoldOwnerAlive      = "owner_alive"
	HoldOwnerUnknown    = "owner_unknown"
	HoldLiveOwner       = "live_owner"
	HoldGrace           = "grace"
	HoldResourceBusy    = "resource_busy"
	HoldResourceUnknown = "resource_unknown"
	HoldReportOnly      = "report_only"
)

// Verdict is the reclaim decision for one lease and the reason for it.
type Verdict struct {
	Liveness ownerpkg.Liveness
	// Reclaim reports that sweep may destroy the resource now.
	Reclaim bool
	// Trigger names the condition that ended the lease, possibly still in its grace period; empty while none has.
	Trigger string
	// Hold says why the lease is kept: no condition fired yet, or one fired but must not act.
	Hold string
	// Due is when the next condition, or a running grace period, ends; zero when none is scheduled.
	Due time.Time
	// Activity is the resource's observed use, when its adapter reports one.
	Activity    *adapterpkg.Activity
	ActivityErr error
}

// decide applies the lease's policy on top of reclaimable: maximum lifetime and
// idle timeout end it too, a grace period delays every condition, the report
// action never acts, and a busy or unobservable resource is never reclaimed.
func decide(ctx context.Context, l Lease, host string, now time.Time, check func(ownerpkg.Reference, time.Time) ownerpkg.Liveness, adapters adapterpkg.Registry) Verdict {
	eligible, live := reclaimable(l, host, now, check)
	v := Verdict{Liveness: live}
	if l.Machine != host {
		v.Hold = HoldWrongMachine
		return v
	}
	p := l.Policy
	shell := l.ResourceRef.Adapter == "shell" && l.ResourceRef.Process != nil
	var grace time.Duration
	if p != nil {
		grace = p.Grace
	}
	// fired maps each condition that holds now to when it began.
	fired := map[string]time.Time{}
	var next []time.Time
	if live == ownerpkg.Dead {
		since := now
		if l.DeadSince != nil {
			since = *l.DeadSince
		}
		fired[TriggerOwnerDead] = since
	} else if eligible {
		fired[TriggerExpired] = l.Expires
	} else if !l.ProtectLiveOwner && !shell && !p.OwnerBound() {
		next = append(next, l.Expires)
	}
	if p != nil && p.MaxLifetime > 0 && !l.ProtectLiveOwner {
		if end := l.Created.Add(p.MaxLifetime); now.Before(end) {
			next = append(next, end)
		} else {
			fired[TriggerMaxLifetime] = end
		}
	}
	idle := p != nil && p.Binding == policypkg.Hybrid && p.Idle > 0 && !l.ProtectLiveOwner
	if d, ok := adapters.Lookup(l.ResourceRef.Adapter); ok && d.Activity != nil && (idle || len(fired) > 0) {
		a, err := d.Activity(ctx, target(l))
		v.Activity, v.ActivityErr = &a, err
		if idle && err == nil && a.IdleKnown && a.Busy == "" {
			if end := now.Add(p.Idle - a.Idle); now.Before(end) {
				next = append(next, end)
			} else {
				fired[TriggerIdle] = end
			}
		}
	}
	if len(fired) == 0 {
		v.Hold, v.Due = HoldOwnerAlive, earliest(next, grace)
		if live != ownerpkg.Alive {
			v.Hold = HoldOwnerUnknown
		}
		return v
	}
	// Trigger names the first condition whose grace period has passed, or else the first that fired.
	var waiting []time.Time
	ready := false
	for _, trigger := range []string{TriggerOwnerDead, TriggerExpired, TriggerMaxLifetime, TriggerIdle} {
		began, ok := fired[trigger]
		switch {
		case !ok:
		case !now.Before(began.Add(grace)) && !ready:
			v.Trigger, ready = trigger, true
		case now.Before(began.Add(grace)):
			waiting = append(waiting, began)
			if v.Trigger == "" {
				v.Trigger = trigger
			}
		}
	}
	switch {
	case !ready:
		v.Hold, v.Due = HoldGrace, earliest(waiting, grace)
	case v.Activity != nil && v.Activity.Busy != "":
		v.Hold = HoldResourceBusy
	case live != ownerpkg.Dead && (shell || (p != nil && p.Binding == policypkg.Hybrid)) && (live != ownerpkg.Alive || p == nil || !p.ReclaimLiveOwner):
		v.Hold = HoldLiveOwner
	case v.ActivityErr != nil:
		v.Hold = HoldResourceUnknown
	case p != nil && p.Action == policypkg.Report:
		v.Hold = HoldReportOnly
	default:
		v.Reclaim = true
	}
	return v
}

func earliest(times []time.Time, grace time.Duration) time.Time {
	var first time.Time
	for _, t := range times {
		if first.IsZero() || t.Before(first) {
			first = t
		}
	}
	if first.IsZero() {
		return first
	}
	return first.Add(grace)
}

// observeDeath starts or clears a grace period's clock; it reports whether the lease changed.
func observeDeath(l *Lease, v Verdict, now time.Time) bool {
	if l.Policy == nil || l.Policy.Grace == 0 {
		return false
	}
	if v.Liveness == ownerpkg.Dead && l.DeadSince == nil {
		l.DeadSince = &now
		return true
	}
	if v.Liveness != ownerpkg.Dead && l.DeadSince != nil {
		l.DeadSince = nil
		return true
	}
	return false
}
