package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	devel := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
			{Key: "vcs.modified", Value: "false"},
		},
	}
	dirty := *devel
	dirty.Settings = append([]debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}, devel.Settings[:1]...)
	for _, tt := range []struct {
		name, override string
		info           *debug.BuildInfo
		want           string
	}{
		{"module", "", &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0"}}, "0.2.0"},
		{"prerelease", "", &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0-rc.1"}}, "0.2.0-rc.1"},
		{"checkout", "", devel, "devel-0123456789ab"},
		{"modified checkout", "", &dirty, "devel-0123456789ab-dirty"},
		{"short revision", "", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}}}, "devel-abc"},
		{"missing build info", "", nil, "devel"},
		{"empty build info", "", &debug.BuildInfo{}, "devel"},
		{"no vcs", "", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "devel"},
		{"override module", "custom", &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0"}}, "custom"},
		{"override checkout", "v0.2.0", &dirty, "v0.2.0"},
		{"override without build info", "0.2.0", nil, "0.2.0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.override, tt.info); got != tt.want {
				t.Fatalf("resolveVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}
