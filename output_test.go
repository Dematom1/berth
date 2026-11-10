package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestLeaseExpiryWarnings(t *testing.T) {
	lease := testLease(t)
	lease.TTL = 12 * time.Minute
	lease.Expires = testNow.Add(4 * time.Minute)
	for _, tc := range []struct {
		name string
		live Liveness
		now  time.Time
		want bool
	}{
		{"last third", Alive, testNow, true},
		{"before last third", Alive, testNow.Add(-time.Nanosecond), false},
		{"expired", Alive, lease.Expires, false},
		{"dead", Dead, testNow, false},
		{"unknown", Unknown, testNow, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := leaseView(lease, tc.live, tc.now)
			if view["expiring_soon"] != tc.want || (view["warning"] != "") != tc.want {
				t.Fatalf("expiry warning: %+v", view)
			}
		})
	}
	view := leaseView(lease, Alive, testNow)
	var out bytes.Buffer
	emit(&out, false, map[string]any{"leases": []map[string]any{{"expiring_soon": view["expiring_soon"], "orphan_suspected": view["orphan_suspected"], "warning": view["warning"]}}})
	want := "leases[1]{expiring_soon,orphan_suspected,warning}:\n  true,false,expires in 4m0s - extend or it will be reclaimed\n"
	if out.String() != want {
		t.Fatalf("AXI warning bytes changed: %q", out.String())
	}
	lease.Job = &JobResource{OrphanSuspected: true}
	view = leaseView(lease, Alive, testNow)
	if view["orphan_suspected"] != true || view["warning"] != "expires in 4m0s - job deadline cannot be extended" {
		t.Fatalf("job warning misled owner: %+v", view)
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
			emit(&out, axiOutput, tc.data)
			if !strings.Contains(out.String(), tc.want) || strings.Contains(out.String(), "\nerror: injected") {
				t.Fatalf("key changed TOON structure: %q; want %q", out.String(), tc.want)
			}
		})
	}
}
