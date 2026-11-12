package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	budgetpkg "github.com/Dematom1/dibs/internal/budget"
	"github.com/Dematom1/dibs/internal/compat"
)

const gib = int64(1 << 30)

type Class struct {
	Cores   int    `json:"cores"`
	Memory  string `json:"memory"`
	Timeout string `json:"timeout"`
}

type Config struct {
	Cores      int               `json:"cores"`
	Memory     string            `json:"memory"`
	RoomCores  int               `json:"room_cores"`
	RoomMemory string            `json:"room_memory"`
	Classes    map[string]Class  `json:"classes"`
	Machines   map[string]Target `json:"machines"`
	PROwner    bool              `json:"pr_owner"`
}

func hostMemory() (int64, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0, errors.New("cannot read host memory; check sysctl hw.memsize")
		}
		return budgetpkg.ParseMemory(strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "MemTotal:" {
			return budgetpkg.ParseMemory(fields[1] + "KiB")
		}
	}
	return 0, errors.New("cannot read MemTotal from /proc/meminfo")
}

func defaultJobConfig(cores int, memory int64) Config {
	room := min(memory/2, max(512<<20, memory/4))
	return Config{Cores: cores, Memory: strconv.FormatInt(memory, 10), RoomCores: min(cores-1, max(1, cores/4)), RoomMemory: strconv.FormatInt(room, 10), Classes: map[string]Class{"default": {Cores: 1, Memory: strconv.FormatInt(min(gib, memory-room), 10), Timeout: "1h"}}}
}

func (c Config) Available() (budgetpkg.Amount, error) {
	memory, err := budgetpkg.ParseMemory(c.Memory)
	if err != nil {
		return budgetpkg.Amount{}, err
	}
	room, err := budgetpkg.ParseMemory(c.RoomMemory)
	if err != nil {
		return budgetpkg.Amount{}, err
	}
	if c.Cores <= 0 || c.RoomCores < 0 || c.RoomCores >= c.Cores || memory <= 0 || room >= memory {
		return budgetpkg.Amount{}, errors.New("budget needs positive cores/memory and owner room smaller than totals")
	}
	return budgetpkg.Amount{Cores: c.Cores - c.RoomCores, Memory: memory - room}, nil
}

func (c Config) Class(name string) (budgetpkg.Amount, time.Duration, error) {
	class, ok := c.Classes[name]
	if !ok || name == "" {
		return budgetpkg.Amount{}, 0, fmt.Errorf("unknown class %q; define it in DIBS_CONFIG or use --class default", name)
	}
	memory, err := budgetpkg.ParseMemory(class.Memory)
	if err != nil {
		return budgetpkg.Amount{}, 0, err
	}
	timeout, err := time.ParseDuration(class.Timeout)
	if err != nil || timeout <= 0 || class.Cores <= 0 || memory <= 0 {
		return budgetpkg.Amount{}, 0, fmt.Errorf("class %q needs positive cores, memory and timeout", name)
	}
	return budgetpkg.Amount{Cores: class.Cores, Memory: memory}, timeout, nil
}

func Load() (Config, error) {
	memory, err := hostMemory()
	if err != nil {
		return Config{}, err
	}
	c := defaultJobConfig(runtime.NumCPU(), memory)
	data, found, err := read()
	if err != nil || !found {
		return c, err
	}
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		return c, errors.New("job config must be a JSON object")
	}
	defaults := c.Classes
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid job config: %w", err)
	}
	if c.Classes == nil {
		c.Classes = map[string]Class{}
	}
	for name, class := range defaults {
		if _, ok := c.Classes[name]; !ok {
			c.Classes[name] = class
		}
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return c, errors.New("job config must contain exactly one JSON object")
	}
	if _, err := c.Available(); err != nil {
		return c, err
	}
	for name := range c.Classes {
		if _, _, err := c.Class(name); err != nil {
			return c, err
		}
	}
	return c, nil
}

// read returns the user config file; a missing file at the default path is not an error.
func read() ([]byte, bool, error) {
	path := compat.Env("CONFIG")
	explicit := path != ""
	if !explicit {
		// Use XDG on both macOS and Linux for a consistent CLI config location.
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, false, err
			}
			base = filepath.Join(home, ".config")
		}
		path = filepath.Join(base, "dibs", "config.json")
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			path = filepath.Join(base, "berth", "config.json")
		}
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		return nil, false, nil
	}
	return data, err == nil, err
}

// PROwner reports the pr_owner opt-in without probing host memory; a missing or unreadable config leaves it off.
func PROwner() bool {
	data, found, err := read()
	var c struct {
		PROwner bool `json:"pr_owner"`
	}
	return err == nil && found && json.Unmarshal(data, &c) == nil && c.PROwner
}

// SSH config aliases may supply proxying, keys, and host-key policy. Dibs never
// disables host-key checking or falls back to interactive authentication.
type Target struct {
	Host         string `json:"host"`
	User         string `json:"user"`
	Workdir      string `json:"workdir"`
	Agent        string `json:"agent"`
	Port         int    `json:"port,omitempty"`
	IdentityFile string `json:"identity_file,omitempty"`
}

func (m Target) Validate() error {
	if m.Host == "" || strings.HasPrefix(m.Host, "-") || strings.ContainsAny(m.Host, " \t\r\n\x00@") || m.User == "" || strings.HasPrefix(m.User, "-") || strings.ContainsAny(m.User, " \t\r\n\x00@") {
		return errors.New("SSH machine requires a host (or SSH config alias) and user, without whitespace or option prefixes")
	}
	if !filepath.IsAbs(m.Workdir) || !filepath.IsAbs(m.Agent) || strings.ContainsRune(m.Workdir+m.Agent+m.IdentityFile, 0) || m.Port < 0 || m.Port > 65535 {
		return errors.New("SSH machine requires absolute workdir and agent paths, and port 1..65535 when set")
	}
	return nil
}
