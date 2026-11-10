# Output modes

Every berth command serves people, agents, and scripts. Berth renders each result in one of three modes: human (tables for a terminal), AXI (TOON for agents), or JSON (for scripts). This page describes how berth picks the mode, what each mode prints, and which output is a stable contract.

Source: `output.go`, and the views built in `cli.go`, `hooks.go`, and `ssh.go`. Tests: `output_modes_test.go` with goldens in `testdata/output/`, `output_test.go`, and `TestCLIHomeHelpVersionAndTOON` and `TestCLITextListKeepsLabels` in `cli_test.go`. Decision: [ADR 0007](adr/0007-output-modes-and-job-results.md).

## Mode selection

`outputMode.effective` picks the mode in this order:

1. An explicit `--axi` or `--json` flag.
2. The mode carried by a `modeWriter`. SSH sessions use this to keep the caller's mode. See [Remote jobs over SSH](remote.md#client-side).
3. `BERTH_AXI=1` selects AXI. `berth setup hooks` sets this variable in agent shells. See [Agent hooks](hooks.md).
4. Human when stdout is a terminal, which berth detects with the `TIOCGWINSZ` ioctl.
5. AXI otherwise, for example when stdout is a pipe.

Rules for the flags:

- `--axi` and `--json` are mutually exclusive. Using both exits 2 before berth touches state. Test: `TestConflictingOutputFlags`.
- Either flag overrides `BERTH_AXI`.
- Both flags accept only the value `true`.
- No `--human` flag exists. `--human` is an unknown flag and exits 2. Test: `TestCLIRejectsUsageBeforeStateAccess`.
- `run` accepts the flags before the command name, more than once, and among the command's flags.

Test: `TestOutputSelection`, `TestOutputProcessesAndTTY`.

## Stream rules

- Results and structured errors go to stdout in every mode.
- Diagnostics from resource tools, job cleanup, and SSH go to stderr.
- `berth job` passes child stdin, stdout, and stderr through unchanged in every mode. The mode affects only errors before the child starts. Test: `TestOutputProcessesAndTTY`.
- `--version` prints the plain version string in every mode.
- `berth create --quiet` (or `-q`) prints only the new lease ID and a newline, in every mode. Test: `TestQuietAndKubeconfig`.
- `berth env <id>` always prints one shell line, `export KUBECONFIG=<quoted path>`, in every mode. Test: `TestQuietAndKubeconfig`.
- Berth never prompts.

## Result shape

Commands build one `map[string]any` and pass it to `emit`. `emit` renders the same map in each mode.

| Command | Keys |
| --- | --- |
| `berth` (home view) | `bin`, `budget`, `count`, `description`, `help`, `leases`, `records` |
| `list` | `budget`, `count`, `help`, `leases`, `records` |
| `create` | `help`, `lease`, `record` |
| `extend` | `lease` |
| `release` | `id`, `status` |
| `sweep` | `confirmed`, `eligible`, `reclaimed`, `unknown`. Adds `help` when not confirmed. Adds `code`, `error`, `exit_code`, and `failures` on failure. |
| `machine install` | `help`, `machine`, `status`, `version` |
| `setup hooks` | `changed`, `help`, `status` |
| Any error | `code`, `error`, `exit_code`, `help` |

Parts of the map:

- `leaseView` in `cli.go` builds every row of `leases` with one key set: `age`, `class`, `cores`, `expires`, `id`, `kubeconfig`, `labels`, `liveness`, `machine`, `memory`, `memory_bytes`, `owner`, `resource`, `resource_adapter`, `resource_name`, and `resources`.
- `leaseRecord` in `output.go` is the stored `Lease` encoded to JSON and decoded back with `UseNumber`, plus `kubeconfig`. See [State](state.md#format-version-1).
- `budgetView` reports the usable budget, owner room, and the reservations in use on this machine, plus a one-line `summary`. If the config fails to load, `list` still succeeds, and `budget` holds only the summary `jobs: budget unavailable (check BERTH_CONFIG)`. Test: `TestBudgetSummary`.

`emitError` sets `code` from the exit code:

| Exit code | `code` |
| --- | --- |
| `2` | `VALIDATION_ERROR` |
| `75` | `NO_CAPACITY` |
| Any other | `COMMAND_FAILED` |

When a hint has more than one line, as full help text does, `emitError` replaces it with `Run berth <command> --help for options`. Test: `TestAXIHelpAndErrors`.

## JSON

`emit` writes the map with `json.NewEncoder(w).Encode`: one JSON object, then a newline. JSON is the canonical, complete form. It always includes `records` and `record`, and it never truncates text. Go's encoder sorts map keys and escapes `<`, `>`, and `&` as HTML-safe sequences.

## AXI

AXI is TOON. Unless `--full` is set, `axiView` first makes a bounded copy of the map:

- It drops `record` and `records`, and renames `count` to `total`.
- It keeps at most 100 rows of `leases` (`axiListLimit`), each reduced to `id`, `owner`, `liveness`, `expires`, `machine`, `resource`, `resources`, `cores`, `memory_bytes`, and `kubeconfig`. When rows are cut, it adds `shown` and `total`.
- It cuts `resources`, and label values in a single `lease`, to 500 characters (`axiTextLimit`). It appends `… (truncated, N chars total)`.
- When it shows any lease, it adds a hint that names the command that prints everything: `berth --axi ps [--machine M] [--owner O] --full`.
- It never changes the original map.

`--full` on a lease command skips `axiView`, so AXI prints every row, every field, and the full records.

Test: `TestAXIBoundedListsAndFull`.

`toonObject` then renders the map:

- Keys appear in sorted order at every level.
- A nested `map[string]any` or `map[string]string` prints `key:` and then its fields indented by two spaces. A nil `map[string]string` prints `key: null`.
- A `[]map[string]any` with only scalar cells prints a table header `key[N]{f1,f2,...}:` and one indented, comma-separated row per item. The columns are the sorted keys of the first row.
- A `[]map[string]any` with any nested map cell prints `key[N]:` and then each item as a `- ` list entry.
- A `[]string` prints `key[N]: a,b,c`.
- An empty list prints `key: []`.
- Scalars:
	- `string` goes through `toonString`.
	- `bool` prints `true` or `false`.
	- `int`, `int64`, and `json.Number` print in decimal.
	- Every other type prints `null`.

`toonKey` leaves a key bare when it matches `^[A-Za-z_][A-Za-z0-9_.]*$` and quotes it otherwise. `toonString` quotes a string when it:

- is empty, or has leading or trailing whitespace,
- contains a control character, or any of `, : " \ [ ] { }`,
- starts with `-` or `#`,
- equals `true`, `false`, or `null`, or
- parses as a number.

`quoteToon` escapes backslash, double quote, newline, carriage return, and tab, and writes other control characters as `\u00XX`. A key or value therefore cannot add a line or a field. Test: `TestTOONQuotesKeysInEveryShape`.

In AXI and JSON, help is data: `emitHelp` emits `usage` (the first help line) and `reference` (the remaining lines). Test: `TestAXIHelpAndErrors`.

## Human

`humanObject` renders the map for a terminal:

- It skips `records`, `record`, `count`, `memory_bytes`, `help`, `code`, and `exit_code`.
- `budget` prints its `summary` line.
- `leases` prints a `tabwriter` table with fixed, uppercase columns, or `No leases.` when empty.
- Empty values print `-`. `memory` prints in KiB, MiB, or GiB. `expires` prints `in <d>` or `expired <d> ago`. `age` prints `<d> ago`.
- Control characters in text become spaces (`humanText`).
- Hints print last, one per line.

A human error is exactly two lines: `Error: <message>` and one hint. Full help never appears in an error. Test: `TestHumanErrorsAreTwoShortLines`.

Help prints as plain text. Human output has goldens too, but agents use AXI and scripts use JSON.

## Goldens

`snapshot` in `output_modes_test.go` compares output with `testdata/output/<name>.golden`. To regenerate the files after an intended change, run:

```sh
UPDATE_GOLDEN=1 go test .
```

A golden diff in a pull request is a visible change to an output contract.

## Invariants

1. **AXI output is byte-stable.** The same values produce the same bytes: sorted keys, fixed quoting, fixed limits, and the same hint text. A change to a key name, a key set, the compact field list, the limits, the quoting rules, or the layout is a contract change. It shows as a golden diff.
2. **JSON is complete.** JSON never truncates and always includes the full records. Only AXI applies `axiView`.
3. **Presentation never mutates data.** `axiView` copies. The canonical map stays intact for JSON and `--full`.
4. **Every table row has the same keys.** Build every row from one function, such as `leaseView`.
5. **Views hold only rendered types.** Use `string`, `bool`, `int`, `int64`, `json.Number`, `[]string`, `map[string]string`, `map[string]any`, or `[]map[string]any`. Other types print `null` in AXI.
6. **Errors are structured, on stdout, and carry `code` and `exit_code`.** Every error path goes through `emitError` and returns a non-zero code.
7. **Input cannot change the output structure.** Quoting keeps newlines, separators, and control characters inside one field.
8. **The mode never changes child output or the exit code.**
9. **`--axi` and `--json` conflict before state access.**

## Planned

The captain-approved scope (2026-11-09) commits to a stable CLI and AXI contract, semantic versioning, and a deprecation policy. No deprecation mechanism exists in the code today.
