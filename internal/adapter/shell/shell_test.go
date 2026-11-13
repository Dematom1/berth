package shell

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
)

const start = "Thu Nov 12 10:00:00 2026"

func fixed(procs ...Process) Lister {
	return func(context.Context) (map[int]Process, error) {
		m := map[int]Process{}
		for _, p := range procs {
			if p.Start == "" {
				p.Start = start
			}
			m[p.PID] = p
		}
		return m, nil
	}
}

func target(pid int, members ...adapterpkg.ProcessMember) adapterpkg.Target {
	return adapterpkg.Target{Ref: adapterpkg.ResourceRef{Adapter: "shell", Name: strconv.Itoa(pid), Adopted: true, Process: &adapterpkg.ProcessRef{PID: pid, Start: start, Members: members}}}
}

func definition(list Lister, log *bytes.Buffer) adapterpkg.Definition {
	return New(Options{List: list, Grace: 200 * time.Millisecond, Log: log, Self: 1, TTYTimes: func(string) (time.Time, error) { return time.Now().Add(-time.Hour), nil }})
}

func TestParseProcessTable(t *testing.T) {
	procs, err := parse("  501   1 501  0 Ss   ??       Thu Nov 12 10:00:00 2026     /bin/zsh\n  502 501 502 502 S+  ttys003  Thu Nov 12 10:00:01 2026     -zsh\n 503 502 503 502 S pts/1 Thu Nov 12 10:00:02 2026 my helper\n")
	if err != nil {
		t.Fatal(err)
	}
	want := Process{PID: 503, PPID: 502, PGID: 503, TPGID: 502, Stat: "S", TTY: "pts/1", Start: "Thu Nov 12 10:00:02 2026", Name: "my helper"}
	if procs[503] != want || procs[502].Name != "-zsh" || len(procs) != 3 {
		t.Fatalf("parsed %+v", procs)
	}
	if _, err := parse("501 1 x 0 Ss ?? Thu Nov 12 10:00:00 2026 zsh"); err == nil {
		t.Fatal("malformed table parsed")
	}
	for name, want := range map[string]bool{"-zsh": true, "/bin/bash": true, "zsh": true, "fish": true, "gitstatusd": false, "/usr/bin/python3": false} {
		if IsShell(name) != want {
			t.Fatalf("IsShell(%q) = %v", name, !want)
		}
	}
}

func TestReclaimGuards(t *testing.T) {
	shell := Process{PID: 500, PPID: 1, PGID: 500, TPGID: 500, Stat: "Ss+", TTY: "ttys001", Name: "-zsh"}
	for _, tc := range []struct {
		name  string
		procs []Process
		self  int
		want  string
	}{
		{"foreground job", []Process{{PID: 500, PPID: 1, PGID: 500, TPGID: 600, Stat: "Ss", TTY: "ttys001", Name: "-zsh"}, {PID: 600, PPID: 500, PGID: 600, TPGID: 600, Stat: "S+", TTY: "ttys001", Name: "go"}}, 1, "foreground job (process group 600: go)"},
		{"dibs inside the shell", []Process{shell, {PID: 700, PPID: 500, PGID: 700, TPGID: 500, Stat: "S", TTY: "ttys001", Name: "dibs"}}, 700, "dibs is running inside shell 500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log bytes.Buffer
			d := New(Options{List: fixed(tc.procs...), Grace: time.Millisecond, Log: &log, Self: tc.self, TTYTimes: TTYTimes})
			if err := d.Adapter.Destroy(context.Background(), target(500)); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "lease retained") {
				t.Fatalf("destroy = %v, want refusal %q", err, tc.want)
			}
			if strings.Contains(log.String(), "SIG") {
				t.Fatalf("refused cleanup signaled: %s", log.String())
			}
		})
	}
	t.Run("unreadable process table", func(t *testing.T) {
		d := definition(func(context.Context) (map[int]Process, error) { return nil, errors.New("ps failed") }, &bytes.Buffer{})
		if err := d.Adapter.Destroy(context.Background(), target(500)); err == nil {
			t.Fatal("destroy without a process table succeeded")
		}
		if _, err := d.Activity(context.Background(), target(500)); err == nil {
			t.Fatal("activity without a process table is known")
		}
	})
	t.Run("PID reused after initial snapshot is never signaled", func(t *testing.T) {
		calls := 0
		var log bytes.Buffer
		old := shell
		old.Start = start
		list := func(context.Context) (map[int]Process, error) {
			calls++
			p := old
			if calls > 1 {
				p.Start = "Fri Nov 13 09:00:00 2026"
			}
			return map[int]Process{p.PID: p}, nil
		}
		d := definition(list, &log)
		if err := d.Adapter.Destroy(context.Background(), target(500)); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(log.String(), "SIGHUP") || !strings.Contains(log.String(), "verified gone") {
			t.Fatalf("reused PID was signaled: %s", log.String())
		}
	})
	t.Run("reused PID is gone, never signaled", func(t *testing.T) {
		var log bytes.Buffer
		reused := shell
		reused.Start = "Fri Nov 13 09:00:00 2026"
		helper := adapterpkg.ProcessMember{PID: 501, Start: start, Command: "gitstatusd"}
		d := definition(fixed(reused, Process{PID: 501, PPID: 1, PGID: 501, Stat: "S", TTY: "??", Start: "Fri Nov 13 09:00:00 2026", Name: "other"}), &log)
		if err := d.Adapter.Destroy(context.Background(), target(500, helper)); err != nil || !strings.Contains(log.String(), "already gone") {
			t.Fatalf("destroy = %v, log %q", err, log.String())
		}
		if found, err := d.Exists(context.Background(), target(500)); err != nil || found {
			t.Fatalf("exists for reused PID = %v, %v", found, err)
		}
	})
}

func TestActivity(t *testing.T) {
	shell := Process{PID: 500, PPID: 1, PGID: 500, TPGID: 500, Stat: "Ss+", TTY: "ttys001", Name: "-zsh"}
	helper := Process{PID: 501, PPID: 500, PGID: 500, TPGID: 500, Stat: "S", TTY: "ttys001", Name: "gitstatusd"}
	a, err := definition(fixed(shell, helper), &bytes.Buffer{}).Activity(context.Background(), target(500))
	if err != nil || a.Busy != "" || !a.IdleKnown || a.Idle < 59*time.Minute || strings.Join(a.Members, ",") != "500 -zsh,501 gitstatusd" {
		t.Fatalf("idle shell activity = %+v, %v", a, err)
	}
	job := Process{PID: 600, PPID: 500, PGID: 600, TPGID: 600, Stat: "S+", TTY: "ttys001", Name: "make"}
	shell.TPGID = 600
	if a, _ := definition(fixed(shell, job), &bytes.Buffer{}).Activity(context.Background(), target(500)); a.Busy == "" || a.Idle != 0 {
		t.Fatalf("busy shell activity = %+v", a)
	}
	shell.TTY, shell.TPGID = "??", 0
	if a, _ := definition(fixed(shell), &bytes.Buffer{}).Activity(context.Background(), target(500)); a.Busy != "" || a.IdleKnown {
		t.Fatalf("shell without a terminal = %+v", a)
	}
}

func TestAdoptPinsShellAndHelpers(t *testing.T) {
	shell := Process{PID: 500, PPID: 1, PGID: 500, Stat: "Ss", TTY: "ttys001", Name: "-zsh"}
	helper := Process{PID: 510, PPID: 500, PGID: 510, Stat: "S", TTY: "??", Name: "gitstatusd"}
	grandchild := Process{PID: 520, PPID: 510, PGID: 510, Stat: "S", TTY: "??", Name: "git"}
	d := definition(fixed(shell, helper, grandchild, Process{PID: 900, PPID: 1, Stat: "S", TTY: "??", Name: "python3"}), &bytes.Buffer{})
	ref, err := d.Prepare(context.Background(), adapterpkg.PrepareRequest{Existing: adapterpkg.ResourceRef{Adapter: "shell", Name: "500", Adopted: true}})
	if err != nil || ref.Process.PID != 500 || ref.Process.Start != start || len(ref.Process.Members) != 2 || ref.Process.Members[0].Command != "gitstatusd" {
		t.Fatalf("prepare = %+v, %v", ref, err)
	}
	if err := d.Validate("", adapterpkg.Metadata{}, ref); err != nil {
		t.Fatal(err)
	}
	// dibs adopting its own parent shell does not record itself as a helper.
	self := Process{PID: 530, PPID: 500, PGID: 530, Stat: "S+", TTY: "ttys001", Name: "dibs"}
	selfChild := Process{PID: 531, PPID: 530, PGID: 530, Stat: "S+", TTY: "ttys001", Name: "ps"}
	inside := New(Options{List: fixed(shell, helper, self, selfChild), Grace: time.Millisecond, Log: &bytes.Buffer{}, Self: 530, TTYTimes: TTYTimes})
	if ref, err := inside.Prepare(context.Background(), adapterpkg.PrepareRequest{Existing: adapterpkg.ResourceRef{Adapter: "shell", Name: "500", Adopted: true}}); err != nil || len(ref.Process.Members) != 1 || ref.Process.Members[0].PID != 510 {
		t.Fatalf("adopt from inside the shell = %+v, %v", ref.Process, err)
	}
	for name, want := range map[string]string{"900": "not a shell", "1": "names the shell's PID", "0500": "names the shell's PID"} {
		if _, err := d.Discover(context.Background(), adapterpkg.ResourceRef{Name: name}); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("discover %s = %v", name, err)
		}
	}
	if rows, err := d.Discover(context.Background(), adapterpkg.ResourceRef{Name: "4242"}); err != nil || len(rows) != 0 {
		t.Fatalf("discover missing shell = %v, %v", rows, err)
	}
	bad := ref
	bad.Name = "501"
	if d.Validate("", adapterpkg.Metadata{}, bad) == nil {
		t.Fatal("mismatched shell name validated")
	}
}

// Real processes: a hangup is enough for a plain shell and its helpers, and a
// tree that ignores HUP and TERM is killed only after each grace period.
func TestDestroyEscalatesAndVerifiesGone(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		want         []string
		absent       string
	}{
		{"hangup", "sleep 300 & sleep 300 & wait", []string{"SIGHUP to", "verified gone"}, "SIGTERM"},
		{"escalation", "trap '' HUP TERM; sleep 300 & wait", []string{"SIGHUP to", "SIGTERM to", "SIGKILL to", "verified gone"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("/bin/sh", "-c", tc.script)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { cmd.Wait(); close(done) }()
			var log bytes.Buffer
			d := New(Options{List: PS, Grace: 300 * time.Millisecond, Log: &log, Self: os.Getpid(), TTYTimes: TTYTimes})
			var ref adapterpkg.ResourceRef
			var err error
			// The sh must have started its helpers before adoption records them.
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				procs, _ := PS(context.Background())
				children := 0
				for _, p := range procs {
					if p.PPID == cmd.Process.Pid {
						children++
					}
				}
				if children >= 1 {
					break
				}
			}
			ref, err = d.Prepare(context.Background(), adapterpkg.PrepareRequest{Existing: adapterpkg.ResourceRef{Adapter: "shell", Name: strconv.Itoa(cmd.Process.Pid), Adopted: true}})
			if err != nil || len(ref.Process.Members) == 0 {
				cmd.Process.Kill()
				t.Fatalf("prepare = %+v, %v", ref, err)
			}
			helpers := ref.Process.Members
			if err := d.Adapter.Destroy(context.Background(), adapterpkg.Target{Ref: ref}); err != nil {
				t.Fatalf("destroy = %v\n%s", err, log.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(log.String(), want) {
					t.Fatalf("log lacks %q:\n%s", want, log.String())
				}
			}
			if tc.absent != "" && strings.Contains(log.String(), tc.absent) {
				t.Fatalf("log has %q:\n%s", tc.absent, log.String())
			}
			<-done
			procs, err := PS(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range helpers {
				if p, ok := procs[h.PID]; ok && p.Start == h.Start && !strings.HasPrefix(p.Stat, "Z") {
					t.Fatalf("helper %d %s survived", h.PID, h.Command)
				}
			}
			if found, err := d.Exists(context.Background(), adapterpkg.Target{Ref: ref}); err != nil || found {
				t.Fatalf("exists after destroy = %v, %v", found, err)
			}
		})
	}
}
