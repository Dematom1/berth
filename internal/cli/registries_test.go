package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestRegistries(t *testing.T) {
	resources := newAdapters()
	if got := strings.Join(resources.CreateNames(), ","); got != "none,kind,k3d,compose" {
		t.Fatal(got)
	}
	for _, name := range resources.CreateNames() {
		d, ok := resources.Lookup(name)
		if !ok || d.Name != name {
			t.Fatal(name)
		}
		file := ""
		if d.RequiresFile {
			file = "fixture.yaml"
		}
		if err := resources.CheckCreate(name, file); err != nil {
			t.Fatal(err)
		}
	}
	if err := resources.CheckCreate("process", ""); err == nil {
		t.Fatal("internal process reservation became a create adapter")
	}
	if got := newOwners().Names(); !reflect.DeepEqual(got, []string{"pid", "session", "heartbeat"}) {
		t.Fatal(got)
	}
}
