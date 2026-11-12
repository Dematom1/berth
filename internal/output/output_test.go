package output

import (
	"bytes"
	"strings"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

func TestOutputSelection(t *testing.T) {
	for _, tc := range []struct {
		env  string
		mode Mode
		want Mode
	}{
		{"", Auto, AXI}, {"1", Auto, AXI},
		{"0", Auto, AXI}, {"1", JSON, JSON}, {"1", AXI, AXI},
	} {
		t.Setenv("DIBS_AXI", tc.env)
		if got := tc.mode.Effective(&bytes.Buffer{}); got != tc.want {
			t.Fatalf("mode %q env %q: %q", tc.mode, tc.env, got)
		}
	}
	// The same resolved mode is carried over SSH rather than redetected on its pipe.
	t.Setenv("DIBS_AXI", "1")
	if got := Auto.Effective(&Writer{Writer: &bytes.Buffer{}, Mode: Human}); got != Human {
		t.Fatal(got)
	}
}

func TestLeaseOutputSnapshots(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	l := leasepkg.Lease{ID: strings.Repeat("a", 32), Machine: "lab", Owner: ownerpkg.Reference{Kind: "pid", ID: "123"}, Created: now.Add(-5 * time.Minute), Expires: now.Add(time.Hour), TTL: time.Hour, Description: "test, lab", Labels: map[string]string{"task": "lab"}, ResourceRef: adapterpkg.ResourceRef{Adapter: "kind", Name: "dibs-lab", File: "/tmp/lab's kubeconfig"}}
	value := map[string]any{"count": 1, "leases": []map[string]any{LeaseView(l, ownerpkg.Alive, now, "/tmp/lab's kubeconfig")}, "records": []map[string]any{LeaseRecord(l, "/tmp/lab's kubeconfig")}}
	for _, mode := range []Mode{AXI, JSON} {
		var out bytes.Buffer
		Emit(&out, mode, value)
		snapshot(t, "lease-"+string(mode), out.String())
	}
	var full bytes.Buffer
	Emit(&Writer{Writer: &full, Mode: AXI, Full: true}, AXI, value)
	snapshot(t, "lease-full-axi", full.String())
	for name, data := range map[string]map[string]any{"list": value, "create": {"lease": LeaseView(l, ownerpkg.Alive, now, "/tmp/lab's kubeconfig"), "record": LeaseRecord(l, "/tmp/lab's kubeconfig")}} {
		var out bytes.Buffer
		humanObject(&out, data, now)
		snapshot(t, name+"-human", out.String())
		if strings.Contains(out.String(), "1073741824B") || !strings.Contains(out.String(), "lab's kubeconfig") || !strings.Contains(out.String(), "in 1h0m0s") {
			t.Fatal(out.String())
		}
	}
}
func TestProtectedLeaseListSnapshot(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	plain := leasepkg.Lease{ID: strings.Repeat("a", 32), Machine: "lab", Owner: ownerpkg.Reference{Kind: "pid", ID: "123"}, Created: now, Expires: now.Add(time.Hour), TTL: time.Hour, ResourceRef: adapterpkg.ResourceRef{Adapter: "none"}}
	protected := plain
	protected.ID = strings.Repeat("b", 32)
	protected.ProtectLiveOwner = true
	value := map[string]any{"count": 2, "leases": []map[string]any{
		LeaseView(plain, ownerpkg.Alive, now, ""),
		LeaseView(protected, ownerpkg.Alive, now, ""),
	}}
	var out bytes.Buffer
	Emit(&out, AXI, value)
	snapshot(t, "protected-list-axi", out.String())
}

func TestAXIPresentationDoesNotMutate(t *testing.T) {
	longText := strings.Repeat("界", 501)
	// Presentation must not mutate the canonical view, including labels.
	data := map[string]any{"lease": map[string]any{"resources": longText, "labels": map[string]string{"long": longText}}}
	compact := axiView(data, "dibs ps --full")
	if compact["lease"].(map[string]any)["resources"] == longText || data["lease"].(map[string]any)["resources"] != longText || data["lease"].(map[string]any)["labels"].(map[string]string)["long"] != longText {
		t.Fatal("presentation mutated data or failed to truncate")
	}
}

func TestTOONQuotesKeysInEveryShape(t *testing.T) {
	key := "x\nerror: injected"
	for _, tc := range []struct {
		name string
		data map[string]any
		want string
	}{
		{"scalar", map[string]any{key: "value"}, `"x\nerror: injected": value`},
		{"object", map[string]any{key: map[string]any{"child": "value"}}, `"x\nerror: injected":`},
		{"labels", map[string]any{key: map[string]string{"child": "value"}}, `"x\nerror: injected":`},
		{"empty table", map[string]any{key: []map[string]any{}}, `"x\nerror: injected": []`},
		{"table", map[string]any{key: []map[string]any{{"child": "value"}}}, `"x\nerror: injected"[1]{child}:`},
		{"column", map[string]any{"rows": []map[string]any{{key: "value"}}}, `rows[1]{"x\nerror: injected"}:`},
		{"empty strings", map[string]any{key: []string{}}, `"x\nerror: injected": []`},
		{"strings", map[string]any{key: []string{"value"}}, `"x\nerror: injected"[1]: value`},
		{"space in key", map[string]any{"two words": "value"}, `"two words": value`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			Emit(&out, AXI, tc.data)
			if !strings.Contains(out.String(), tc.want) || strings.Contains(out.String(), "\nerror: injected") {
				t.Fatalf("key changed TOON structure: %q; want %q", out.String(), tc.want)
			}
		})
	}
}

func TestPendingAndReconcileSnapshots(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	l := leasepkg.Lease{ID: strings.Repeat("b", 32), Machine: "lab", Owner: ownerpkg.Reference{Kind: "heartbeat", ID: "/run/task.beat"}, Created: now, Expires: now.Add(time.Hour), TTL: time.Hour, Key: "task-7:lab", ResourceRef: adapterpkg.ResourceRef{Adapter: "kind", Name: "lab-7", Context: "default", Pending: true}}
	plain := l
	plain.ID, plain.Key, plain.ResourceRef = strings.Repeat("d", 32), "", adapterpkg.ResourceRef{Adapter: "none"}
	list := map[string]any{"count": 2, "leases": []map[string]any{LeaseView(plain, ownerpkg.Alive, now, ""), LeaseView(l, ownerpkg.Alive, now, "")}}
	results := map[string]any{"contract": 1, "status": "preview", "count": 2, "results": []map[string]any{
		{"id": l.ID, "status": "bindable", "resource_adapter": "kind", "resource_name": "lab-7", "matches": []string{"lab-7"}, "conflicts": []string{}},
		{"id": strings.Repeat("c", 32), "status": "absent", "resource_adapter": "compose", "resource_name": "", "matches": []string{}, "conflicts": []string{}},
	}}
	for name, value := range map[string]map[string]any{"pending-list": list, "reconcile": results} {
		for _, mode := range []Mode{AXI, JSON} {
			var out bytes.Buffer
			Emit(&out, mode, value)
			snapshot(t, name+"-"+string(mode), out.String())
		}
		var out bytes.Buffer
		humanObject(&out, value, now)
		snapshot(t, name+"-human", out.String())
	}
}
