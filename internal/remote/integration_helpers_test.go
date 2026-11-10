//go:build sshintegration

package remote

import (
	"os/exec"
	"testing"
	"time"
)

const version = "restructure-test"

func childExit(t *testing.T, cmd *exec.Cmd, want int) string {
	t.Helper()
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	t.Logf("command=%q exit=%d output=%q", cmd.Args, code, out)
	if code != want {
		t.Fatalf("%v exited %d want %d: %s", cmd.Args, code, want, out)
	}
	return string(out)
}

func awaitJob(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for job state")
}
