---
status: accepted
---

# Keep command output separate from child results

Lease commands emit concise TOON-style output by default and JSON with `--json`, including explicit empty results and structured stdout errors. JSON listings include full records and native label objects, while text tables encode label cells as compact JSON. Help and version remain plain text, and resource-tool diagnostics go to stderr. No command prompts for input.

Job output is a different contract. Child stdout and stderr pass through unchanged, even with `--json`, so output formatting cannot corrupt arbitrary command output. `--json` affects pre-start errors only. Post-start cleanup diagnostics go to stderr, retain failed cleanup records, and never replace the child's result. The private SSH frames are transport, not another public output mode.

R-B4 preserves the child's exit code on completion, returns 124 when the deadline kills it, and returns the documented code 75 when no slot frees within the wait. Dibs's own AXI codes 1 and 2 apply only before a child starts. Child exit codes may overlap those values. Signal termination returns 128 plus the signal number, and helper or wait failures use 125 or 126 with stderr diagnostics. SSH transport failure after SSH starts returns 76 with an unknown remote outcome, distinct from an authoritative child result of 76 or 255.

The [AXI exit-code table](../../README.md#axi-exit-codes) and `dibs job --help` are the public lookup points for these results. Restructuring output must preserve both the content and the stdout or stderr channel, not merely the exit code.

Evidence: [rendering](../../output.go), [command output and help](../../cli.go), [job results](../../job.go), [SSH results](../../ssh.go), and [R-B4 in the local-job change](https://github.com/Dematom1/dibs/pull/2).
