package lease

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	kindpkg "github.com/Dematom1/dibs/internal/adapter/kind"
	nonepkg "github.com/Dematom1/dibs/internal/adapter/none"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

// fakeEngine models kind clusters in one Docker context, each with its node labels.
type fakeEngine struct {
	clusters map[string]map[string]string
	fail     error
	deleted  []string
}

func (e *fakeEngine) run(_ context.Context, _ []string, tool string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	if e.fail != nil && tool == "docker" && strings.Contains(joined, "container") {
		return nil, e.fail
	}
	names := make([]string, 0, len(e.clusters))
	for name := range e.clusters {
		names = append(names, name)
	}
	sort.Strings(names)
	switch {
	case joined == "context show":
		return []byte("test-context"), nil
	case strings.Contains(joined, "context inspect"):
		return []byte("unix:///test.sock"), nil
	case strings.Contains(joined, "container ls"):
		return []byte(strings.Join(names, "\n")), nil
	case strings.Contains(joined, "container inspect"):
		objects := []any{}
		for _, name := range names {
			labels := map[string]string{"io.x-k8s.kind.cluster": name}
			for k, v := range e.clusters[name] {
				labels[k] = v
			}
			objects = append(objects, map[string]any{"Name": name + "-control-plane", "Created": "2026-01-01T00:00:00Z", "Config": map[string]any{"Labels": labels}})
		}
		return json.Marshal(objects)
	case tool == "kind" && strings.HasPrefix(joined, "create cluster"):
		engineName := args[len(args)-1]
		e.clusters[engineName] = nil
		return nil, nil
	case tool == "kind" && strings.HasPrefix(joined, "get kubeconfig"):
		if _, ok := e.clusters[args[len(args)-1]]; !ok {
			return nil, errors.New("no cluster")
		}
		return []byte("apiVersion: v1\nkind: Config\n"), nil
	case tool == "kind" && strings.HasPrefix(joined, "delete cluster"):
		delete(e.clusters, args[3])
		e.deleted = append(e.deleted, args[3])
		return nil, nil
	case tool == "kind" && joined == "get clusters":
		return []byte(strings.Join(names, "\n")), nil
	}
	return nil, errors.New("unexpected command: " + tool + " " + joined)
}

type fakeOwner struct{ live ownerpkg.Liveness }

func pendingManager(t *testing.T) (*Manager, *fakeEngine, *fakeOwner) {
	t.Helper()
	t.Setenv("DOCKER_HOST", "")
	engine := &fakeEngine{clusters: map[string]map[string]string{}}
	owner := &fakeOwner{ownerpkg.Alive}
	fake := ownerpkg.Definition{Name: "fake",
		Bind: func(context.Context, string, time.Duration, time.Time) (ownerpkg.Reference, error) {
			return ownerpkg.Reference{Kind: "fake", ID: "1"}, nil
		},
		Check: func(context.Context, ownerpkg.Reference, time.Time) ownerpkg.Observation {
			return ownerpkg.Observation{Liveness: owner.live}
		}}
	m := New(Options{Directory: t.TempDir(), Adapters: adapterpkg.NewRegistry(nonepkg.New(), kindpkg.New(engine.run)), Owners: ownerpkg.NewRegistry(fake)})
	return m, engine, owner
}

var fakeRef = ownerpkg.Reference{Kind: "fake", ID: "1"}
var testScope = adapterpkg.ResourceRef{Context: "test-context", DockerHost: "unix:///test.sock"}

func pendingRequest(name, key string) CreateRequest {
	return CreateRequest{Owner: fakeRef, TTL: time.Hour, Adapter: "kind", Pending: true, Name: name, Key: key, Scope: testScope}
}

func reason(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Reason
	}
	return ""
}

func diskVersion(t *testing.T, m *Manager) int {
	t.Helper()
	s, err := m.readInventoryStore(m.options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	return s.Version
}

func TestPendingPinnedHandleLifecycle(t *testing.T) {
	ctx := context.Background()
	m, engine, _ := pendingManager(t)
	engine.clusters["taken"] = nil
	if _, _, err := m.Create(ctx, pendingRequest("taken", "")); reason(err) != "handle_exists" {
		t.Fatalf("existing handle accepted: %v", err)
	}
	l, done, err := m.Create(ctx, pendingRequest("lab-7", ""))
	if err != nil || done || !l.ResourceRef.Pending || l.ResourceRef.Name != "lab-7" || l.ResourceRef.Context != "test-context" {
		t.Fatalf("pending not recorded: %+v %v %v", l, done, err)
	}
	if v := diskVersion(t, m); v != 3 {
		t.Fatalf("pending lease saved as version %d", v)
	}
	if _, _, err := m.Create(ctx, pendingRequest("lab-7", "")); reason(err) != "already_leased" {
		t.Fatalf("second pending lease pinned the same handle: %v", err)
	}
	bindReq := AdoptRequest{Resource: adapterpkg.ResourceRef{Adapter: "kind", Name: "lab-7"}, Pending: l.ID}
	if _, _, err := m.Adopt(ctx, bindReq); reason(err) != "resource_absent" {
		t.Fatalf("bound before creation: %v", err)
	}
	engine.clusters["lab-7"] = nil
	plain := AdoptRequest{Owner: fakeRef, TTL: time.Hour, Resource: adapterpkg.ResourceRef{Adapter: "kind", Name: "lab-7", Context: "test-context", DockerHost: "unix:///test.sock"}}
	if _, _, err := m.Adopt(ctx, plain); reason(err) != "already_leased" {
		t.Fatalf("plain adopt took a pending lease's handle: %v", err)
	}
	if rows, err := m.Orphans(ctx, testScope); err != nil || len(rows) != 1 || rows[0]["name"] != "taken" {
		t.Fatalf("pending handle listed as an orphan: %v %v", rows, err)
	}
	wrong := bindReq
	wrong.Resource.Name = "taken"
	if _, _, err := m.Adopt(ctx, wrong); reason(err) != "handle_mismatch" {
		t.Fatalf("other handle bound: %v", err)
	}
	missing := bindReq
	missing.Pending = strings.Repeat("0", 32)
	if _, _, err := m.Adopt(ctx, missing); reason(err) != "not_found" {
		t.Fatalf("missing lease: %v", err)
	}
	bound, done, err := m.Adopt(ctx, bindReq)
	if err != nil || done || bound.ID != l.ID || bound.ResourceRef.Pending || !bound.ResourceRef.Adopted || bound.Labels[LeaseLabel] != l.ID {
		t.Fatalf("bind: %+v %v %v", bound, done, err)
	}
	if _, err := os.Stat(filepath.Join(m.options.Directory, "resources", l.ID, "kubeconfig")); err != nil {
		t.Fatalf("bind did not capture cleanup config: %v", err)
	}
	if again, done, err := m.Adopt(ctx, bindReq); err != nil || !done || again.ID != l.ID {
		t.Fatalf("retried bind not idempotent: %v %v", done, err)
	}
	if _, _, err := m.Adopt(ctx, wrong); reason(err) != "not_pending" {
		t.Fatalf("bound lease rebound: %v", err)
	}
	if v := diskVersion(t, m); v != 2 {
		t.Fatalf("version 3 kept without version-3 records: %d", v)
	}
	if _, err := m.Release(ctx, ReleaseRequest{ID: l.ID, Confirm: true}); err != nil || len(engine.clusters) != 1 {
		t.Fatalf("release: %v %v", err, engine.clusters)
	}
}

func TestPendingWrongMachine(t *testing.T) {
	ctx := context.Background()
	m, _, _ := pendingManager(t)
	l, _, err := m.Create(ctx, pendingRequest("lab", ""))
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.openStore(ctx, m.options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	foreign := s.Leases[l.ID]
	foreign.Machine = "linux:elsewhere"
	s.Leases[l.ID] = foreign
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, _, err := m.Adopt(ctx, AdoptRequest{Resource: adapterpkg.ResourceRef{Adapter: "kind", Name: "lab"}, Pending: l.ID}); reason(err) != "wrong_machine" {
		t.Fatalf("bind: %v", err)
	}
	if _, err := m.Reconcile(ctx, l.ID, true); reason(err) != "wrong_machine" {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := m.Reclaim(ctx, l.ID, true); reason(err) != "wrong_machine" {
		t.Fatalf("reclaim: %v", err)
	}
	if rows, err := m.Reconcile(ctx, "", true); err != nil || len(rows) != 0 {
		t.Fatalf("reconcile touched a foreign lease: %v %v", rows, err)
	}
}

func TestReconcileByLabel(t *testing.T) {
	ctx := context.Background()
	m, engine, _ := pendingManager(t)
	l, _, err := m.Create(ctx, pendingRequest("", ""))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := m.Reconcile(ctx, "", false)
	if err != nil || len(rows) != 1 || rows[0].Status != "absent" {
		t.Fatalf("absent: %+v %v", rows, err)
	}
	engine.clusters["unrelated"] = nil
	engine.clusters["tool-picked"] = map[string]string{LeaseLabel: l.ID}
	if _, _, err := m.Adopt(ctx, AdoptRequest{Owner: fakeRef, TTL: time.Hour, Resource: adapterpkg.ResourceRef{Adapter: "kind", Name: "tool-picked", Context: "test-context", DockerHost: "unix:///test.sock"}}); reason(err) != "already_leased" {
		t.Fatalf("plain adopt took a stamped resource: %v", err)
	}
	if rows, err := m.Orphans(ctx, testScope); err != nil || len(rows) != 1 || rows[0]["name"] != "unrelated" {
		t.Fatalf("stamped resource listed as orphan: %v %v", rows, err)
	}
	rows, err = m.Reconcile(ctx, l.ID, false)
	if err != nil || rows[0].Status != "bindable" || rows[0].Matches[0] != "tool-picked" {
		t.Fatalf("preview: %+v %v", rows, err)
	}
	if got, _, _ := m.Get(ctx, l.ID); !got.ResourceRef.Pending {
		t.Fatal("preview bound the resource")
	}
	rows, err = m.Reconcile(ctx, "", true)
	if err != nil || rows[0].Status != "bound" || rows[0].Name != "tool-picked" {
		t.Fatalf("confirm: %+v %v", rows, err)
	}
	if got, _, _ := m.Get(ctx, l.ID); got.ResourceRef.Pending || got.ResourceRef.Name != "tool-picked" {
		t.Fatalf("not bound: %+v", got.ResourceRef)
	}
	rows, err = m.Reconcile(ctx, l.ID, true)
	if err != nil || rows[0].Status != "already_done" {
		t.Fatalf("retry: %+v %v", rows, err)
	}
	if _, err := m.Reconcile(ctx, strings.Repeat("0", 32), false); reason(err) != "not_found" {
		t.Fatalf("missing: %v", err)
	}
}

func TestReconcileAmbiguousConflictUnknown(t *testing.T) {
	ctx := context.Background()
	m, engine, _ := pendingManager(t)
	l, _, err := m.Create(ctx, pendingRequest("", ""))
	if err != nil {
		t.Fatal(err)
	}
	engine.clusters["a"] = map[string]string{LeaseLabel: l.ID}
	engine.clusters["b"] = map[string]string{LeaseLabel: l.ID}
	rows, err := m.Reconcile(ctx, l.ID, true)
	if err != nil || rows[0].Status != "ambiguous" || len(rows[0].Matches) != 2 {
		t.Fatalf("ambiguous: %+v %v", rows, err)
	}
	if _, err := m.Release(ctx, ReleaseRequest{ID: l.ID, Confirm: true}); reason(err) != "ambiguous" || len(engine.clusters) != 2 {
		t.Fatalf("ambiguous release destroyed something: %v %v", err, engine.clusters)
	}
	delete(engine.clusters, "b")
	s, err := m.openStore(ctx, m.options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	other, err := newLease(fakeRef, time.Hour, "", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other.ResourceRef = adapterpkg.ResourceRef{Adapter: "kind", Name: "a", Context: "test-context", DockerHost: "unix:///test.sock", Adopted: true, File: filepath.Join(s.dir, "resources", other.ID, "kubeconfig")}
	s.Leases[other.ID] = other
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	rows, err = m.Reconcile(ctx, l.ID, true)
	if err != nil || rows[0].Status != "conflict" || rows[0].Conflicts[0] != "a" {
		t.Fatalf("conflict: %+v %v", rows, err)
	}
	engine.fail = errors.New("engine unavailable")
	rows, err = m.Reconcile(ctx, "", true)
	if err != nil || rows[0].Status != "unknown" || !strings.Contains(rows[0].Error, "unavailable") {
		t.Fatalf("unknown: %+v %v", rows, err)
	}
	if _, err := m.Release(ctx, ReleaseRequest{ID: l.ID, Confirm: true}); err == nil {
		t.Fatal("pending lease forgotten without discovery")
	}
	if _, ok, _ := m.Get(ctx, l.ID); !ok {
		t.Fatal("pending lease lost on discovery failure")
	}
}

func TestPendingCleanupAfterCrash(t *testing.T) {
	ctx := context.Background()
	m, engine, owner := pendingManager(t)
	never, _, err := m.Create(ctx, pendingRequest("never-built", ""))
	if err != nil {
		t.Fatal(err)
	}
	built, _, err := m.Create(ctx, pendingRequest("built", ""))
	if err != nil {
		t.Fatal(err)
	}
	engine.clusters["built"] = nil // created, then the caller crashed before dibs adopt --pending
	if r, err := m.Release(ctx, ReleaseRequest{ID: never.ID, Confirm: true}); err != nil || !r.Released || len(engine.deleted) != 0 {
		t.Fatalf("never-built pending lease: %+v %v %v", r, err, engine.deleted)
	}
	owner.live = ownerpkg.Dead
	result, err := m.Sweep(ctx, SweepRequest{Confirm: true})
	if err != nil || len(result.Reclaimed) != 1 || result.Reclaimed[0] != built.ID || len(engine.clusters) != 0 || engine.deleted[0] != "built" {
		t.Fatalf("crash leftover not reclaimed: %+v %v %v", result, err, engine.clusters)
	}
	if v := diskVersion(t, m); v != 2 {
		t.Fatalf("version 3 kept after last pending lease: %d", v)
	}
}

func TestIdempotencyKeys(t *testing.T) {
	ctx := context.Background()
	m, engine, _ := pendingManager(t)
	first, done, err := m.Create(ctx, CreateRequest{Owner: fakeRef, TTL: time.Hour, Adapter: "none", Key: "task-7:meta"})
	if err != nil || done || first.Key != "task-7:meta" {
		t.Fatalf("keyed create: %v %v", done, err)
	}
	again, done, err := m.Create(ctx, CreateRequest{Owner: fakeRef, TTL: time.Hour, Adapter: "none", Key: "task-7:meta"})
	if err != nil || !done || again.ID != first.ID {
		t.Fatalf("retried create made a second lease: %v %v", done, err)
	}
	if _, _, err := m.Create(ctx, CreateRequest{Owner: fakeRef, TTL: time.Hour, Adapter: "kind", Pending: true, Scope: testScope, Key: "task-7:meta"}); reason(err) != "key_conflict" {
		t.Fatalf("key reused for another request: %v", err)
	}
	_, _, err = m.Create(ctx, CreateRequest{Owner: fakeRef, TTL: time.Hour, Adapter: "kind", Key: "mode-normal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Create(ctx, pendingRequest("lab", "mode-normal")); reason(err) != "key_conflict" {
		t.Fatalf("normal key reused for pending create: %v", err)
	}
	pending, _, err := m.Create(ctx, pendingRequest("lab", "task-7:lab"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Create(ctx, CreateRequest{Owner: fakeRef, TTL: time.Hour, Adapter: "kind", Key: "task-7:lab"}); reason(err) != "key_conflict" {
		t.Fatalf("pending key reused for normal create: %v", err)
	}
	engine.clusters["lab"] = nil
	if _, done, err := m.Adopt(ctx, AdoptRequest{Pending: pending.ID, Resource: adapterpkg.ResourceRef{Name: "lab"}}); err != nil || done {
		t.Fatalf("bind pending create: %v %v", done, err)
	}
	if retry, done, err := m.Create(ctx, pendingRequest("lab", "task-7:lab")); err != nil || !done || retry.ID != pending.ID {
		t.Fatalf("pending retry after binding: %v %v", done, err)
	}
	if _, _, err := m.Create(ctx, pendingRequest("other", "task-7:lab")); reason(err) != "key_conflict" {
		t.Fatalf("key reused for another handle: %v", err)
	}
	engine.clusters["existing"] = nil
	adopt := AdoptRequest{Owner: fakeRef, TTL: time.Hour, Resource: adapterpkg.ResourceRef{Adapter: "kind", Name: "existing", Context: "test-context", DockerHost: "unix:///test.sock"}, Key: "task-7:existing"}
	adopted, done, err := m.Adopt(ctx, adopt)
	if err != nil || done {
		t.Fatalf("keyed adopt: %v", err)
	}
	if retry, done, err := m.Adopt(ctx, adopt); err != nil || !done || retry.ID != adopted.ID {
		t.Fatalf("retried adopt: %v %v", done, err)
	}
	otherScope := adopt
	otherScope.Resource.Context, otherScope.Resource.DockerHost = "other-context", "unix:///other.sock"
	if _, _, err := m.Adopt(ctx, otherScope); reason(err) != "key_conflict" {
		t.Fatalf("adopt key reused across Docker scopes: %v", err)
	}
	if _, err := m.Release(ctx, ReleaseRequest{ID: first.ID, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if fresh, done, err := m.Create(ctx, CreateRequest{Owner: fakeRef, TTL: time.Hour, Adapter: "none", Key: "task-7:meta"}); err != nil || done || fresh.ID == first.ID {
		t.Fatalf("released key not reusable: %v %v", done, err)
	}
}

func TestReclaimRequiresProvenDeath(t *testing.T) {
	ctx := context.Background()
	m, engine, owner := pendingManager(t)
	engine.clusters["lab"] = nil
	l, _, err := m.Adopt(ctx, AdoptRequest{Owner: fakeRef, TTL: time.Nanosecond, Resource: adapterpkg.ResourceRef{Adapter: "kind", Name: "lab", Context: "test-context", DockerHost: "unix:///test.sock"}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	for live, want := range map[ownerpkg.Liveness]string{ownerpkg.Alive: "owner_alive", ownerpkg.Unknown: "owner_unknown"} {
		owner.live = live
		if _, err := m.Reclaim(ctx, l.ID, true); reason(err) != want {
			t.Fatalf("%s owner of an expired lease: %v", live, err)
		}
		e, err := m.Explain(ctx, l.ID)
		if err != nil || !e.Expired || !e.Sweep || e.Reclaim || !strings.Contains(e.Reason, "sweep would destroy") {
			t.Fatalf("explain %s: %+v %v", live, e, err)
		}
	}
	if len(engine.deleted) != 0 {
		t.Fatal("refused reclaim destroyed the resource")
	}
	owner.live = ownerpkg.Dead
	if r, err := m.Reclaim(ctx, l.ID, false); err != nil || r.Status != "preview" || len(engine.deleted) != 0 {
		t.Fatalf("preview: %+v %v", r, err)
	}
	if e, err := m.Explain(ctx, l.ID); err != nil || !e.Reclaim || !strings.Contains(e.Reason, "owner is dead") {
		t.Fatalf("explain dead: %+v %v", e, err)
	}
	if r, err := m.Reclaim(ctx, l.ID, true); err != nil || r.Status != "reclaimed" || len(engine.clusters) != 0 {
		t.Fatalf("reclaim: %+v %v", r, err)
	}
	if r, err := m.Reclaim(ctx, l.ID, true); err != nil || r.Status != "already_done" {
		t.Fatalf("retried reclaim: %+v %v", r, err)
	}
	if _, err := m.Explain(ctx, l.ID); reason(err) != "not_found" {
		t.Fatalf("explain missing: %v", err)
	}
}

func TestExplainUnexpired(t *testing.T) {
	ctx := context.Background()
	m, _, owner := pendingManager(t)
	l, _, err := m.Create(ctx, pendingRequest("lab", ""))
	if err != nil {
		t.Fatal(err)
	}
	for live, want := range map[ownerpkg.Liveness]string{ownerpkg.Alive: "owner is alive", ownerpkg.Unknown: "unknown"} {
		owner.live = live
		e, err := m.Explain(ctx, l.ID)
		if err != nil || e.Sweep || e.Reclaim || !strings.Contains(e.Reason, want) || !strings.Contains(e.Reason, "pending registration") {
			t.Fatalf("%s: %+v %v", live, e, err)
		}
	}
}

func TestVersionThreeValidation(t *testing.T) {
	s := testStore(t)
	l := testLease(t)
	l.Key = "k"
	s.Leases[l.ID] = l
	s.Version = 2
	if err := s.validate(); err == nil {
		t.Fatal("version 2 accepted a key")
	}
	s.Version = 3
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	twin := testLease(t)
	twin.Key = "k"
	s.Leases[twin.ID] = twin
	if err := s.validate(); err == nil {
		t.Fatal("duplicate key accepted")
	}
	delete(s.Leases, twin.ID)
	for name, ref := range map[string]adapterpkg.ResourceRef{
		"unknown adapter": {Adapter: "nope", Pending: true},
		"no discovery":    {Adapter: "none", Pending: true},
		"adopted":         {Adapter: "kind", Pending: true, Adopted: true},
		"bad handle":      {Adapter: "kind", Pending: true, Name: "-x"},
	} {
		p := testLease(t)
		p.ResourceRef = ref
		s.Leases[p.ID] = p
		if err := s.validate(); err == nil {
			t.Fatalf("%s pending record accepted", name)
		}
		delete(s.Leases, p.ID)
	}
	p := testLease(t)
	p.ResourceRef = adapterpkg.ResourceRef{Adapter: "kind", Pending: true, Context: "c"}
	s.Leases[p.ID] = p
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	s.Version = 2
	if err := s.validate(); err == nil {
		t.Fatal("version 2 accepted a pending record")
	}
	s.Version = 4
	if err := s.validate(); err == nil {
		t.Fatal("version 4 accepted")
	}
}
