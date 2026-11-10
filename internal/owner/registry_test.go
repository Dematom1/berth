package owner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHeartbeatIgnoresProcessIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heartbeat")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	reference := Reference{Kind: "heartbeat", ID: path, Identity: "ignored process start", Stale: time.Hour}
	observation := NewRegistry(PID(), Session(), Heartbeat()).Check(context.Background(), reference, time.Now())
	if observation.Liveness != Alive {
		t.Fatalf("heartbeat with irrelevant process identity = %s, want alive", observation.Liveness)
	}
}
