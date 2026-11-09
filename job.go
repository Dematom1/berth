package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type JobResource struct {
	Budget Budget `json:"budget"`
	Class  string `json:"class"`
	Leader Owner  `json:"leader"`
}

type JobWaiter struct {
	ID      string    `json:"id"`
	Machine string    `json:"machine"`
	Owner   Owner     `json:"owner"`
	Expires time.Time `json:"expires"`
}

func budgetFits(s *Store, host string, capacity, request Budget) bool {
	for _, l := range s.Leases {
		if l.Machine != host || l.Job == nil {
			continue
		}
		capacity.Cores = max(0, capacity.Cores-l.Job.Budget.Cores)
		capacity.Memory = max(0, capacity.Memory-l.Job.Budget.Memory)
	}
	return request.Cores <= capacity.Cores && request.Memory <= capacity.Memory
}

func pruneWaiters(s *Store, host string, now time.Time) {
	// ponytail: linear queue scans poll owner PIDs; add event-driven wakeups only if queue size makes this costly.
	kept := s.Waiters[:0]
	for _, w := range s.Waiters {
		if w.Machine != host || (now.Before(w.Expires) && checkOwner(w.Owner, now) != Dead) {
			kept = append(kept, w)
		}
	}
	s.Waiters = kept
}

func removeWaiter(s *Store, id string) {
	for i, w := range s.Waiters {
		if w.ID == id {
			s.Waiters = append(s.Waiters[:i], s.Waiters[i+1:]...)
			return
		}
	}
}

var errNoCapacity = errors.New("no slot freed within --wait")

func admitJob(ctx context.Context, dir string, l Lease, capacity Budget, wait time.Duration) (Lease, error) {
	deadline := time.Now().Add(wait)
	lockCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	queued := false
	defer func() {
		if !queued {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if s, err := openStore(cleanupCtx, dir); err == nil {
			defer s.Close()
			removeWaiter(s, l.ID)
			if err := s.Save(); err != nil {
				fmt.Fprintln(os.Stderr, "berth: waiter cleanup failed; it expires automatically:", err)
			}
		}
	}()
	for {
		s, err := openStore(lockCtx, dir)
		if err != nil {
			if ctx.Err() == nil && lockCtx.Err() != nil {
				return l, errNoCapacity
			}
			return l, err
		}
		now := time.Now().UTC()
		previousCount := len(s.Waiters)
		pruneWaiters(s, l.Machine, now)
		changed := len(s.Waiters) != previousCount
		if ctx.Err() != nil || (wait > 0 && !now.Before(deadline)) {
			s.Close()
			if ctx.Err() != nil {
				return l, ctx.Err()
			}
			return l, errNoCapacity
		}
		if !queued && wait > 0 {
			s.Waiters = append(s.Waiters, JobWaiter{l.ID, l.Machine, l.Owner, deadline})
			queued, changed = true, true
		}
		for _, id := range s.IDs() {
			existing := s.Leases[id]
			if existing.Job != nil {
				if eligible, _ := reclaimable(existing, l.Machine, now, checkOwner); eligible {
					if _, err := releaseLease(lockCtx, s, id, l.Machine, adapters()); err != nil {
						fmt.Fprintln(os.Stderr, "berth: retaining job reservation after cleanup failure:", err)
					}
				}
			}
		}
		now = time.Now().UTC()
		if ctx.Err() != nil || (wait > 0 && !now.Before(deadline)) {
			s.Close()
			if ctx.Err() != nil {
				return l, ctx.Err()
			}
			return l, errNoCapacity
		}
		first := true
		for _, w := range s.Waiters {
			if w.Machine == l.Machine {
				first = queued && w.ID == l.ID && now.Before(w.Expires)
				break
			}
		}
		if first && budgetFits(s, l.Machine, capacity, l.Job.Budget) {
			removeWaiter(s, l.ID)
			l.Created, l.Expires = now, now.Add(l.TTL)
			s.Leases[l.ID] = l
			err = s.Save()
			s.Close()
			if err == nil {
				queued = false
			}
			return l, err
		}
		if changed {
			err = s.Save()
		}
		s.Close()
		if err != nil {
			return l, err
		}
		if wait == 0 {
			return l, errNoCapacity
		}
		select {
		case <-lockCtx.Done():
			if ctx.Err() != nil {
				return l, ctx.Err()
			}
			return l, errNoCapacity
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// The helper cannot run user code until its group identity is durable. If the
// parent dies before committing, EOF on fd 3 makes the helper exit instead.
func jobExec(args []string) int {
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

func startJob(ctx context.Context, dir string, l *Lease, args []string, out io.Writer) (*exec.Cmd, <-chan error, error) {
	s, err := openStore(ctx, dir)
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
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, os.Stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	leader, err := parseOwner("pid:"+strconv.Itoa(cmd.Process.Pid), 0, time.Now())
	if err == nil {
		current.Job.Leader = leader
		l.Job.Leader = leader
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

type processResource struct{}

func (processResource) Create(context.Context, Lease) error { return nil }

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
	if l.Job == nil {
		return errors.New("process lease has no job record")
	}
	leader := l.Job.Leader
	if leader.ID == "" {
		return nil
	} // Reserved but never started.
	pid, err := strconv.Atoi(leader.ID)
	if err != nil || pid <= 1 {
		return errors.New("invalid job process identity")
	}
	identity, live := processIdentity(pid)
	if live == Unknown || ((live == Alive || identity != "") && identity != leader.Start) {
		return errors.New("job group identity cannot be verified; lease retained")
	}
	if live == Dead && identity == "" {
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

func finishJob(dir string, l Lease, groupCleaned bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := openStore(ctx, dir)
	if err == nil {
		defer s.Close()
		if groupCleaned {
			if _, ok := s.Leases[l.ID]; ok {
				delete(s.Leases, l.ID)
				err = s.Save()
				if err != nil {
					s.Leases[l.ID] = l
				}
			}
		} else {
			_, err = releaseLease(ctx, s, l.ID, l.Machine, adapters())
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "berth: job cleanup incomplete; retry berth sweep --confirm:", err)
	}
}

func runJob(ctx context.Context, args []string, out io.Writer, jsonOutput bool) int {
	fs := flag.NewFlagSet("job", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	wantHelp := fs.Bool("help", false, "help")
	fs.BoolVar(&jsonOutput, "json", jsonOutput, "structured errors; child output is unchanged")
	cores := fs.Int("cores", 0, "requested logical cores")
	memory := fs.String("memory", "", "requested memory")
	class := fs.String("class", "default", "resource class")
	timeout := fs.Duration("timeout", 0, "job deadline")
	wait := fs.Duration("wait", 30*time.Minute, "admission wait")
	// Unlike lease commands, everything after -- is opaque child argv.
	split := -1
	for i, a := range args {
		if a == "--" {
			split = i
			break
		}
	}
	options := args
	if split >= 0 {
		options = args[:split]
	}
	usageError := func(err error) int { return emitError(out, jsonOutput, 2, err.Error(), help("job")) }
	if err := fs.Parse(options); err != nil {
		return usageError(err)
	}
	if *wantHelp {
		fmt.Fprint(out, help("job"))
		return 0
	}
	if fs.NArg() != 0 || split < 0 || split == len(args)-1 || *wait < 0 {
		return usageError(errors.New("use berth job [flags] -- <cmd>; --wait must be nonnegative"))
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	config, err := loadJobConfig()
	if err != nil {
		return usageError(err)
	}
	capacity, err := config.available()
	if err != nil {
		return usageError(err)
	}
	request, duration, err := config.class(*class)
	if err != nil {
		return usageError(err)
	}
	if explicit["cores"] {
		request.Cores = *cores
	}
	if explicit["memory"] {
		request.Memory, err = memoryBytes(*memory)
		if err != nil {
			return usageError(err)
		}
	}
	if explicit["timeout"] {
		duration = *timeout
	}
	if request.Cores <= 0 || request.Memory <= 0 || duration <= 0 || request.Cores > capacity.Cores || request.Memory > capacity.Memory {
		return usageError(errors.New("request must be positive and fit machine budget minus owner room; adjust flags or BERTH_CONFIG"))
	}
	child := append([]string(nil), args[split+1:]...)
	child[0], err = exec.LookPath(child[0])
	if err != nil {
		return emitError(out, jsonOutput, 1, "child executable not found; no child started", "Check the command and PATH, then retry berth job")
	}
	owner, err := parseOwner("pid:"+strconv.Itoa(os.Getpid()), 0, time.Now())
	if err != nil {
		return emitError(out, jsonOutput, 1, err.Error(), "Retry berth job when the process is inspectable")
	}
	l, err := newLease(owner, duration, filepath.Base(child[0]), nil, time.Now().UTC())
	if err != nil {
		return emitError(out, jsonOutput, 1, err.Error(), "Retry berth job")
	}
	l.Resource.Adapter = "process"
	l.Job = &JobResource{Budget: request, Class: *class}
	dir, err := stateDir()
	if err != nil {
		return emitError(out, jsonOutput, 1, err.Error(), "Set BERTH_STATE to a private writable directory")
	}
	l, err = admitJob(ctx, dir, l, capacity, *wait)
	if err != nil {
		code := 1
		if errors.Is(err, errNoCapacity) {
			code = 75
		}
		return emitError(out, jsonOutput, code, err.Error(), "Run berth ps to inspect reservations; retry berth job with --wait")
	}
	finish, groupCleaned := true, false
	defer func() {
		if finish {
			finishJob(dir, l, groupCleaned)
		}
	}()
	cmd, observed, err := startJob(ctx, dir, &l, child, out)
	if err != nil {
		return emitError(out, jsonOutput, 1, err.Error(), "Run berth ps to inspect retained reservations")
	}
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
		default:
			cleanup()
			waitErr = cmd.Wait()
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
}
