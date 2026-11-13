package owner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// execOptIn names the settings that register the exec owner kind.
const execOptIn = `set DIBS_EXEC_OWNER=1 or "exec_owner": true in the user config`

// ExecTimeout bounds every exec owner probe.
const ExecTimeout = 10 * time.Second

// probe runs a caller-supplied liveness command: exit 0 is alive, 1-125 dead,
// and anything that is not a clean answer (timeout, signal, 126 cannot execute,
// 127 not found, or a failed start) is unknown, which never permits reclaim.
func probe(ctx context.Context, command string, timeout time.Duration) Liveness {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	// The probe's own group lets a timeout stop everything it started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	cmd.Dir = "/"
	err := cmd.Run()
	if ctx.Err() != nil {
		return Unknown
	}
	if err == nil {
		return Alive
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return Unknown
	}
	if code := exit.ExitCode(); code >= 1 && code <= 125 {
		return Dead
	}
	return Unknown
}

// Exec follows a liveness command any tool supplies, so dibs never needs to
// know what the owner is.
func Exec(timeout time.Duration) Definition {
	return Definition{Name: "exec",
		Bind: func(ctx context.Context, value string, _ time.Duration, _ time.Time) (Reference, error) {
			command := strings.TrimPrefix(value, "exec:")
			if strings.TrimSpace(command) == "" || strings.ContainsRune(command, 0) {
				return Reference{}, errors.New("exec owner must be exec:<command>, a shell command that exits 0 while the owner is alive")
			}
			switch live := probe(ctx, command, timeout); live {
			case Alive:
				return Reference{Kind: "exec", ID: command}, nil
			case Dead:
				return Reference{}, fmt.Errorf("exec owner probe %q exited nonzero; only a live owner can own a lease", command)
			default:
				return Reference{}, fmt.Errorf("exec owner probe %q timed out after %s or could not run; liveness is unknown", command, timeout)
			}
		},
		Check: func(ctx context.Context, r Reference, _ time.Time) Observation {
			if strings.TrimSpace(r.ID) == "" {
				return Observation{Liveness: Unknown}
			}
			return Observation{Liveness: probe(ctx, r.ID, timeout)}
		},
	}
}
