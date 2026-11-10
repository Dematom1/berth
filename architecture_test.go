package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

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
	seen := map[string]bool{}
	err := filepath.WalkDir("internal", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		pkg := filepath.ToSlash(strings.TrimPrefix(filepath.Dir(path), "internal"+string(filepath.Separator)))
		edges, ok := allowed[pkg]
		if !ok {
			t.Errorf("unreviewed production package %s", pkg)
		}
		seen[pkg] = true
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			target, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if strings.HasPrefix(target, prefix) {
				dependency := strings.TrimPrefix(target, prefix)
				if !strings.Contains(" "+edges+" ", " "+dependency+" ") {
					t.Errorf("forbidden production edge %s -> %s in %s", pkg, dependency, path)
				}
			}
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "init" {
				t.Errorf("register explicitly, not in init: %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for pkg := range allowed {
		if !seen[pkg] {
			t.Errorf("missing production package %s", pkg)
		}
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if path != "main.go" && !strings.HasSuffix(path, "_test.go") {
			t.Errorf("root implementation must stay in internal packages: %s", path)
		}
	}
}
