package workitems

import "testing"

const ciBase = "/repos/acme/web"

var ciGreenRuns = `{"check_runs":[
  {"name":"build","status":"completed","conclusion":"success","check_suite":{"id":1}},
  {"name":"test","status":"completed","conclusion":"success","check_suite":{"id":1}}]}`

func ciRoutes(extra map[string]string) map[string]string {
	r := map[string]string{
		ciBase + "/pulls/3":                             string(mergePR(``)),
		ciBase + "/commits/" + mergeSHA + "/check-runs": ciGreenRuns,
	}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

func ciDetail(t *testing.T, routes map[string]string) workItemChecksOut {
	t.Helper()
	fakeGitHubScopes(t, nil, routes)
	out, err := githubPullRequestDetail("tok", "acme/web#3")
	if err != nil {
		t.Fatalf("an extra lookup must never fail the panel: %v", err)
	}
	return out.Checks
}

func TestGitHubPanelCIBaseline(t *testing.T) {
	// Every extra lookup 404s: the line is what the check runs alone said.
	if got := ciDetail(t, ciRoutes(nil)); got.State != "success" || got.Total != 2 {
		t.Errorf("degraded = %+v, want the check-run-only success", got)
	}
}

func TestGitHubPanelCIUnreportedRequiredRuleset(t *testing.T) {
	got := ciDetail(t, ciRoutes(map[string]string{
		ciBase + "/rules/branches/main": `[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"build"},{"context":"e2e"}]}}]`,
	}))
	if got.State != "pending" || got.Pending != 1 || got.Total != 3 {
		t.Errorf("= %+v, want pending with the unreported e2e counted", got)
	}
}

func TestGitHubPanelCIUnreportedRequiredClassic(t *testing.T) {
	got := ciDetail(t, ciRoutes(map[string]string{
		ciBase + "/branches/main": `{"protection":{"required_status_checks":{"contexts":["test","lint"]}}}`,
	}))
	if got.State != "pending" || got.Pending != 1 {
		t.Errorf("= %+v, want pending for lint", got)
	}
}

func TestGitHubPanelCIQueuedWorkflowRun(t *testing.T) {
	got := ciDetail(t, ciRoutes(map[string]string{
		ciBase + "/actions/runs": `{"workflow_runs":[
		  {"status":"queued","check_suite_id":9},
		  {"status":"in_progress","check_suite_id":1},
		  {"status":"completed","check_suite_id":8}]}`,
	}))
	// suite 1 already has check runs (counted through them), suite 8 is done.
	if got.State != "pending" || got.Pending != 1 || got.Total != 3 {
		t.Errorf("= %+v, want only the queued run on a new suite counted", got)
	}
}

func TestGitHubPanelCICommitStatuses(t *testing.T) {
	got := ciDetail(t, ciRoutes(map[string]string{
		ciBase + "/commits/" + mergeSHA + "/status": `{"statuses":[{"context":"legacy","state":"failure"}]}`,
	}))
	if got.State != "failure" || got.Failed != 1 || got.Total != 3 {
		t.Errorf("= %+v, want the failing status to turn the line red", got)
	}
	got = ciDetail(t, ciRoutes(map[string]string{
		ciBase + "/commits/" + mergeSHA + "/status": `{"statuses":[{"context":"legacy","state":"success"}]}`,
		ciBase + "/rules/branches/main":             `[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"legacy"}]}}]`,
	}))
	if got.State != "success" || got.Total != 3 {
		t.Errorf("= %+v, want a required context satisfied by a status to be green", got)
	}
}

func TestGitHubPanelCIBrokenExtraBodies(t *testing.T) {
	got := ciDetail(t, ciRoutes(map[string]string{
		ciBase + "/commits/" + mergeSHA + "/status": `not json`,
		ciBase + "/actions/runs":                    `{"workflow_runs":"x"}`,
		ciBase + "/rules/branches/main":             `{}`,
		ciBase + "/branches/main":                   `[]`,
	}))
	if got.State != "success" || got.Total != 2 {
		t.Errorf("= %+v, want the check-run-only success", got)
	}
}
