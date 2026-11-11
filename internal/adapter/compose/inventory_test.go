package compose

import (
	"context"
	"encoding/json"
	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	"strings"
	"testing"
)

func TestComposeAdoptionRequiresProjectLabels(t *testing.T) {
	ref := adapterpkg.ResourceRef{Adapter: "compose", Name: "lab", Context: "test"}
	config := map[string]any{"volumes": map[string]any{"data": map[string]any{"name": "lab_data"}}}
	for _, project := range []string{"", "other", "lab"} {
		runner := func(_ context.Context, _ []string, _ string, args ...string) ([]byte, error) {
			if strings.Contains(strings.Join(args, " "), "volume ls") {
				return []byte("lab_data"), nil
			}
			if strings.Contains(strings.Join(args, " "), "inspect") {
				return json.Marshal(map[string]string{"com.docker.compose.project": project})
			}
			return nil, nil
		}
		err := verifyComposeAdoption(context.Background(), ref, config, runner)
		if (err == nil) != (project == "lab") {
			t.Fatalf("project %q: %v", project, err)
		}
	}
}
