package lease

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Dematom1/dibs/internal/compat"
)

// Cache the OS identity, not the hostname: a rename during a process's lifetime
// must not change which leases it can act on.
var machine = sync.OnceValues(func() (string, error) {
	var raw string
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile("/etc/machine-id")
		if err != nil {
			return "", fmt.Errorf("cannot read machine identity: %w", err)
		}
		raw = strings.TrimSpace(string(data))
	case "darwin":
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		data, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		if err != nil {
			return "", fmt.Errorf("cannot read platform identity: %w", err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if _, value, ok := strings.Cut(line, `"IOPlatformUUID" = "`); ok {
				raw = strings.TrimSuffix(strings.TrimSpace(value), `"`)
				break
			}
		}
	default:
		return "", errors.New("stable machine identity requires macOS or Linux")
	}
	id := strings.ToLower(strings.ReplaceAll(raw, "-", ""))
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 16 || id == strings.Repeat("0", 32) {
		return "", errors.New("OS machine identity is missing or invalid; refusing hostname fallback")
	}
	return runtime.GOOS + ":" + id, nil
})

var machineHostname = os.Hostname

// v1 used hostnames. Preserve the old exact-hostname locality rule, or require
// explicit same-user attestation for a renamed host. Never rebind stable IDs.
func (s *store) migrateMachines() error {
	needsHostname := s.Version == 1
	for _, l := range s.Leases {
		needsHostname = needsHostname || !strings.Contains(l.Machine, ":")
	}
	for _, w := range s.Waiters {
		needsHostname = needsHostname || !strings.Contains(w.Machine, ":")
	}
	if !needsHostname {
		return nil
	}
	host, err := machine()
	if err != nil {
		return err
	}
	hostname, err := machineHostname()
	if err != nil || hostname == "" {
		return errors.New("cannot determine hostname for legacy state migration")
	}
	alias := compat.Env("LEGACY_MACHINE")
	local := func(name string) bool {
		return !strings.Contains(name, ":") && (name == hostname || (alias != "" && name == alias))
	}
	changed := s.Version == 1
	for id, l := range s.Leases {
		if local(l.Machine) {
			l.Machine = host
			s.Leases[id] = l
			changed = true
		}
	}
	for i, w := range s.Waiters {
		if local(w.Machine) {
			s.Waiters[i].Machine = host
			changed = true
		}
	}
	s.Version = stateVersion
	if changed {
		return s.Save()
	}
	return nil
}
