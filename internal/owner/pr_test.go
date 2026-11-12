package owner

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	openPR   = `{"number":7,"state":"open","draft":false,"node_id":"PR_a","base":{"repo":{"node_id":"R_a"}}}`
	draftPR  = `{"number":7,"state":"open","draft":true,"node_id":"PR_a","base":{"repo":{"node_id":"R_a"}}}`
	closedPR = `{"number":7,"state":"closed","merged":false,"node_id":"PR_a","base":{"repo":{"node_id":"R_a"}}}`
	mergedPR = `{"number":7,"state":"closed","merged":true,"node_id":"PR_a","base":{"repo":{"node_id":"R_a"}}}`
)

// fakeGH installs a gh on PATH that records its arguments and prints body with the given exit code.
func fakeGH(t *testing.T, body string, exit int) string {
	t.Helper()
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '" + args + "'\ncat <<'JSON'\n" + body + "\nJSON\nexit " + strconv.Itoa(exit) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	return args
}

func prReference() Reference {
	return Reference{Kind: "pr", ID: "github.com/octo/repo#7", Identity: "PR_a R_a"}
}

func TestPRLivenessMapping(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		exit int
		want Liveness
	}{
		{"open", openPR, 0, Alive},
		{"draft", draftPR, 0, Alive},
		{"closed", closedPR, 0, Dead},
		{"merged", mergedPR, 0, Dead},
		{"replaced PR at the same path", strings.Replace(closedPR, "PR_a", "PR_b", 1), 0, Unknown},
		{"recreated repository", strings.Replace(openPR, "R_a", "R_b", 1), 0, Unknown},
		{"not found or hidden", `{"message":"Not Found","status":"404"}`, 1, Unknown},
		{"rate limited", `{"message":"API rate limit exceeded","status":"403"}`, 1, Unknown},
		{"server error", `{"message":"Server Error","status":"502"}`, 1, Unknown},
		{"malformed output", `not json`, 0, Unknown},
		{"missing identity", `{"number":7,"state":"closed"}`, 0, Unknown},
		{"unexpected state", strings.Replace(openPR, `"open"`, `"locked"`, 1), 0, Unknown},
		{"wrong number", strings.Replace(closedPR, `"number":7`, `"number":8`, 1), 0, Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := fakeGH(t, tc.body, tc.exit)
			got := NewRegistry(PR()).Check(context.Background(), prReference(), time.Now()).Liveness
			if got != tc.want {
				t.Fatalf("liveness = %s, want %s", got, tc.want)
			}
			data, err := os.ReadFile(args)
			if want := "api\n--hostname\ngithub.com\n--method\nGET\nrepos/octo/repo/pulls/7\n"; err != nil || string(data) != want {
				t.Fatalf("gh called with %q, want exactly one read-only call %q: %v", data, want, err)
			}
		})
	}
}

func TestPRUnknownWithoutGHOrWithinTimeout(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if got := NewRegistry(PR()).Check(context.Background(), prReference(), time.Now()).Liveness; got != Unknown {
		t.Fatalf("missing gh = %s, want unknown", got)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if got := NewRegistry(PR()).Check(ctx, prReference(), time.Now()).Liveness; got != Unknown {
		t.Fatalf("hung gh = %s, want unknown", got)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("hung gh check took %s", elapsed)
	}
}

func TestPRCorruptReferenceIsUnknownWithoutCalling(t *testing.T) {
	args := fakeGH(t, closedPR, 0)
	for _, r := range []Reference{
		{Kind: "pr", ID: "github.com/octo/repo#7"},
		{Kind: "pr", ID: "octo/repo#7", Identity: "PR_a R_a"},
		{Kind: "pr", ID: "github.com/-octo/repo#7", Identity: "PR_a R_a"},
	} {
		if got := NewRegistry(PR()).Check(context.Background(), r, time.Now()).Liveness; got != Unknown {
			t.Fatalf("%+v = %s, want unknown", r, got)
		}
	}
	if _, err := os.Stat(args); !os.IsNotExist(err) {
		t.Fatalf("corrupt reference reached gh: %v", err)
	}
}

func TestPRBind(t *testing.T) {
	ctx := context.Background()
	for _, selector := range []string{"pr:octo/repo#7", "pr:Octo/Repo#07", "pr:github.com/octo/repo#7", "pr:https://GitHub.com/octo/repo/pull/7"} {
		fakeGH(t, draftPR, 0)
		r, err := NewRegistry(PR()).Bind(ctx, selector, 0, time.Now())
		if err != nil || r != prReference() {
			t.Fatalf("%s bound %+v, %v", selector, r, err)
		}
		if got := CanonicalSelector(selector); got != "pr:github.com/octo/repo#7" {
			t.Fatalf("%s canonical selector = %q", selector, got)
		}
	}
	args := fakeGH(t, openPR, 0)
	if _, err := NewRegistry(PR()).Bind(ctx, "pr:https://ghe.example.com/octo/repo/pull/7", 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(args); !strings.Contains(string(data), "--hostname\nghe.example.com\n") {
		t.Fatalf("enterprise host not passed to gh: %q", data)
	}
	for _, tc := range []struct {
		body string
		exit int
		want string
	}{{mergedPR, 0, "closed or merged"}, {`{"message":"Not Found"}`, 1, "gh auth status --hostname github.com"}} {
		fakeGH(t, tc.body, tc.exit)
		if _, err := NewRegistry(PR()).Bind(ctx, "pr:octo/repo#7", 0, time.Now()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("bind error = %v, want %q", err, tc.want)
		}
	}
}

func TestPRSelectorValidation(t *testing.T) {
	args := fakeGH(t, openPR, 0)
	for _, selector := range []string{
		"pr:octo#7", "pr:octo/repo", "pr:octo/repo#0", "pr:octo/repo#-1", "pr:octo/repo#+7", "pr:octo/repo#x",
		"pr:-octo/repo#7", "pr:octo/../x#7", "pr:a/b/c/d#7", "pr:-host/octo/repo#7", "pr:octo/re po#7",
		"pr:http://github.com/octo/repo/pull/7", "pr:https://user@github.com/octo/repo/pull/7",
		"pr:https://github.com:8443/octo/repo/pull/7", "pr:https://github.com/octo/repo/pull/7?x=1",
		"pr:https://github.com/octo/repo/issues/7", "pr:https://github.com/octo/repo/pull/7/files",
	} {
		if _, err := NewRegistry(PR()).Bind(context.Background(), selector, 0, time.Now()); err == nil || !strings.Contains(err.Error(), "pr owner must be") {
			t.Fatalf("%s accepted or wrong error: %v", selector, err)
		}
	}
	if _, err := os.Stat(args); !os.IsNotExist(err) {
		t.Fatalf("invalid selector reached gh: %v", err)
	}
}

func TestPROffNamesTheOptIn(t *testing.T) {
	registry := NewRegistry(PID(), Session(), Heartbeat())
	if _, err := registry.Bind(context.Background(), "pr:octo/repo#7", 0, time.Now()); err == nil || !strings.Contains(err.Error(), "DIBS_PR_OWNER=1") {
		t.Fatalf("disabled pr owner error = %v", err)
	}
	if got := registry.Check(context.Background(), prReference(), time.Now()).Liveness; got != Unknown {
		t.Fatalf("recorded pr lease with the owner off = %s, want unknown", got)
	}
	if _, err := NewRegistry(PID(), Session(), Heartbeat(), PR()).Bind(context.Background(), "x:1", 0, time.Now()); err == nil || !strings.Contains(err.Error(), "heartbeat, or pr") {
		t.Fatalf("unknown kind error = %v", err)
	}
	if _, err := registry.Bind(context.Background(), "x:1", 0, time.Now()); err == nil || !strings.Contains(err.Error(), "use pid, session, or heartbeat") {
		t.Fatalf("default unknown kind error changed: %v", err)
	}
}
