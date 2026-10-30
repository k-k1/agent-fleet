package workitems

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
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
func githubFoldExtraChecks(token, base, key, head string, out *workItemDetailOut, checkRunsBody []byte) {
	reported, suites := githubCheckRunNames(checkRunsBody)
	c := out.Checks
	sha := url.PathEscape(head)

	if sb, err := githubGetJSON(token, fmt.Sprintf("%s/commits/%s/status?per_page=%d", base, sha, workItemDetailChecks), key); err == nil {
		var st struct {
			Statuses []struct {
				Context string `json:"context"`
				State   string `json:"state"`
			} `json:"statuses"`
		}
		if json.Unmarshal(sb, &st) == nil {
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
	}

	// The remaining lookups only matter while the PR can still change, and a red line cannot get
	// redder.
	if out.State == "open" {
		if rb, err := githubGetJSON(token, fmt.Sprintf("%s/actions/runs?head_sha=%s&per_page=%d", base, sha, workItemDetailChecks), key); err == nil {
			var runs struct {
				WorkflowRuns []struct {
					Status       string `json:"status"`
					CheckSuiteID int64  `json:"check_suite_id"`
				} `json:"workflow_runs"`
			}
			if json.Unmarshal(rb, &runs) == nil {
				for _, r := range runs.WorkflowRuns {
					// A run whose suite already has check runs is counted through them.
					if strings.ToLower(r.Status) == "completed" || suites[r.CheckSuiteID] {
						continue
					}
					c.Total++
					c.Pending++
				}
			}
		}
		if c.Failed == 0 && out.BaseBranch != "" {
			for _, name := range githubRequiredContexts(token, base, key, out.BaseBranch) {
				if reported[name] {
					continue
				}
				reported[name] = true
				c.Total++
				c.Pending++
			}
		}
	}
	c.settle()
	out.Checks = c
}

// githubCheckRunNames returns the names of the listed check runs and the check suites they
// belong to.
func githubCheckRunNames(body []byte) (names map[string]bool, suites map[int64]bool) {
	names, suites = map[string]bool{}, map[int64]bool{}
	var cr struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			CheckSuite struct {
				ID int64 `json:"id"`
			} `json:"check_suite"`
		} `json:"check_runs"`
	}
	if json.Unmarshal(body, &cr) != nil {
		return
	}
	for _, r := range cr.CheckRuns {
		names[r.Name] = true
		if r.CheckSuite.ID != 0 {
			suites[r.CheckSuite.ID] = true
		}
	}
	return
}

// githubRequiredContexts lists the status contexts the base branch requires, from rulesets
// (readable with read access) and from classic branch protection (the branch object carries it
// for readers who can see it). A lookup that fails contributes nothing.
func githubRequiredContexts(token, base, key, branch string) []string {
	esc := strings.ReplaceAll(url.PathEscape(branch), "%2F", "/")
	var names []string
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	if b, err := githubGetJSON(token, fmt.Sprintf("%s/rules/branches/%s", base, esc), key); err == nil {
		var rules []struct {
			Type       string `json:"type"`
			Parameters struct {
				Checks []struct {
					Context string `json:"context"`
				} `json:"required_status_checks"`
			} `json:"parameters"`
		}
		if json.Unmarshal(b, &rules) == nil {
			for _, r := range rules {
				if r.Type == "required_status_checks" {
					for _, c := range r.Parameters.Checks {
						add(c.Context)
					}
				}
			}
		}
	}
	if b, err := githubGetJSON(token, fmt.Sprintf("%s/branches/%s", base, esc), key); err == nil {
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
		if json.Unmarshal(b, &br) == nil {
			for _, c := range br.Protection.Required.Contexts {
				add(c)
			}
			for _, c := range br.Protection.Required.Checks {
				add(c.Context)
			}
		}
	}
	return names
}
