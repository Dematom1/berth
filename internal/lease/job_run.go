package lease

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

func (m *Manager) RunJob(ctx context.Context, reservation Reservation, child []string, input io.Reader, out io.Writer) JobResult {
	l, dir := reservation.lease, reservation.directory
	if l.ID == "" || dir == "" {
		return JobResult{Code: 1, Err: errors.New("invalid job reservation")}
	}

	finish, groupCleaned := true, false
	defer func() {
		if finish {
			m.finishJob(dir, l, groupCleaned)
		}
	}()
	cmd, observed, err := m.startJob(ctx, dir, &l, child, out, input)
	if err != nil {
		return JobResult{Code: 1, Err: err}
	}
	code := func() int {
		timer := time.NewTimer(time.Until(l.Expires))
		defer timer.Stop()
		cleanup := func() {
			if err := stopJob(l, cmd); err != nil {
				finish = false
			} else {
				groupCleaned = true
			}
		}
		var waitErr error
		select {
		case err := <-observed:
			if err != nil {
				fmt.Fprintln(os.Stderr, "berth: cannot observe job exit without reaping; sweep will retry:", err)
				finish = false
				cmd.Process.Kill()
			} else {
				cleanup()
			}
			waitErr = cmd.Wait()
		case <-timer.C:
			completed, err := childCompletion(observed, cmd.Process.Pid)
			if err != nil {
				fmt.Fprintln(os.Stderr, "berth: cannot verify job completion at deadline; sweep will retry:", err)
				finish = false
				cmd.Process.Kill()
				waitErr = cmd.Wait()
				return 124
			}
			cleanup()
			waitErr = cmd.Wait()
			if !completed {
				return 124
			}
		case <-ctx.Done():
			cleanup()
			waitErr = cmd.Wait()
		}
		if waitErr == nil {
			return 0
		}
		if cmd.ProcessState == nil {
			return 125
		}
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		code := cmd.ProcessState.ExitCode()
		if code < 0 {
			return 125
		}
		return code
	}()
	return JobResult{Code: code, Started: true}
}
