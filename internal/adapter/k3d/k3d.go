package k3d

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	adapterpkg "github.com/Dematom1/berth/internal/adapter"
)

type k3dAdapter struct{ run adapterpkg.CommandRunner }

func k3dEnv(ref adapterpkg.ResourceRef) []string {
	return []string{"DOCKER_CONTEXT=", "DOCKER_HOST=" + ref.DockerHost, "KIND_EXPERIMENTAL_PROVIDER=docker", "KUBECONFIG=" + ref.File}
}

func (a k3dAdapter) Create(ctx context.Context, l adapterpkg.Target) error {
	_, err := a.run(ctx, k3dEnv(l.Ref), "k3d", "cluster", "create", l.Ref.Name)
	return err
}

func (a k3dAdapter) Destroy(ctx context.Context, l adapterpkg.Target) error {
	_, err := a.run(ctx, k3dEnv(l.Ref), "k3d", "cluster", "delete", l.Ref.Name)
	if err != nil {
		return err
	}
	out, err := a.run(ctx, k3dEnv(l.Ref), "k3d", "cluster", "list", "-o", "json")
	if err != nil {
		return err
	}
	var clusters []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &clusters); err != nil {
		return errors.New("cannot verify k3d cluster cleanup")
	}
	for _, cluster := range clusters {
		if cluster.Name == l.Ref.Name {
			return errors.New("cluster survived cleanup; retry berth release")
		}
	}
	return nil
}

func prepare(ctx context.Context, request adapterpkg.PrepareRequest, run adapterpkg.CommandRunner) (ref adapterpkg.ResourceRef, err error) {
	contextName := request.Existing.Context
	if contextName == "" {
		contextName, err = adapterpkg.DockerContext(ctx, run)
	}
	if err != nil {
		return ref, err
	}
	dir := request.SnapshotDir
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ref, err
	}
	ref = adapterpkg.ResourceRef{Adapter: "k3d", Name: "berth-" + request.Metadata.LeaseID[:26], Context: contextName, File: filepath.Join(dir, "k3d")}
	out, err := run(ctx, nil, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}", contextName)
	if err != nil {
		return ref, err
	}
	ref.DockerHost = strings.TrimSpace(string(out))
	if ref.DockerHost == "" {
		return ref, errors.New("selected Docker context has no endpoint")
	}
	if request.Existing.Adopted {
		ref.Name = request.Existing.Name
		ref.Context = request.Existing.Context
		ref.DockerHost = request.Existing.DockerHost
		ref.Adopted = true
		data, err := run(ctx, k3dEnv(ref), "k3d", "kubeconfig", "get", ref.Name)
		if err != nil {
			return ref, err
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			return ref, errors.New("cannot capture existing cluster kubeconfig")
		}
		if err := os.WriteFile(ref.File, data, 0600); err != nil {
			return ref, err
		}
	}
	return ref, nil
}

func validate(dir string, metadata adapterpkg.Metadata, ref adapterpkg.ResourceRef) error {
	if ref.Adopted && (!adapterpkg.ValidResourceName(ref.Name) || ref.DockerHost == "") {
		return errors.New("invalid adopted resource identity")
	}
	if (!ref.Adopted && ref.Name != "berth-"+metadata.LeaseID[:26]) || ref.Context == "" {
		return errors.New("invalid managed resource identity")
	}
	if ref.DockerHost == "" {
		return errors.New("k3d resource is missing its captured Docker endpoint")
	}
	if ref.File != filepath.Join(dir, "k3d") {
		return errors.New("managed resource file must remain inside its original BERTH_STATE directory")
	}
	return nil
}

func New(run adapterpkg.CommandRunner) adapterpkg.Definition {
	return adapterpkg.Definition{Discover: func(ctx context.Context, r adapterpkg.ResourceRef) ([]map[string]any, error) {
		return adapterpkg.DiscoverDocker(ctx, r, "k3d", run)
	}, Name: "k3d", Adapter: k3dAdapter{run}, CreateAllowed: true, RequiresFile: false,
		Prepare: func(ctx context.Context, r adapterpkg.PrepareRequest) (adapterpkg.ResourceRef, error) {
			return prepare(ctx, r, run)
		}, Validate: validate,
		Env: func(ref adapterpkg.ResourceRef) adapterpkg.Environment {
			return adapterpkg.Environment{Command: k3dEnv(ref), Exports: map[string]string{"KUBECONFIG": ref.File}}
		},
	}
}
