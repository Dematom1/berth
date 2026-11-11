package output

import (
	"bytes"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	budgetpkg "github.com/Dematom1/dibs/internal/budget"
	configpkg "github.com/Dematom1/dibs/internal/config"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

func TestBudgetSummary(t *testing.T) {
	fixedBudget(t)
	config, err := configpkg.Load()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	l := leasepkg.Lease{ID: "job", Machine: "lab", Created: now, Expires: now.Add(time.Hour), ResourceRef: adapterpkg.ResourceRef{Adapter: "process"}, Reservation: &leasepkg.ReservationView{Budget: budgetpkg.Amount{Cores: 9, Memory: 1 << 30}, Class: "suite"}}
	budget := BudgetView(config, budgetpkg.Amount{Cores: 9, Memory: 1 << 30})
	if budget["used_cores"] != 9 || budget["usable_cores"] != 11 || budget["used_memory_bytes"] != int64(1<<30) || budget["usable_memory_bytes"] != int64(24<<30) {
		t.Fatal(budget)
	}
	value := map[string]any{"budget": budget, "leases": []map[string]any{LeaseView(l, ownerpkg.Alive, now, "")}}
	var out bytes.Buffer
	humanObject(&out, value, now)
	snapshot(t, "budget-human", out.String())
	for _, mode := range []Mode{AXI, JSON} {
		out.Reset()
		Emit(&out, mode, value)
		snapshot(t, "budget-"+string(mode), out.String())
	}
}
