package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	outputpkg "github.com/Dematom1/dibs/internal/output"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

const version = "restructure-test"

func testApplication() application {
	resources, owners := newAdapters(), newOwners()
	return application{version, resources, owners, leasepkg.New(leasepkg.Options{Adapters: resources, Owners: owners})}
}
func run(ctx context.Context, args []string, out io.Writer) int { return Run(ctx, args, out, version) }
func runWithOutput(ctx context.Context, args []string, out io.Writer, mode outputpkg.Mode) int {
	return testApplication().runWithOutput(ctx, args, out, mode)
}
func runAgent(ctx context.Context, args []string, in io.Reader, out io.Writer) int {
	return testApplication().runAgent(ctx, args, in, out)
}

type fixtureStore struct {
	Version int                       `json:"version"`
	Leases  map[string]leasepkg.Lease `json:"leases"`
	dir     string
}

func openStore(_ context.Context, dir string) (*fixtureStore, error) {
	s := &fixtureStore{Version: 1, Leases: map[string]leasepkg.Lease{}, dir: dir}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err == nil {
		err = json.Unmarshal(data, s)
	}
	return s, err
}
func (s *fixtureStore) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "state.json"), append(data, '\n'), 0600)
}
func (s *fixtureStore) Close() error { return nil }
func (s *fixtureStore) IDs() []string {
	var ids []string
	for id := range s.Leases {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func machine() (string, error) { return os.Hostname() }
func newLease(owner ownerpkg.Reference, ttl time.Duration, resources string, labels map[string]string, now time.Time) (leasepkg.Lease, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return leasepkg.Lease{}, err
	}
	host, err := os.Hostname()
	return leasepkg.Lease{ID: fmt.Sprintf("%x", token), Machine: host, Owner: owner, Created: now, Expires: now.Add(ttl), TTL: ttl, Description: resources, Labels: labels, ResourceRef: adapterpkg.ResourceRef{Adapter: "none"}}, err
}

func kubeconfig(l leasepkg.Lease) string { return kubeconfigFor(newAdapters(), l) }
