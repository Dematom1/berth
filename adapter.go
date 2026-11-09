package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Create runs only after its lease has been committed. Destroy must tolerate
// partially created or already absent resources; failures retain the lease.
type Adapter interface {
	Create(context.Context, Lease) error
	Destroy(context.Context, Lease) error
}

type noResource struct{}

func (noResource) Create(context.Context, Lease) error  { return nil }
func (noResource) Destroy(context.Context, Lease) error { return nil }

type commandRunner func(context.Context, []string, string, ...string) ([]byte, error)

func runCommand(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = nil
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprint(os.Stderr, diagnostics.String())
		return nil, errors.New("resource operation failed; check installed resource tools and their connectivity, then retry berth release or berth sweep --confirm")
	}
	return out, nil
}

func dockerEnv(l Lease) []string {
	return []string{"DOCKER_HOST=", "DOCKER_CONTEXT=" + l.Resource.Context, "KIND_EXPERIMENTAL_PROVIDER=docker", "COMPOSE_INTERACTIVE_NO_CLI=1"}
}

type kindAdapter struct{ run commandRunner }

func (a kindAdapter) Create(ctx context.Context, l Lease) error {
	_, err := a.run(ctx, dockerEnv(l), "kind", "create", "cluster", "--name", l.Resource.Name, "--kubeconfig", l.Resource.File, "--wait", "120s")
	return err
}

func (a kindAdapter) Destroy(ctx context.Context, l Lease) error {
	_, err := a.run(ctx, dockerEnv(l), "kind", "delete", "cluster", "--name", l.Resource.Name, "--kubeconfig", l.Resource.File)
	if err != nil {
		return err
	}
	// Verify before dropping the only cleanup record, including after interrupted creates.
	out, err := a.run(ctx, dockerEnv(l), "kind", "get", "clusters")
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(string(out)) {
		if name == l.Resource.Name {
			return errors.New("cluster survived cleanup; retry berth release")
		}
	}
	return nil
}

type composeAdapter struct{ run commandRunner }

func (a composeAdapter) Create(ctx context.Context, l Lease) error {
	_, err := a.run(ctx, dockerEnv(l), "docker", "--context", l.Resource.Context, "compose", "-p", l.Resource.Name, "-f", l.Resource.File, "up", "-d")
	return err
}

func (a composeAdapter) Destroy(ctx context.Context, l Lease) error {
	_, err := a.run(ctx, dockerEnv(l), "docker", "--context", l.Resource.Context, "compose", "-p", l.Resource.Name, "-f", l.Resource.File, "down", "--volumes", "--remove-orphans")
	if err != nil {
		return err
	}
	out, err := a.run(ctx, dockerEnv(l), "docker", "--context", l.Resource.Context, "ps", "-aq", "--filter", "label=com.docker.compose.project="+l.Resource.Name)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(out)) != 0 {
		return errors.New("stack containers survived cleanup; retry berth release")
	}
	for _, kind := range []string{"volume", "network"} {
		out, err := a.run(ctx, dockerEnv(l), "docker", "--context", l.Resource.Context, kind, "ls", "-q", "--filter", "label=io.berth.lease="+l.ID)
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(out)) != 0 {
			return fmt.Errorf("stack %s survived cleanup; retry berth release", kind)
		}
	}
	return nil
}

func adapters() map[string]Adapter {
	return map[string]Adapter{"none": noResource{}, "kind": kindAdapter{runCommand}, "compose": composeAdapter{runCommand}}
}

// Prepare snapshots the resolved Compose config before saving the lease. Cleanup
// never depends on the original worktree, environment, or Compose file surviving.
func prepareResource(ctx context.Context, s *Store, l *Lease, adapter, file string, run commandRunner) error {
	if adapter == "none" {
		return nil
	}
	if os.Getenv("DOCKER_HOST") != "" {
		return errors.New("DOCKER_HOST is unsupported; select a named Docker context before running berth create")
	}
	out, err := run(ctx, nil, "docker", "context", "show")
	if err != nil {
		return err
	}
	contextName := strings.TrimSpace(string(out))
	if contextName == "" {
		return errors.New("no Docker context selected; select a context before berth create")
	}
	dir := filepath.Join(s.dir, "resources", l.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	l.Resource = Resource{Adapter: adapter, Name: "berth-" + l.ID, Context: contextName, File: filepath.Join(dir, "kubeconfig")}
	if adapter != "compose" {
		return nil
	}
	out, err = run(ctx, dockerEnv(*l), "docker", "--context", contextName, "compose", "-p", l.Resource.Name, "-f", file, "config", "--format", "json")
	if err != nil {
		return err
	}
	var config map[string]any
	if err := json.Unmarshal(out, &config); err != nil {
		return errors.New("cannot resolve stack configuration; check the file passed to berth create --file")
	}
	services, ok := config["services"].(map[string]any)
	if !ok || len(services) == 0 {
		return errors.New("stack must define services; check berth create --file")
	}
	tags := map[string]string{"io.berth.lease": l.ID, "io.berth.owner": l.Owner.String(), "io.berth.expires": l.Expires.Format(time.RFC3339Nano), "io.berth.machine": l.Machine}
	for _, section := range []string{"services", "volumes", "networks"} {
		entries, _ := config[section].(map[string]any)
		for key, entry := range entries {
			object, ok := entry.(map[string]any)
			if !ok {
				return errors.New("invalid resolved stack entry")
			}
			if external, _ := object["external"].(bool); external {
				continue
			}
			if section == "services" && object["container_name"] != nil {
				return errors.New("fixed container_name is unsupported; remove it so berth owns a unique project")
			}
			if section != "services" {
				if name, _ := object["name"].(string); name != "" && name != l.Resource.Name+"_"+key {
					return errors.New("fixed volume or network names are unsafe to reclaim; remove name or mark the resource external")
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
		return err
	}
	l.Resource.File = filepath.Join(dir, "compose.json")
	return atomicWrite(l.Resource.File, data)
}
