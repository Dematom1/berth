package adapter

import (
	"context"
	"errors"
	"os"
	"strings"
)

func DockerContext(ctx context.Context, run CommandRunner) (string, error) {
	if os.Getenv("DOCKER_HOST") != "" {
		return "", errors.New("DOCKER_HOST is unsupported; select a named Docker context before running dibs create")
	}
	out, err := run(ctx, nil, "docker", "context", "show")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return "", errors.New("no Docker context selected; select a context before dibs create")
	}
	return name, nil
}
