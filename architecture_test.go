package main

import (
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"
)

type listedPackage struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	Imports    []string
}

func TestPackageGraph(t *testing.T) {
	allowed := map[string]string{
		"cli":          "lease owner adapter adapter/none adapter/kind adapter/k3d adapter/compose job config output remote budget atomicfile",
		"job":          "lease owner budget config output remote",
		"remote":       "config output",
		"output":       "lease owner config budget",
		"lease":        "adapter owner budget atomicfile",
		"adapter/none": "adapter", "adapter/kind": "adapter", "adapter/k3d": "adapter", "adapter/compose": "adapter atomicfile",
		"config": "budget", "adapter": "", "owner": "", "budget": "", "atomicfile": "",
	}
	const prefix = "github.com/Dematom1/berth/internal/"
	cmd := exec.Command("go", "list", "-json", "./...")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	seen := map[string]bool{}
	for {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(pkg.ImportPath, prefix) {
			name := strings.TrimPrefix(pkg.ImportPath, prefix)
			edges, ok := allowed[name]
			if !ok {
				t.Errorf("unreviewed production package %s", name)
				continue
			}
			seen[name] = true
			for _, target := range pkg.Imports {
				if strings.HasPrefix(target, prefix) {
					dependency := strings.TrimPrefix(target, prefix)
					if !strings.Contains(" "+edges+" ", " "+dependency+" ") {
						t.Errorf("forbidden production edge %s -> %s", name, dependency)
					}
				}
			}
		}
		if pkg.ImportPath == "github.com/Dematom1/berth" {
			for _, file := range pkg.GoFiles {
				if file != "main.go" {
					t.Errorf("root implementation must stay in internal packages: %s", file)
				}
			}
		}
	}
	for name := range allowed {
		if !seen[name] {
			t.Errorf("missing production package %s", name)
		}
	}
}
