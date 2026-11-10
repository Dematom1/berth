package compose

import (
	"context"
	"encoding/json"
	"fmt"
	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	"strings"
)

func verifyComposeAdoption(ctx context.Context, ref adapterpkg.ResourceRef, config map[string]any, run adapterpkg.CommandRunner) error {
	for _, kind := range []string{"volume", "network"} {
		out, err := run(ctx, adapterpkg.DockerEnv(ref), "docker", "--context", ref.Context, kind, "ls", "--format", "{{.Name}}")
		if err != nil {
			return err
		}
		existing := map[string]bool{}
		for _, name := range strings.Fields(string(out)) {
			existing[name] = true
		}
		entries, _ := config[kind+"s"].(map[string]any)
		for key, entry := range entries {
			object, _ := entry.(map[string]any)
			if external, _ := object["external"].(bool); external {
				continue
			}
			name, _ := object["name"].(string)
			if name == "" {
				name = ref.Name + "_" + key
			}
			if !existing[name] {
				continue
			}
			out, err := run(ctx, adapterpkg.DockerEnv(ref), "docker", "--context", ref.Context, kind, "inspect", name, "--format", "{{json .Labels}}")
			if err != nil {
				return err
			}
			var labels map[string]string
			if json.Unmarshal(out, &labels) != nil || labels["com.docker.compose.project"] != ref.Name {
				return fmt.Errorf("%s %q is not labeled as part of this project; mark shared resources external before adoption", kind, name)
			}
		}
	}
	return nil
}
