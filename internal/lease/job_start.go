package lease

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// The helper cannot run user code until its group identity is durable. If the
// parent dies before committing, EOF on fd 3 makes the helper exit instead.
func Exec(args []string) int {
	gate := os.NewFile(3, "job-start")
	if gate == nil || len(args) == 0 {
		return 125
	}
	var b [1]byte
	n, err := gate.Read(b[:])
	gate.Close()
	if err != nil || n != 1 || b[0] != 1 {
		return 125
	}
	if err := syscall.Exec(args[0], args, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "berth: cannot execute child:", err)
		return 126
	}
	return 126
}

func (m *Manager) startJob(ctx context.Context, dir string, l *Lease, args []string, out io.Writer, input io.Reader) (*exec.Cmd, <-chan error, error) {
	s, err := m.openStore(ctx, dir)
	if err != nil {
		return nil, nil, err
	}
	defer s.Close()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	current, ok := s.Leases[l.ID]
	if !ok || !time.Now().Before(current.Expires) {
		return nil, nil, errors.New("job reservation expired or was released before start")
	}
	gate, start, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	defer gate.Close()
	defer start.Close()
	bin, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(bin, append([]string{"__job_exec"}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.ExtraFiles = []*os.File{gate}
	cmd.Env = append(os.Environ(), "BERTH_LEASE="+l.ID)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, out, os.Stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	leader, err := m.options.Owners.Bind(context.Background(), "pid:"+strconv.Itoa(cmd.Process.Pid), 0, time.Now())
	if err == nil {
		current.Reservation.Leader = leader
		l.Reservation.Leader = leader
		s.Leases[l.ID] = current
		err = s.Save()
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && !time.Now().Before(current.Expires) {
		err = errors.New("job reservation expired before child start")
	}
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, nil, err
	}
	observed := make(chan error, 1)
	go func() { observed <- waitChildExit(cmd.Process.Pid) }()
	if _, err := start.Write([]byte{1}); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, nil, err
	}
	return cmd, observed, nil
}
