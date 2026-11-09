package main

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
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
	case map[string]string:
		if len(v) == 0 {
			return `"{}"`
		}
		encoded, _ := json.Marshal(v)
		return toonString(string(encoded))
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
	if s != "" && strings.TrimSpace(s) == s && !strings.ContainsAny(s, ",:\"\\[]{}\n\r\t") && !strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "#") && s != "true" && s != "false" && s != "null" {
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return s
		}
	}
	return quoteToon(s)
}

func quoteToon(s string) string {
	// TOON's quoted strings use backslash, quote, newline, return, and tab escapes.
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\r", "\\r", "\t", "\\t")
	return "\"" + replacer.Replace(s) + "\""
}
