package main

import "testing"

func TestNixWorkflowMatrixAndBuildChecks(t *testing.T) {
	doc := workflow(t, "nix")
	jobs := object(doc["jobs"])
	job := object(jobs["nix"])
	matrix := object(object(object(job["strategy"])["matrix"]))
	require(t, len(list(matrix["os"])) == 2, "Nix checks must cover Linux and macOS")
	var runs []string
	for _, raw := range list(job["steps"]) {
		if run, ok := object(raw)["run"].(string); ok {
			runs = append(runs, run)
		}
	}
	require(t, len(runs) >= 3, "Nix job must evaluate, build, and verify the package")
}
