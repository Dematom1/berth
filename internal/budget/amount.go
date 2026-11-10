package budget

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

type Amount struct {
	Cores  int   `json:"cores"`
	Memory int64 `json:"memory_bytes"`
}

func ParseMemory(s string) (int64, error) {
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
