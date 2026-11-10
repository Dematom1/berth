package cli

import (
	"bytes"
	"context"
	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	remotepkg "github.com/Dematom1/dibs/internal/remote"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLegacyComposeLabelsCleanedByRecordedProject(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_STATE", dir)
	t.Setenv("DIBS_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
	id := invoke(t, 0, "create", "--ttl", "1h")["record"].(map[string]any)["id"].(string)
	s, err := openStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	l := s.Leases[id]
	l.ResourceRef = adapterpkg.ResourceRef{Adapter: "compose", Name: "berth-" + l.ID, Context: "old-context", File: filepath.Join(s.dir, "resources", l.ID, "compose.json")}
	if err := os.MkdirAll(filepath.Dir(l.ResourceRef.File), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"services":{"web":{"image":"old","labels":{"io.berth.lease":"` + l.ID + `"}}}}`)
	if err := os.WriteFile(l.ResourceRef.File, data, 0600); err != nil {
		t.Fatal(err)
	}
	s.Leases[l.ID] = l
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	t.Setenv("DIBS_STATE", s.dir)
	if invoke(t, 0, "ps")["count"] != float64(1) {
		t.Fatal("ps lost old-label lease")
	}
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "docker.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + remotepkg.ShellQuote(log) + "\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	invoke(t, 0, "release", l.ID, "--confirm")
	if invoke(t, 0, "ps")["count"] != float64(0) {
		t.Fatal("release did not reclaim old-label resource")
	}
	s, err = openStore(context.Background(), s.dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Expires = time.Now().Add(-time.Hour)
	s.Leases[l.ID] = l
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if len(invoke(t, 0, "sweep", "--confirm")["reclaimed"].([]any)) != 1 {
		t.Fatal("sweep did not reclaim old-label resource")
	}
	commands, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(commands), "compose -p "+l.ResourceRef.Name+" -f "+l.ResourceRef.File+" down --volumes --remove-orphans") || strings.Count(string(commands), "label=com.docker.compose.project="+l.ResourceRef.Name) != 6 {
		t.Fatalf("cleanup must verify the recorded project, independent of io label namespace: %s, %v", commands, err)
	}
	unchanged, err := os.ReadFile(l.ResourceRef.File)
	if err != nil || !bytes.Equal(unchanged, data) {
		t.Fatal("cleanup rewrote the legacy snapshot", err)
	}
}
