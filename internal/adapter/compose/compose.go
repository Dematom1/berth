package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	adapterpkg "github.com/Dematom1/berth/internal/adapter"
	atomicfilepkg "github.com/Dematom1/berth/internal/atomicfile"
)

type composeAdapter struct{ run adapterpkg.CommandRunner }

func (a composeAdapter) Create(ctx context.Context, l adapterpkg.Target) error {
	_, err := a.run(ctx, adapterpkg.DockerEnv(l.Ref), "docker", "--context", l.Ref.Context, "compose", "-p", l.Ref.Name, "-f", l.Ref.File, "up", "-d")
	return err
}

func (a composeAdapter) Destroy(ctx context.Context, l adapterpkg.Target) error {
	_, err := a.run(ctx, adapterpkg.DockerEnv(l.Ref), "docker", "--context", l.Ref.Context, "compose", "-p", l.Ref.Name, "-f", l.Ref.File, "down", "--volumes", "--remove-orphans")
	if err != nil {
		return err
	}
	out, err := a.run(ctx, adapterpkg.DockerEnv(l.Ref), "docker", "--context", l.Ref.Context, "ps", "-aq", "--filter", "label=com.docker.compose.project="+l.Ref.Name)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(out)) != 0 {
		return errors.New("stack containers survived cleanup; retry berth release")
	}
	for _, kind := range []string{"volume", "network"} {
		out, err := a.run(ctx, adapterpkg.DockerEnv(l.Ref), "docker", "--context", l.Ref.Context, kind, "ls", "-q", "--filter", "label=com.docker.compose.project="+l.Ref.Name)
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(out)) != 0 {
			return fmt.Errorf("stack %s survived cleanup; retry berth release", kind)
		}
	}
	return nil
}

// Prepare snapshots the resolved Compose config before saving the lease. Cleanup
// never depends on the original worktree, environment, or Compose file surviving.
func prepare(ctx context.Context, request adapterpkg.PrepareRequest, run adapterpkg.CommandRunner) (ref adapterpkg.ResourceRef, err error) {
	file := request.File
	contextName, err := adapterpkg.DockerContext(ctx, run)
	if err != nil {
		return ref, err
	}
	var out []byte
	dir := request.SnapshotDir
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ref, err
	}
	resourceFile := "kubeconfig"

	resourceName := "berth-" + request.Metadata.LeaseID

	ref = adapterpkg.ResourceRef{Adapter: "compose", Name: resourceName, Context: contextName, File: filepath.Join(dir, resourceFile)}
	out, err = run(ctx, adapterpkg.DockerEnv(ref), "docker", "--context", contextName, "compose", "-p", ref.Name, "-f", file, "config", "--format", "json")
	if err != nil {
		return ref, err
	}
	var config map[string]any
	if err := json.Unmarshal(out, &config); err != nil {
		return ref, errors.New("cannot resolve stack configuration; check the file passed to berth create --file")
	}
	services, ok := config["services"].(map[string]any)
	if !ok || len(services) == 0 {
		return ref, errors.New("stack must define services; check berth create --file")
	}
	tags := map[string]string{"io.berth.lease": request.Metadata.LeaseID, "io.berth.owner": request.Metadata.Owner, "io.berth.expires": request.Metadata.Expires.Format(time.RFC3339Nano), "io.berth.machine": request.Metadata.Machine}
	for _, section := range []string{"services", "volumes", "networks"} {
		entries, _ := config[section].(map[string]any)
		for key, entry := range entries {
			object, ok := entry.(map[string]any)
			if !ok {
				return ref, errors.New("invalid resolved stack entry")
			}
			if external, _ := object["external"].(bool); external {
				continue
			}
			if section == "services" && object["container_name"] != nil {
				return ref, errors.New("fixed container_name is unsupported; remove it so berth owns a unique project")
			}
			if section != "services" {
				if name, _ := object["name"].(string); name != "" && name != ref.Name+"_"+key {
					return ref, errors.New("fixed volume or network names are unsafe to reclaim; remove name or mark the resource external")
				}
			}
			labels, _ := object["labels"].(map[string]any)
			if labels == nil {
				labels = map[string]any{}
			}
			for k, v := range tags {
				labels[k] = v
			}
			object["labels"] = labels
		}
	}
	data, err := json.Marshal(config)
	if err != nil {
		return ref, err
	}
	ref.File = filepath.Join(dir, "compose.json")
	return ref, atomicfilepkg.Write(ref.File, data)
}

func validate(dir string, metadata adapterpkg.Metadata, ref adapterpkg.ResourceRef) error {
	if ref.Name != "berth-"+metadata.LeaseID || ref.Context == "" {
		return errors.New("invalid managed resource identity")
	}
	if ref.File != filepath.Join(dir, "compose.json") {
		return errors.New("managed resource file must remain inside its original BERTH_STATE directory")
	}
	return nil
}

func New(run adapterpkg.CommandRunner) adapterpkg.Definition {
	return adapterpkg.Definition{Name: "compose", Adapter: composeAdapter{run}, CreateAllowed: true, RequiresFile: true,
		Prepare: func(ctx context.Context, r adapterpkg.PrepareRequest) (adapterpkg.ResourceRef, error) {
			return prepare(ctx, r, run)
		}, Validate: validate,
		Env: func(ref adapterpkg.ResourceRef) adapterpkg.Environment {
			return adapterpkg.Environment{Command: adapterpkg.DockerEnv(ref)}
		},
	}
}
