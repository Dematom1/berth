package adapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

type CommandRunner func(context.Context, []string, string, ...string) ([]byte, error)

func RunCommand(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
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

func DockerEnv(ref ResourceRef) []string {
	return []string{"DOCKER_HOST=", "DOCKER_CONTEXT=" + ref.Context, "KIND_EXPERIMENTAL_PROVIDER=docker", "COMPOSE_INTERACTIVE_NO_CLI=1"}
}
