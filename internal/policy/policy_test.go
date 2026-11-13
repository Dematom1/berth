package policy

import (
	"strings"
	"testing"
	"time"
)

var kinds = Kinds{Resources: []string{"none", "kind", "k3d", "compose", "shell"}, Owners: []string{"pid", "pr", "exec"}, Idle: func(r string) bool { return r == "shell" }}

func TestDefaultsCoverEveryResource(t *testing.T) {
	table, err := Table(nil, kinds)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range kinds.Resources {
		p, ok := Resolve(table, r, "pid")
		if !ok || p.Row != r || p.check(r) != nil {
			t.Fatalf("default %s = %+v, %v", r, p, ok)
		}
	}
	if p := table["shell"]; p.Binding != Hybrid || p.Idle != 30*time.Minute {
		t.Fatalf("shell default = %+v", p)
	}
	if p := table["kind"]; p.Binding != Time || p.TTL != 4*time.Hour || p.MaxLifetime != 24*time.Hour {
		t.Fatalf("lab default = %+v", p)
	}
}

func TestConfiguredRowsReplaceAndNarrow(t *testing.T) {
	table, err := Table(map[string]Row{
		"kind":    {Binding: "owner", TTL: "1h"},
		"kind/pr": {Binding: "owner", TTL: "2h", Grace: "10m", Action: "report"},
	}, kinds)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := Resolve(table, "kind", "pr"); p.Row != "kind/pr" || p.Grace != 10*time.Minute || p.Action != Report {
		t.Fatalf("owner-narrowed row = %+v", p)
	}
	if p, _ := Resolve(table, "kind", "pid"); p.Row != "kind" || p.Binding != Owner || p.MaxLifetime != 0 {
		t.Fatalf("replaced row = %+v", p)
	}
	for row, want := range map[string]Row{
		"binding must be time, owner, or hybrid":    {Binding: "forever"},
		"action must be destroy or report":          {Binding: "time", Action: "delete"},
		"non-negative Go duration":                  {Binding: "time", TTL: "soon"},
		"max_lifetime must be at least ttl":         {Binding: "time", TTL: "4h", MaxLifetime: "1h"},
		"owner binding has no idle or max_lifetime": {Binding: "owner", MaxLifetime: "24h"},
		"idle needs a hybrid binding":               {Binding: "time", Idle: "5m"},
		"hybrid binding needs idle or max_lifetime": {Binding: "hybrid"},
		"renew must not exceed ttl":                 {Binding: "time", TTL: "1h", Renew: "2h"},
		"no idle signal":                            {Binding: "hybrid", Idle: "5m"},
		"job deadlines come from job classes":       {Binding: "time"},
		"key must be a resource kind":               {Binding: "time"},
		"optionally /<owner kind> (pid, pr, exec)":  {Binding: "time"},
	} {
		key := map[string]string{"no idle signal": "kind", "job deadlines come from job classes": "process", "key must be a resource kind": "vm", "optionally /<owner kind> (pid, pr, exec)": "kind/github"}[row]
		if key == "" {
			key = "shell"
		}
		if _, err := Table(map[string]Row{key: want}, kinds); err == nil || !strings.Contains(err.Error(), row) {
			t.Fatalf("%s %+v = %v, want %q", key, want, err, row)
		}
	}
}

func TestOverrideOnlyTightens(t *testing.T) {
	table, _ := Table(nil, kinds)
	lab, shell := table["kind"], table["shell"]
	if p, err := lab.Override(0, 0, time.Hour); err != nil || p.MaxLifetime != time.Hour || p.TTL != time.Hour || p.Renew != time.Hour {
		t.Fatalf("tighter max lifetime = %+v, %v", p, err)
	}
	if p, err := lab.Override(2*time.Hour, 0, 0); err != nil || p.TTL != 2*time.Hour {
		t.Fatalf("ttl within max = %+v, %v", p, err)
	}
	if p, err := shell.Override(0, 10*time.Minute, 0); err != nil || p.Idle != 10*time.Minute {
		t.Fatalf("tighter idle = %+v, %v", p, err)
	}
	for name, err := range map[string]error{
		"ttl past max":     second(lab.Override(25*time.Hour, 0, 0)),
		"looser max":       second(lab.Override(0, 0, 48*time.Hour)),
		"looser idle":      second(shell.Override(0, time.Hour, 0)),
		"idle on time":     second(lab.Override(0, time.Minute, 0)),
		"max on owner row": second(Policy{Row: "x", Binding: Owner, TTL: time.Hour}.Override(0, 0, time.Hour)),
	} {
		if err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func second(_ Policy, err error) error { return err }
