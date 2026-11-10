package budget

import "testing"

const gib = int64(1 << 30)

func TestMemoryBytes(t *testing.T) {
	for s, want := range map[string]int64{"1GiB": gib, "2MiB": 2 << 20, "1GB": 1000000000, "123": 123, "0B": 0} {
		if got, err := ParseMemory(s); err != nil || got != want {
			t.Fatalf("memory %s: %d %v", s, got, err)
		}
	}
	for _, s := range []string{"-1", "1.5GiB", "bogus", "9223372036854775807GiB"} {
		if _, err := ParseMemory(s); err == nil {
			t.Fatalf("accepted memory %q", s)
		}
	}
}
