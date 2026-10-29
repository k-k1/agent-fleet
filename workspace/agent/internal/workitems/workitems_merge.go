package workitems

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Merging a GitHub pull request from the detail modal (ADR 0061, 2026-10-25 note on #2632).
//
// The second write af performs against a tracker. It is reachable only from a human pressing a
// confirm button in the Console, through the Control Plane, and the Agent enforces that itself:
// every session holds AGENT_TOKEN, so the route takes only a member-origin mark the CP signed for
// this workspace's own member (ADR 0113). There is also no MCP tool and no automatic trigger.
// GitHub only: Bitbucket stays read-only (decision 19) and a Jira key has nothing to merge.

// workItemMergeMethods are the three values GitHub accepts for `merge_method`.
var workItemMergeMethods = map[string]bool{"merge": true, "squash": true, "rebase": true}

// workItemMergeSHARe is the shape of a full commit id. An abbreviated sha would be accepted by
// GitHub as a prefix match, which is weaker than "the commit the panel displayed".
var workItemMergeSHARe = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)

// githubMergeError is a refusal the Console tells apart by code: the pull request moved, or
// GitHub says it cannot be merged right now. Anything else is a plain provider error (502).
type githubMergeError struct {
	status int
	code   string
	msg    string
}

func (e *githubMergeError) Error() string { return e.msg }

// WorkItemMergeMaxRequest bounds the body memberorigin.Require buffers: four short strings.
const WorkItemMergeMaxRequest = 4 << 10

// workItemMergeOut is the success body. The Console counts only a literal merged:true as a merge.
type workItemMergeOut struct {
	Merged bool   `json:"merged"`
	SHA    string `json:"sha"`
}

// HandleWorkItemsMerge — POST /work-items/merge {provider, key, sha, method}.
func HandleWorkItemsMerge(w http.ResponseWriter, r *http.Request) {
	// Registered behind memberorigin.Require; refusing here as well keeps a route that is ever
	// registered without the wrapper closed rather than open.
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	var in struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
		// SHA is the head commit the panel displayed. Required, never defaulted from a fresh
		// read here: a fresh read would defeat the check that the member saw what is merged.
		SHA    string `json:"sha"`
		Method string `json:"method"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	switch strings.TrimSpace(in.Provider) {
	case "", "github":
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_provider", "merging is available for GitHub pull requests only")
		return
	}
	key := strings.TrimSpace(in.Key)
	repo, number, ok := parseGitHubIssueKey(key)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "key must be owner/name#number")
		return
	}
	sha := strings.TrimSpace(in.SHA)
	if !workItemMergeSHARe.MatchString(sha) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "sha must be the full head commit id shown in the panel")
		return
	}
	method := strings.TrimSpace(in.Method)
	if !workItemMergeMethods[method] {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "method must be merge, squash or rebase")
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	errNotConnected := errors.New("GitHub is not connected")
	var merged string
	err = gitx.WithGitHubToken(s, errNotConnected, func(tok string) (e error) {
		merged, e = githubMergePullRequest(tok, repo, number, sha, method)
		return e
	})
	if errors.Is(err, errNotConnected) {
		httpx.WriteErr(w, http.StatusBadRequest, "not_connected", "GitHub is not connected")
		return
	}
	var me *githubMergeError
	if errors.As(err, &me) {
		httpx.WriteErr(w, me.status, me.code, me.msg)
		return
	}
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "provider_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, workItemMergeOut{Merged: true, SHA: merged})
}

// githubMergePullRequest sends the merge. `sha` makes GitHub answer 409 when the head moved, so
// a stale panel can never merge commits its member did not see.
//
// The request is never retried here: a timeout after GitHub accepted it would turn a second
// attempt into "already merged" noise, and the Console re-reads the pull request after any
// outcome instead.
func githubMergePullRequest(token, repo string, number int, sha, method string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"sha": sha, "merge_method": method})
	u := fmt.Sprintf("%s/repos/%s/pulls/%d/merge", githubAPIBase, gitx.EscapeRepoPath(repo), number)
	req, err := http.NewRequest(http.MethodPut, u, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	gitx.GithubHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := workItemHTTPClient.Do(req)
	if err != nil {
		// The transport error names the URL, not the token. It cannot be told whether GitHub saw
		// the request, which is why the panel re-reads on every outcome.
		return "", fmt.Errorf("could not reach GitHub to merge: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var body struct {
		SHA     string `json:"sha"`
		Merged  bool   `json:"merged"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &body)
	switch resp.StatusCode {
	case http.StatusOK:
		if !body.Merged {
			return "", &githubMergeError{http.StatusConflict, "not_mergeable", mergeRefusal(body.Message)}
		}
		return body.SHA, nil
	case http.StatusConflict:
		return "", &githubMergeError{http.StatusConflict, "head_changed",
			"the pull request changed after this panel was drawn, so nothing was merged"}
	case http.StatusMethodNotAllowed:
		// 405 is GitHub's "cannot be merged now": branch protection, a conflict, a method the
		// repository does not allow. Its message says which, and is not secret.
		return "", &githubMergeError{http.StatusConflict, "not_mergeable", mergeRefusal(body.Message)}
	case http.StatusUnauthorized:
		return "", gitx.NewGitHubUnauthorized("github refused the merge (401)")
	case http.StatusForbidden:
		if strings.Contains(strings.ToLower(string(raw)), "rate limit") {
			return "", fmt.Errorf("github rate limit reached")
		}
		return "", fmt.Errorf("github refused the merge (403): the connection's token is not allowed to merge here")
	case http.StatusNotFound:
		return "", fmt.Errorf("github has no %s#%d visible to this connection", repo, number)
	}
	return "", fmt.Errorf("github answered %d for the merge: %s", resp.StatusCode, mergeRefusal(body.Message))
}

// mergeRefusal words GitHub's own message for the panel, bounded: it is shown to a member and
// must not be able to carry a page of text.
func mergeRefusal(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "GitHub did not accept the merge"
	}
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	return "GitHub did not accept the merge: " + msg
}
