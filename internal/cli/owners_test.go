package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	configpkg "github.com/Dematom1/dibs/internal/config"
)

func TestPROwnerOptIn(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DIBS_CONFIG", config)
	t.Setenv("DIBS_STATE", t.TempDir())
	for _, tc := range []struct {
		name, config, env string
		setEnv, want      bool
	}{
		{name: "absent", want: false},
		{name: "config off", config: `{"pr_owner":false}`, want: false},
		{name: "config on", config: `{"pr_owner":true}`, want: true},
		{name: "env on", env: "1", setEnv: true, want: true},
		{name: "env off overrides config", config: `{"pr_owner":true}`, env: "0", setEnv: true, want: false},
		{name: "env on overrides config", config: `{"pr_owner":false}`, env: "true", setEnv: true, want: true},
		{name: "invalid env stays off", config: `{"pr_owner":true}`, env: "yes", setEnv: true, want: false},
		{name: "invalid config stays off", config: `{"pr_owner":true`, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			os.Remove(config)
			if tc.config != "" {
				if err := os.WriteFile(config, []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("BERTH_PR_OWNER", "1")
			t.Setenv("DIBS_PR_OWNER", tc.env)
			if !tc.setEnv {
				os.Unsetenv("DIBS_PR_OWNER")
			}
			if got := slices.Contains(newOwners().Names(), "pr"); got != tc.want {
				t.Fatalf("pr registered = %v, want %v", got, tc.want)
			}
			if !tc.want {
				result := invoke(t, 2, "create", "--owner", "pr:octocat/Hello-World#32")
				if msg, _ := result["error"].(string); !strings.Contains(msg, "DIBS_PR_OWNER=1") {
					t.Fatalf("disabled pr owner error does not name the opt-in: %v", result)
				}
			}
		})
	}
}

func TestConfigAcceptsPROwner(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DIBS_CONFIG", config)
	if err := os.WriteFile(config, []byte(`{"pr_owner":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if c, err := configpkg.Load(); err != nil || !c.PROwner {
		t.Fatalf("strict config load rejected pr_owner: %+v, %v", c, err)
	}
}
