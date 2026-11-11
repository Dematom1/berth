package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}

func TestRenamedConfigFallback(t *testing.T) {
	for _, xdg := range []bool{false, true} {
		t.Run(strconv.FormatBool(xdg), func(t *testing.T) {
			unsetEnv(t, "DIBS_CONFIG")
			unsetEnv(t, "BERTH_CONFIG")
			home := t.TempDir()
			t.Setenv("HOME", home)
			base := filepath.Join(home, ".config")
			t.Setenv("XDG_CONFIG_HOME", "")
			if xdg {
				base = t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", base)
			}
			for i, product := range []string{"berth", "dibs"} {
				dir := filepath.Join(base, product)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"cores":`+strconv.Itoa(4+i)+`,"room_cores":1,"memory":"8GiB","room_memory":"1GiB"}`), 0600); err != nil {
					t.Fatal(err)
				}
				c, err := Load()
				if err != nil || c.Cores != 4+i {
					t.Fatalf("%s config: %+v, %v", product, c, err)
				}
			}
			if err := os.WriteFile(filepath.Join(base, "dibs", "config.json"), []byte("broken"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err == nil {
				t.Fatal("invalid preferred config silently fell back")
			}
			preferred := filepath.Join(base, "dibs", "config.json")
			if err := os.Remove(preferred); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(preferred, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err == nil {
				t.Fatal("unreadable preferred config silently fell back")
			}
			for _, product := range []string{"BERTH", "DIBS"} {
				t.Setenv(product+"_CONFIG", filepath.Join(base, "explicit-missing"))
				if _, err := Load(); err == nil {
					t.Fatal("explicit missing config silently fell back")
				}
			}
		})
	}
}
