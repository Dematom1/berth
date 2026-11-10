package main

import "testing"

// This published workflow owns title, attestation, release identity, and
// generated-file verdicts. Berth tests its caller, not a second implementation.
const contributionWorkflow = "Dematom1/oss-workflows/.github/workflows/contribution-checks.yml@ee220d669cbb1376363825c273291dea4a539847"

func contribution(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	doc := workflow(t, "guard-generated-files")
	job := object(object(doc["jobs"])["contribution"])
	require(t, job["name"] == "contribution", "required contribution check prefix must remain stable")
	require(t, job["uses"] == contributionWorkflow, "contribution checks must use the reviewed shared workflow pin")
	require(t, len(list(job["steps"])) == 0 && job["if"] == nil, "shared contribution checks must not be bypassed by local code or a job guard")
	return doc, job
}

func TestGuardGeneratedFilesWorkflow(t *testing.T) {
	doc, job := contribution(t)
	require(t, object(job["with"])["release-bot-login"] == "${{ vars.RELEASE_APP_BOT_LOGIN }}", "release exemptions must use the configured App identity")
	require(t, job["secrets"] == nil, "metadata-only contribution checks need no secrets")
	permissions := object(doc["permissions"])
	require(t, len(permissions) == 2 && permissions["contents"] == "read" && permissions["pull-requests"] == "read", "shared policy caller must retain read-only metadata permissions")
	on := object(doc["on"])
	require(t, len(on) == 1 && on["pull_request"] != nil, "shared policy must retain the pull_request trust boundary")
}
