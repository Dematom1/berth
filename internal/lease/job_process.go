package lease

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	ownerpkg "github.com/Dematom1/berth/internal/owner"
)

type processResource struct{}

func childCompletion(observed <-chan error, pid int) (bool, error) {
	select {
	case err := <-observed:
		if err == nil {
			return true, nil
		}
	default:
	}
	return childExited(pid)
}

func groupAlive(ctx context.Context, pgid int) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pgid=", "-o", "stat=").Output()
	if err != nil {
		return false, errors.New("cannot verify process group; cleanup record retained")
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == strconv.Itoa(pgid) && !strings.HasPrefix(fields[1], "Z") {
			return true, nil
		}
	}
	return false, nil
}

func (processResource) Destroy(ctx context.Context, l Lease) error {
	if l.Reservation == nil {
		return errors.New("process lease has no job record")
	}
	leader := l.Reservation.Leader
	if leader.ID == "" {
		return nil
	} // Reserved but never started.
	pid, err := strconv.Atoi(leader.ID)
	if err != nil || pid <= 1 {
		return errors.New("invalid job process identity")
	}
	identity, live := ownerpkg.ProcessIdentity(pid)
	if live == ownerpkg.Unknown || ((live == ownerpkg.Alive || identity != "") && identity != leader.Identity) {
		return errors.New("job group identity cannot be verified; lease retained")
	}
	if live == ownerpkg.Dead && identity == "" {
		fmt.Fprintln(os.Stderr, "berth: job leader is gone; releasing lease without signaling an unverifiable process group")
		return nil
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		alive, err := groupAlive(ctx, pid)
		if err != nil || !alive {
			return err
		}
		if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		until := time.Now().Add(time.Second)
		for time.Now().Before(until) {
			alive, err := groupAlive(ctx, pid)
			if err != nil || !alive {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
	return errors.New("job processes survived cleanup; lease retained")
}

func stopJob(l Lease, cmd *exec.Cmd) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := (processResource{}).Destroy(ctx, l); err != nil {
		fmt.Fprintln(os.Stderr, "berth: group cleanup incomplete; sweep will retry:", err)
		cmd.Process.Kill()
		return err
	}
	return nil
}
