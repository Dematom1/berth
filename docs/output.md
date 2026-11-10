# Output formats

Berth prints every result and every error to stdout in one of two formats: TOON (the default) or JSON (`--json`). TOON is the AXI format that agents parse, and it is also what people read in a terminal. Berth has no separate human-only renderer. This page describes both formats and the stream rules.

Source: `output.go`, and the views built in `cli.go` and `ssh.go`. Tests: `output_test.go`, and `TestCLIHomeHelpVersionAndTOON` and `TestCLITextListKeepsLabels` in `cli_test.go`.

## Stream rules

- Results and structured errors go to stdout.
- Diagnostics from resource tools, job cleanup, and SSH go to stderr.
- `berth job` passes child stdin, stdout, and stderr through unchanged. `--json` affects only errors before the child starts.
- `--help` and `--version` print plain text to stdout and exit 0.
- Berth never prompts.

## Result shape

Commands build a `map[string]any` and pass it to `emit`. Errors go through `emitError`, which emits `{"error": <message>, "help": [<hint>]}` and returns the exit code. Most results also carry a `help` list of next commands.

| Command | Top-level keys |
| --- | --- |
| `berth` (home view) | `bin`, `count`, `description`, `help`, `leases` |
| `list` | `count`, `help`, `leases`, plus `records` with `--json` |
| `create` | `help`, `lease`, plus `record` with `--json` |
| `extend` | `lease` |
| `release` | `id`, `status` |
| `sweep` | `confirmed`, `eligible`, `reclaimed`, `unknown`. Adds `help` when not confirmed. Adds `failures` and `error` on failure. |
| `machine install` | `help`, `machine`, `status`, `version` |

`leaseView` in `cli.go` builds each row of `leases` with the same keys: `age`, `class`, `cores`, `expires`, `id`, `labels`, `liveness`, `machine`, `memory`, `owner`, `resource`, `resource_adapter`, `resource_name`, and `resources`. Leases without a job report `cores: 0`, `memory: 0B`, and `class: ""`. `records` and `record` hold the full `Lease` struct as stored in [State](state.md).

## JSON

With `--json`, `emit` writes the map with `json.NewEncoder(w).Encode`: one JSON object, then a newline. Go's encoder sorts map keys and escapes `<`, `>`, and `&` as HTML-safe sequences. `run` in `cli.go` accepts `--json` before the command name or among the command's flags.

## TOON

Without `--json`, `toonObject` renders the map:

- Keys appear in sorted order at every level.
- A nested `map[string]any` or `map[string]string` prints `key:` and then its fields indented by two spaces.
- A `[]map[string]any` prints a table header `key[N]{f1,f2,...}:`, then one indented, comma-separated row per item. The columns are the sorted keys of the first row.
- A `[]string` prints `key[N]: a,b,c`.
- An empty list prints `key: []`.
- Scalars:
	- `string` goes through `toonString`.
	- `bool` prints `true` or `false`.
	- `int` prints in decimal.
	- A `map[string]string` inside a table cell prints as a quoted compact JSON string. An empty map prints `"{}"`.
	- Every other type prints `null`.

`toonKey` leaves a key bare when it matches `^[A-Za-z_][A-Za-z0-9_.]*$` and quotes it otherwise. `toonString` quotes a string when it:

- is empty, or has leading or trailing whitespace,
- contains any of `, : " \ [ ] { }`, a newline, a carriage return, or a tab,
- starts with `-` or `#`,
- equals `true`, `false`, or `null`, or
- parses as a number.

Quoted strings escape backslash, double quote, newline, carriage return, and tab. A key or value therefore cannot add a line or a field to the output. Test: `TestTOONQuotesKeysInEveryShape`.

`TestCLIHomeHelpVersionAndTOON` pins exact bytes for one table:

```text
leases[1]{id,resources}:
  abc,"lab, \"db\"\nnext"
```

## Invariants

1. **AXI output is byte-stable.** The same values produce the same bytes. Keys are sorted, quoting rules are fixed, and table columns come from the first row. A change to a key name, the key set, the quoting rules, or the layout is a contract change for every agent that parses berth.
2. **Every table row has the same keys.** `toonObject` takes columns from the first row only. Build every row from one function, such as `leaseView`.
3. **Views hold only rendered types.** Use `string`, `bool`, `int`, `[]string`, `map[string]string`, `map[string]any`, or `[]map[string]any`. Other types print `null` in TOON. For example, `leaseView` formats memory as a `"<n>B"` string, because an `int64` would print `null`.
4. **Errors are structured and on stdout.** Every error path uses `emitError` with a message and a hint, and returns a non-zero code.
5. **Input cannot change the output structure.** Keys and values are quoted so that newlines and separators stay inside one field. Test: `TestTOONQuotesKeysInEveryShape`.
6. **`--json` never changes child output.**

## Planned

The captain-approved scope (2026-11-09) commits to a stable CLI and AXI contract, semantic versioning, and a deprecation policy. No deprecation mechanism exists in the code today.
