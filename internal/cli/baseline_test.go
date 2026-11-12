package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	outputpkg "github.com/Dematom1/dibs/internal/output"
)

// UPDATE_GOLDEN does not regenerate these accepted command bytes.

func TestBaselineCommands(t *testing.T) {
	t.Setenv("DIBS_STATE", t.TempDir())
	fixedBudget(t)
	id := strings.Repeat("a", 32)
	cases := [][]string{{"list"}, {"ls"}, {"ps"}, {"who"}, {"sweep"}, {"sweep", "--confirm"}, {"release", id}, {"down", id, "--confirm"}, {"env", id}, {"extend", id, "--ttl", "1h"}, {"help"}, {"help", "unknown"}, {"unknown"}, {"ps", "--typo"}, {"ps", "extra"}, {"create", "--ttl", "0s"}, {"create", "--adapter", "unknown"}, {"create", "--adapter", "compose"}, {"create", "--file", "x"}, {"create", "--label", "bad"}, {"create", "--owner", "pid:-2"}, {"release"}, {"job"}, {"job", "--cores", "999", "--", "true"}, {"job", "--memory", "999GiB", "--", "true"}, {"job", "--wait", "-1s", "--", "true"}, {"machine"}, {"setup"}, {"--axi", "--json", "ps"}}
	for _, command := range []string{"", "create", "list", "extend", "release", "sweep", "job", "machine", "setup", "env", "agent"} {
		args := []string{"--help"}
		if command != "" {
			args = append([]string{command}, args...)
		}
		cases = append(cases, args)
	}
	for _, mode := range []outputpkg.Mode{outputpkg.Human, outputpkg.AXI, outputpkg.JSON} {
		for i, args := range cases {
			t.Run(fmt.Sprintf("%s/%02d", mode, i), func(t *testing.T) {
				var out bytes.Buffer
				diagnostics, err := os.CreateTemp(t.TempDir(), "stderr")
				if err != nil {
					t.Fatal(err)
				}
				previous := os.Stderr
				os.Stderr = diagnostics
				code := func() int {
					defer func() { os.Stderr = previous }()
					return runWithOutput(context.Background(), args, &out, mode)
				}()
				diagnostics.Close()
				stderr, err := os.ReadFile(diagnostics.Name())
				if err != nil || len(stderr) != 0 {
					t.Fatalf("stderr changed: %q (%v)", stderr, err)
				}
				got := fmt.Sprintf("exit: %d\nstdout:\n%s", code, out.String())
				path := filepath.Join("..", "..", "testdata", "baseline", fmt.Sprintf("%s-%02d.golden", mode, i))
				if os.Getenv("UPDATE_GOLDEN") == "1" {
					if err := os.WriteFile(path, []byte(got), 0600); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if got != string(want) {
					t.Fatalf("command %q changed\nwant:\n%s\ngot:\n%s", args, want, got)
				}
			})
		}
	}
}
