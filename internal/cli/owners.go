package cli

import (
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

func newOwners() ownerpkg.Registry {
	return ownerpkg.NewRegistry(ownerpkg.PID(), ownerpkg.Session(), ownerpkg.Heartbeat())
}
