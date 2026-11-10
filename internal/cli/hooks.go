package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	atomicfilepkg "github.com/Dematom1/berth/internal/atomicfile"
	outputpkg "github.com/Dematom1/berth/internal/output"
	"github.com/pelletier/go-toml/v2"
)

// Inline commands need neither a persistent executable path nor path repair.

const hookMarker = "# berth-axi"

const sessionHook = hookMarker + `
env_file=${CODEX_ENV_FILE:-${CLAUDE_ENV_FILE:-}}
if [ -n "$env_file" ]; then
  printf '%s\n' 'export BERTH_AXI=1' >> "$env_file"
else
  printf '%s\n' 'Use berth --axi for agent output, or export BERTH_AXI=1.'
fi`

const piHook = `// berth-axi: managed by berth setup hooks
export default function (pi: { on(event: "session_start", handler: () => void): void }) {
  pi.on("session_start", () => { process.env.BERTH_AXI = "1"; });
}
`

const opencodeHook = `// berth-axi: managed by berth setup hooks
export const BerthAXI = async () => ({
  "shell.env": async (_input, output) => { output.env.BERTH_AXI = "1"; },
});
`

func configHome(env, fallback string) string {
	if dir := os.Getenv(env); dir != "" {
		return dir
	}
	return fallback
}

func runSetup(args []string, out io.Writer, mode outputpkg.Mode) int {
	fs := flag.NewFlagSet("setup hooks", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	remove := fs.Bool("remove", false, "remove berth-managed hooks")
	wantHelp := fs.Bool("help", false, "help")
	outputpkg.Flags(fs, &mode)
	if len(args) == 1 && args[0] == "--help" {
		emitHelp(out, mode, "setup")
		return 0
	}
	if len(args) == 0 || args[0] != "hooks" {
		return outputpkg.Error(out, mode, 2, "expected setup hooks", "Run berth setup hooks [--remove]")
	}
	if err := parseFlags(fs, args[1:]); err != nil {
		return outputpkg.Error(out, mode, 2, err.Error(), help("setup"))
	}
	if *wantHelp {
		emitHelp(out, mode, "setup")
		return 0
	}
	if fs.NArg() != 0 {
		return outputpkg.Error(out, mode, 2, "unexpected arguments", help("setup"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return outputpkg.Error(out, mode, 1, err.Error(), "Set HOME and retry berth setup hooks")
	}
	claude := configHome("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	codex := configHome("CODEX_HOME", filepath.Join(home, ".codex"))
	pi := configHome("PI_CODING_AGENT_DIR", filepath.Join(home, ".pi", "agent"))
	xdg := configHome("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// Prepare every edit first: malformed user configuration must not be replaced.
	targets := []struct {
		path string
		edit func([]byte, bool) ([]byte, error)
	}{
		{filepath.Join(claude, "settings.json"), editSessionHook},
		{filepath.Join(codex, "hooks.json"), editSessionHook},
		{filepath.Join(codex, "config.toml"), editCodexFeature},
		{filepath.Join(pi, "extensions", "berth-axi.ts"), managedPlugin(piHook)},
		{filepath.Join(xdg, "opencode", "plugins", "berth-axi.js"), managedPlugin(opencodeHook)},
	}
	type change struct {
		path string
		data []byte
	}
	changes := []change{}
	for _, target := range targets {
		data, readErr := os.ReadFile(target.path)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return outputpkg.Error(out, mode, 1, readErr.Error(), "Check config permissions and retry berth setup hooks")
		}
		next, editErr := target.edit(data, *remove)
		if editErr != nil {
			return outputpkg.Error(out, mode, 1, target.path+": "+editErr.Error(), "Fix the config and retry berth setup hooks")
		}
		if !bytes.Equal(data, next) {
			changes = append(changes, change{target.path, next})
		}
	}
	changed := []string{}
	for _, change := range changes {
		if change.data == nil {
			err = os.Remove(change.path)
		} else {
			err = os.MkdirAll(filepath.Dir(change.path), 0700)
			if err == nil {
				err = atomicfilepkg.Write(change.path, change.data)
			}
		}
		if err != nil {
			return outputpkg.Error(out, mode, 1, err.Error(), "Earlier changes may be installed; fix permissions and rerun berth setup hooks")
		}
		changed = append(changed, change.path)
	}
	status := "installed"
	if *remove {
		status = "removed"
	}
	if len(changes) == 0 {
		status = "unchanged (no-op)"
	}
	outputpkg.Emit(out, mode, map[string]any{"status": status, "changed": changed, "help": []string{"Restart agent sessions to apply the change"}})
	return 0
}

func editSessionHook(data []byte, remove bool) ([]byte, error) {
	config := map[string]any{}
	if len(data) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&config); err != nil {
			return nil, err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return nil, errors.New("invalid trailing JSON")
		}
		if config == nil {
			return nil, errors.New("expected a JSON object")
		}
	}
	before, _ := json.Marshal(config)
	hooks, ok := config["hooks"].(map[string]any)
	if !ok {
		if _, exists := config["hooks"]; exists {
			return nil, errors.New("hooks must be an object")
		}
		hooks = map[string]any{}
	}
	groups, ok := hooks["SessionStart"].([]any)
	if !ok && hooks["SessionStart"] != nil {
		return nil, errors.New("SessionStart must be an array")
	}
	kept := []any{}
	for _, entry := range groups {
		group, ok := entry.(map[string]any)
		if !ok {
			return nil, errors.New("SessionStart group must be an object")
		}
		commands, ok := group["hooks"].([]any)
		if !ok {
			return nil, errors.New("SessionStart hooks must be an array")
		}
		remaining := []any{}
		for _, command := range commands {
			hook, ok := command.(map[string]any)
			if !ok {
				return nil, errors.New("hook must be an object")
			}
			text, _ := hook["command"].(string)
			if strings.HasPrefix(text, hookMarker+"\n") {
				continue
			}
			remaining = append(remaining, hook)
		}
		if len(remaining) != 0 || len(commands) == 0 {
			group["hooks"] = remaining
			kept = append(kept, group)
		}
	}
	if !remove {
		kept = append(kept, map[string]any{"matcher": "", "hooks": []any{map[string]any{"type": "command", "command": sessionHook}}})
	}
	if len(kept) == 0 {
		delete(hooks, "SessionStart")
	} else {
		hooks["SessionStart"] = kept
	}
	if len(hooks) == 0 {
		delete(config, "hooks")
	} else {
		config["hooks"] = hooks
	}
	after, _ := json.Marshal(config)
	if bytes.Equal(before, after) {
		return data, nil
	}
	next, err := json.MarshalIndent(config, "", "  ")
	return append(next, '\n'), err
}

func managedPlugin(content string) func([]byte, bool) ([]byte, error) {
	return func(data []byte, remove bool) ([]byte, error) {
		if len(data) != 0 && !bytes.HasPrefix(data, []byte("// berth-axi: managed by berth setup hooks\n")) {
			return nil, errors.New("refusing to replace an unmanaged file")
		}
		if remove {
			return nil, nil
		}
		return []byte(content), nil
	}
}

var featuresHeader = regexp.MustCompile(`^\s*\[features\]\s*(?:#.*)?$`)

var hooksFeature = regexp.MustCompile(`^(\s*(?:features\.)?hooks\s*=\s*)(true|false)(\s*(?:#.*)?)$`)

// Validate both documents and permit only the hooks toggle to change. This keeps
// comments intact without treating arbitrary TOML as a line-oriented format.
func editCodexFeature(data []byte, remove bool) ([]byte, error) {
	before := map[string]any{}
	if err := toml.Unmarshal(data, &before); err != nil {
		return nil, err
	}
	next, err := editCodexFeatureText(data, remove)
	if err != nil {
		return nil, err
	}
	after := map[string]any{}
	if err := toml.Unmarshal(next, &after); err != nil {
		return nil, fmt.Errorf("unsupported hooks feature layout: %w", err)
	}
	if !remove {
		features, _ := after["features"].(map[string]any)
		if features["hooks"] != true {
			return nil, errors.New("could not enable features.hooks; use [features] with hooks = true")
		}
	}
	for _, config := range []map[string]any{before, after} {
		if value, exists := config["features"]; exists {
			features, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("features must be a table")
			}
			if hook, exists := features["hooks"]; exists {
				if _, ok := hook.(bool); !ok {
					return nil, errors.New("features.hooks must be a boolean")
				}
			}
			delete(features, "hooks")
			if len(features) == 0 {
				delete(config, "features")
			}
		}
	}
	if !reflect.DeepEqual(before, after) {
		return nil, errors.New("unsupported hooks feature layout; no config files changed")
	}
	return next, nil
}

// Only the hooks feature is edited; no TOML rewrite or unrelated feature toggles.
func editCodexFeatureText(data []byte, remove bool) ([]byte, error) {
	text := string(data)
	lines := strings.Split(text, "\n")
	inFeatures, inRoot, found := false, true, false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inFeatures, inRoot = featuresHeader.MatchString(line), false
		}
		match := hooksFeature.FindStringSubmatch(line)
		if match == nil || !(inFeatures && !strings.Contains(match[1], ".") || inRoot && strings.Contains(match[1], ".")) {
			continue
		}
		if found {
			return nil, errors.New("duplicate hooks feature")
		}
		found = true
		managed := match[3] == " # berth-axi" || strings.HasPrefix(match[3], " # berth-axi was-false")
		if remove {
			if managed {
				if strings.HasPrefix(match[3], " # berth-axi was-false") {
					lines[i] = match[1] + "false" + strings.TrimPrefix(match[3], " # berth-axi was-false")
				} else {
					lines[i] = "\x00"
				}
			}
		} else if match[2] == "false" {
			lines[i] = match[1] + "true # berth-axi was-false" + match[3]
		}
	}
	if !found && !remove {
		for i, line := range lines {
			if featuresHeader.MatchString(line) {
				lines[i] += "\nhooks = true # berth-axi"
				return []byte(strings.Join(lines, "\n")), nil
			}
			if strings.HasPrefix(strings.TrimSpace(line), "features") && strings.Contains(line, "=") && !strings.Contains(line, ".") {
				return nil, errors.New("inline features table is unsupported; use [features] with hooks = true")
			}
		}
		return []byte("features.hooks = true # berth-axi\n" + text), nil
	}
	kept := lines[:0]
	for _, line := range lines {
		if line != "\x00" {
			kept = append(kept, line)
		}
	}
	return []byte(strings.Join(kept, "\n")), nil
}
