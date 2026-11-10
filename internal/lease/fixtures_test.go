package lease

import (
	"context"
	"strings"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	composepkg "github.com/Dematom1/dibs/internal/adapter/compose"
	k3dpkg "github.com/Dematom1/dibs/internal/adapter/k3d"
	kindpkg "github.com/Dematom1/dibs/internal/adapter/kind"
	nonepkg "github.com/Dematom1/dibs/internal/adapter/none"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

const gib = int64(1 << 30)
const version = "restructure-test"

func testAdapters() adapterpkg.Registry {
	return adapterpkg.NewRegistry(nonepkg.New(), kindpkg.New(adapterpkg.RunCommand), k3dpkg.New(adapterpkg.RunCommand), composepkg.New(adapterpkg.RunCommand))
}
func testManager() *Manager {
	return New(Options{Adapters: testAdapters(), Owners: ownerpkg.NewRegistry(ownerpkg.PID(), ownerpkg.Session(), ownerpkg.Heartbeat())})
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func bindOwner(value string, stale time.Duration, now time.Time) (ownerpkg.Reference, error) {
	return testManager().options.Owners.Bind(context.Background(), value, stale, now)
}
func checkOwner(r ownerpkg.Reference, now time.Time) ownerpkg.Liveness {
	return testManager().checkOwner(r, now)
}
