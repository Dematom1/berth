package output

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	budgetpkg "github.com/Dematom1/dibs/internal/budget"
)

func humanText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
}

func humanValue(key string, value any, now time.Time) string {
	if value == nil || value == "" {
		return "-"
	}
	if key == "memory" {
		if bytes, err := budgetpkg.ParseMemory(fmt.Sprint(value)); err == nil {
			return readableMemory(bytes)
		}
	}
	if key == "expires" {
		if expiry, err := time.Parse(time.RFC3339Nano, fmt.Sprint(value)); err == nil {
			d := expiry.Sub(now).Round(time.Second)
			if d <= 0 {
				return "expired " + (-d).String() + " ago"
			}
			return "in " + d.String()
		}
	}
	if key == "age" {
		return fmt.Sprint(value) + " ago"
	}
	if labels, ok := value.(map[string]string); ok {
		if len(labels) == 0 {
			return "-"
		}
		pairs := make([]string, 0, len(labels))
		for label, text := range labels {
			pairs = append(pairs, label+"="+text)
		}
		sort.Strings(pairs)
		return humanText(strings.Join(pairs, ", "))
	}
	return humanText(fmt.Sprint(value))
}

func humanObject(w io.Writer, value map[string]any, now time.Time) {
	for _, key := range keys(value) {
		switch key {
		case "records", "record", "count", "memory_bytes", "help", "code", "exit_code", "contract":
			continue
		case "budget":
			fmt.Fprintln(w, value[key].(map[string]any)["summary"])
		case "evidence":
			evidence := value[key].(map[string]any)
			for _, name := range keys(evidence) {
				fmt.Fprintln(w, "Resource:", humanText(name))
				humanObject(w, evidence[name].(map[string]any), now)
			}
		case "orphans":
			rows := value[key].([]map[string]any)
			if len(rows) == 0 {
				fmt.Fprintln(w, "No unleased kind, k3d or Compose resources in this Docker context.")
			}
			for _, row := range rows {
				fmt.Fprintf(w, "%s %s (%s)\n", humanText(row["type"].(string)), humanText(row["name"].(string)), humanText(row["docker_context"].(string)))
				humanObject(w, row, now)
			}
		case "leases":
			rows := value[key].([]map[string]any)
			if len(rows) == 0 {
				fmt.Fprintln(w, "No leases.")
				continue
			}
			table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fields := []string{"id", "owner", "liveness", "age", "expires", "machine", "resource", "resources", "cores", "memory", "class", "kubeconfig"}
			headings := make([]string, len(fields))
			for i, field := range fields {
				headings[i] = strings.ToUpper(field)
			}
			fmt.Fprintln(table, strings.Join(headings, "\t"))
			for _, row := range rows {
				cells := make([]string, len(fields))
				for i, field := range fields {
					cells[i] = humanValue(field, row[field], now)
				}
				if row["pending"] == true {
					cells[6] += " (pending)"
				}
				fmt.Fprintln(table, strings.Join(cells, "\t"))
			}
			table.Flush()
		case "lease":
			humanObject(w, value[key].(map[string]any), now)
		case "results":
			rows := value[key].([]map[string]any)
			if len(rows) == 0 {
				fmt.Fprintln(w, "No pending leases.")
			}
			table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			if len(rows) > 0 {
				fmt.Fprintln(table, "ID\tSTATUS\tRESOURCE\tMATCHES\tCONFLICTS")
			}
			for _, row := range rows {
				resource := fmt.Sprint(row["resource_adapter"])
				if name := fmt.Sprint(row["resource_name"]); name != "" {
					resource += ":" + name
				}
				list := func(field string) string {
					items, _ := row[field].([]string)
					if len(items) == 0 {
						return "-"
					}
					return strings.Join(items, ", ")
				}
				cells := []string{fmt.Sprint(row["id"]), fmt.Sprint(row["status"]), resource, list("matches"), list("conflicts")}
				for i, cell := range cells {
					cells[i] = humanText(cell)
				}
				fmt.Fprintln(table, strings.Join(cells, "\t"))
			}
			table.Flush()
		default:
			if items, ok := value[key].([]string); ok {
				text := "none"
				if len(items) > 0 {
					text = strings.Join(items, ", ")
				}
				fmt.Fprintf(w, "%s: %s\n", key, humanText(text))
			} else {
				fmt.Fprintf(w, "%s: %s\n", key, humanValue(key, value[key], now))
			}
		}
	}
	if hints, ok := value["help"].([]string); ok {
		for _, hint := range hints {
			fmt.Fprintln(w, humanText(hint))
		}
	}
}

func readableMemory(bytes int64) string {
	if bytes >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", float64(bytes)/(1<<30))
	}
	if bytes >= 1<<20 {
		return fmt.Sprintf("%.1f MiB", float64(bytes)/(1<<20))
	}
	if bytes >= 1<<10 {
		return fmt.Sprintf("%.1f KiB", float64(bytes)/(1<<10))
	}
	return fmt.Sprintf("%d B", bytes)
}
