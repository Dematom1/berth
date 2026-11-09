package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Owner struct {
	Kind  string        `json:"kind"`
	ID    string        `json:"id"`
	Start string        `json:"start,omitempty"`
	Stale time.Duration `json:"stale,omitempty"`
}

func (o Owner) String() string { return o.Kind + ":" + o.ID }

func parseOwner(value string, stale time.Duration, now time.Time) (Owner, error) {
	kind, id, ok := strings.Cut(value, ":")
	if !ok || id == "" {
		return Owner{}, errors.New("--owner must be pid:<pid>, session:<holder-pid>, or heartbeat:<file>")
	}
	o := Owner{Kind: kind, ID: id}
	switch kind {
	case "pid", "session":
		pid, err := strconv.Atoi(id)
		if err != nil || pid <= 0 || pid > math.MaxInt32 {
			return Owner{}, errors.New("owner PID must be a positive signed 32-bit integer")
		}
		o.ID = strconv.Itoa(pid)
		start, live := processIdentity(pid)
		if live != Alive {
			return Owner{}, errors.New("owner process must be alive and inspectable")
		}
		o.Start = start
	case "heartbeat":
		if stale <= 0 {
			return Owner{}, errors.New("--stale must be positive")
		}
		path, err := filepath.Abs(id)
		if err != nil {
			return Owner{}, err
		}
		o.ID, o.Stale = path, stale
		if checkOwner(o, now) != Alive {
			return Owner{}, errors.New("heartbeat must be an existing, fresh regular file; touch the file before creating the lease")
		}
	default:
		return Owner{}, fmt.Errorf("unknown owner kind %q; use pid, session, or heartbeat", kind)
	}
	return o, nil
}

func processIdentity(pid int) (string, Liveness) {
	if err := syscall.Kill(pid, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return "", Dead
		}
		return "", Unknown
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// ponytail: ps start time has one-second precision; use native birth tokens if PID churn reaches reuse within a second.
	cmd := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "lstart=", "-o", "stat=")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return "", Dead
		}
		return "", Unknown
	}
	fields := strings.Fields(string(out))
	if len(fields) != 6 {
		return "", Unknown
	}
	if strings.HasPrefix(fields[5], "Z") {
		return "", Dead
	}
	return strings.Join(fields[:5], " "), Alive
}

func checkOwner(o Owner, now time.Time) Liveness {
	switch o.Kind {
	case "pid", "session":
		pid, err := strconv.Atoi(o.ID)
		if err != nil || pid <= 0 || pid > math.MaxInt32 || o.Start == "" {
			return Unknown
		}
		start, live := processIdentity(pid)
		if live == Alive && start != o.Start {
			return Dead
		}
		return live
	case "heartbeat":
		if o.Stale <= 0 || !filepath.IsAbs(o.ID) {
			return Unknown
		}
		info, err := os.Stat(o.ID)
		if errors.Is(err, os.ErrNotExist) {
			return Dead
		}
		if err != nil || !info.Mode().IsRegular() || info.ModTime().After(now) {
			return Unknown
		}
		if now.Sub(info.ModTime()) >= o.Stale {
			return Dead
		}
		return Alive
	default:
		return Unknown
	}
}
