package cli

import (
	adapterpkg "github.com/Dematom1/berth/internal/adapter"
	composepkg "github.com/Dematom1/berth/internal/adapter/compose"
	k3dpkg "github.com/Dematom1/berth/internal/adapter/k3d"
	kindpkg "github.com/Dematom1/berth/internal/adapter/kind"
	nonepkg "github.com/Dematom1/berth/internal/adapter/none"
	leasepkg "github.com/Dematom1/berth/internal/lease"
)

func newAdapters() adapterpkg.Registry {
	return adapterpkg.NewRegistry(nonepkg.New(), kindpkg.New(adapterpkg.RunCommand), k3dpkg.New(adapterpkg.RunCommand), composepkg.New(adapterpkg.RunCommand))
}

func kubeconfigFor(r adapterpkg.Registry, l leasepkg.Lease) string {
	d, ok := r.Lookup(l.ResourceRef.Adapter)
	if !ok {
		return ""
	}
	return d.Env(l.ResourceRef).Exports["KUBECONFIG"]
}
