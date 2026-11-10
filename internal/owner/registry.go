package owner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Observation struct {
	Liveness Liveness
	Identity string
}
type Definition struct {
	Name  string
	Bind  func(context.Context, string, time.Duration, time.Time) (Reference, error)
	Check func(context.Context, Reference, time.Time) Observation
}
type Registry struct{ definitions []Definition }

func NewRegistry(definitions ...Definition) Registry {
	seen := map[string]bool{}
	for _, d := range definitions {
		if d.Name == "" || seen[d.Name] || d.Bind == nil || d.Check == nil {
			panic("incomplete or duplicate owner checker")
		}
		seen[d.Name] = true
	}
	return Registry{append([]Definition(nil), definitions...)}
}
func (r Registry) Names() []string {
	var names []string
	for _, d := range r.definitions {
		names = append(names, d.Name)
	}
	return names
}
func (r Registry) Lookup(name string) (Definition, bool) {
	for _, d := range r.definitions {
		if d.Name == name {
			return d, true
		}
	}
	return Definition{}, false
}
func (r Registry) Bind(ctx context.Context, value string, stale time.Duration, now time.Time) (Reference, error) {
	kind, id, ok := strings.Cut(value, ":")
	if !ok || id == "" {
		return Reference{}, errors.New("--owner must be pid:<pid>, session:<holder-pid>, or heartbeat:<file>")
	}
	d, ok := r.Lookup(kind)
	if !ok {
		return Reference{}, fmt.Errorf("unknown owner kind %q; use pid, session, or heartbeat", kind)
	}
	return d.Bind(ctx, value, stale, now)
}
func (r Registry) Check(ctx context.Context, reference Reference, now time.Time) Observation {
	d, ok := r.Lookup(reference.Kind)
	if !ok {
		return Observation{Liveness: Unknown}
	}
	observation := d.Check(ctx, reference, now)
	if (reference.Kind == "pid" || reference.Kind == "session") && observation.Liveness == Alive && observation.Identity != reference.Identity {
		observation.Liveness = Dead
	}
	return observation
}
func native(name string) Definition {
	return Definition{Name: name,
		Bind: func(_ context.Context, value string, stale time.Duration, now time.Time) (Reference, error) {
			return bind(value, stale, now)
		},
		Check: func(_ context.Context, r Reference, now time.Time) Observation {
			if name == "pid" || name == "session" {
				pid, err := strconv.ParseInt(r.ID, 10, 32)
				if err != nil || pid <= 0 || r.Identity == "" {
					return Observation{Liveness: Unknown}
				}
				identity, live := ProcessIdentity(int(pid))
				return Observation{Liveness: live, Identity: identity}
			}
			return Observation{Liveness: check(r, now)}
		},
	}
}
func PID() Definition       { return native("pid") }
func Session() Definition   { return native("session") }
func Heartbeat() Definition { return native("heartbeat") }
func CanonicalSelector(value string) string {
	kind, id, ok := strings.Cut(value, ":")
	if ok && id != "" {
		switch kind {
		case "pid", "session":
			if pid, err := strconv.ParseInt(id, 10, 32); err == nil && pid > 0 {
				return kind + ":" + strconv.FormatInt(pid, 10)
			}
		case "heartbeat":
			if path, err := filepath.Abs(id); err == nil {
				return kind + ":" + path
			}
		}
	}
	return value
}
