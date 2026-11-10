package owner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func TestOutOfRangePIDIsUnknownNotDead(t *testing.T) {
	for _, kind := range []string{"pid", "session"} {
		o := Reference{Kind: kind, ID: "4299161601", Identity: "invalid persisted process"}
		if live := observed(o, testNow); live != Unknown {
			t.Fatalf("%s PID outside pid_t range is %s, want unknown", kind, live)
		}
		if _, err := bind(o.String(), time.Minute, testNow); err == nil {
			t.Fatalf("accepted out-of-range %s owner", kind)
		}
	}
}

func TestOwnerLiveness(t *testing.T) {
	now := time.Now()
	o, err := bind("pid:"+strconv.Itoa(os.Getpid()), time.Minute, now)
	if err != nil || observed(o, now) != Alive {
		t.Fatalf("current process: %+v, %v", o, err)
	}
	t.Setenv("TZ", "Pacific/Honolulu")
	if observed(o, now) != Alive {
		t.Fatal("timezone change changed process identity")
	}
	o.Identity = "different process start"
	if observed(o, now) != Dead {
		t.Fatal("reused PID not detected")
	}
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	o, err = bind("session:"+strconv.Itoa(child.Process.Pid), time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	child.Process.Kill()
	child.Wait()
	if observed(o, now) != Dead {
		t.Fatal("dead session holder still alive")
	}
	file := filepath.Join(t.TempDir(), "heartbeat")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	now = time.Now()
	o, err = bind("heartbeat:"+file, 3*time.Minute, now)
	if err != nil || observed(o, now) != Alive {
		t.Fatalf("fresh heartbeat: %+v, %v", o, err)
	}
	if observed(o, now.Add(3*time.Minute)) != Dead {
		t.Fatal("stale heartbeat did not die")
	}
	os.Chtimes(file, now.Add(time.Hour), now.Add(time.Hour))
	if observed(o, now) != Unknown {
		t.Fatal("future heartbeat must be unknown")
	}
	os.Remove(file)
	if observed(o, now) != Dead {
		t.Fatal("missing heartbeat did not die")
	}
	for _, bad := range []string{"pid:0", "pid:-1", "session:bad", "other:123", "heartbeat:"} {
		if _, err := bind(bad, time.Minute, now); err == nil {
			t.Fatalf("accepted invalid owner %q", bad)
		}
	}
}

func observed(r Reference, now time.Time) Liveness {
	return NewRegistry(PID(), Session(), Heartbeat()).Check(context.Background(), r, now).Liveness
}
