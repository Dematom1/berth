package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/sys/unix"
)

type outputMode string

const (
	autoOutput  outputMode = ""
	humanOutput outputMode = "human"
	axiOutput   outputMode = "axi"
	jsonOutput  outputMode = "json"
)

type modeWriter struct {
	io.Writer
	mode        outputMode
	full        bool
	fullCommand string
	machine     string
}

func (mode outputMode) effective(w io.Writer) outputMode {
	if mode != autoOutput {
		return mode
	}
	if writer, ok := w.(*modeWriter); ok {
		return writer.mode
	}
	if getenv("AXI") == "1" {
		return axiOutput
	}
	if file, ok := w.(*os.File); ok {
		if _, err := unix.IoctlGetWinsize(int(file.Fd()), unix.TIOCGWINSZ); err == nil {
			return humanOutput
		}
	}
	return axiOutput
}

type outputFlag struct {
	mode   *outputMode
	choice outputMode
}

func (f outputFlag) String() string   { return "false" }
func (f outputFlag) IsBoolFlag() bool { return true }
func (f outputFlag) Set(value string) error {
	if value != "true" {
		return fmt.Errorf("--%s only accepts true", f.choice)
	}
	if *f.mode != autoOutput && *f.mode != f.choice {
		return fmt.Errorf("--json and --axi are mutually exclusive")
	}
	*f.mode = f.choice
	return nil
}

func outputFlags(fs *flag.FlagSet, mode *outputMode) {
	fs.Var(outputFlag{mode, axiOutput}, "axi", "agent output (TOON)")
	fs.Var(outputFlag{mode, jsonOutput}, "json", "script output (JSON)")
}

func emitError(w io.Writer, mode outputMode, code int, message, hint string) int {
	if strings.Contains(hint, "\n") {
		command := strings.Fields(strings.SplitN(hint, "\n", 2)[0])
		hint = "Run dibs --help for usage"
		if len(command) > 1 && command[0] == "dibs" {
			hint = "Run dibs " + command[1] + " --help for options"
		}
	}
	if mode.effective(w) == humanOutput {
		fmt.Fprintln(w, "Error:", humanText(message))
		fmt.Fprintln(w, humanText(hint))
	} else {
		kind := "COMMAND_FAILED"
		if code == 2 {
			kind = "VALIDATION_ERROR"
		}
		if code == 75 {
			kind = "NO_CAPACITY"
		}
		emit(w, mode, map[string]any{"code": kind, "exit_code": code, "error": message, "help": []string{hint}})
	}
	return code
}

func emit(w io.Writer, mode outputMode, value map[string]any) {
	switch mode.effective(w) {
	case jsonOutput:
		json.NewEncoder(w).Encode(value)
	case humanOutput:
		if message, ok := value["error"].(string); ok {
			hint := "Run dibs ps to inspect retained leases"
			if hints, ok := value["help"].([]string); ok && len(hints) > 0 {
				hint = hints[0]
			}
			emitError(w, humanOutput, 1, message, hint)
			return
		}
		humanObject(w, value, time.Now())
	default:
		writer, _ := w.(*modeWriter)
		if writer == nil || !writer.full {
			command := "dibs --axi ps --full"
			if writer != nil && writer.fullCommand != "" {
				command = writer.fullCommand
			}
			value = axiView(value, command)
		}
		toonObject(w, value, "")
	}
}

func emitHelp(w io.Writer, mode outputMode, command string) {
	body := help(command)
	if mode.effective(w) == humanOutput {
		fmt.Fprint(w, body)
		return
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	emit(w, mode, map[string]any{"usage": lines[0], "reference": lines[1:]})
}

func writerMachine(w io.Writer) string {
	if writer, ok := w.(*modeWriter); ok {
		if writer.machine != "" {
			return writer.machine
		}
		return writerMachine(writer.Writer)
	}
	return ""
}

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
			for _, row := range rows[:min(total, axiListLimit)] {
				compact := map[string]any{}
				for _, field := range []string{"id", "owner", "liveness", "expires", "machine", "resource", "resources", "cores", "memory_bytes", "kubeconfig"} {
					compact[field] = row[field]
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
		if bytes, err := memoryBytes(fmt.Sprint(value)); err == nil {
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
	return humanText(fmt.Sprint(value))
}

func humanObject(w io.Writer, value map[string]any, now time.Time) {
	for _, key := range keys(value) {
		switch key {
		case "records", "record", "count", "memory_bytes", "help", "code", "exit_code":
			continue
		case "budget":
			fmt.Fprintln(w, value[key].(map[string]any)["summary"])
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
				fmt.Fprintln(table, strings.Join(cells, "\t"))
			}
			table.Flush()
		case "lease":
			humanObject(w, value[key].(map[string]any), now)
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

func budgetView(config JobConfig, s *Store, host string) map[string]any {
	capacity, _ := config.available() // loadJobConfig has validated the budget.
	roomMemory, _ := memoryBytes(config.RoomMemory)
	used := Budget{}
	for _, l := range s.Leases {
		if l.Machine == host && l.Job != nil {
			used.Cores += l.Job.Budget.Cores
			used.Memory += l.Job.Budget.Memory
		}
	}
	return map[string]any{
		"used_cores": used.Cores, "used_memory_bytes": used.Memory,
		"usable_cores": capacity.Cores, "usable_memory_bytes": capacity.Memory,
		"room_cores": config.RoomCores, "room_memory_bytes": roomMemory,
		"summary": fmt.Sprintf("jobs: %d of %d cores, %s of %s in use; %d cores / %s kept free for you", used.Cores, capacity.Cores, readableMemory(used.Memory), readableMemory(capacity.Memory), config.RoomCores, readableMemory(roomMemory)),
	}
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
					switch cell.(type) {
					case map[string]any, map[string]string:
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

func leaseRecord(l Lease) map[string]any {
	data, _ := json.Marshal(l)
	var record map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	_ = decoder.Decode(&record)
	record["kubeconfig"] = kubeconfig(l)
	return record
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
