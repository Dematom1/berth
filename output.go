package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

func emitError(w io.Writer, jsonOutput bool, code int, message, hint string) int {
	emit(w, jsonOutput, map[string]any{"error": message, "help": []string{hint}})
	return code
}

func emit(w io.Writer, jsonOutput bool, value map[string]any) {
	if jsonOutput {
		json.NewEncoder(w).Encode(value)
		return
	}
	toonObject(w, value, "")
}

func keys(m map[string]any) []string {
	k := make([]string, 0, len(m))
	for key := range m {
		k = append(k, key)
	}
	sort.Strings(k)
	return k
}

// This output boundary renders only the CLI's flat scalar views, not arbitrary Go values.
func toonObject(w io.Writer, object map[string]any, indent string) {
	for _, key := range keys(object) {
		value := object[key]
		switch v := value.(type) {
		case map[string]any:
			fmt.Fprintf(w, "%s%s:\n", indent, toonString(key))
			toonObject(w, v, indent+"  ")
		case []map[string]any:
			if len(v) == 0 {
				fmt.Fprintf(w, "%s%s: []\n", indent, key)
				continue
			}
			fields := keys(v[0])
			fmt.Fprintf(w, "%s%s[%d]{%s}:\n", indent, key, len(v), strings.Join(fields, ","))
			for _, row := range v {
				cells := make([]string, len(fields))
				for i, field := range fields {
					cells[i] = toonScalar(row[field])
				}
				fmt.Fprintln(w, indent+"  "+strings.Join(cells, ","))
			}
		case []string:
			if len(v) == 0 {
				fmt.Fprintf(w, "%s%s: []\n", indent, key)
				continue
			}
			cells := make([]string, len(v))
			for i, s := range v {
				cells[i] = toonString(s)
			}
			fmt.Fprintf(w, "%s%s[%d]: %s\n", indent, key, len(v), strings.Join(cells, ","))
		case map[string]string:
			fields := map[string]any{}
			for k, s := range v {
				fields[k] = s
			}
			fmt.Fprintf(w, "%s%s:\n", indent, key)
			toonObject(w, fields, indent+"  ")
		default:
			fmt.Fprintf(w, "%s%s: %s\n", indent, key, toonScalar(v))
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
	default:
		return "null"
	}
}

func toonString(s string) string {
	if s != "" && strings.TrimSpace(s) == s && !strings.ContainsAny(s, ",:\"\\[]{}\n\r\t") && !strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "#") && s != "true" && s != "false" && s != "null" {
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return s
		}
	}
	// TOON's quoted strings use backslash, quote, newline, return, and tab escapes.
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\r", "\\r", "\t", "\\t")
	return "\"" + replacer.Replace(s) + "\""
}
