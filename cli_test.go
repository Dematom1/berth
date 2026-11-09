package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func invoke(t *testing.T, want int, args ...string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	args = append([]string{"--json"}, args...)
	if got := run(context.Background(), args, &out); got != want {
		t.Fatalf("%v exited %d, want %d: %s", args, got, want, out.String())
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("not JSON: %s: %v", out.String(), err)
	}
	return result
}

func TestCLILeaseLifecycle(t *testing.T) {
	t.Setenv("BERTH_STATE", t.TempDir())
	t.Setenv("BERTH_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
	created := invoke(t, 0, "create", "--ttl", "1h", "--resources", "2 CPUs, test lab", "--label", "task=lab")
	record := created["record"].(map[string]any)
	id := record["id"].(string)
	before, err := time.Parse(time.RFC3339Nano, record["expires"].(string))
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"list", "ls", "ps", "who"} {
		list := invoke(t, 0, alias)
		if list["count"] != float64(1) {
			t.Fatalf("%s missing lease: %+v", alias, list)
		}
		l := list["leases"].([]any)[0].(map[string]any)
		if l["resources"] != "2 CPUs, test lab" || l["age"] == "" || l["owner"] != os.Getenv("BERTH_OWNER") {
			t.Fatalf("incomplete listing: %+v", l)
		}
		if l["labels"].(map[string]any)["task"] != "lab" || l["resource_adapter"] != "none" || l["resource_name"] != "" {
			t.Fatalf("missing durable lease identity: %+v", l)
		}
	}
	invoke(t, 0, "extend", id, "--ttl", "2h")
	list := invoke(t, 0, "ps")
	l := list["leases"].([]any)[0].(map[string]any)
	after, _ := time.Parse(time.RFC3339Nano, l["expires"].(string))
	if !after.After(before.Add(59 * time.Minute)) {
		t.Fatalf("extend failed to move expiry: %s", l["expires"])
	}
	invoke(t, 0, "release", id)
	if invoke(t, 0, "ps")["count"] != float64(1) {
		t.Fatal("release preview deleted lease")
	}
	invoke(t, 0, "down", id, "--confirm")
	invoke(t, 0, "release", id, "--confirm")
	if invoke(t, 0, "ps")["count"] != float64(0) {
		t.Fatal("release did not remove lease")
	}
}

func TestCLIReleaseFailureDoesNotReportReleased(t *testing.T) {
	t.Setenv("BERTH_STATE", t.TempDir())
	t.Setenv("BERTH_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
	created := invoke(t, 0, "create", "--ttl", "1h")
	id := created["record"].(map[string]any)["id"].(string)
	s, err := openStore(context.Background(), os.Getenv("BERTH_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	lease := s.Leases[id]
	lease.Machine = "another-machine"
	s.Leases[id] = lease
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	result := invoke(t, 1, "release", id, "--confirm")
	if result["status"] == "released" || result["error"] == nil {
		t.Fatalf("failed release reported success: %+v", result)
	}
	if invoke(t, 0, "ps")["count"] != float64(1) {
		t.Fatal("failed release discarded lease")
	}
}

func TestCLIExpiredLiveOwnerAndFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERTH_STATE", dir)
	t.Setenv("BERTH_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
	created := invoke(t, 0, "create", "--ttl", "1ns")
	id := created["record"].(map[string]any)["id"].(string)
	invoke(t, 1, "extend", id, "--ttl", "1h")
	r := invoke(t, 0, "sweep")
	if len(r["eligible"].([]any)) != 1 || len(r["reclaimed"].([]any)) != 0 {
		t.Fatalf("incorrect preview: %+v", r)
	}
	r = invoke(t, 0, "sweep", "--confirm")
	if len(r["reclaimed"].([]any)) != 1 {
		t.Fatalf("did not reclaim expired live owner: %+v", r)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("bad state"), 0600); err != nil {
		t.Fatal(err)
	}
	if invoke(t, 1, "ps")["error"] == nil {
		t.Fatal("state corruption not reported")
	}
}

func TestCLIRejectsUsageBeforeStateAccess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "must-not-exist")
	t.Setenv("BERTH_STATE", dir)
	for _, args := range [][]string{
		{"ps", "--typo"}, {"ps", "extra"}, {"create", "--ttl", "0s"},
		{"create", "--ttl", "bad"}, {"create", "--adapter", "unknown"},
		{"create", "--adapter", "compose"}, {"create", "--file", "x"},
		{"create", "--label", "bad"}, {"create", "--owner", "pid:-2"},
		{"extend", "../../state", "--ttl", "1h"}, {"release"}, {"job"},
	} {
		result := invoke(t, 2, args...)
		if result["error"] == nil || result["help"] == nil {
			t.Fatalf("missing structured error or help: %+v", result)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("invalid input reached state store")
	}
}

func TestCLICreateFailureKeepsDurableCleanupIntent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERTH_STATE", dir)
	t.Setenv("BERTH_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
	t.Setenv("DOCKER_HOST", "")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nprintf 'test-context\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	kind := `#!/bin/sh
case "$1" in
create)
  test -s "$BERTH_STATE/state.json" && touch "$BERTH_STATE/saw-intent"
  printf 'dependency-output-must-not-leak'
  exit 1
  ;;
delete|get) exit 0 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "kind"), []byte(kind), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	result := invoke(t, 1, "create", "--adapter", "kind")
	if strings.Contains(result["error"].(string), "dependency-output-must-not-leak") {
		t.Fatal("raw resource output leaked into structured error")
	}
	if _, err := os.Stat(filepath.Join(dir, "saw-intent")); err != nil {
		t.Fatal("external create ran without durable cleanup intent")
	}
	list := invoke(t, 0, "ps")
	if list["count"] != float64(1) {
		t.Fatal("failed create lost cleanup lease")
	}
	id := list["leases"].([]any)[0].(map[string]any)["id"].(string)
	invoke(t, 0, "release", id, "--confirm")
	if invoke(t, 0, "ps")["count"] != float64(0) {
		t.Fatal("partial creation did not converge after cleanup")
	}
}

func TestCLIOwnerFilterUsesCanonicalIdentity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERTH_STATE", filepath.Join(dir, "state"))
	pid := strconv.Itoa(os.Getpid())
	invoke(t, 0, "create", "--owner", "pid:"+pid)
	if invoke(t, 0, "who", "--owner", "pid:0"+pid)["count"] != float64(1) {
		t.Fatal("PID filter did not normalize leading zeros")
	}
	t.Chdir(dir)
	file := filepath.Join(dir, "heartbeat")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	invoke(t, 0, "create", "--owner", "heartbeat:./heartbeat")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if invoke(t, 0, "ps", "--owner", "heartbeat:./heartbeat")["count"] != float64(1) {
		t.Fatal("relative heartbeat filter failed after owner death")
	}
	if invoke(t, 0, "ps", "--owner", "unknown:owner")["count"] != float64(0) {
		t.Fatal("unrecognized owner selector must not match")
	}
}

func TestCLITextListKeepsLabels(t *testing.T) {
	for _, tc := range []struct {
		name, label, cell string
	}{
		{"empty", "", `,"{}",`},
		{"populated", "task=lab", `,"{\"task\":\"lab\"}",`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BERTH_STATE", t.TempDir())
			t.Setenv("BERTH_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
			args := []string{"create"}
			if tc.label != "" {
				args = append(args, "--label", tc.label)
			}
			invoke(t, 0, args...)
			var out bytes.Buffer
			if code := run(context.Background(), []string{"ps"}, &out); code != 0 {
				t.Fatalf("ps failed: %d, %s", code, out.String())
			}
			if !strings.Contains(out.String(), tc.cell) || strings.Contains(out.String(), ",null,") {
				t.Fatalf("TOON table lost %s labels: %s", tc.name, out.String())
			}
			list := invoke(t, 0, "ps")
			labels, ok := list["leases"].([]any)[0].(map[string]any)["labels"].(map[string]any)
			if !ok || (tc.label != "" && labels["task"] != "lab") {
				t.Fatalf("JSON labels must remain an object: %#v", labels)
			}
		})
	}
}

func TestCLIHomeHelpVersionAndTOON(t *testing.T) {
	t.Setenv("BERTH_STATE", t.TempDir())
	var out bytes.Buffer
	if code := run(context.Background(), nil, &out); code != 0 || !strings.Contains(out.String(), "bin:") || !strings.Contains(out.String(), "leases: []") || !strings.Contains(out.String(), "count: 0") {
		t.Fatalf("home view: %d, %s", code, out.String())
	}
	for _, flag := range []string{"--version", "-v", "-V"} {
		out.Reset()
		if code := run(context.Background(), []string{flag}, &out); code != 0 || out.String() != version+"\n" {
			t.Fatalf("version fast path: %d, %q", code, out.String())
		}
	}
	for _, cmd := range []string{"create", "extend", "release", "list", "sweep", "job", "agent", "machine"} {
		out.Reset()
		if code := run(context.Background(), []string{cmd, "--help"}, &out); code != 0 || !strings.Contains(out.String(), "Exit codes:") {
			t.Fatalf("%s help: %d %s", cmd, code, out.String())
		}
		if cmd == "extend" && (!strings.Contains(out.String(), "staleness/3") || !strings.Contains(out.String(), "one third")) {
			t.Fatal("R-B3 cadence missing from emitted extend help")
		}
		if cmd == "job" && (!strings.Contains(out.String(), "124") || !strings.Contains(out.String(), "75") || !strings.Contains(out.String(), "76")) {
			t.Fatal("R-B4 exit codes missing from emitted job help")
		}
	}
	var rendered bytes.Buffer
	emit(&rendered, false, map[string]any{"leases": []map[string]any{{"id": "abc", "resources": "lab, \"db\"\nnext"}}})
	if rendered.String() != "leases[1]{id,resources}:\n  abc,\"lab, \\\"db\\\"\\nnext\"\n" {
		t.Fatalf("TOON string escaping changed: %q", rendered.String())
	}
}
