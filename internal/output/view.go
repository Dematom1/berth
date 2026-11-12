package output

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	budgetpkg "github.com/Dematom1/dibs/internal/budget"
	configpkg "github.com/Dematom1/dibs/internal/config"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

func LeaseRecord(l leasepkg.Lease, kubeconfig string) map[string]any {
	data, _ := json.Marshal(l)
	var record map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	_ = decoder.Decode(&record)
	record["kubeconfig"] = kubeconfig
	return record
}

func LeaseView(l leasepkg.Lease, live ownerpkg.Liveness, now time.Time, kubeconfig string) map[string]any {
	view := map[string]any{"id": l.ID, "owner": l.Owner.String(), "liveness": string(live), "machine": l.Machine, "age": now.Sub(l.Created).Round(time.Second).String(), "expires": l.Expires.Format(time.RFC3339Nano), "resources": l.Description, "resource": l.ResourceRef.Adapter + ":" + l.ResourceRef.Name, "resource_adapter": l.ResourceRef.Adapter, "resource_name": l.ResourceRef.Name, "labels": l.Labels, "kubeconfig": kubeconfig}
	if l.ResourceRef.Pending {
		view["pending"] = true
	}
	if l.Key != "" {
		view["key"] = l.Key
	}
	if l.ProtectLiveOwner {
		view["protect_live_owner"] = true
	}
	view["cores"], view["memory"], view["memory_bytes"], view["class"] = 0, "0B", int64(0), ""
	if l.Reservation != nil {
		view["cores"], view["memory"], view["memory_bytes"], view["class"] = l.Reservation.Budget.Cores, strconv.FormatInt(l.Reservation.Budget.Memory, 10)+"B", l.Reservation.Budget.Memory, l.Reservation.Class
	}
	return view
}

func BudgetView(config configpkg.Config, used budgetpkg.Amount) map[string]any {
	capacity, _ := config.Available() // loadJobConfig has validated the budget.
	roomMemory, _ := budgetpkg.ParseMemory(config.RoomMemory)
	return map[string]any{
		"used_cores": used.Cores, "used_memory_bytes": used.Memory,
		"usable_cores": capacity.Cores, "usable_memory_bytes": capacity.Memory,
		"room_cores": config.RoomCores, "room_memory_bytes": roomMemory,
		"summary": fmt.Sprintf("jobs: %d of %d cores, %s of %s in use; %d cores / %s kept free for you", used.Cores, capacity.Cores, readableMemory(used.Memory), readableMemory(capacity.Memory), config.RoomCores, readableMemory(roomMemory)),
	}
}
