package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Work item inbox — the fetching half (docs/80 / ADR 0061).
//
// The Control Plane owns the saved queries and the cache; this Agent owns the provider
// tokens. CP posts the queries here, we resolve them against the provider and hand back
// NON-SECRET rows (key / title / state / url / assignee / labels). The token never
// leaves the container and no description or comment is returned — those are read inside
// the session, where the agent can use `gh` or the Jira MCP.
//
// ★ Deliberately NOT via the `gh` CLI (ADR 0061 decision 3). This process is the very
// credential helper that hands `gh` its GH_TOKEN, so shelling out would be a round trip
// through our own wrapper; and a 5-minute自走 job must not depend on `gh --json` field
// names, which move with the binary's version.
//
// This feature stores nothing in the container: no cache, no state, no config.

const (
	// workItemFetchPerQuery caps one query's rows. Full synchronisation is a non-goal
	// (docs/80 §80.12) — the saved query is what keeps the rail short.
	workItemFetchPerQuery = 50
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
	QueryID   string   `json:"queryId"`
	Provider  string   `json:"provider"`
	Kind      string   `json:"kind"`
	Key       string   `json:"key"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	URL       string   `json:"url"`
	Assignee  string   `json:"assignee"`
	Labels    []string `json:"labels"`
	Repo      string   `json:"repo"`
	UpdatedAt string   `json:"updatedAt"`
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
	if len(in.Queries) == 0 {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "errors": errs})
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
		rows, err := fetchWorkItemQuery(s, q)
		if err != nil {
			errs = append(errs, workItemErrOut{QueryID: q.ID, Message: err.Error()})
			continue
		}
		items = append(items, rows...)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "errors": errs})
}

func fetchWorkItemQuery(s *secrets.Data, q workItemQueryIn) ([]workItemOut, error) {
	query := strings.TrimSpace(q.Query)
	if query == "" {
		return nil, fmt.Errorf("query is empty")
	}
	switch strings.TrimSpace(q.Provider) {
	case "", "github":
		e, ok := s.Git["github.com"]
		if !ok || e.Token == "" {
			return nil, fmt.Errorf("GitHub is not connected")
		}
		return githubSearchWorkItems(e.Token, q.ID, query)
	case "jira":
		if !jiraConnected(s.Jira) {
			return nil, fmt.Errorf("Jira is not connected")
		}
		return jiraSearchWorkItems(s.Jira, q.ID, query)
	case "bitbucket":
		// ★ 接続の有無は bitbucketAuthHeader が見る（OAuth と API トークンの 2 経路が
		// あり、「接続済み」の定義がそこにしかない）。
		return bitbucketSearchWorkItems(s, q.ID, query)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", q.Provider)
	}
}

// githubSearchWorkItems resolves one saved search through GET /search/issues, which
// covers issues and pull requests in one call and is what the GitHub UI's own "assigned
// to me" view is built on. One page only — see workItemFetchPerQuery.
//
// ⚠️ The token is the Connections one, whose scope is `repo` (no `read:org`), and the
// host is fixed to github.com: GitHub Enterprise Server is out of scope for v1, exactly
// as for the `gh` wrapper (docs/dev/08 §8.3).
func githubSearchWorkItems(token, queryID, query string) ([]workItemOut, error) {
	u := "https://api.github.com/search/issues?per_page=" + fmt.Sprint(workItemFetchPerQuery) +
		"&sort=updated&order=desc&q=" + url.QueryEscape(query)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	githubHeaders(req, token)
	resp, err := workItemHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			// 403 は権限だけでなくレート制限でも返る。どちらなのかを言い分けないと
			// 「再接続しろ」と言われた利用者が無駄に再認証する。
			if strings.Contains(strings.ToLower(string(body)), "rate limit") {
				return nil, fmt.Errorf("github rate limit reached")
			}
			return nil, fmt.Errorf("github rejected the token (re-connect GitHub)")
		case http.StatusUnprocessableEntity:
			return nil, fmt.Errorf("github could not parse the query")
		}
		return nil, fmt.Errorf("github search %d", resp.StatusCode)
	}
	return parseGitHubSearchItems(body, queryID)
}

// parseGitHubSearchItems maps a /search/issues body onto the shared row shape. Split out
// from the HTTP call so the mapping — the part that actually decides what the rail shows
// — is testable without a network.
func parseGitHubSearchItems(body []byte, queryID string) ([]workItemOut, error) {
	var gr struct {
		Items []struct {
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
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
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
		key := fmt.Sprintf("%s#%d", repo, it.Number)
		if repo == "" {
			key = fmt.Sprintf("#%d", it.Number)
		}
		out = append(out, workItemOut{
			QueryID: queryID, Provider: "github", Kind: kind, Key: key,
			Title: it.Title, State: normalizeGitHubState(it.State, it.Draft),
			URL: it.HTMLURL, Assignee: assignee, Labels: labels,
			Repo: repo, UpdatedAt: it.UpdatedAt,
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
