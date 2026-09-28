package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// CI and merge-conflict status for the pull request rows of a GitHub list (#1113).
//
// /search/issues carries neither, and the detail panel's way of reading them (three REST calls
// per PR) cannot run for every row on every unattended refresh: a page is up to 100 rows. One
// GraphQL `nodes(ids:)` call reads both for the whole page instead — measured at a cost of 2
// points and about 3 s for 100 pull requests.
//
// Best effort, like the detail panel's checks: a failed call (a token GraphQL refuses, a
// timeout, the secondary rate limit) leaves the rows as they were — the rail then draws no CI
// and no conflict marker, and the list itself is never lost to this.

const githubGraphQLURL = "https://api.github.com/graphql"

// githubPRStatusQuery asks for counts rather than the check runs themselves, so the answer stays
// the same size whether a commit has two checks or two hundred. `contexts` spans both check runs
// (Actions and other apps) and commit statuses (older CI services); the detail panel reads check
// runs only.
const githubPRStatusQuery = `query($ids:[ID!]!){nodes(ids:$ids){... on PullRequest{id mergeable ` +
	`commits(last:1){nodes{commit{statusCheckRollup{contexts(first:0){` +
	`checkRunCountsByState{state count} statusContextCountsByState{state count}}}}}}}}}`

// githubEnrichPullRequests fills Checks and Mergeable on the open pull requests among rows.
//
// Closed and merged ones are skipped: GitHub no longer computes their mergeability (measured:
// every one answers UNKNOWN), and a red CI on a PR that is already merged is not something to
// act on.
func githubEnrichPullRequests(token string, rows []workItemOut) {
	byID := map[string][]int{}
	ids := []string{}
	for i, r := range rows {
		if r.Kind != "pr" || r.nodeID == "" || (r.State != "open" && r.State != "in_progress") {
			continue
		}
		if _, seen := byID[r.nodeID]; !seen {
			ids = append(ids, r.nodeID)
		}
		byID[r.nodeID] = append(byID[r.nodeID], i)
	}
	if len(ids) == 0 {
		return
	}
	payload, err := json.Marshal(map[string]any{"query": githubPRStatusQuery, "variables": map[string]any{"ids": ids}})
	if err != nil {
		return
	}
	req, err := http.NewRequest("POST", githubGraphQLURL, bytes.NewReader(payload))
	if err != nil {
		return
	}
	gitx.GithubHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := workItemHTTPClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return
	}
	for id, st := range parseGitHubPRStatuses(body) {
		for _, i := range byID[id] {
			rows[i].Checks = st.checks
			rows[i].Mergeable = st.mergeable
		}
	}
}

type githubPRStatus struct {
	checks    workItemChecksOut
	mergeable string
}

// parseGitHubPRStatuses maps a nodes(ids:) answer by node ID. The `errors` array is ignored on
// purpose: GitHub answers 200 with a null node and an error for an ID it cannot resolve (a
// deleted PR, a repository the token lost), and the other nodes in the same answer are still good.
func parseGitHubPRStatuses(body []byte) map[string]githubPRStatus {
	type counts []struct {
		State string `json:"state"`
		Count int    `json:"count"`
	}
	var gr struct {
		Data struct {
			Nodes []*struct {
				ID        string `json:"id"`
				Mergeable string `json:"mergeable"`
				Commits   struct {
					Nodes []struct {
						Commit struct {
							Rollup *struct {
								Contexts struct {
									CheckRuns counts `json:"checkRunCountsByState"`
									Statuses  counts `json:"statusContextCountsByState"`
								} `json:"contexts"`
							} `json:"statusCheckRollup"`
						} `json:"commit"`
					} `json:"nodes"`
				} `json:"commits"`
			} `json:"nodes"`
		} `json:"data"`
	}
	out := map[string]githubPRStatus{}
	if err := json.Unmarshal(body, &gr); err != nil {
		return out
	}
	for _, n := range gr.Data.Nodes {
		if n == nil || n.ID == "" {
			continue
		}
		st := githubPRStatus{mergeable: githubMergeableState(n.Mergeable)}
		// A null rollup is a head commit nothing ran against: no checks, which stays "" rather
		// than green.
		if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.Rollup != nil {
			c := n.Commits.Nodes[0].Commit.Rollup.Contexts
			for _, x := range c.CheckRuns {
				st.checks.Total += x.Count
				switch strings.ToUpper(x.State) {
				// The same conclusions parseGitHubCheckRuns counts as failed, so the row and the
				// panel agree on what red means.
				case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
					st.checks.Failed += x.Count
				case "QUEUED", "IN_PROGRESS", "PENDING", "WAITING":
					st.checks.Pending += x.Count
				}
			}
			for _, x := range c.Statuses {
				st.checks.Total += x.Count
				switch strings.ToUpper(x.State) {
				case "FAILURE", "ERROR":
					st.checks.Failed += x.Count
				// EXPECTED is a required status no one has reported yet — still waiting.
				case "PENDING", "EXPECTED":
					st.checks.Pending += x.Count
				}
			}
			st.checks.settle()
		}
		out[n.ID] = st
	}
	return out
}

// githubMergeableState maps GraphQL's MergeableState onto the detail panel's vocabulary.
// UNKNOWN is what GitHub answers while it is still computing — usually the first read after a
// push — and must not read as clean.
func githubMergeableState(s string) string {
	switch strings.ToUpper(s) {
	case "MERGEABLE":
		return "clean"
	case "CONFLICTING":
		return "conflict"
	}
	return "unknown"
}
