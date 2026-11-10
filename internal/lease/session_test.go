package lease

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestSessionWaiterStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := testManager().openStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := bindOwner("session:"+strconv.Itoa(os.Getpid()), 0, time.Now())
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Waiters = []waiter{{ID: "0123456789abcdef0123456789abcdef", Machine: "remote", Owner: owner, Expires: time.Now().Add(time.Minute)}}
	err = s.Save()
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err = testManager().openStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.Waiters) != 1 || s.Waiters[0].Owner != owner {
		t.Fatal("session waiter lost its owner identity")
	}
}
