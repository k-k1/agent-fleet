package workitems

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// The check-runs list alone under-reports CI in two windows: a workflow run that is queued has
// no check run yet, and a required context that nothing has reported has neither a run nor a
// status. Both read as "every check that exists passed". githubFoldExtraChecks closes them, and
// also counts commit statuses (older CI services), which the inbox row's statusCheckRollup
// already does.
//
// Why REST lookups rather than the row's GraphQL rollup: the rollup holds only contexts that
// already reported, so it has the same blind spot for queued runs and absent required contexts,
// and its isRequired flag is computed per pull request only for contexts that exist.
//
// Every lookup here is best effort and can only move the line toward pending/failure with
// evidence: a 403 (branch protection for a non-admin), a rate limit or a malformed body leaves
// that part out, so the line degrades to what the check runs alone said — never to success
// that was not there before and never to an error for the panel.

// githubFoldExtraChecks adds commit statuses, unfinished workflow runs and unreported required
// contexts to out.Checks, which already holds the folded check runs (checkRunsBody).
func githubFoldExtraChecks(
	token, base, key, head string, out *workItemDetailOut, checkRunsBody []byte,
) {
	reported, suites, complete := githubCheckRunNames(checkRunsBody)
	c := out.Checks
	sha := url.PathEscape(head)
	open := out.State == "open"
	// Runs and required contexts are only judged against a complete check-run list: with the
	// first page alone, a context or suite on page two would read as unreported.
	judge := open && complete

	// The lookups are independent, so they go out together: serial, a slow GitHub would cost
	// the panel one client timeout per lookup.
	var statusBody, runsBody, rulesBody, branchBody []byte
	var wg sync.WaitGroup
	get := func(dst *[]byte, u string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b, err := githubGetJSON(token, u, key); err == nil {
				*dst = b
			}
		}()
	}
	get(&statusBody, fmt.Sprintf("%s/commits/%s/status?per_page=%d", base, sha, workItemDetailChecks))
	if judge {
		runsURL := fmt.Sprintf("%s/actions/runs?head_sha=%s&per_page=%d", base, sha, workItemDetailChecks)
		get(&runsBody, runsURL)
		if out.BaseBranch != "" {
			esc := strings.ReplaceAll(url.PathEscape(out.BaseBranch), "%2F", "/")
			get(&rulesBody, fmt.Sprintf("%s/rules/branches/%s", base, esc))
			get(&branchBody, fmt.Sprintf("%s/branches/%s", base, esc))
		}
	}
	wg.Wait()

	var st struct {
		Statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		} `json:"statuses"`
	}
	if statusBody != nil && json.Unmarshal(statusBody, &st) == nil {
		for _, s := range st.Statuses {
			reported[s.Context] = true
			c.Total++
			switch strings.ToLower(s.State) {
			case "failure", "error":
				c.Failed++
			case "success":
			default:
				c.Pending++
			}
		}
	}

	var runs struct {
		WorkflowRuns []struct {
			Status       string `json:"status"`
			Conclusion   string `json:"conclusion"`
			CheckSuiteID int64  `json:"check_suite_id"`
		} `json:"workflow_runs"`
	}
	if runsBody != nil && json.Unmarshal(runsBody, &runs) == nil {
		for _, r := range runs.WorkflowRuns {
			// A run whose suite already has check runs is counted through them.
			if suites[r.CheckSuiteID] {
				continue
			}
			if strings.ToLower(r.Status) != "completed" {
				c.Total++
				c.Pending++
			} else if githubConclusionFailed(r.Conclusion) {
				// A workflow that could not start (broken YAML) creates no jobs, so no check
				// run ever records its failure.
				c.Total++
				c.Failed++
			}
		}
	}

	if c.Failed == 0 {
		for _, name := range githubRequiredContexts(rulesBody, branchBody) {
			if reported[name] {
				continue
			}
			reported[name] = true
			c.Total++
			c.Pending++
		}
	}
	c.settle()
	out.Checks = c
}

// githubCheckRunNames returns the names of the listed check runs and the check suites they
// belong to.
//
// complete is false when the page read is not the whole list (total_count says more exist).
func githubCheckRunNames(
	body []byte,
) (names map[string]bool, suites map[int64]bool, complete bool) {
	names, suites = map[string]bool{}, map[int64]bool{}
	var cr struct {
		TotalCount int `json:"total_count"`
		CheckRuns  []struct {
			Name       string `json:"name"`
			CheckSuite struct {
				ID int64 `json:"id"`
			} `json:"check_suite"`
		} `json:"check_runs"`
	}
	if json.Unmarshal(body, &cr) != nil {
		return
	}
	complete = cr.TotalCount <= len(cr.CheckRuns)
	for _, r := range cr.CheckRuns {
		names[r.Name] = true
		if r.CheckSuite.ID != 0 {
			suites[r.CheckSuite.ID] = true
		}
	}
	return
}

// githubRequiredContexts lists the status contexts the base branch requires, from the rulesets
// body (readable with read access) and the branch object (carrying classic protection for
// readers who can see it). A body that is absent or malformed contributes nothing.
//
// Names only: a ruleset also pins an integration_id, and a same-named check from another app does
// not satisfy it on GitHub but does here. That edge is accepted; the cost is a green line GitHub
// itself would hold back, and only for a name collision between apps.
func githubRequiredContexts(rulesBody, branchBody []byte) []string {
	var names []string
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			Checks []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	if rulesBody != nil && json.Unmarshal(rulesBody, &rules) == nil {
		for _, r := range rules {
			if r.Type == "required_status_checks" {
				for _, c := range r.Parameters.Checks {
					add(c.Context)
				}
			}
		}
	}
	var br struct {
		Protection struct {
			Required struct {
				Contexts []string `json:"contexts"`
				Checks   []struct {
					Context string `json:"context"`
				} `json:"checks"`
			} `json:"required_status_checks"`
		} `json:"protection"`
	}
	if branchBody != nil && json.Unmarshal(branchBody, &br) == nil {
		for _, c := range br.Protection.Required.Contexts {
			add(c)
		}
		for _, c := range br.Protection.Required.Checks {
			add(c.Context)
		}
	}
	return names
}
