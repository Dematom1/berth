package cli

import (
	ownerpkg "github.com/Dematom1/berth/internal/owner"
)

func newOwners() ownerpkg.Registry {
	return ownerpkg.NewRegistry(ownerpkg.PID(), ownerpkg.Session(), ownerpkg.Heartbeat())
}
