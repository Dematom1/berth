package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const gib = int64(1 << 30)

type Budget struct {
	Cores  int   `json:"cores"`
	Memory int64 `json:"memory_bytes"`
}

type JobClass struct {
	Cores   int    `json:"cores"`
	Memory  string `json:"memory"`
	Timeout string `json:"timeout"`
}

type JobConfig struct {
	Cores      int                   `json:"cores"`
	Memory     string                `json:"memory"`
	RoomCores  int                   `json:"room_cores"`
	RoomMemory string                `json:"room_memory"`
	Classes    map[string]JobClass   `json:"classes"`
	Machines   map[string]SSHMachine `json:"machines"`
}

func memoryBytes(s string) (int64, error) {
	units := []struct {
		suffix string
		scale  int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"GB", 1000000000}, {"MB", 1000000}, {"KB", 1000}, {"B", 1}}
	scale := int64(1)
	for _, unit := range units {
		if strings.HasSuffix(s, unit.suffix) {
			s = strings.TrimSuffix(s, unit.suffix)
			scale = unit.scale
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/scale {
		return 0, errors.New("memory must be nonnegative integer bytes or use KiB, MiB, GiB, KB, MB, GB")
	}
	return n * scale, nil
}

func hostMemory() (int64, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0, errors.New("cannot read host memory; check sysctl hw.memsize")
		}
		return memoryBytes(strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "MemTotal:" {
			return memoryBytes(fields[1] + "KiB")
		}
	}
	return 0, errors.New("cannot read MemTotal from /proc/meminfo")
}

func defaultJobConfig(cores int, memory int64) JobConfig {
	room := min(memory/2, max(512<<20, memory/4))
	return JobConfig{Cores: cores, Memory: strconv.FormatInt(memory, 10), RoomCores: min(cores-1, max(1, cores/4)), RoomMemory: strconv.FormatInt(room, 10), Classes: map[string]JobClass{"default": {Cores: 1, Memory: strconv.FormatInt(min(gib, memory-room), 10), Timeout: "1h"}}}
}

func (c JobConfig) available() (Budget, error) {
	memory, err := memoryBytes(c.Memory)
	if err != nil {
		return Budget{}, err
	}
	room, err := memoryBytes(c.RoomMemory)
	if err != nil {
		return Budget{}, err
	}
	if c.Cores <= 0 || c.RoomCores < 0 || c.RoomCores >= c.Cores || memory <= 0 || room >= memory {
		return Budget{}, errors.New("budget needs positive cores/memory and owner room smaller than totals")
	}
	return Budget{c.Cores - c.RoomCores, memory - room}, nil
}

func (c JobConfig) class(name string) (Budget, time.Duration, error) {
	class, ok := c.Classes[name]
	if !ok || name == "" {
		return Budget{}, 0, fmt.Errorf("unknown class %q; define it in DIBS_CONFIG or use --class default", name)
	}
	memory, err := memoryBytes(class.Memory)
	if err != nil {
		return Budget{}, 0, err
	}
	timeout, err := time.ParseDuration(class.Timeout)
	if err != nil || timeout <= 0 || class.Cores <= 0 || memory <= 0 {
		return Budget{}, 0, fmt.Errorf("class %q needs positive cores, memory and timeout", name)
	}
	return Budget{class.Cores, memory}, timeout, nil
}

func loadJobConfig() (JobConfig, error) {
	memory, err := hostMemory()
	if err != nil {
		return JobConfig{}, err
	}
	c := defaultJobConfig(runtime.NumCPU(), memory)
	path := getenv("CONFIG")
	explicit := path != ""
	var base string
	if !explicit {
		// Use XDG on both macOS and Linux for a consistent CLI config location.
		base = os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return c, err
			}
			base = filepath.Join(home, ".config")
		}
		path = filepath.Join(base, "dibs", "config.json")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		data, err = readLegacyConfig(base, path)
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
	}
	if err != nil {
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
		c.Classes = map[string]JobClass{}
	}
	for name, class := range defaults {
		if _, ok := c.Classes[name]; !ok {
			c.Classes[name] = class
		}
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return c, errors.New("job config must contain exactly one JSON object")
	}
	if _, err := c.available(); err != nil {
		return c, err
	}
	for name := range c.Classes {
		if _, _, err := c.class(name); err != nil {
			return c, err
		}
	}
	return c, nil
}
