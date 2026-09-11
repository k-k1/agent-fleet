package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Pull request detail — "what does this PR look like right now", read when a member opens
// the rail row (docs/log/80 §80.24).
//
// The rail draws the CP cache: key/title/state/url/assignee/labels/repo/updated, refreshed at
// most every five minutes. For an issue that is enough to choose one. For a pull request it is
// not — whether it is a draft, whether it conflicts, who has approved it and whether CI is red
// are exactly what decides if it is worth opening, and all four change between two refreshes.
//
// Three things this deliberately does not do:
//   - It returns no description and no comments. The promise that the CP never holds a ticket
//     body (ADR 0061 decision 2) is kept by not fetching one: the field lists below are the
//     promise written into the request, as in the list adapter.
//   - Nothing is stored. Not here (this feature keeps no state in the container) and not in the
//     CP, which relays this response to the browser and forgets it.
//   - It never runs on its own. The list refresh is a background job on a timer; this runs only
//     when a human opened the panel, which is why it may spend three calls on one item.
const (
	// workItemDetailReviews caps the review page. A PR with more than this many reviews is one
	// whose approval count is not what anybody is still reading the panel for.
	workItemDetailReviews = 100
	// workItemDetailChecks caps the check runs folded into one state.
	workItemDetailChecks = 100
	// workItemDetailFiles caps the diffstat page Bitbucket is asked for (GitHub returns the
	// counts on the pull request object itself and needs no second call).
	workItemDetailFiles = 100
)

// workItemChecksOut folds a commit's checks into one line. The counts are kept because
// "failing" alone cannot say whether one job of forty is red or the whole run is.
type workItemChecksOut struct {
	// State is "success" / "failure" / "pending", or "" when the provider reported no checks
	// at all — which must not render as green.
	State   string `json:"state"`
	Total   int    `json:"total"`
	Failed  int    `json:"failed"`
	Pending int    `json:"pending"`
}

// workItemReviewOut is one reviewer's standing on the PR. State is "approved",
// "changes_requested" or "pending" (asked, has not answered).
type workItemReviewOut struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// workItemDetailOut is the panel's payload. Everything here is non-secret metadata of the same
// kind the rail already shows — no body, no comment text, no token.
type workItemDetailOut struct {
	Provider string   `json:"provider"`
	Key      string   `json:"key"`
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	State    string   `json:"state"`
	URL      string   `json:"url"`
	Author   string   `json:"author"`
	Assignee string   `json:"assignee"`
	Labels   []string `json:"labels"`
	Repo     string   `json:"repo"`
	// UpdatedAt is UTC RFC3339, normalised per provider exactly as the list rows are: the panel
	// prints it next to the cached stamp, and two formats side by side read as two clocks.
	UpdatedAt string `json:"updatedAt"`
	Draft     bool   `json:"draft"`
	Merged    bool   `json:"merged"`
	// Mergeable is "clean" / "conflict" / "unknown". GitHub computes it asynchronously and
	// answers null while it is still thinking, and Bitbucket does not expose it at all — both
	// are "unknown", because claiming "clean" from a missing field is how a member is sent to
	// a conflicted branch.
	Mergeable    string              `json:"mergeable"`
	BaseBranch   string              `json:"baseBranch"`
	HeadBranch   string              `json:"headBranch"`
	Additions    int                 `json:"additions"`
	Deletions    int                 `json:"deletions"`
	ChangedFiles int                 `json:"changedFiles"`
	Comments     int                 `json:"comments"`
	Reviews      []workItemReviewOut `json:"reviews"`
	Checks       workItemChecksOut   `json:"checks"`
}

// handleWorkItemsDetail — POST /work-items/detail {provider, key}.
func handleWorkItemsDetail(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "key is required")
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	var out *workItemDetailOut
	switch strings.TrimSpace(in.Provider) {
	case "", "github":
		e, ok := s.Git["github.com"]
		if !ok || e.Token == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "not_connected", "GitHub is not connected")
			return
		}
		out, err = githubPullRequestDetail(e.Token, key)
	case "bitbucket":
		out, err = bitbucketPullRequestDetail(s, key)
	default:
		// Jira lands here. There is no pull request behind a Jira key, so this is not a gap to
		// fill later — the Console asks only for rows whose kind is "pr".
		httpx.WriteErr(w, http.StatusBadRequest, "bad_provider",
			"pull request details are available for GitHub and Bitbucket only")
		return
	}
	if err != nil {
		// 502: the provider refused or could not be reached. The panel keeps showing the cached
		// row and says the refresh failed, so this must stay distinguishable from af's own faults.
		httpx.WriteErr(w, http.StatusBadGateway, "provider_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// --- GitHub -------------------------------------------------------------------

// githubPullRequestDetail reads one PR: the pull request itself, its reviews, and the check
// runs of its head commit.
//
// Three calls, and the last two are best effort. A token without the checks permission, or a
// head commit no check ever ran against, must still leave the panel with the PR — the review
// and CI lines then simply do not appear, which is what "" on those fields means.
func githubPullRequestDetail(token, key string) (*workItemDetailOut, error) {
	repo, number, ok := parseGitHubIssueKey(key)
	if !ok {
		return nil, fmt.Errorf("cannot read %q (expected owner/name#number)", key)
	}
	base := "https://api.github.com/repos/" + gitx.EscapeRepoPath(repo)
	body, err := githubGetJSON(token, fmt.Sprintf("%s/pulls/%d", base, number), key)
	if err != nil {
		return nil, err
	}
	out, head, err := parseGitHubPullRequest(body, key)
	if err != nil {
		return nil, err
	}
	if rv, err := githubGetJSON(token, fmt.Sprintf("%s/pulls/%d/reviews?per_page=%d", base, number, workItemDetailReviews), key); err == nil {
		out.Reviews = mergeGitHubReviews(parseGitHubReviews(rv), out.Reviews)
	}
	if head != "" {
		if cr, err := githubGetJSON(token, fmt.Sprintf("%s/commits/%s/check-runs?per_page=%d", base, url.PathEscape(head), workItemDetailChecks), key); err == nil {
			out.Checks = parseGitHubCheckRuns(cr)
		}
	}
	return out, nil
}

func githubGetJSON(token, u, key string) ([]byte, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	gitx.GithubHeaders(req, token)
	resp, err := workItemHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			if strings.Contains(strings.ToLower(string(body)), "rate limit") {
				return nil, fmt.Errorf("github rate limit reached")
			}
			return nil, fmt.Errorf("github refused this read (%d)", resp.StatusCode)
		case http.StatusNotFound:
			// Also what a private repository the token cannot see answers, so do not say "deleted".
			return nil, fmt.Errorf("github has no %s visible to this connection", key)
		}
		return nil, fmt.Errorf("github answered %d", resp.StatusCode)
	}
	return body, nil
}

// parseGitHubPullRequest maps the pull request object. Returns the head SHA as well, which is
// the coordinate the check runs hang off.
func parseGitHubPullRequest(body []byte, key string) (*workItemDetailOut, string, error) {
	var pr struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		State     string `json:"state"`
		HTMLURL   string `json:"html_url"`
		UpdatedAt string `json:"updated_at"`
		Draft     bool   `json:"draft"`
		Merged    bool   `json:"merged"`
		// null while GitHub is still computing it — see workItemDetailOut.Mergeable.
		Mergeable      *bool `json:"mergeable"`
		Additions      int   `json:"additions"`
		Deletions      int   `json:"deletions"`
		ChangedFiles   int   `json:"changed_files"`
		Comments       int   `json:"comments"`
		ReviewComments int   `json:"review_comments"`
		User           struct {
			Login string `json:"login"`
		} `json:"user"`
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"`
		RequestedReviewers []struct {
			Login string `json:"login"`
		} `json:"requested_reviewers"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Base struct {
			Ref  string `json:"ref"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, "", err
	}
	out := &workItemDetailOut{
		Provider: "github", Key: key, Kind: "pr", Title: pr.Title,
		State: normalizeGitHubState(pr.State, pr.Draft), URL: pr.HTMLURL,
		Author: pr.User.Login, Repo: pr.Base.Repo.FullName, UpdatedAt: pr.UpdatedAt,
		Draft: pr.Draft, Merged: pr.Merged, Mergeable: githubMergeable(pr.Mergeable),
		BaseBranch: pr.Base.Ref, HeadBranch: pr.Head.Ref,
		Additions: pr.Additions, Deletions: pr.Deletions, ChangedFiles: pr.ChangedFiles,
		// Both counts, because a PR discussed entirely in line comments otherwise reads as
		// untouched.
		Comments: pr.Comments + pr.ReviewComments,
		// Empty slices, never nil: a nil slice marshals to JSON null and the Console iterates
		// these (the null-labels white screen, docs/log/80 §80.17.5).
		Labels: []string{}, Reviews: []workItemReviewOut{},
	}
	if len(pr.Assignees) > 0 {
		out.Assignee = pr.Assignees[0].Login
	}
	for _, l := range pr.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	// Whoever was asked and has not answered. They are listed with the reviews rather than
	// separately: "who is this waiting on" is one question.
	for _, r := range pr.RequestedReviewers {
		out.Reviews = append(out.Reviews, workItemReviewOut{Name: r.Login, State: "pending"})
	}
	return out, pr.Head.SHA, nil
}

// githubMergeable turns GitHub's tri-state into ours. null means "still computing", which is
// not the same as mergeable and must not be shown as one.
func githubMergeable(v *bool) string {
	if v == nil {
		return "unknown"
	}
	if *v {
		return "clean"
	}
	return "conflict"
}

// parseGitHubReviews folds a review list into one standing per reviewer.
//
// A reviewer can review many times, so only their LAST decision counts — the endpoint returns
// them oldest first, which is what makes "keep overwriting" correct. COMMENTED and PENDING
// reviews are not decisions and are skipped; DISMISSED removes an earlier approval, so it
// drops the reviewer from the list rather than adding a state of its own.
func parseGitHubReviews(body []byte) []workItemReviewOut {
	var rows []struct {
		State string `json:"state"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil
	}
	order := []string{}
	byUser := map[string]string{}
	for _, r := range rows {
		name := r.User.Login
		if name == "" {
			continue
		}
		var state string
		switch strings.ToUpper(strings.TrimSpace(r.State)) {
		case "APPROVED":
			state = "approved"
		case "CHANGES_REQUESTED":
			state = "changes_requested"
		case "DISMISSED":
			state = ""
		default:
			continue // COMMENTED / PENDING: not a decision
		}
		if _, seen := byUser[name]; !seen {
			order = append(order, name)
		}
		byUser[name] = state
	}
	out := []workItemReviewOut{}
	for _, name := range order {
		if s := byUser[name]; s != "" {
			out = append(out, workItemReviewOut{Name: name, State: s})
		}
	}
	return out
}

// mergeGitHubReviews puts the decisions first and the still-pending reviewers after them,
// with one line per person.
//
// A name can be in both lists: GitHub drops a reviewer from requested_reviewers when they
// submit, but a re-request puts them back while their old review is still in the history.
// Pending wins there — the PR is waiting on them again, which is the whole question this
// line answers.
func mergeGitHubReviews(decided, pending []workItemReviewOut) []workItemReviewOut {
	stillAsked := map[string]bool{}
	for _, p := range pending {
		stillAsked[p.Name] = true
	}
	out := []workItemReviewOut{}
	for _, d := range decided {
		if !stillAsked[d.Name] {
			out = append(out, d)
		}
	}
	return append(out, pending...)
}

// parseGitHubCheckRuns folds every check run of the head commit into one state.
//
// Precedence is failure > pending > success, because the panel's single line has to answer
// "can I merge this": one red job among forty green ones is a red PR. neutral and skipped
// count as neither — they are jobs that decided not to have an opinion.
func parseGitHubCheckRuns(body []byte) workItemChecksOut {
	var cr struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := json.Unmarshal(body, &cr); err != nil {
		return workItemChecksOut{}
	}
	out := workItemChecksOut{}
	for _, run := range cr.CheckRuns {
		out.Total++
		if strings.ToLower(run.Status) != "completed" {
			out.Pending++
			continue
		}
		switch strings.ToLower(run.Conclusion) {
		case "failure", "timed_out", "cancelled", "action_required", "startup_failure", "stale":
			out.Failed++
		}
	}
	switch {
	case out.Total == 0:
		out.State = ""
	case out.Failed > 0:
		out.State = "failure"
	case out.Pending > 0:
		out.State = "pending"
	default:
		out.State = "success"
	}
	return out
}

// --- Bitbucket ----------------------------------------------------------------

// bitbucketPullRequestDetail reads one PR, its build statuses and its diffstat.
//
// The 401-refresh-and-retry is the same as the list path: an access token lives about two
// hours, so a panel opened in the afternoon routinely meets an expired one and must not tell
// the member to re-connect (docs/log/80 §80.19).
func bitbucketPullRequestDetail(s *secrets.Data, key string) (*workItemDetailOut, error) {
	repo, number, ok := parseGitHubIssueKey(key)
	if !ok {
		// Same "<workspace>/<repo>#<id>" shape as the GitHub key — both adapters build it in
		// parseBitbucketPullRequests / parseGitHubSearchItems.
		return nil, fmt.Errorf("cannot read %q (expected workspace/repo#number)", key)
	}
	auth, err := gitx.BitbucketAuthHeader(s)
	if err != nil {
		return nil, err
	}
	out, err := bitbucketFetchPRDetail(auth, repo, number, key)
	err = gitx.RefreshBitbucketAndRetry(s, err, func(a string) error {
		var e error
		out, e = bitbucketFetchPRDetail(a, repo, number, key)
		return e
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func bitbucketFetchPRDetail(auth, repo string, number int, key string) (*workItemDetailOut, error) {
	ws, name, _ := strings.Cut(repo, "/")
	prBase := fmt.Sprintf("%s/2.0/repositories/%s/%s/pullrequests/%d",
		bitbucketAPIBase, url.PathEscape(ws), url.PathEscape(name), number)
	// fields= keeps `description` out of the response, the same promise the list adapter makes.
	params := url.Values{"fields": {"id,title,state,draft,updated_on,comment_count,task_count," +
		"links.html.href,author.display_name,author.nickname," +
		"source.branch.name,destination.branch.name,destination.repository.full_name," +
		"participants.role,participants.approved,participants.state," +
		"participants.user.display_name,participants.user.nickname"}}
	body, status, err := gitx.BitbucketGetStatus(workItemHTTPClient, auth, prBase+"?"+params.Encode())
	if err != nil {
		return nil, fmt.Errorf("could not reach %s", bitbucketAPIBase)
	}
	if status != http.StatusOK {
		return nil, bitbucketDetailError(status, body, key)
	}
	out, err := parseBitbucketPullRequest(body, key)
	if err != nil {
		return nil, err
	}
	// Best effort from here: a PR with no build status, or a connection that may not read them,
	// still leaves a usable panel. Only the first call decides whether this succeeded.
	if b, st, err := gitx.BitbucketGetStatus(workItemHTTPClient, auth,
		prBase+"/statuses?pagelen=50&fields=values.state"); err == nil && st == http.StatusOK {
		out.Checks = parseBitbucketStatuses(b)
	}
	if b, st, err := gitx.BitbucketGetStatus(workItemHTTPClient, auth,
		fmt.Sprintf("%s/diffstat?pagelen=%d&fields=size,values.lines_added,values.lines_removed", prBase, workItemDetailFiles)); err == nil && st == http.StatusOK {
		out.ChangedFiles, out.Additions, out.Deletions = parseBitbucketDiffstat(b)
	}
	return out, nil
}

// bitbucketDetailError mirrors the list path's wording. 403 names the missing permission
// rather than saying "re-connect", which would send the member round a loop that cannot fix it.
func bitbucketDetailError(status int, body []byte, key string) error {
	switch status {
	case http.StatusUnauthorized:
		return gitx.ErrBitbucketUnauthorized
	case http.StatusForbidden:
		return fmt.Errorf("bitbucket refused this read — the connection is missing the pull request permission " +
			"(`pullrequest` on the OAuth consumer, `read:pullrequest:bitbucket` on an API token)")
	case http.StatusNotFound:
		return fmt.Errorf("bitbucket has no %s visible to this connection", key)
	}
	return fmt.Errorf("bitbucket answered %d: %s", status, gitx.BitbucketErrText(body))
}

func parseBitbucketPullRequest(body []byte, key string) (*workItemDetailOut, error) {
	var pr struct {
		ID           int    `json:"id"`
		Title        string `json:"title"`
		State        string `json:"state"`
		Draft        bool   `json:"draft"`
		UpdatedOn    string `json:"updated_on"`
		CommentCount int    `json:"comment_count"`
		Author       struct {
			DisplayName string `json:"display_name"`
			Nickname    string `json:"nickname"`
		} `json:"author"`
		Source struct {
			Branch struct {
				Name string `json:"name"`
			} `json:"branch"`
		} `json:"source"`
		Destination struct {
			Branch struct {
				Name string `json:"name"`
			} `json:"branch"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		} `json:"destination"`
		Links struct {
			HTML struct {
				Href string `json:"href"`
			} `json:"html"`
		} `json:"links"`
		Participants []struct {
			Role     string `json:"role"`
			Approved bool   `json:"approved"`
			State    string `json:"state"`
			User     struct {
				DisplayName string `json:"display_name"`
				Nickname    string `json:"nickname"`
			} `json:"user"`
		} `json:"participants"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, err
	}
	out := &workItemDetailOut{
		Provider: "bitbucket", Key: key, Kind: "pr", Title: pr.Title,
		State: normalizeBitbucketPRState(pr.State, pr.Draft), URL: pr.Links.HTML.Href,
		Author:    firstNonEmpty(pr.Author.DisplayName, pr.Author.Nickname),
		Repo:      strings.TrimSpace(pr.Destination.Repository.FullName),
		UpdatedAt: bitbucketTimeToRFC3339(pr.UpdatedOn),
		Draft:     pr.Draft,
		Merged:    strings.EqualFold(pr.State, "MERGED"),
		// Bitbucket does not report whether a PR merges cleanly without starting a merge task.
		Mergeable:  "unknown",
		BaseBranch: pr.Destination.Branch.Name,
		HeadBranch: pr.Source.Branch.Name,
		Comments:   pr.CommentCount,
		Labels:     []string{},
		Reviews:    []workItemReviewOut{},
	}
	// Only reviewers. A Bitbucket participant list also holds everyone who commented, and
	// "6 reviewers" that is really 4 bystanders is worse than no line at all.
	for _, p := range pr.Participants {
		if !strings.EqualFold(p.Role, "REVIEWER") {
			continue
		}
		state := "pending"
		switch {
		case p.Approved || strings.EqualFold(p.State, "approved"):
			state = "approved"
		case strings.EqualFold(p.State, "changes_requested"):
			state = "changes_requested"
		}
		out.Reviews = append(out.Reviews, workItemReviewOut{
			Name:  firstNonEmpty(p.User.DisplayName, p.User.Nickname),
			State: state,
		})
	}
	return out, nil
}

// parseBitbucketStatuses folds the build statuses. Same precedence as the GitHub side, and
// the same rule that no status at all is not "green" (docs/log/80 §80.24).
func parseBitbucketStatuses(body []byte) workItemChecksOut {
	var st struct {
		Values []struct {
			State string `json:"state"`
		} `json:"values"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return workItemChecksOut{}
	}
	out := workItemChecksOut{}
	for _, v := range st.Values {
		out.Total++
		switch strings.ToUpper(strings.TrimSpace(v.State)) {
		case "FAILED", "ERROR":
			out.Failed++
		case "INPROGRESS":
			out.Pending++
		case "STOPPED":
			// A cancelled build is not a pass. It is counted as failing so that the panel never
			// claims green for a run nobody finished.
			out.Failed++
		}
	}
	switch {
	case out.Total == 0:
		out.State = ""
	case out.Failed > 0:
		out.State = "failure"
	case out.Pending > 0:
		out.State = "pending"
	default:
		out.State = "success"
	}
	return out
}

// parseBitbucketDiffstat sums the per-file line counts. `size` is the file count for the whole
// diff, so it is preferred over len(values) — which is only the first page.
func parseBitbucketDiffstat(body []byte) (files, added, removed int) {
	var ds struct {
		Size   int `json:"size"`
		Values []struct {
			LinesAdded   int `json:"lines_added"`
			LinesRemoved int `json:"lines_removed"`
		} `json:"values"`
	}
	if err := json.Unmarshal(body, &ds); err != nil {
		return 0, 0, 0
	}
	for _, v := range ds.Values {
		added += v.LinesAdded
		removed += v.LinesRemoved
	}
	files = ds.Size
	if files == 0 {
		files = len(ds.Values)
	}
	return files, added, removed
}
