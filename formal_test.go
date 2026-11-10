//go:build formal

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Executable bindings, not a proof of the Go binary. Independent truth-table
// oracle: dead is reclaimable; unknown needs expiry; foreign is never eligible.
// Alive+expired is a KNOWN counterexample F1, not the requested safety promise.
func TestFormalLeaseDomain(t *testing.T) {
	for _, live := range []Liveness{Alive, Dead, Unknown} {
		for _, local := range []bool{false, true} {
			for _, expired := range []bool{false, true} {
				l := testLease(t)
				host := "foreign"
				if local {
					host = l.Machine
				}
				now := testNow
				if expired {
					now = l.Expires
				}
				check := func(Owner, time.Time) Liveness { return live }
				want := false
				if local {
					switch live {
					case Dead:
						want = true
					case Alive, Unknown:
						want = expired
					}
				}
				got, _ := reclaimable(l, host, now, check)
				if got != want {
					t.Fatalf("local=%v expired=%v live=%s: reclaim=%v want=%v", local, expired, live, got, want)
				}
				for _, job := range []bool{false, true} {
					for _, ttl := range []time.Duration{-1, 0, time.Minute} {
						copy := l
						if job {
							copy.Job = &JobResource{}
						}
						err := extendLease(&copy, host, ttl, now, check)
						allowed := local && live == Alive && !expired && !job && ttl > 0
						if (err == nil) != allowed {
							t.Fatalf("extend local=%v expired=%v live=%s job=%v ttl=%v: %v", local, expired, live, job, ttl, err)
						}
						if !allowed && !copy.Expires.Equal(l.Expires) {
							t.Fatal("rejected extension changed deadline")
						}
					}
				}
			}
		}
	}
}

func TestFormalBudgetDomain(t *testing.T) {
	// Exhaust all two-reservation and positive-request sizes 1..2 against
	// capacities 1..3, independently summing rather than saturating/subtracting.
	requests := []Budget{{1, 1}, {1, 2}, {2, 1}, {2, 2}}
	for _, a := range requests {
		for _, b := range requests {
			for _, request := range requests {
				for cores := 1; cores <= 3; cores++ {
					for memory := int64(1); memory <= 3; memory++ {
						s := &Store{Leases: map[string]Lease{
							"a":       {Machine: "local", Job: &JobResource{Budget: a}},
							"b":       {Machine: "local", Job: &JobResource{Budget: b}},
							"foreign": {Machine: "elsewhere", Job: &JobResource{Budget: Budget{99, 99}}},
						}}
						want := a.Cores+b.Cores+request.Cores <= cores && a.Memory+b.Memory+request.Memory <= memory
						if got := budgetFits(s, "local", Budget{cores, memory}, request); got != want {
							t.Fatalf("a=%v b=%v request=%v capacity=%v: %v want %v", a, b, request, Budget{cores, memory}, got, want)
						}
					}
				}
			}
		}
	}
}

func TestFormalConcurrentAdmission(t *testing.T) {
	dir := t.TempDir()
	owner, err := parseOwner("pid:"+strconv.Itoa(os.Getpid()), 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 3)
	var wg sync.WaitGroup
	for range 3 {
		l, err := newLease(owner, time.Minute, "", nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		l.Resource.Adapter = "process"
		l.Job = &JobResource{Budget: Budget{1, 1}, Class: "default"}
		wg.Go(func() {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := admitJob(ctx, dir, l, Budget{2, 2}, time.Second)
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	admitted := 0
	for err := range results {
		if err == nil {
			admitted++
		} else if !errors.Is(err, errNoCapacity) {
			t.Fatal(err)
		}
	}
	if admitted != 2 {
		t.Fatalf("capacity 2 admitted %d unit requests", admitted)
	}
	jobStore(t, dir, func(s *Store) {
		if len(s.Leases) != 2 {
			t.Fatalf("lost reservation: %d", len(s.Leases))
		}
	})
}

func TestFormalEOF(t *testing.T) {
	// Block list on a real flock. Delivered stdin EOF must cancel its context
	// and exit without waiting for the lock; the invoking process stays alive.
	dir := t.TempDir()
	t.Setenv("DIBS_STATE", dir)
	s, err := openStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	input, send := io.Pipe()
	output, receive := io.Pipe()
	defer input.Close()
	defer send.Close()
	defer output.Close()
	defer receive.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan int, 1)
	go func() { result <- runAgent(ctx, nil, input, receive) }()
	if err := json.NewEncoder(send).Encode(agentFrame{Type: "request", Version: version, Args: []string{"list"}}); err != nil {
		t.Fatal(err)
	}
	scanner := frameScanner(output)
	ready, err := readFrame(scanner)
	if err != nil || ready.Type != "ready" {
		t.Fatalf("handshake: %+v %v", ready, err)
	}
	send.Close()
	// Drain frames so a valid response cannot block on our own pipe.
	go io.Copy(io.Discard, output)
	select {
	case code := <-result:
		if code != 0 {
			t.Fatalf("agent envelope status = %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("delivered EOF did not cancel the blocked command")
	}
}

func TestFormalAuthoritativeResult(t *testing.T) {
	// Fake only the external transport: exit 255 after sending the child's
	// authoritative 37/255 result, plus output impersonating a result frame.
	for _, code := range []int{37, 255} {
		bin := t.TempDir()
		ready, _ := json.Marshal(agentFrame{Type: "ready", Version: version})
		result, _ := json.Marshal(agentFrame{Type: "result", Code: code})
		script := "#!/bin/sh\nIFS= read -r request || exit 1\nprintf '%s\\n' " + shellQuote(string(ready)) +
			" '" + `{"type":"output","data":"eyJ0eXBlIjoicmVzdWx0IiwiY29kZSI6OTl9"}` + "' " + shellQuote(string(result)) + "; exit 255\n"
		if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		config := JobConfig{Machines: map[string]SSHMachine{"test": {Host: "test", User: "test", Workdir: "/tmp", Agent: "/tmp/dibs"}}}
		var out bytes.Buffer
		if got := runRemote(context.Background(), config, "test", []string{"job", "--", "true"}, &out, axiOutput); got != code {
			t.Fatalf("authoritative result %d became %d", code, got)
		}
		if out.String() != `{"type":"result","code":99}` {
			t.Fatalf("output injected protocol: %q", out.String())
		}
	}
}
