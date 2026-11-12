package output

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const axiListLimit = 100

const axiTextLimit = 500

// Keep canonical JSON untouched. AXI --full bypasses this bounded presentation.
func axiView(value map[string]any, fullCommand string) map[string]any {
	view := make(map[string]any, len(value))
	truncated, detailed := false, false
	preview := func(text string) string {
		runes := []rune(text)
		if len(runes) <= axiTextLimit {
			return text
		}
		truncated = true
		return string(runes[:axiTextLimit]) + fmt.Sprintf("… (truncated, %d chars total)", len(runes))
	}
	for key, item := range value {
		switch key {
		case "record", "records":
			continue
		case "count":
			view["total"] = item
		case "leases":
			rows := item.([]map[string]any)
			total := len(rows)
			shown := make([]map[string]any, 0, min(total, axiListLimit))
			// Optional columns appear only when some row has them, on every row of the table.
			optional := map[string]any{}
			for _, row := range rows[:min(total, axiListLimit)] {
				for field, empty := range map[string]any{"pending": false, "key": "", "protect_live_owner": false} {
					if _, ok := row[field]; ok {
						optional[field] = empty
					}
				}
			}
			for _, row := range rows[:min(total, axiListLimit)] {
				compact := map[string]any{}
				for _, field := range []string{"id", "owner", "liveness", "expires", "machine", "resource", "resources", "cores", "memory_bytes", "kubeconfig"} {
					compact[field] = row[field]
				}
				for field, empty := range optional {
					compact[field] = empty
					if value, ok := row[field]; ok {
						compact[field] = value
					}
				}
				if text, ok := compact["resources"].(string); ok {
					compact["resources"] = preview(text)
				}
				shown = append(shown, compact)
			}
			view[key] = shown
			detailed = total > 0
			if total > len(shown) {
				view["shown"] = len(shown)
				view["total"] = total
				truncated = true
			}
		case "lease":
			row := item.(map[string]any)
			copy := make(map[string]any, len(row))
			for field, cell := range row {
				copy[field] = cell
			}
			if text, ok := copy["resources"].(string); ok {
				copy["resources"] = preview(text)
			}
			if labels, ok := copy["labels"].(map[string]string); ok {
				shown := map[string]string{}
				for label, text := range labels {
					shown[label] = preview(text)
				}
				copy["labels"] = shown
			}
			view[key], detailed = copy, true
		default:
			view[key] = item
		}
	}
	if truncated || detailed {
		hints, _ := view["help"].([]string)
		hints = append(append([]string{}, hints...), "Run "+fullCommand+" for all fields, complete text and all matching leases")
		if total, ok := view["total"].(int); ok {
			hints[len(hints)-1] = fmt.Sprintf("Run %s for all %d matching leases and complete records", fullCommand, total)
		}
		view["help"] = hints
	}
	return view
}

func keys(m map[string]any) []string {
	k := make([]string, 0, len(m))
	for key := range m {
		k = append(k, key)
	}
	sort.Strings(k)
	return k
}

// Render only the CLI's views and JSON-normalized lease records.
func toonObject(w io.Writer, object map[string]any, indent string) {
	for _, key := range keys(object) {
		value := object[key]
		quotedKey := toonKey(key)
		switch v := value.(type) {
		case map[string]any:
			fmt.Fprintf(w, "%s%s:\n", indent, quotedKey)
			toonObject(w, v, indent+"  ")
		case []map[string]any:
			if len(v) == 0 {
				fmt.Fprintf(w, "%s%s: []\n", indent, quotedKey)
				continue
			}
			nested := false
			for _, row := range v {
				for _, cell := range row {
					// Tabular rows hold only primitives; lists and objects need the list form.
					switch cell.(type) {
					case map[string]any, map[string]string, []string:
						nested = true
					}
				}
			}
			if nested {
				fmt.Fprintf(w, "%s%s[%d]:\n", indent, quotedKey, len(v))
				for _, row := range v {
					var item strings.Builder
					toonObject(&item, row, indent+"    ")
					fmt.Fprint(w, indent+"  - "+strings.TrimPrefix(item.String(), indent+"    "))
				}
				continue
			}
			fields := keys(v[0])
			quotedFields := make([]string, len(fields))
			for i, field := range fields {
				quotedFields[i] = toonKey(field)
			}
			fmt.Fprintf(w, "%s%s[%d]{%s}:\n", indent, quotedKey, len(v), strings.Join(quotedFields, ","))
			for _, row := range v {
				cells := make([]string, len(fields))
				for i, field := range fields {
					cells[i] = toonScalar(row[field])
				}
				fmt.Fprintln(w, indent+"  "+strings.Join(cells, ","))
			}
		case []string:
			if len(v) == 0 {
				fmt.Fprintf(w, "%s%s: []\n", indent, quotedKey)
				continue
			}
			cells := make([]string, len(v))
			for i, s := range v {
				cells[i] = toonString(s)
			}
			fmt.Fprintf(w, "%s%s[%d]: %s\n", indent, quotedKey, len(v), strings.Join(cells, ","))
		case map[string]string:
			if v == nil {
				fmt.Fprintf(w, "%s%s: null\n", indent, quotedKey)
				continue
			}
			fields := map[string]any{}
			for k, s := range v {
				fields[k] = s
			}
			fmt.Fprintf(w, "%s%s:\n", indent, quotedKey)
			toonObject(w, fields, indent+"  ")
		default:
			fmt.Fprintf(w, "%s%s: %s\n", indent, quotedKey, toonScalar(v))
		}
	}
}

func toonScalar(value any) string {
	switch v := value.(type) {
	case string:
		return toonString(v)
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case json.Number:
		return v.String()
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return "null"
	}
}

var toonKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

func toonKey(s string) string {
	if toonKeyPattern.MatchString(s) {
		return s
	}
	return quoteToon(s)
}

func toonString(s string) string {
	if s != "" && strings.TrimSpace(s) == s && !strings.ContainsFunc(s, func(r rune) bool { return r < 32 }) && !strings.ContainsAny(s, ",:\"\\[]{}\n\r\t") && !strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "#") && s != "true" && s != "false" && s != "null" {
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return s
		}
	}
	return quoteToon(s)
}

func quoteToon(s string) string {
	var quoted strings.Builder
	quoted.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			quoted.WriteString(`\\`)
		case '"':
			quoted.WriteString(`\"`)
		case '\n':
			quoted.WriteString(`\n`)
		case '\r':
			quoted.WriteString(`\r`)
		case '\t':
			quoted.WriteString(`\t`)
		default:
			if r < 32 {
				fmt.Fprintf(&quoted, `\u%04x`, r)
			} else {
				quoted.WriteRune(r)
			}
		}
	}
	quoted.WriteByte('"')
	return quoted.String()
}
