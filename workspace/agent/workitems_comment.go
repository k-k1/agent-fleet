package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Posting a comment back to the ticket (docs/log/80 §80.10 / ADR 0061 decision 6).
//
// The first write af performs against a tracker, and it is reachable only from a human
// clicking Post on a draft they have just read. Every session holds AGENT_TOKEN, so the Agent
// enforces that itself: the route takes only a member-origin mark the CP signed for this
// workspace's own member (ADR 0113). There is deliberately:
//   - no MCP tool for it (an agent cannot reach this path),
//   - no automatic trigger (no "session finished → comment"),
//   - no state transition, no close, no assignee change.
//
// The reason is the same one that keeps the ticket body out of the first prompt: a
// ticket's text is written by third parties, so anything that lets that text steer a
// write back into the tracker closes a loop we do not want closed. A human reading the
// draft is the gate.

// workItemCommentMaxRequest bounds the body memberorigin.Require buffers: a drafted comment is
// long-form text, and the provider limits (GitHub 65536 characters) sit below this.
const workItemCommentMaxRequest = 256 << 10

// handleWorkItemsComment — POST /work-items/comment {provider, key, body}.
func handleWorkItemsComment(w http.ResponseWriter, r *http.Request) {
	// Registered behind memberorigin.Require; refusing here as well keeps a route that is ever
	// registered without the wrapper closed rather than open.
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	var in struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
		Body     string `json:"body"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	body := strings.TrimSpace(in.Body)
	key := strings.TrimSpace(in.Key)
	if key == "" || body == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "key and body are required")
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	var url string
	switch strings.TrimSpace(in.Provider) {
	case "", "github":
		errNotConnected := errors.New("GitHub is not connected")
		err = gitx.WithGitHubToken(s, errNotConnected, func(tok string) (e error) {
			url, e = githubPostIssueComment(tok, key, body)
			return e
		})
		if errors.Is(err, errNotConnected) {
			httpx.WriteErr(w, http.StatusBadRequest, "not_connected", "GitHub is not connected")
			return
		}
	case "jira":
		if !jiraConnected(s.Jira) {
			httpx.WriteErr(w, http.StatusBadRequest, "not_connected", "Jira is not connected")
			return
		}
		url, err = jiraPostIssueComment(s.Jira, key, body)
	case "bitbucket":
		// Pull requests only: Bitbucket work items are PRs, and issue comments are out of scope.
		url, err = bitbucketPostPullRequestComment(s, key, body)
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_provider", "unsupported provider: "+in.Provider)
		return
	}
	if err != nil {
		// 502: the other side refused (missing permission, no such issue). Keep it distinguishable
		// from a fault on af's side.
		httpx.WriteErr(w, http.StatusBadGateway, "provider_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"url": url})
}

// parseGitHubIssueKey splits "owner/name#45" into its parts. The rail's key is the only
// coordinate we keep for an item, so this is what a write has to work from.
func parseGitHubIssueKey(key string) (repo string, number int, ok bool) {
	i := strings.LastIndex(key, "#")
	if i <= 0 {
		return "", 0, false
	}
	repo = key[:i]
	n, err := strconv.Atoi(key[i+1:])
	if err != nil || n <= 0 || !gitx.ValidRemoteRepo(repo) {
		return "", 0, false
	}
	return repo, n, true
}

func githubPostIssueComment(token, key, body string) (string, error) {
	repo, number, ok := parseGitHubIssueKey(key)
	if !ok {
		return "", fmt.Errorf("cannot post to %q (expected owner/name#number)", key)
	}
	payload, _ := json.Marshal(map[string]string{"body": body})
	u := fmt.Sprintf("%s/repos/%s/issues/%d/comments", githubAPIBase, gitx.EscapeRepoPath(repo), number)
	req, err := http.NewRequest("POST", u, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	gitx.GithubHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := workItemHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			// Reads can succeed while only the post gets a 403 (token scope, or a closed/locked
			// issue), so do not flatly tell the user to reconnect.
			msg := fmt.Sprintf("github refused the comment (%d) — the token may lack write access, or the issue is locked", resp.StatusCode)
			if resp.StatusCode == http.StatusUnauthorized {
				return "", gitx.NewGitHubUnauthorized(msg)
			}
			return "", fmt.Errorf("%s", msg)
		case http.StatusNotFound:
			return "", fmt.Errorf("github has no %s", key)
		}
		return "", fmt.Errorf("github comment %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		HTMLURL string `json:"html_url"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.HTMLURL, nil
}

// bitbucketPostPullRequestComment posts a comment on "<workspace>/<repo>#<id>". Needs
// `pullrequest:write` (OAuth) or `write:pullrequest:bitbucket` (API token); a connection
// without it gets a 403 here, which is reported as such rather than as a success.
func bitbucketPostPullRequestComment(s *secrets.Data, key, body string) (string, error) {
	repo, number, ok := parseGitHubIssueKey(key)
	if !ok {
		return "", fmt.Errorf("cannot post to %q (expected workspace/repo#number)", key)
	}
	auth, err := gitx.BitbucketAuthHeader(s)
	if err != nil {
		return "", fmt.Errorf("Bitbucket is not connected")
	}
	var link string
	post := func(a string) error {
		var e error
		link, e = bitbucketPostComment(a, repo, number, key, body)
		return e
	}
	err = post(auth)
	err = gitx.RefreshBitbucketAndRetry(s, err, post)
	if err != nil {
		if errors.Is(err, gitx.ErrBitbucketUnauthorized) {
			return "", fmt.Errorf("bitbucket rejected the connection (401) — re-connect Bitbucket")
		}
		return "", err
	}
	return link, nil
}

func bitbucketPostComment(auth, repo string, number int, key, body string) (string, error) {
	payload, _ := json.Marshal(map[string]any{"content": map[string]string{"raw": body}})
	u := fmt.Sprintf("%s/2.0/repositories/%s/pullrequests/%d/comments", bitbucketAPIBase, gitx.EscapeRepoPath(repo), number)
	req, err := http.NewRequest("POST", u, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := workItemHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach %s", bitbucketAPIBase)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		var out struct {
			Links struct {
				HTML struct {
					Href string `json:"href"`
				} `json:"html"`
			} `json:"links"`
		}
		_ = json.Unmarshal(raw, &out)
		return out.Links.HTML.Href, nil
	case http.StatusUnauthorized:
		return "", gitx.ErrBitbucketUnauthorized // refreshed and retried by the caller
	case http.StatusForbidden:
		// Reads can succeed while only the post gets a 403: the connection lacks the write scope.
		return "", fmt.Errorf("bitbucket refused the comment (403) — the connection lacks the pull request " +
			"write permission (`pullrequest:write` on the OAuth consumer, `write:pullrequest:bitbucket` on an API token). " +
			"A tenant administrator adds it to the app; then re-connect Bitbucket")
	case http.StatusNotFound:
		return "", fmt.Errorf("bitbucket has no %s visible to this connection", key)
	}
	return "", fmt.Errorf("bitbucket comment %d: %s", resp.StatusCode, gitx.BitbucketErrText(raw))
}

func jiraPostIssueComment(c *secrets.JiraCreds, key, body string) (string, error) {
	payload, _ := json.Marshal(map[string]any{"body": jiraADF(body)})
	u := jiraAPIBase(c) + "/rest/api/3/issue/" + key + "/comment"
	if err := jiraEnsureFresh(c); err != nil {
		return "", err
	}
	raw, status, err := jiraRequest(c, "POST", u, payload)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized && c.AuthKind == "oauth" {
		if rerr := jiraRefreshNow(c); rerr == nil {
			raw, status, err = jiraRequest(c, "POST", u, payload)
			if err != nil {
				return "", err
			}
		}
	}
	if status != http.StatusCreated && status != http.StatusOK {
		switch status {
		case http.StatusUnauthorized, http.StatusForbidden:
			// With OAuth, a missing write:jira-work scope also lands here. It shows up as reads
			// working while only the post gets a 403, so word it as a permission problem.
			return "", fmt.Errorf("jira refused the comment (%d) — the account or the OAuth app may lack write access", status)
		case http.StatusNotFound:
			return "", fmt.Errorf("jira has no %s", key)
		}
		return "", fmt.Errorf("jira comment %d: %s", status, jiraErrText(raw))
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &out)
	url := c.Site + "/browse/" + key
	if out.ID != "" {
		url += "?focusedCommentId=" + out.ID
	}
	return url, nil
}

// jiraADF wraps plain text in the Atlassian Document Format Jira's REST v3 requires.
//
// v3 does NOT take a plain string for a comment body — posting one is a 400, which is
// the single most likely way this call breaks. Blank lines separate paragraphs; the line
// breaks inside a paragraph become hardBreak nodes, so a pasted list stays a list
// instead of collapsing into one run-on line.
func jiraADF(text string) map[string]any {
	content := []any{}
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		lines := strings.Split(strings.Trim(para, "\n"), "\n")
		nodes := []any{}
		for i, ln := range lines {
			if i > 0 {
				nodes = append(nodes, map[string]any{"type": "hardBreak"})
			}
			if ln == "" {
				continue
			}
			nodes = append(nodes, map[string]any{"type": "text", "text": ln})
		}
		if len(nodes) == 0 {
			continue
		}
		content = append(content, map[string]any{"type": "paragraph", "content": nodes})
	}
	if len(content) == 0 {
		// An empty doc is a 400. The caller already rejects an empty body; this covers a body that
		// is nothing but blank lines (a paragraph cannot hold empty text, so keep one character).
		content = append(content, map[string]any{"type": "paragraph",
			"content": []any{map[string]any{"type": "text", "text": "-"}}})
	}
	return map[string]any{"type": "doc", "version": 1, "content": content}
}
