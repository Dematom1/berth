package kind

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
)

type kindAdapter struct{ run adapterpkg.CommandRunner }

func (a kindAdapter) Create(ctx context.Context, l adapterpkg.Target) error {
	_, err := a.run(ctx, adapterpkg.DockerEnv(l.Ref), "kind", "create", "cluster", "--name", l.Ref.Name, "--kubeconfig", l.Ref.File, "--wait", "120s")
	return err
}

func (a kindAdapter) Destroy(ctx context.Context, l adapterpkg.Target) error {
	_, err := a.run(ctx, adapterpkg.DockerEnv(l.Ref), "kind", "delete", "cluster", "--name", l.Ref.Name, "--kubeconfig", l.Ref.File)
	if err != nil {
		return err
	}
	// Verify before dropping the only cleanup record, including after interrupted creates.
	out, err := a.run(ctx, adapterpkg.DockerEnv(l.Ref), "kind", "get", "clusters")
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(string(out)) {
		if name == l.Ref.Name {
			return errors.New("cluster survived cleanup; retry dibs release")
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
	resourceFile := "kubeconfig"

	resourceName := "dibs-" + request.Metadata.LeaseID

	ref = adapterpkg.ResourceRef{Adapter: "kind", Name: resourceName, Context: contextName, File: filepath.Join(dir, resourceFile)}
	if request.Existing.Adopted {
		ref.Name = request.Existing.Name
		ref.Context = request.Existing.Context
		ref.DockerHost = request.Existing.DockerHost
		ref.Adopted = true
		data, err := run(ctx, adapterpkg.DockerEnv(ref), "kind", "get", "kubeconfig", "--name", ref.Name)
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
	if (!ref.Adopted && ref.Name != "dibs-"+metadata.LeaseID && ref.Name != "berth-"+metadata.LeaseID) || ref.Context == "" {
		return errors.New("invalid managed resource identity")
	}
	if !adapterpkg.SnapshotMatches(ref.File, filepath.Join(dir, "kubeconfig")) {
		return errors.New("managed resource file must remain inside its original DIBS_STATE directory")
	}
	return nil
}

func New(run adapterpkg.CommandRunner) adapterpkg.Definition {
	return adapterpkg.Definition{Discover: func(ctx context.Context, r adapterpkg.ResourceRef) ([]map[string]any, error) {
		return adapterpkg.DiscoverDocker(ctx, r, "kind", run)
	}, Name: "kind", Adapter: kindAdapter{run}, CreateAllowed: true, RequiresFile: false,
		Prepare: func(ctx context.Context, r adapterpkg.PrepareRequest) (adapterpkg.ResourceRef, error) {
			return prepare(ctx, r, run)
		}, Validate: validate,
		Env: func(ref adapterpkg.ResourceRef) adapterpkg.Environment {
			return adapterpkg.Environment{Command: adapterpkg.DockerEnv(ref), Exports: map[string]string{"KUBECONFIG": ref.File}}
		},
	}
}
