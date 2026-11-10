package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var resourceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func ValidResourceName(name string) bool {
	return resourceNamePattern.MatchString(name)
}

func ResolveDockerContext(ctx context.Context, name string, run CommandRunner) (ResourceRef, error) {
	if os.Getenv("DOCKER_HOST") != "" {
		return ResourceRef{}, errors.New("DOCKER_HOST is unsupported; select a named Docker context")
	}
	if name == "" {
		out, err := run(ctx, nil, "docker", "context", "show")
		if err != nil {
			return ResourceRef{}, err
		}
		name = strings.TrimSpace(string(out))
	}
	if name == "" || strings.HasPrefix(name, "-") {
		return ResourceRef{}, errors.New("select a named Docker context")
	}
	out, err := run(ctx, nil, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}", name)
	if err != nil {
		return ResourceRef{}, err
	}
	endpoint := strings.TrimSpace(string(out))
	if endpoint == "" {
		return ResourceRef{}, errors.New("selected Docker context has no endpoint")
	}
	return ResourceRef{Context: name, DockerHost: endpoint}, nil
}

func DiscoverDocker(ctx context.Context, r ResourceRef, adapter string, run CommandRunner) ([]map[string]any, error) {
	label := map[string]string{"kind": "io.x-k8s.kind.cluster", "k3d": "k3d.cluster", "compose": "com.docker.compose.project"}[adapter]
	groups := map[string]map[string]any{}
	kinds := []string{"container"}
	if adapter == "compose" {
		kinds = append(kinds, "volume", "network")
	}
	for _, kind := range kinds {
		args := []string{"--context", r.Context, kind, "ls", "-q", "--filter", "label=" + label}
		if kind == "container" {
			args = append(args, "-a")
		}
		out, err := run(ctx, DockerEnv(r), "docker", args...)
		if err != nil {
			return nil, err
		}
		ids := strings.Fields(string(out))
		if len(ids) == 0 {
			continue
		}
		args = append([]string{"--context", r.Context, kind, "inspect"}, ids...)
		out, err = run(ctx, DockerEnv(r), "docker", args...)
		if err != nil {
			return nil, err
		}
		var objects []struct {
			Name      string
			Created   string
			CreatedAt string
			Labels    map[string]string
			Config    struct{ Labels map[string]string }
			State     struct{ Status string }
		}
		if err := json.Unmarshal(out, &objects); err != nil {
			return nil, errors.New("cannot decode resource inventory; existence is unknown")
		}
		if len(objects) != len(ids) {
			return nil, errors.New("incomplete resource inventory; existence is unknown")
		}
		for _, object := range objects {
			labels := object.Labels
			if kind == "container" {
				labels = object.Config.Labels
			}
			name := labels[label]
			if !ValidResourceName(name) {
				return nil, errors.New("invalid resource identity in inventory; existence is unknown")
			}
			created := object.Created
			if created == "" {
				created = object.CreatedAt
			}
			row := groups[name]
			if row == nil {
				row = map[string]any{"type": adapter, "name": name, "docker_context": r.Context, "created": created, "labels": labels, "working_dir": "", "working_dir_status": "unknown", "config_files": "", "evidence": map[string]any{}}
				groups[name] = row
			}
			row["evidence"].(map[string]any)[kind+":"+object.Name] = map[string]any{"created": created, "labels": labels, "status": object.State.Status}
			if dir := labels["com.docker.compose.project.working_dir"]; dir != "" {
				row["working_dir"], row["config_files"] = dir, labels["com.docker.compose.project.config_files"]
				status := "unknown"
				if filepath.IsAbs(dir) {
					info, err := os.Stat(dir)
					if err == nil {
						if info.IsDir() {
							status = "exists"
						} else {
							status = "not a directory"
						}
					} else if errors.Is(err, os.ErrNotExist) {
						status = "missing"
					}
				}
				row["working_dir_status"] = status
			}
		}
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]map[string]any, 0, len(names))
	for _, name := range names {
		rows = append(rows, groups[name])
	}
	return rows, nil
}

// A snapshot read must not create directories, lock files, chmod state, or migrate it on disk.
