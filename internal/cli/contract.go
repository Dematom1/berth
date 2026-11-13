package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"strings"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	outputpkg "github.com/Dematom1/dibs/internal/output"
	policypkg "github.com/Dematom1/dibs/internal/policy"
)

// contractVersion versions the status, reason and exit-code contract that new
// commands and the --pending and --key flags follow; see docs/consumer-contract.md.
const contractVersion = 1

var reasonHints = map[string]string{
	"not_found":        "Run dibs create to acquire a new lease; a released lease cannot be restored",
	"wrong_machine":    "Run this command on the lease's machine",
	"not_pending":      "Run dibs explain <id>; the lease already covers a resource",
	"already_leased":   "Run dibs ps to find the lease that owns the resource",
	"key_conflict":     "Repeat the original request, or choose a new --key",
	"handle_exists":    "Run dibs adopt for a resource that already exists",
	"handle_mismatch":  "Pass the handle the pending lease recorded; run dibs explain <id>",
	"resource_absent":  "Create the resource first, then retry",
	"ambiguous":        "Resolve the ambiguous resource matches, then retry this command",
	"owner_alive":      "Wait for the owner to end, or have its owner run dibs release <id> --confirm",
	"owner_unknown":    "Retry later; unknown liveness never permits reclaim, but an expired unprotected lease can still be swept, so inspect it with dibs explain <id>",
	"expired":          "Release the lease and acquire again; do not create a second lease for a resource still covered",
	"owner_dead":       "Run dibs reclaim <id> --confirm or dibs sweep --confirm",
	"resource_unknown": "Retry dibs extend <id> when the resource tool is reachable; the lease is retained",
	"job_deadline":     "Start a new dibs job with --timeout",
	"max_lifetime":     "Release the lease and acquire again; its policy caps how long one lease may live",
	"grace":            "Retry after the grace period; dibs explain <id> shows when it ends",
	"resource_busy":    "Wait for the foreground job to finish; a busy resource is never reclaimed",
}

const keyRule = "--key must be 1-200 letters, digits or ._:/@+=- and start with a letter or digit"

func (app application) checkCreate(driver, file, key, name string, pending bool) error {
	if key != "" && !leasepkg.ValidKey(key) {
		return errors.New(keyRule)
	}
	if !pending {
		if name != "" {
			return errors.New("--name requires --pending")
		}
		return app.resources.CheckCreate(driver, file)
	}
	var adoptable []string
	for _, candidate := range app.resources.CreateNames() {
		if d, _ := app.resources.Lookup(candidate); d.Discover != nil {
			adoptable = append(adoptable, candidate)
		}
	}
	d, ok := app.resources.Lookup(driver)
	if !ok || d.Discover == nil || d.RequiresFile != (file != "") {
		return errors.New("--pending needs --adapter " + strings.Join(adoptable, ", ") + "; --file is required only for compose")
	}
	if name != "" && !adapterpkg.ValidResourceName(name) {
		return errors.New("--name must start with a letter or digit and use only letters, digits, '.', '_' or '-'")
	}
	return nil
}

func contractResult(status string, value map[string]any) map[string]any {
	value["contract"], value["status"] = contractVersion, status
	return value
}

func refusalFields(value map[string]any, r *leasepkg.Refusal, id string) map[string]any {
	hint := reasonHints[r.Reason]
	if id != "" {
		hint = strings.ReplaceAll(hint, "<id>", id)
	}
	value["contract"], value["error"], value["reason"] = contractVersion, r.Error(), r.Reason
	if id != "" {
		value["id"] = id
	}
	value["code"], value["exit_code"], value["help"] = "COMMAND_FAILED", 3, []string{hint}
	return value
}

// contractError reports a safety refusal as exit 3 with its reason; other
// failures keep the ordinary exit 1 lease error.
func (app application) contractError(out io.Writer, mode outputpkg.Mode, command, id string, err error) int {
	var refusal *leasepkg.Refusal
	if errors.As(err, &refusal) {
		outputpkg.Emit(out, mode, refusalFields(map[string]any{}, refusal, id))
		return 3
	}
	return app.leaseError(out, mode, command, err)
}

func (app application) contractOperationError(out io.Writer, mode outputpkg.Mode, command, id string, err error) int {
	var refusal *leasepkg.Refusal
	if errors.As(err, &refusal) {
		return app.contractError(out, mode, command, id, err)
	}
	outputpkg.Emit(out, mode, map[string]any{
		"contract": contractVersion, "code": "COMMAND_FAILED", "exit_code": 1,
		"error": err.Error(), "help": []string{"Retry dibs " + command + " " + id + " after resolving the reported failure"},
	})
	return 1
}

func (app application) runContract(ctx context.Context, command string, args []string, out io.Writer, mode outputpkg.Mode) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	wantHelp := fs.Bool("help", false, "help")
	full := fs.Bool("full", false, "complete AXI lease record")
	outputpkg.Flags(fs, &mode)
	confirm := new(bool)
	if command != "explain" {
		fs.BoolVar(confirm, "confirm", false, "act instead of previewing")
	}
	if err := parseFlags(fs, args); err != nil {
		return outputpkg.Error(out, mode, 2, err.Error(), help(command))
	}
	if *wantHelp {
		emitHelp(out, mode, command)
		return 0
	}
	if !consumerContractEnabled() {
		return outputpkg.Error(out, mode, 2, consumerContractOff, help(command))
	}
	maxArgs, minArgs := 1, 1
	if command == "reconcile" {
		minArgs = 0
	}
	if fs.NArg() < minArgs || fs.NArg() > maxArgs || (fs.NArg() == 1 && !leasepkg.ValidID(fs.Arg(0))) {
		return outputpkg.Error(out, mode, 2, "invalid or unexpected arguments", help(command))
	}
	id := fs.Arg(0)
	out = &outputpkg.Writer{Writer: out, Mode: mode.Effective(out), Full: *full, FullCommand: "dibs --axi ps --full"}
	switch command {
	case "reclaim":
		r, err := app.leases.Reclaim(ctx, id, *confirm)
		if err != nil {
			return app.contractError(out, mode, command, id, err)
		}
		hints := map[string][]string{
			"preview":      {"Run dibs reclaim " + id + " --confirm to destroy the resource and forget the lease"},
			"reclaimed":    {"The resource is verified gone and the lease is forgotten"},
			"already_done": {"No lease " + id + " remains on this machine; nothing to reclaim"},
		}[r.Status]
		result := contractResult(r.Status, map[string]any{"id": id, "help": hints})
		if r.Status != "already_done" {
			result["liveness"] = string(r.Liveness)
		}
		outputpkg.Emit(out, mode, result)
		return 0
	case "explain":
		e, err := app.leases.Explain(ctx, id)
		if err != nil {
			return app.contractError(out, mode, command, id, err)
		}
		kube := kubeconfigFor(app.resources, e.Lease)
		hints := []string{"Run dibs extend " + id + " --ttl <duration> to renew", "Run dibs release " + id + " --confirm to release"}
		if e.Lease.Policy.OwnerBound() {
			hints = []string{"The lease lives while its owner does; run dibs release " + id + " --confirm to end it sooner"}
		}
		if e.Lease.ResourceRef.Pending {
			handle := e.Lease.ResourceRef.Name
			if handle == "" {
				handle = "<handle>"
			}
			hints = []string{"Run dibs reconcile " + id + " to look for its resource", "Run dibs adopt " + e.Lease.ResourceRef.Adapter + " " + handle + " --pending " + id + " to register it"}
		}
		if e.Reclaim {
			hints = []string{"Run dibs reclaim " + id + " --confirm to reclaim it"}
		}
		result := map[string]any{
			"lease": outputpkg.LeaseView(e.Lease, e.Liveness, e.At, kube), "record": outputpkg.LeaseRecord(e.Lease, kube),
			"liveness": string(e.Liveness), "expired": e.Expired, "this_machine": e.ThisMachine,
			"sweep_would_reclaim": e.Sweep, "reclaim_would_reclaim": e.Reclaim, "why": e.Reason, "help": hints,
		}
		// Policy fields appear only where policies or resource activity exist, so other leases explain exactly as before.
		if leasePoliciesEnabled() || e.Lease.Policy != nil || e.Verdict.Activity != nil {
			explainPolicy(result, e)
		}
		outputpkg.Emit(out, mode, contractResult("explained", result))
		return 0
	}
	rows, err := app.leases.Reconcile(ctx, id, *confirm)
	if err != nil {
		return app.contractError(out, mode, command, id, err)
	}
	results := make([]map[string]any, 0, len(rows))
	code, failures, refusal := 0, map[string]string{}, (*leasepkg.Refusal)(nil)
	for _, row := range rows {
		results = append(results, map[string]any{"id": row.ID, "status": row.Status, "resource_adapter": row.Adapter, "resource_name": row.Name, "matches": row.Matches, "conflicts": row.Conflicts})
		switch row.Status {
		case "unknown":
			failures[row.ID], code = row.Error, 1
		case "ambiguous":
			refusal = &leasepkg.Refusal{Reason: "ambiguous", Message: "pending lease " + row.ID + " matches more than one resource; nothing was bound"}
		case "conflict":
			refusal = &leasepkg.Refusal{Reason: "already_leased", Message: "the resource matching pending lease " + row.ID + " is under another lease"}
		}
	}
	status := "preview"
	if *confirm {
		status = "reconciled"
	}
	hints := []string{"Run dibs reconcile --confirm to bind single unclaimed matches"}
	if len(rows) == 0 {
		hints = []string{"Nothing to reconcile; dibs create --pending records a pending lease"}
	} else if *confirm {
		hints = []string{"For each absent lease, check for a resource built without its handle or label, then build it and run dibs adopt <type> <handle> --pending <id>"}
	}
	result := contractResult(status, map[string]any{"count": len(results), "results": results, "help": hints})
	if code == 1 {
		result["failures"], result["error"] = failures, "resource discovery or binding failed; pending leases retained"
		result["code"], result["exit_code"] = "COMMAND_FAILED", 1
		result["help"] = []string{"Retry dibs reconcile once the resource tool is reachable"}
	} else if id != "" && refusal != nil {
		refusalFields(result, refusal, id)
		result["status"], code = status, 3
	}
	outputpkg.Emit(out, mode, result)
	return code
}

func duration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
}

// explainPolicy adds the governing policy, the time left before the next reclaim
// condition, and why the lease is still held.
func explainPolicy(result map[string]any, e leasepkg.Explanation) {
	p, v := e.Policy, e.Verdict
	result["policy"] = map[string]any{"row": p.Row, "source": e.PolicySource, "binding": p.Binding, "ttl": duration(p.TTL), "renew": duration(p.Renew), "max_lifetime": duration(p.MaxLifetime), "grace": duration(p.Grace), "idle": duration(p.Idle), "action": p.Action}
	result["trigger"], result["held_because"], result["reclaim_at"], result["remaining"], result["renew_by"] = v.Trigger, v.Hold, "", "", ""
	if !v.Due.IsZero() {
		result["reclaim_at"], result["remaining"] = v.Due.Format(time.RFC3339), max(0, v.Due.Sub(e.At)).Round(time.Second).String()
	}
	if p.Renew > 0 && p.Binding == policypkg.Time && e.PolicySource == "lease" {
		result["renew_by"] = e.Lease.Expires.Add(p.Renew - e.Lease.TTL).Format(time.RFC3339)
	}
	if a := v.Activity; a != nil {
		activity := map[string]any{"busy": a.Busy, "idle": "", "members": a.Members}
		if a.IdleKnown {
			activity["idle"] = a.Idle.Round(time.Second).String()
		}
		if v.ActivityErr != nil {
			activity["error"] = v.ActivityErr.Error()
		}
		result["activity"] = activity
	}
}
