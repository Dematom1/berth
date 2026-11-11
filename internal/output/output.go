package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

func Error(w io.Writer, mode Mode, code int, message, hint string) int {
	if strings.Contains(hint, "\n") {
		command := strings.Fields(strings.SplitN(hint, "\n", 2)[0])
		hint = "Run dibs --help for usage"
		if len(command) > 1 && command[0] == "dibs" {
			hint = "Run dibs " + command[1] + " --help for options"
		}
	}
	if mode.Effective(w) == Human {
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
		Emit(w, mode, map[string]any{"code": kind, "exit_code": code, "error": message, "help": []string{hint}})
	}
	return code
}

func Emit(w io.Writer, mode Mode, value map[string]any) {
	switch mode.Effective(w) {
	case JSON:
		json.NewEncoder(w).Encode(value)
	case Human:
		if message, ok := value["error"].(string); ok {
			hint := "Run dibs ps to inspect retained leases"
			if hints, ok := value["help"].([]string); ok && len(hints) > 0 {
				hint = hints[0]
			}
			Error(w, Human, 1, message, hint)
			return
		}
		humanObject(w, value, time.Now())
	default:
		writer, _ := w.(*Writer)
		if writer == nil || !writer.Full {
			command := "dibs --axi ps --full"
			if writer != nil && writer.FullCommand != "" {
				command = writer.FullCommand
			}
			value = axiView(value, command)
		}
		toonObject(w, value, "")
	}
}

func Help(w io.Writer, mode Mode, body string) {
	if mode.Effective(w) == Human {
		fmt.Fprint(w, body)
		return
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	Emit(w, mode, map[string]any{"usage": lines[0], "reference": lines[1:]})
}
