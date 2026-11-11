package config

import (
	budgetpkg "github.com/Dematom1/dibs/internal/budget"
	"os"
	"path/filepath"
	"testing"
)

func TestJobBudgetAndConfig(t *testing.T) {
	for _, tc := range []struct {
		cores      int
		memory     int64
		wantCores  int
		wantMemory int64
	}{
		{16, 32 * gib, 12, 24 * gib}, {1, gib, 1, gib / 2}, {4, 4 * gib, 3, 3 * gib},
	} {
		c := defaultJobConfig(tc.cores, tc.memory)
		got, err := c.Available()
		if err != nil || got != (budgetpkg.Amount{Cores: tc.wantCores, Memory: tc.wantMemory}) {
			t.Fatalf("owner room: %+v %v", got, err)
		}
	}
	c := defaultJobConfig(4, 4*gib)
	c.RoomCores = 4
	if _, err := c.Available(); err == nil {
		t.Fatal("owner cores consumed entire budget")
	}
	c = defaultJobConfig(4, 4*gib)
	c.RoomMemory = "4GiB"
	if _, err := c.Available(); err == nil {
		t.Fatal("owner memory consumed entire budget")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DIBS_CONFIG", path)
	for _, data := range []string{`null`, `[]`, `{"classes":{"":{"cores":1,"memory":"1GiB","timeout":"1h"}}}`, `{"typo":1}`, `{"cores":0}`, `{"room_memory":"999999GiB"}`, `{"classes":{"suite":{"cores":1,"memory":"1MiB","timeout":"0s"}}}`, `{} {}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(); err == nil {
			t.Fatalf("accepted bad config %s", data)
		}
	}
}

func TestPartialConfigKeepsDefaultClass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"classes":{"suite":{"cores":1,"memory":"1GiB","timeout":"1h"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_CONFIG", path)
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := config.Class("default"); err != nil {
		t.Fatalf("default class lost: %v", err)
	}
	if _, _, err := config.Class("suite"); err != nil {
		t.Fatalf("configured class lost: %v", err)
	}
}
