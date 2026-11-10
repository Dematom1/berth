package adapter_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	composepkg "github.com/Dematom1/dibs/internal/adapter/compose"
	k3dpkg "github.com/Dematom1/dibs/internal/adapter/k3d"
	kindpkg "github.com/Dematom1/dibs/internal/adapter/kind"
)

func TestLegacyAndAdoptedSnapshotsThroughStateAlias(t *testing.T) {
	base := t.TempDir()
	legacy, alias, foreign := filepath.Join(base, "berth"), filepath.Join(base, "dibs"), t.TempDir()
	if err := os.Mkdir(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(legacy, alias); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	for _, definition := range []adapterpkg.Definition{kindpkg.New(nil), k3dpkg.New(nil), composepkg.New(nil)} {
		for _, adopted := range []bool{false, true} {
			for _, prefix := range []string{"berth-", "dibs-"} {
				file, suffix := "kubeconfig", id
				if definition.Name == "k3d" {
					file, suffix = "k3d", id[:26]
				} else if definition.Name == "compose" {
					file = "compose.json"
				}
				name := prefix + suffix
				if adopted {
					name = prefix + "adopted-lab"
				}
				ref := adapterpkg.ResourceRef{Adapter: definition.Name, Adopted: adopted, Name: name, Context: "test", DockerHost: "unix:///test.sock", File: filepath.Join(legacy, "resources", id, file)}
				metadata := adapterpkg.Metadata{LeaseID: id}
				if err := definition.Validate(filepath.Join(alias, "resources", id), metadata, ref); err != nil {
					t.Fatalf("%s adopted=%v prefix=%s: %v", definition.Name, adopted, prefix, err)
				}
				for _, bad := range []string{filepath.Join(foreign, "resources", id, file), filepath.Join(legacy, "resources", strings.Repeat("b", 32), file), filepath.Join(legacy, "resources", id, "wrong-file")} {
					ref.File = bad
					if err := definition.Validate(filepath.Join(alias, "resources", id), metadata, ref); err == nil {
						t.Fatalf("accepted unrelated snapshot: %s", bad)
					}
				}
			}
		}
	}
}
