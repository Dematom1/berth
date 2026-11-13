// Package shell leases running shells, with their helper processes, by PID and start time.
package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
)

// Process is one row of a process table snapshot.
type Process struct {
	PID, PPID, PGID, TPGID int
	Stat, TTY, Start, Name string
}

// Lister snapshots the process table; an error means the table is unknown.
type Lister func(context.Context) (map[int]Process, error)

type Options struct {
	List Lister
	// Grace is how long each signal gets before the next, stronger one.
	Grace time.Duration
	// Log receives one line per cleanup step.
	Log io.Writer
	// Self is the PID whose ancestry cleanup must never signal.
	Self int
	// TTYTimes reports a terminal's last input and output.
	TTYTimes func(string) (time.Time, error)
}

var shells = map[string]bool{"sh": true, "ash": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "mksh": true, "oksh": true, "fish": true, "tcsh": true, "csh": true, "yash": true, "nu": true, "xonsh": true, "elvish": true, "pwsh": true}

// IsShell reports whether a process name is a shell, including login shells named -zsh.
func IsShell(name string) bool {
	return shells[filepath.Base(strings.TrimPrefix(name, "-"))]
}

// PS reads the process table through ps with the same start-time format the pid owner pins.
func PS(ctx context.Context) (map[int]Process, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=", "-o", "ppid=", "-o", "pgid=", "-o", "tpgid=", "-o", "stat=", "-o", "tty=", "-o", "lstart=", "-o", "comm=")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New("cannot read the process table; shell lease retained")
	}
	return parse(string(out))
}

func parse(out string) (map[int]Process, error) {
	procs := map[int]Process{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) < 12 {
			return nil, errors.New("malformed process table; shell lease retained")
		}
		var n [4]int
		for i := range n {
			v, err := strconv.Atoi(f[i])
			if err != nil {
				return nil, errors.New("malformed process table; shell lease retained")
			}
			n[i] = v
		}
		procs[n[0]] = Process{PID: n[0], PPID: n[1], PGID: n[2], TPGID: n[3], Stat: f[4], TTY: f[5], Start: strings.Join(f[6:11], " "), Name: strings.Join(f[11:], " ")}
	}
	return procs, nil
}

// TTYTimes returns the later of a terminal device's last read and last write.
func TTYTimes(tty string) (time.Time, error) {
	var st unix.Stat_t
	if err := unix.Stat("/dev/"+tty, &st); err != nil {
		return time.Time{}, err
	}
	a, m := time.Unix(st.Atim.Unix()), time.Unix(st.Mtim.Unix())
	if m.After(a) {
		return m, nil
	}
	return a, nil
}

func live(p Process, ok bool, start string) bool {
	return ok && p.Start == start && !strings.HasPrefix(p.Stat, "Z")
}

func hasTTY(p Process) bool { return p.TTY != "" && p.TTY != "?" && p.TTY != "??" }

// busy names a foreground job: a process group other than the shell's own holding its terminal.
func busy(procs map[int]Process, shell Process) string {
	if !hasTTY(shell) || shell.TPGID <= 0 || shell.TPGID == shell.PGID {
		return ""
	}
	name := "unknown command"
	if leader, ok := procs[shell.TPGID]; ok {
		name = leader.Name
	}
	return fmt.Sprintf("shell %d has a foreground job (process group %d: %s)", shell.PID, shell.TPGID, name)
}

type adapter struct{ o Options }

// members returns every process the lease covers that is still running: the
// pinned shell and recorded helpers whose start times match, every descendant of
// those, and anything seen earlier in this cleanup (known) that still matches.
func members(procs map[int]Process, ref *adapterpkg.ProcessRef, known map[int]string) map[int]Process {
	found := map[int]Process{}
	add := func(pid int, start string) {
		if p, ok := procs[pid]; live(p, ok, start) {
			found[pid] = p
			known[pid] = start
		}
	}
	add(ref.PID, ref.Start)
	for _, m := range ref.Members {
		add(m.PID, m.Start)
	}
	for pid, start := range known {
		add(pid, start)
	}
	for grew := true; grew; {
		grew = false
		for _, p := range procs {
			if _, in := found[p.PID]; !in && !strings.HasPrefix(p.Stat, "Z") {
				if _, parent := found[p.PPID]; parent && p.PID > 1 {
					found[p.PID], known[p.PID], grew = p, p.Start, true
				}
			}
		}
	}
	return found
}

func sorted(found map[int]Process) []int {
	pids := make([]int, 0, len(found))
	for pid := range found {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids
}

// withoutSelf drops dibs's own process and its children when they run inside
// the shell, which they do only while dibs does: walking up from a member, it
// is dropped when it reaches dibs before it reaches the shell.
func withoutSelf(found, procs map[int]Process, shell, self int) map[int]Process {
	for pid := range found {
		for p := pid; p > 1 && p != shell; p = procs[p].PPID {
			if p == self {
				delete(found, pid)
				break
			}
		}
	}
	return found
}

func describe(found map[int]Process) []string {
	out := []string{}
	for _, pid := range sorted(found) {
		out = append(out, strconv.Itoa(pid)+" "+found[pid].Name)
	}
	return out
}

func (a adapter) Create(context.Context, adapterpkg.Target) error {
	return errors.New("dibs does not start shells; lease a running shell with dibs adopt shell <pid>")
}

func (a adapter) logf(ref *adapterpkg.ProcessRef, format string, args ...any) {
	fmt.Fprintf(a.o.Log, "dibs: shell %d: %s\n", ref.PID, fmt.Sprintf(format, args...))
}

// Destroy hangs up the shell and its helpers, escalates to SIGTERM and then
// SIGKILL only after a grace period each, and succeeds only once every member is gone.
func (a adapter) Destroy(ctx context.Context, t adapterpkg.Target) error {
	ref := t.Ref.Process
	if ref == nil {
		return errors.New("shell lease has no process identity; lease retained")
	}
	procs, err := a.o.List(ctx)
	if err != nil {
		return err
	}
	known := map[int]string{}
	found := members(procs, ref, known)
	if len(found) == 0 {
		a.logf(ref, "already gone")
		return nil
	}
	for pid := a.o.Self; pid > 1; pid = procs[pid].PPID {
		if _, in := found[pid]; in {
			return fmt.Errorf("dibs is running inside shell %d; release it from another shell; lease retained", ref.PID)
		}
	}
	if shell, ok := procs[ref.PID]; live(shell, ok, ref.Start) {
		if why := busy(procs, shell); why != "" {
			return fmt.Errorf("%s; lease retained: %w", why, adapterpkg.ErrBusy)
		}
	}
	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGTERM, syscall.SIGKILL} {
		procs, err = a.o.List(ctx)
		if err != nil {
			return err
		}
		found = members(procs, ref, known)
		if len(found) == 0 {
			a.logf(ref, "verified gone")
			return nil
		}
		a.logf(ref, "%s to %s", sigName(sig), strings.Join(describe(found), ", "))
		for pid, p := range found {
			if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("cannot signal %d %s: %w; lease retained", pid, p.Name, err)
			}
		}
		deadline := time.Now().Add(a.o.Grace)
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
			if procs, err = a.o.List(ctx); err != nil {
				return err
			}
			if found = members(procs, ref, known); len(found) == 0 {
				a.logf(ref, "verified gone")
				return nil
			}
			if !time.Now().Before(deadline) {
				break
			}
		}
		a.logf(ref, "%d still running after %s", len(found), a.o.Grace)
	}
	return fmt.Errorf("shell processes survived SIGKILL (%s); lease retained", strings.Join(describe(found), ", "))
}

func sigName(sig syscall.Signal) string {
	return map[syscall.Signal]string{syscall.SIGHUP: "SIGHUP", syscall.SIGTERM: "SIGTERM", syscall.SIGKILL: "SIGKILL"}[sig]
}

func (a adapter) lookup(ctx context.Context, name string) (Process, map[int]Process, error) {
	pid, err := strconv.Atoi(name)
	if err != nil || pid <= 1 || strconv.Itoa(pid) != name {
		return Process{}, nil, errors.New("a shell lease names the shell's PID")
	}
	procs, err := a.o.List(ctx)
	if err != nil {
		return Process{}, nil, err
	}
	p, ok := procs[pid]
	if !ok || strings.HasPrefix(p.Stat, "Z") {
		return Process{}, procs, nil
	}
	if !IsShell(p.Name) {
		return Process{}, nil, fmt.Errorf("process %d is %s, not a shell", pid, p.Name)
	}
	return p, procs, nil
}

func (a adapter) activity(ctx context.Context, t adapterpkg.Target) (adapterpkg.Activity, error) {
	ref := t.Ref.Process
	if ref == nil {
		return adapterpkg.Activity{}, errors.New("shell lease has no process identity")
	}
	procs, err := a.o.List(ctx)
	if err != nil {
		return adapterpkg.Activity{}, err
	}
	activity := adapterpkg.Activity{Members: describe(withoutSelf(members(procs, ref, map[int]string{}), procs, ref.PID, a.o.Self))}
	shell, ok := procs[ref.PID]
	if !live(shell, ok, ref.Start) {
		return activity, nil
	}
	if activity.Busy = busy(procs, shell); activity.Busy != "" {
		activity.IdleKnown = true
		return activity, nil
	}
	if hasTTY(shell) {
		if last, err := a.o.TTYTimes(shell.TTY); err == nil {
			activity.Idle, activity.IdleKnown = max(0, time.Since(last)), true
		}
	}
	return activity, nil
}

func New(o Options) adapterpkg.Definition {
	a := adapter{o}
	return adapterpkg.Definition{Name: "shell", Adapter: a, HostScoped: true,
		// Discover answers only for the named PID: unleased shells are never inventoried.
		Discover: func(ctx context.Context, r adapterpkg.ResourceRef) ([]map[string]any, error) {
			p, _, err := a.lookup(ctx, r.Name)
			if err != nil || p.PID == 0 {
				return nil, err
			}
			return []map[string]any{{"type": "shell", "name": r.Name, "pid": p.PID, "start": p.Start, "command": p.Name, "tty": p.TTY}}, nil
		},
		Prepare: func(ctx context.Context, r adapterpkg.PrepareRequest) (adapterpkg.ResourceRef, error) {
			p, procs, err := a.lookup(ctx, r.Existing.Name)
			if err != nil {
				return adapterpkg.ResourceRef{}, err
			}
			if p.PID == 0 || !r.Existing.Adopted {
				return adapterpkg.ResourceRef{}, errors.New("a shell enters a lease only through dibs adopt shell <pid> while it runs")
			}
			ref := &adapterpkg.ProcessRef{PID: p.PID, Start: p.Start}
			found := withoutSelf(members(procs, ref, map[int]string{}), procs, ref.PID, a.o.Self)
			for _, pid := range sorted(found) {
				if pid != p.PID {
					ref.Members = append(ref.Members, adapterpkg.ProcessMember{PID: pid, Start: found[pid].Start, Command: found[pid].Name})
				}
			}
			return adapterpkg.ResourceRef{Adapter: "shell", Name: r.Existing.Name, Adopted: true, Process: ref}, nil
		},
		Validate: func(_ string, _ adapterpkg.Metadata, ref adapterpkg.ResourceRef) error {
			p := ref.Process
			if p == nil || !ref.Adopted || ref.Pending || p.PID <= 1 || p.Start == "" || ref.Name != strconv.Itoa(p.PID) || ref.Context != "" || ref.DockerHost != "" || ref.File != "" {
				return errors.New("invalid shell resource identity")
			}
			for _, m := range p.Members {
				if m.PID <= 1 || m.Start == "" {
					return errors.New("invalid shell member identity")
				}
			}
			return nil
		},
		Env: func(adapterpkg.ResourceRef) adapterpkg.Environment { return adapterpkg.Environment{} },
		Exists: func(ctx context.Context, t adapterpkg.Target) (bool, error) {
			if t.Ref.Process == nil {
				return false, errors.New("shell lease has no process identity")
			}
			procs, err := a.o.List(ctx)
			if err != nil {
				return false, err
			}
			p, ok := procs[t.Ref.Process.PID]
			return live(p, ok, t.Ref.Process.Start), nil
		},
		Activity: a.activity,
	}
}
