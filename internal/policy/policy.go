// Package policy decides a lease's lifetime rule from its resource kind and owner kind.
package policy

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	// Time leases end at expiry or owner death: the rule every lease follows without a policy.
	Time = "time"
	// Owner leases live exactly while the owner is alive; expiry never reclaims them.
	Owner = "owner"
	// Hybrid leases are owner-bound, and also end after an idle timeout or a maximum lifetime.
	Hybrid = "hybrid"

	Destroy = "destroy"
	// Report lists a due lease but never reclaims it automatically.
	Report = "report"
)

// Row is one configured policy as the user config spells it; durations use Go syntax.
type Row struct {
	Binding          string `json:"binding"`
	TTL              string `json:"ttl"`
	Renew            string `json:"renew"`
	MaxLifetime      string `json:"max_lifetime"`
	Grace            string `json:"grace"`
	Idle             string `json:"idle"`
	Action           string `json:"action"`
	ReclaimLiveOwner bool   `json:"reclaim_live_owner,omitempty"`
}

// Policy is the lifetime rule a lease records when it is acquired; later config
// edits never change a recorded policy.
type Policy struct {
	// Row is the table key that supplied the policy, such as "shell" or "kind/pr".
	Row              string        `json:"row"`
	Binding          string        `json:"binding"`
	TTL              time.Duration `json:"ttl"`
	Renew            time.Duration `json:"renew,omitempty"`
	MaxLifetime      time.Duration `json:"max_lifetime,omitempty"`
	Grace            time.Duration `json:"grace,omitempty"`
	Idle             time.Duration `json:"idle,omitempty"`
	Action           string        `json:"action"`
	ReclaimLiveOwner bool          `json:"reclaim_live_owner,omitempty"`
}

// OwnerBound reports whether expiry is ignored and owner death ends the lease.
func (p *Policy) OwnerBound() bool { return p != nil && (p.Binding == Owner || p.Binding == Hybrid) }

// Defaults are the built-in rows; a configured row with the same key replaces one.
func Defaults() map[string]Policy {
	lab := Policy{Binding: Time, TTL: 4 * time.Hour, Renew: 2 * time.Hour, MaxLifetime: 24 * time.Hour, Action: Destroy}
	rows := map[string]Policy{
		"none":    {Binding: Time, TTL: 8 * time.Hour, Action: Destroy},
		"kind":    lab,
		"k3d":     lab,
		"compose": lab,
		"shell":   {Binding: Hybrid, TTL: 8 * time.Hour, Idle: 30 * time.Minute, Action: Destroy},
	}
	for key, p := range rows {
		p.Row = key
		rows[key] = p
	}
	return rows
}

// Kinds names what a table may refer to: the resource adapters, the owner kinds,
// and which adapters can observe idleness.
type Kinds struct {
	Resources, Owners []string
	Idle              func(resource string) bool
}

func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

// Table merges configured rows over the defaults and validates every row.
func Table(rows map[string]Row, kinds Kinds) (map[string]Policy, error) {
	table := Defaults()
	keys := make([]string, 0, len(rows))
	for key := range rows {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		resource, owner, scoped := strings.Cut(key, "/")
		if resource == "process" {
			return nil, errors.New(`policy "process": job deadlines come from job classes, not lease policies`)
		}
		if !contains(kinds.Resources, resource) || (scoped && !contains(kinds.Owners, owner)) {
			return nil, fmt.Errorf("policy %q: key must be a resource kind (%s), optionally /<owner kind> (%s)", key, strings.Join(kinds.Resources, ", "), strings.Join(kinds.Owners, ", "))
		}
		p, err := parse(key, rows[key])
		if err != nil {
			return nil, err
		}
		if p.Idle > 0 && (kinds.Idle == nil || !kinds.Idle(resource)) {
			return nil, fmt.Errorf("policy %q: %s resources have no idle signal; remove idle", key, resource)
		}
		table[key] = p
	}
	return table, nil
}

func parse(key string, row Row) (Policy, error) {
	p := Policy{Row: key, Binding: row.Binding, TTL: 8 * time.Hour, Action: row.Action, ReclaimLiveOwner: row.ReclaimLiveOwner}
	if p.Action == "" {
		p.Action = Destroy
	}
	for _, field := range []struct {
		name, value string
		into        *time.Duration
	}{{"ttl", row.TTL, &p.TTL}, {"renew", row.Renew, &p.Renew}, {"max_lifetime", row.MaxLifetime, &p.MaxLifetime}, {"grace", row.Grace, &p.Grace}, {"idle", row.Idle, &p.Idle}} {
		if field.value == "" {
			continue
		}
		d, err := time.ParseDuration(field.value)
		if err != nil || d < 0 {
			return Policy{}, fmt.Errorf("policy %q: %s must be a non-negative Go duration such as 30m", key, field.name)
		}
		*field.into = d
	}
	if row.TTL == "" && p.MaxLifetime > 0 {
		p.TTL = min(p.TTL, p.MaxLifetime)
	}
	return p, p.check(key)
}

// Validate checks a recorded policy against the rules a configured row must meet.
func (p Policy) Validate() error { return p.check(p.Row) }

func (p Policy) check(key string) error {
	invalid := func(why string) error { return fmt.Errorf("policy %q: %s", key, why) }
	switch {
	case p.Binding != Time && p.Binding != Owner && p.Binding != Hybrid:
		return invalid("binding must be time, owner, or hybrid")
	case p.Action != Destroy && p.Action != Report:
		return invalid("action must be destroy or report")
	case p.TTL <= 0:
		return invalid("ttl must be positive")
	case p.Renew < 0 || p.MaxLifetime < 0 || p.Grace < 0 || p.Idle < 0:
		return invalid("renew, max_lifetime, grace, and idle must be non-negative")
	case p.Renew > p.TTL:
		return invalid("renew must not exceed ttl")
	case p.MaxLifetime > 0 && p.MaxLifetime < p.TTL:
		return invalid("max_lifetime must be at least ttl")
	case p.ReclaimLiveOwner && p.Binding != Hybrid:
		return invalid("reclaim_live_owner requires a hybrid binding")
	case p.Binding == Owner && (p.Idle > 0 || p.MaxLifetime > 0):
		return invalid("an owner binding has no idle or max_lifetime; use hybrid")
	case p.Binding == Time && p.Idle > 0:
		return invalid("idle needs a hybrid binding")
	case p.Binding == Hybrid && p.Idle == 0 && p.MaxLifetime == 0:
		return invalid("a hybrid binding needs idle or max_lifetime")
	}
	return nil
}

// Resolve picks the row for a resource kind narrowed by owner kind, then the resource row.
func Resolve(table map[string]Policy, resource, owner string) (Policy, bool) {
	if p, ok := table[resource+"/"+owner]; ok {
		return p, true
	}
	p, ok := table[resource]
	return p, ok
}

// Override applies per-acquire values, which may only tighten the row: a TTL
// within the maximum lifetime, and an idle timeout or maximum lifetime no longer
// than the row's.
func (p Policy) Override(ttl, idle, maxLifetime time.Duration) (Policy, error) {
	if maxLifetime > 0 {
		if p.Binding == Owner {
			return Policy{}, fmt.Errorf("--max-lifetime does not apply to the owner-bound %q policy", p.Row)
		}
		if p.MaxLifetime > 0 && maxLifetime > p.MaxLifetime {
			return Policy{}, fmt.Errorf("--max-lifetime %s exceeds the %q policy's %s", maxLifetime, p.Row, p.MaxLifetime)
		}
		p.MaxLifetime = maxLifetime
	}
	if idle > 0 {
		if p.Binding != Hybrid || p.Idle == 0 {
			return Policy{}, fmt.Errorf("--idle needs a policy with an idle timeout; %q has none", p.Row)
		}
		if idle > p.Idle {
			return Policy{}, fmt.Errorf("--idle %s exceeds the %q policy's %s", idle, p.Row, p.Idle)
		}
		p.Idle = idle
	}
	if ttl > 0 {
		p.TTL = ttl
	} else if p.MaxLifetime > 0 {
		p.TTL = min(p.TTL, p.MaxLifetime)
	}
	if p.MaxLifetime > 0 && p.TTL > p.MaxLifetime {
		return Policy{}, fmt.Errorf("--ttl %s exceeds the %q policy's max_lifetime %s", p.TTL, p.Row, p.MaxLifetime)
	}
	if p.Renew > p.TTL {
		p.Renew = p.TTL
	}
	return p, nil
}
