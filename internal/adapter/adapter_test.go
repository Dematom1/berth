package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	composepkg "github.com/Dematom1/dibs/internal/adapter/compose"
	k3dpkg "github.com/Dematom1/dibs/internal/adapter/k3d"
	kindpkg "github.com/Dematom1/dibs/internal/adapter/kind"
)

func TestAdapterCommandsAndSurvivors(t *testing.T) {
	l := testLease(t)
	l.Ref = adapterpkg.ResourceRef{Adapter: "kind", Name: "dibs-" + l.Metadata.LeaseID, Context: "test-context", File: "/private/kubeconfig"}
	type invocation struct {
		name string
		args []string
	}
	var calls []invocation
	runner := func(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
		if !reflect.DeepEqual(env, []string{"DOCKER_HOST=", "DOCKER_CONTEXT=test-context", "KIND_EXPERIMENTAL_PROVIDER=docker", "COMPOSE_INTERACTIVE_NO_CLI=1"}) {
			t.Fatalf("context environment not pinned to recorded context: %v", env)
		}
		calls = append(calls, invocation{name, args})
		return nil, nil
	}
	a := kindAdapter{runner}
	if err := a.Create(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	want := []string{"create", "cluster", "--name", l.Ref.Name, "--kubeconfig", l.Ref.File, "--wait", "120s"}
	if calls[0].name != "kind" || !reflect.DeepEqual(calls[0].args, want) {
		t.Fatalf("kind creation contract: %+v", calls)
	}
	for i := 0; i < 2; i++ {
		if err := a.Destroy(context.Background(), l); err != nil {
			t.Fatal(err)
		}
	}
	if calls[1].args[0] != "delete" || calls[2].args[0] != "get" {
		t.Fatalf("kind deletion must verify absence: %+v", calls)
	}
	calls = nil
	l.Ref.DockerHost = "unix:///docker.sock"
	k3d := k3dAdapter{func(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
		if !reflect.DeepEqual(env, []string{"DOCKER_CONTEXT=", "DOCKER_HOST=" + l.Ref.DockerHost, "KIND_EXPERIMENTAL_PROVIDER=docker", "KUBECONFIG=" + l.Ref.File}) || name != "k3d" {
			t.Fatalf("k3d did not use captured Docker context: %s %v", name, env)
		}
		calls = append(calls, invocation{name, args})
		if reflect.DeepEqual(args, []string{"cluster", "list", "-o", "json"}) {
			return []byte("[]"), nil
		}
		return nil, nil
	}}
	if err := k3d.Create(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if err := k3d.Destroy(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls[0].args, []string{"cluster", "create", l.Ref.Name}) || !reflect.DeepEqual(calls[1].args, []string{"cluster", "delete", l.Ref.Name}) {
		t.Fatalf("unexpected k3d lifecycle: %+v", calls)
	}
	alive := kindAdapter{func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte(l.Ref.Name + "\n"), nil
	}}
	if err := alive.Destroy(context.Background(), l); err == nil {
		t.Fatal("surviving kind cluster accepted as gone")
	}
	calls = nil
	c := composeAdapter{runner}
	if err := c.Create(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if err := c.Destroy(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if calls[0].name != "docker" || !reflect.DeepEqual(calls[0].args, []string{"--context", "test-context", "compose", "-p", l.Ref.Name, "-f", l.Ref.File, "up", "-d"}) {
		t.Fatalf("compose create contract: %+v", calls)
	}
	if !reflect.DeepEqual(calls[1].args, []string{"--context", "test-context", "compose", "-p", l.Ref.Name, "-f", l.Ref.File, "down", "--volumes", "--remove-orphans"}) {
		t.Fatalf("compose destruction contract: %+v", calls[1])
	}
	for _, survive := range []string{"container", "volume", "network"} {
		c := composeAdapter{func(_ context.Context, _ []string, _ string, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, survive+" ls") && survive != "container" && !strings.Contains(joined, "label=com.docker.compose.project="+l.Ref.Name) {
				t.Fatalf("cleanup verification did not use project identity: %s", joined)
			}
			if (survive == "container" && strings.Contains(joined, "ps -aq")) || strings.Contains(joined, survive+" ls") {
				return []byte("survivor\n"), nil
			}
			return nil, nil
		}}
		if err := c.Destroy(context.Background(), l); err == nil {
			t.Fatalf("surviving %s accepted as gone", survive)
		}
	}
	failed := composeAdapter{func(context.Context, []string, string, ...string) ([]byte, error) {
		return nil, errors.New("engine unavailable")
	}}
	if failed.Destroy(context.Background(), l) == nil {
		t.Fatal("adapter hid engine failure")
	}
}

func TestK3dKeepsCallerKubeconfig(t *testing.T) {
	dir := t.TempDir()
	caller := filepath.Join(dir, "caller-kubeconfig")
	if err := os.WriteFile(caller, []byte("caller configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", caller)
	// Model k3d's documented create/delete writes at the real subprocess boundary.
	stub := "#!/bin/sh\nif [ \"$2\" = list ]; then printf '[]'; else printf 'updated by k3d' > \"$KUBECONFIG\"; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "k3d"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	l := testLease(t)
	l.Ref = adapterpkg.ResourceRef{Adapter: "k3d", Name: "dibs-test", File: filepath.Join(dir, "private-kubeconfig"), DockerHost: "unix:///docker.sock"}
	a := k3dAdapter{adapterpkg.RunCommand}
	if err := a.Create(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if err := a.Destroy(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(caller)
	if err != nil || string(data) != "caller configuration" {
		t.Fatalf("caller kubeconfig changed: %q, %v", data, err)
	}
	if data, err := os.ReadFile(l.Ref.File); err != nil || string(data) != "updated by k3d" {
		t.Fatalf("k3d did not use its private kubeconfig: %q, %v", data, err)
	}
}

func TestComposeSnapshotAndUnsafeNames(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	for _, unsafe := range []string{"", "volume", "network", "container"} {
		s := struct{ dir string }{t.TempDir()}
		l := testLease(t)
		runner := func(_ context.Context, _ []string, _ string, args ...string) ([]byte, error) {
			if reflect.DeepEqual(args, []string{"context", "show"}) {
				return []byte("pinned\n"), nil
			}
			config := map[string]any{
				"services": map[string]any{"db": map[string]any{"image": "postgres", "labels": map[string]any{"custom": "keep"}}},
				"volumes":  map[string]any{"data": map[string]any{"name": "dibs-" + l.Metadata.LeaseID + "_data"}},
				"networks": map[string]any{"default": map[string]any{"name": "dibs-" + l.Metadata.LeaseID + "_default"}},
			}
			switch unsafe {
			case "volume":
				config["volumes"].(map[string]any)["foreign"] = map[string]any{"name": "somebody-elses-data"}
			case "network":
				config["networks"].(map[string]any)["foreign"] = map[string]any{"name": "somebody-elses-network"}
			case "container":
				config["services"].(map[string]any)["db"].(map[string]any)["container_name"] = "foreign"
			}
			return json.Marshal(config)
		}
		ref, err := composepkg.New(runner).Prepare(context.Background(), adapterpkg.PrepareRequest{Metadata: l.Metadata, SnapshotDir: filepath.Join(s.dir, "resources", l.Metadata.LeaseID), File: "input.yaml"})
		l.Ref = ref
		if unsafe != "" {
			if err == nil {
				t.Fatalf("unsafe %s accepted", unsafe)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if l.Ref.Context != "pinned" || !strings.HasPrefix(l.Ref.File, s.dir+string(filepath.Separator)) {
			t.Fatalf("cleanup files or context not pinned: %+v", l.Ref)
		}
		data, err := os.ReadFile(l.Ref.File)
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := json.Unmarshal(data, &config); err != nil {
			t.Fatal(err)
		}
		db := config["services"].(map[string]any)["db"].(map[string]any)
		labels := db["labels"].(map[string]any)
		if labels["io.dibs.lease"] != l.Metadata.LeaseID || labels["custom"] != "keep" || labels["io.dibs.owner"] != l.Metadata.Owner {
			t.Fatalf("snapshot lost attribution: %+v", labels)
		}
	}
}
func testLease(t *testing.T) adapterpkg.Target {
	t.Helper()
	return adapterpkg.Target{Ref: adapterpkg.ResourceRef{Adapter: "none"}, Metadata: adapterpkg.Metadata{LeaseID: strings.Repeat("a", 32), Machine: "lab", Owner: "pid:123", Expires: time.Date(2026, 11, 9, 13, 0, 0, 0, time.UTC)}}
}

type kindAdapter struct{ run adapterpkg.CommandRunner }

func (a kindAdapter) Create(ctx context.Context, l adapterpkg.Target) error {
	return kindpkg.New(a.run).Adapter.Create(ctx, l)
}
func (a kindAdapter) Destroy(ctx context.Context, l adapterpkg.Target) error {
	return kindpkg.New(a.run).Adapter.Destroy(ctx, l)
}

type k3dAdapter struct{ run adapterpkg.CommandRunner }

func (a k3dAdapter) Create(ctx context.Context, l adapterpkg.Target) error {
	return k3dpkg.New(a.run).Adapter.Create(ctx, l)
}
func (a k3dAdapter) Destroy(ctx context.Context, l adapterpkg.Target) error {
	return k3dpkg.New(a.run).Adapter.Destroy(ctx, l)
}

type composeAdapter struct{ run adapterpkg.CommandRunner }

func (a composeAdapter) Create(ctx context.Context, l adapterpkg.Target) error {
	return composepkg.New(a.run).Adapter.Create(ctx, l)
}
func (a composeAdapter) Destroy(ctx context.Context, l adapterpkg.Target) error {
	return composepkg.New(a.run).Adapter.Destroy(ctx, l)
}
