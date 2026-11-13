package owner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecProbeOutcomes(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	for _, tc := range []struct {
		name, command string
		want          Liveness
	}{
		{"exit 0 is alive", "true", Alive},
		{"exit 1 is dead", "false", Dead},
		{"exit 125 is dead", "exit 125", Dead},
		{"cannot execute is unknown", "exit 126", Unknown},
		{"not found is unknown", "/nonexistent/dibs-probe", Unknown},
		{"signal is unknown", "kill -TERM $$", Unknown},
		{"timeout is unknown", "touch " + marker + "; sleep 30", Unknown},
		{"probe runs from /", `test "$(pwd)" = /`, Alive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			if got := probe(context.Background(), tc.command, 300*time.Millisecond); got != tc.want {
				t.Fatalf("probe %q = %s, want %s", tc.command, got, tc.want)
			}
			if time.Since(start) > 3*time.Second {
				t.Fatalf("probe %q outlived its timeout", tc.command)
			}
		})
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("timeout case never started its command:", err)
	}
}

func TestExecOwnerBindAndCheck(t *testing.T) {
	registry := NewRegistry(PID(), Session(), Heartbeat(), Exec(time.Second))
	alive := filepath.Join(t.TempDir(), "alive")
	if err := os.WriteFile(alive, nil, 0600); err != nil {
		t.Fatal(err)
	}
	command := "test -e " + alive
	ref, err := registry.Bind(context.Background(), "exec:"+command, 0, time.Now())
	if err != nil || ref.Kind != "exec" || ref.ID != command || ref.String() != "exec:"+command {
		t.Fatalf("bind = %+v, %v", ref, err)
	}
	if got := registry.Check(context.Background(), ref, time.Now()).Liveness; got != Alive {
		t.Fatalf("check while file exists = %s", got)
	}
	if err := os.Remove(alive); err != nil {
		t.Fatal(err)
	}
	if got := registry.Check(context.Background(), ref, time.Now()).Liveness; got != Dead {
		t.Fatalf("check after removal = %s", got)
	}
	for value, want := range map[string]string{
		"exec:false":                 "exited nonzero",
		"exec:/nonexistent/dibs-bin": "liveness is unknown",
		"exec:   ":                   "exec:<command>",
	} {
		if _, err := registry.Bind(context.Background(), value, 0, time.Now()); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("bind %q = %v, want %q", value, err, want)
		}
	}
}

func TestExecOwnerOff(t *testing.T) {
	registry := NewRegistry(PID(), Session(), Heartbeat())
	if _, err := registry.Bind(context.Background(), "exec:true", 0, time.Now()); err == nil || !strings.Contains(err.Error(), "DIBS_EXEC_OWNER=1") {
		t.Fatalf("bind with exec off = %v", err)
	}
	if got := registry.Check(context.Background(), Reference{Kind: "exec", ID: "true"}, time.Now()).Liveness; got != Unknown {
		t.Fatalf("recorded exec lease with the owner off = %s, want unknown", got)
	}
}
