package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Work item inbox — the fetching half (docs/log/80 / ADR 0061).
//
// The Control Plane owns the saved queries and the cache; this Agent owns the provider
// tokens. CP posts the queries here, we resolve them against the provider and hand back
// NON-SECRET rows (key / title / state / url / assignee / labels). The token never
// leaves the container and no description or comment is returned — those are read inside
// the session, where the agent can use `gh` or the Jira MCP.
//
// Deliberately NOT via the `gh` CLI (ADR 0061 decision 3). This process is the very
// credential helper that hands `gh` its GH_TOKEN, so shelling out would be a round trip
// through our own wrapper; and an unattended job running every 5 minutes must not depend on
// `gh --json` field names, which move with the binary's version.
//
// This feature stores nothing in the container: no cache, no state, no config.

const (
	// workItemFetchPerQuery caps one query's rows. Full synchronisation is a non-goal
	// (docs/log/80 §80.12) — the saved query is what keeps the rail short. A query that
	// matches more says so on the rail (workItemTruncOut) instead of dropping rows silently.
	// 50 is Bitbucket's largest page, and Jira shares it.
	workItemFetchPerQuery = 50
	// workItemFetchGitHub is GitHub's own cap: /search/issues takes per_page up to 100. At 50
	// a plain `is:open involves:@me` already overflowed for an active member (#1095).
	workItemFetchGitHub = 100
	// workItemFetchQueries caps how many queries one request may carry, so a bad CP
	// request cannot fan out into an unbounded number of provider calls.
	workItemFetchQueries = 10
)

var workItemHTTPClient = &http.Client{Timeout: 20 * time.Second}

type workItemQueryIn struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Query    string `json:"query"`
}

// workItemOut is one row. Kind is "issue" or "pr"; State is normalised across providers
// to open / in_progress / done / other; Repo is "owner/name" when the provider has one
// (the Console seeds the launch target from it).
type workItemOut struct {
	QueryID  string   `json:"queryId"`
	Provider string   `json:"provider"`
	Kind     string   `json:"kind"`
	Key      string   `json:"key"`
	Title    string   `json:"title"`
	State    string   `json:"state"`
	URL      string   `json:"url"`
	Assignee string   `json:"assignee"`
	Labels   []string `json:"labels"`
	// Type is the tracker's own issue type (GitHub's issue type, Jira's issuetype), "" when the
	// tracker has none. The branch-name resolver maps it to a kind before the labels (ADR 0103
	// decision 4).
	Type string `json:"type"`
	// LabelColors maps a label name to the tracker's own colour as lowercase "rrggbb". Only
	// GitHub has label colours; a label missing here is drawn in a colour derived from its
	// name. Never nil, so it marshals to {} rather than null.
	LabelColors map[string]string `json:"labelColors"`
	Repo        string            `json:"repo"`
	UpdatedAt   string            `json:"updatedAt"`
	// Checks and Mergeable are filled for open GitHub pull requests only (see
	// githubEnrichPullRequests). Zero values mean "not read", which the rail draws as nothing —
	// the same as "no checks", and never as green.
	Checks    workItemChecksOut `json:"checks"`
	Mergeable string            `json:"mergeable"`

	// nodeID is GitHub's global ID, the key the enrichment call looks rows up by. Not on the wire.
	nodeID string
}

// gitHubLabel is one entry of a GitHub issue's or pull request's `labels` array.
type gitHubLabel struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

var hexColorRe = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)

// gitHubLabelColors keeps the colours that are well-formed "rrggbb". The value ends up in a
// CSS custom property on the Console, so anything else is dropped here rather than trusted.
func gitHubLabelColors(labels []gitHubLabel) map[string]string {
	out := map[string]string{}
	for _, l := range labels {
		if l.Name != "" && hexColorRe.MatchString(l.Color) {
			out[l.Name] = strings.ToLower(l.Color)
		}
	}
	return out
}

// workItemTruncOut marks a query that matched more rows than one page carries. Total is the
// provider's count of matches, or 0 when the provider does not say (the page was simply
// full). Only truncated queries are listed.
type workItemTruncOut struct {
	QueryID string `json:"queryId"`
	Total   int    `json:"total"`
}

type workItemErrOut struct {
	QueryID string `json:"queryId"`
	Message string `json:"message"`
}

// handleWorkItemsFetch — POST /work-items/fetch {queries:[{id,provider,query}]}.
//
// Per-query failures come back in `errors`, not as an HTTP error: one broken query
// (a typo, a revoked token) must not blank the whole rail, and the Console shows the
// message on that query's row.
func handleWorkItemsFetch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Queries []workItemQueryIn `json:"queries"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	items := []workItemOut{}
	errs := []workItemErrOut{}
	truncs := []workItemTruncOut{}
	if len(in.Queries) == 0 {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "errors": errs, "truncated": truncs})
		return
	}
	if len(in.Queries) > workItemFetchQueries {
		in.Queries = in.Queries[:workItemFetchQueries]
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	for _, q := range in.Queries {
		rows, total, err := fetchWorkItemQuery(s, q)
		if err != nil {
			errs = append(errs, workItemErrOut{QueryID: q.ID, Message: err.Error()})
			continue
		}
		items = append(items, rows...)
		if workItemTruncated(len(rows), total, workItemFetchCap(q.Provider)) {
			truncs = append(truncs, workItemTruncOut{QueryID: q.ID, Total: total})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "errors": errs, "truncated": truncs})
}

// workItemFetchCap is the page size asked of the provider a query names.
func workItemFetchCap(provider string) int {
	switch strings.TrimSpace(provider) {
	case "", "github":
		return workItemFetchGitHub
	}
	return workItemFetchPerQuery
}

// workItemTruncated decides whether a query matched more than the rail received. A known total
// is trusted; without one, a full page is taken to mean there is more (Jira's newer search and
// Bitbucket's projected page do not count). The cost of that guess is one needless note on the
// rare query that matches exactly the cap.
func workItemTruncated(fetched, total, capN int) bool {
	if total > 0 {
		return total > fetched
	}
	return fetched >= capN
}

// fetchWorkItemQuery resolves one saved query. total is the provider's count of matches, or 0
// when it does not report one.
func fetchWorkItemQuery(s *secrets.Data, q workItemQueryIn) ([]workItemOut, int, error) {
	query := strings.TrimSpace(q.Query)
	if query == "" {
		return nil, 0, fmt.Errorf("query is empty")
	}
	switch strings.TrimSpace(q.Provider) {
	case "", "github":
		e, ok := s.Git["github.com"]
		if !ok || e.Token == "" {
			return nil, 0, fmt.Errorf("GitHub is not connected")
		}
		return githubSearchWorkItems(e.Token, q.ID, query)
	case "jira":
		if !jiraConnected(s.Jira) {
			return nil, 0, fmt.Errorf("Jira is not connected")
		}
		rows, err := jiraSearchWorkItems(s.Jira, q.ID, query)
		return rows, 0, err
	case "bitbucket":
		// bitbucketAuthHeader is what decides whether we are connected: there are two paths
		// (OAuth and an API token), and "connected" is defined nowhere else.
		rows, err := bitbucketSearchWorkItems(s, q.ID, query)
		return rows, 0, err
	default:
		return nil, 0, fmt.Errorf("unsupported provider: %s", q.Provider)
	}
}

// githubSearchWorkItems resolves one saved search through GET /search/issues, which
// covers issues and pull requests in one call and is what the GitHub UI's own "assigned
// to me" view is built on. One page only — see workItemFetchGitHub. The second result is
// GitHub's total_count, which is what tells the rail how many rows the page left out.
//
// advanced_search=true is what lets a saved query use `OR` and parentheses. Members need them
// to ask one query for "assigned to me OR mine OR waiting on my review" — `assignee:` alone
// never matches a pull request they opened, because GitHub does not make a PR's author its
// assignee. Without the parameter GitHub answers 422 and the rail says only "could not parse
// the query", which reads as their typo. Measured before turning it on: queries that use no
// operator return the identical rows either way (`author:@me is:open`, `assignee:@me is:pr`),
// so it does not reinterpret the queries members already saved.
//
// The Console's default query deliberately does NOT use `OR` (WorkItemQueryModal.tsx): a
// workspace keeps the Agent it started with, so after an upgrade the previous image is still
// answering here, and a default that only this one can parse would greet those members with
// that same 422.
//
// The token is the Connections one, whose scope is `repo` (no `read:org`), and the
// host is fixed to github.com: GitHub Enterprise Server is out of scope for v1, exactly
// as for the `gh` wrapper (docs/build/08 §8.3).
func githubSearchWorkItems(token, queryID, query string) ([]workItemOut, int, error) {
	u := "https://api.github.com/search/issues?per_page=" + fmt.Sprint(workItemFetchGitHub) +
		"&sort=updated&order=desc&advanced_search=true&q=" + url.QueryEscape(query)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, 0, err
	}
	gitx.GithubHeaders(req, token)
	resp, err := workItemHTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			// 403 covers rate limiting as well as permissions. Without telling the two apart, a
			// user told to re-connect re-authenticates for nothing.
			if strings.Contains(strings.ToLower(string(body)), "rate limit") {
				return nil, 0, fmt.Errorf("github rate limit reached")
			}
			return nil, 0, fmt.Errorf("github rejected the token (re-connect GitHub)")
		case http.StatusUnprocessableEntity:
			return nil, 0, fmt.Errorf("github could not parse the query")
		}
		return nil, 0, fmt.Errorf("github search %d", resp.StatusCode)
	}
	rows, err := parseGitHubSearchItems(body, queryID)
	if err != nil {
		return nil, 0, err
	}
	githubEnrichPullRequests(token, rows)
	var tc struct {
		TotalCount int `json:"total_count"`
	}
	_ = json.Unmarshal(body, &tc)
	return rows, tc.TotalCount, nil
}

// parseGitHubSearchItems maps a /search/issues body onto the shared row shape. Split out
// from the HTTP call so the mapping — the part that actually decides what the rail shows
// — is testable without a network.
func parseGitHubSearchItems(body []byte, queryID string) ([]workItemOut, error) {
	var gr struct {
		Items []struct {
			NodeID      string `json:"node_id"`
			Number      int    `json:"number"`
			Title       string `json:"title"`
			State       string `json:"state"`
			StateReason string `json:"state_reason"`
			HTMLURL     string `json:"html_url"`
			RepoURL     string `json:"repository_url"`
			UpdatedAt   string `json:"updated_at"`
			Draft       bool   `json:"draft"`
			PullRequest *struct {
				URL string `json:"url"`
			} `json:"pull_request"`
			Assignees []struct {
				Login string `json:"login"`
			} `json:"assignees"`
			Labels []gitHubLabel `json:"labels"`
			// Type is null unless the organisation has issue types set up.
			Type *struct {
				Name string `json:"name"`
			} `json:"type"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, err
	}
	out := make([]workItemOut, 0, len(gr.Items))
	for _, it := range gr.Items {
		repo := repoFromGitHubAPIURL(it.RepoURL)
		kind := "issue"
		if it.PullRequest != nil {
			kind = "pr"
		}
		assignee := ""
		if len(it.Assignees) > 0 {
			assignee = it.Assignees[0].Login
		}
		labels := make([]string, 0, len(it.Labels))
		for _, l := range it.Labels {
			labels = append(labels, l.Name)
		}
		typ := ""
		if it.Type != nil {
			typ = it.Type.Name
		}
		key := fmt.Sprintf("%s#%d", repo, it.Number)
		if repo == "" {
			key = fmt.Sprintf("#%d", it.Number)
		}
		out = append(out, workItemOut{
			QueryID: queryID, Provider: "github", Kind: kind, Key: key,
			Title: it.Title, State: normalizeGitHubState(it.State, it.Draft),
			URL: it.HTMLURL, Assignee: assignee, Labels: labels, Type: typ,
			LabelColors: gitHubLabelColors(it.Labels), Repo: repo, UpdatedAt: it.UpdatedAt,
			nodeID: it.NodeID,
		})
	}
	return out, nil
}

// normalizeGitHubState maps GitHub's two states onto the shared vocabulary. A draft PR
// is in_progress rather than open: it is the one case where "open" would tell the reader
// something untrue (nobody is waiting on them).
func normalizeGitHubState(state string, draft bool) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "open":
		if draft {
			return "in_progress"
		}
		return "open"
	case "closed":
		return "done"
	default:
		return "other"
	}
}

// repoFromGitHubAPIURL turns "https://api.github.com/repos/owner/name" into "owner/name"
// ("" when the shape is not what we expect — the row still renders, just without a repo).
func repoFromGitHubAPIURL(apiURL string) string {
	const marker = "/repos/"
	i := strings.LastIndex(apiURL, marker)
	if i < 0 {
		return ""
	}
	rest := strings.Trim(apiURL[i+len(marker):], "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return rest
}
