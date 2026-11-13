package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	composepkg "github.com/Dematom1/dibs/internal/adapter/compose"
	k3dpkg "github.com/Dematom1/dibs/internal/adapter/k3d"
	kindpkg "github.com/Dematom1/dibs/internal/adapter/kind"
	nonepkg "github.com/Dematom1/dibs/internal/adapter/none"
	shellpkg "github.com/Dematom1/dibs/internal/adapter/shell"
	configpkg "github.com/Dematom1/dibs/internal/config"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	policypkg "github.com/Dematom1/dibs/internal/policy"
)

// newAdapters registers every built-in, so a recorded lease always validates and
// can be released; opt-ins gate only how new leases enter.
func newAdapters() adapterpkg.Registry {
	shell := shellpkg.New(shellpkg.Options{List: shellpkg.PS, Grace: 5 * time.Second, Log: os.Stderr, Self: os.Getpid(), TTYTimes: shellpkg.TTYTimes})
	return adapterpkg.NewRegistry(nonepkg.New(), kindpkg.New(adapterpkg.RunCommand), k3dpkg.New(adapterpkg.RunCommand), composepkg.New(adapterpkg.RunCommand), shell)
}

// ownerKinds are every kind a policy row may name, whether or not its opt-in is on.
var ownerKinds = []string{"pid", "session", "heartbeat", "pr", "exec"}

// leasePolicy resolves a new lease's policy and TTL. With policies off it
// returns no policy and the caller's TTL, and refuses the policy-only flags.
func (app application) leasePolicy(resource, ownerKind string, ttl time.Duration, explicitTTL bool, idle, maxLifetime time.Duration) (*policypkg.Policy, time.Duration, error) {
	if !leasePoliciesEnabled() {
		if idle != 0 || maxLifetime != 0 {
			return nil, 0, errors.New("--idle and --max-lifetime need lease policies; " + leasePoliciesOff)
		}
		return nil, ttl, nil
	}
	if idle < 0 || maxLifetime < 0 {
		return nil, 0, errors.New("--idle and --max-lifetime must be positive")
	}
	rows, err := configpkg.Policies()
	if err != nil {
		return nil, 0, err
	}
	table, err := policypkg.Table(rows, policypkg.Kinds{Resources: app.resources.Names(), Owners: ownerKinds, Idle: func(name string) bool {
		d, ok := app.resources.Lookup(name)
		return ok && d.Activity != nil
	}})
	if err != nil {
		return nil, 0, err
	}
	p, ok := policypkg.Resolve(table, resource, ownerKind)
	if !ok {
		return nil, 0, fmt.Errorf("no lease policy for %s; add a %q row to policies in the user config", resource, resource)
	}
	if !explicitTTL {
		ttl = 0
	}
	if p, err = p.Override(ttl, idle, maxLifetime); err != nil {
		return nil, 0, err
	}
	return &p, p.TTL, nil
}

func kubeconfigFor(r adapterpkg.Registry, l leasepkg.Lease) string {
	d, ok := r.Lookup(l.ResourceRef.Adapter)
	if !ok {
		return ""
	}
	return d.Env(l.ResourceRef).Exports["KUBECONFIG"]
}
