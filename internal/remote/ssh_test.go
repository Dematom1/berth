package remote

import (
	"context"
	"strings"
	"testing"

	configpkg "github.com/Dematom1/berth/internal/config"
)

func TestSSHCommand(t *testing.T) {
	target := configpkg.Target{Host: "lenovo", User: "worker", Workdir: "/home/worker/source 'quoted'", Agent: "/home/worker/bin/berth"}
	cmd := sshCommand(context.Background(), target, "echo safe")
	joined := strings.Join(cmd.Args, " ")
	for _, required := range []string{"BatchMode=yes", "ServerAliveInterval=5", "ServerAliveCountMax=3", "-- lenovo echo safe"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %s in %v", required, cmd.Args)
		}
	}
}
