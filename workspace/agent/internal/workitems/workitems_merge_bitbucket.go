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
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Merging a Bitbucket Cloud pull request from the detail modal. Same stance as the GitHub merge in
// workitems_merge.go: reachable only through the member-origin route, no MCP tool, no trigger.
//
// Bitbucket's merge endpoint takes no "expected head" the way GitHub's `sha` does, so the guard
// is made here: the PR is read again immediately before the merge and the merge is refused when
// its head no longer starts with the commit the panel displayed. The window between that read and
// the POST is small but not zero; the Console re-reads the PR after any outcome.

// workItemBitbucketStrategies are the values the merge endpoint documents for `merge_strategy`.
var workItemBitbucketStrategies = map[string]bool{
	"merge_commit": true, "squash": true, "fast_forward": true,
	"squash_fast_forward": true, "rebase_fast_forward": true, "rebase_merge": true,
}

// workItemBitbucketSHARe: the PR payload carries a 12-character abbreviated head, which is what
// the panel displays and sends back; a full id is accepted too. Compared as a prefix.
var workItemBitbucketSHARe = regexp.MustCompile(`^[0-9a-fA-F]{12,40}$`)

func handleBitbucketMerge(w http.ResponseWriter, key, sha, method string, closeSource *bool) {
	repo, number, ok := parseGitHubIssueKey(key)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "key must be workspace/repo#number")
		return
	}
	if !workItemBitbucketSHARe.MatchString(sha) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "sha must be the head commit shown in the panel")
		return
	}
	if !workItemBitbucketStrategies[method] {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "method is not a Bitbucket merge strategy")
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	auth, err := gitx.BitbucketAuthHeader(s)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "not_connected", "Bitbucket is not connected")
		return
	}
	var merged string
	do := func(a string) error {
		var e error
		merged, e = bitbucketMergePullRequest(a, repo, number, key, sha, method, closeSource)
		return e
	}
	err = do(auth)
	err = gitx.RefreshBitbucketAndRetry(s, err, do)
	var me *githubMergeError
	if errors.As(err, &me) {
		httpx.WriteErr(w, me.status, me.code, me.msg)
		return
	}
	if errors.Is(err, gitx.ErrBitbucketUnauthorized) {
		httpx.WriteErr(w, http.StatusBadGateway, "provider_error", "bitbucket rejected the connection (401) — re-connect Bitbucket")
		return
	}
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "provider_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, workItemMergeOut{Merged: true, SHA: merged})
}

// bitbucketMergePullRequest checks the head, then sends the merge once. A 401 is the only
// outcome retried by the caller (the request was not processed); everything else is final,
// because a timeout after Bitbucket accepted the merge would otherwise read as a second attempt.
func bitbucketMergePullRequest(auth, repo string, number int, key, sha, method string, closeSource *bool) (string, error) {
	base := fmt.Sprintf("%s/2.0/repositories/%s/pullrequests/%d", BitbucketAPIBase, gitx.EscapeRepoPath(repo), number)
	body, status, err := gitx.BitbucketGetStatus(workItemHTTPClient, auth, base+"?fields=state,source.commit.hash")
	if err != nil {
		return "", fmt.Errorf("could not reach %s", BitbucketAPIBase)
	}
	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return "", gitx.ErrBitbucketUnauthorized
	case http.StatusNotFound:
		return "", fmt.Errorf("bitbucket has no %s visible to this connection", key)
	default:
		return "", bitbucketDetailError(status, body, key)
	}
	var cur struct {
		State  string `json:"state"`
		Source struct {
			Commit struct {
				Hash string `json:"hash"`
			} `json:"commit"`
		} `json:"source"`
	}
	if err := json.Unmarshal(body, &cur); err != nil {
		return "", fmt.Errorf("bitbucket answered with a body that is not JSON")
	}
	if !strings.EqualFold(cur.State, "OPEN") {
		return "", &githubMergeError{http.StatusConflict, "not_mergeable", "the pull request is no longer open, so nothing was merged"}
	}
	head := strings.ToLower(cur.Source.Commit.Hash)
	want := strings.ToLower(sha)
	if head == "" || !(strings.HasPrefix(head, want) || strings.HasPrefix(want, head)) {
		return "", &githubMergeError{http.StatusConflict, "head_changed",
			"the pull request changed after this panel was drawn, so nothing was merged"}
	}

	payload := map[string]any{"type": "pullrequest", "merge_strategy": method}
	if closeSource != nil {
		payload["close_source_branch"] = *closeSource
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, base+"/merge", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := workItemHTTPClient.Do(req)
	if err != nil {
		// Cannot be told whether Bitbucket saw it: the panel re-reads on every outcome.
		return "", fmt.Errorf("could not reach Bitbucket to merge: %w", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		var pr struct {
			MergeCommit struct {
				Hash string `json:"hash"`
			} `json:"merge_commit"`
		}
		_ = json.Unmarshal(out, &pr)
		return pr.MergeCommit.Hash, nil
	case http.StatusAccepted:
		// Bitbucket merges large pull requests as a background task.
		return "", &githubMergeError{http.StatusAccepted, "merge_pending",
			"Bitbucket accepted the merge and is still processing it; the panel shows the result when it reads again"}
	case http.StatusBadRequest, http.StatusConflict:
		// A conflict, a failed merge check or a strategy the branch does not allow. The message
		// says which, and is not secret.
		return "", &githubMergeError{http.StatusConflict, "not_mergeable", bitbucketMergeRefusal(out)}
	case http.StatusUnauthorized:
		return "", gitx.ErrBitbucketUnauthorized
	case http.StatusForbidden:
		return "", fmt.Errorf("bitbucket refused the merge (403) — the connection lacks the pull request " +
			"write permission (`pullrequest:write` on the OAuth consumer, `write:pullrequest:bitbucket` on an API token), " +
			"or the account may not merge into this branch")
	case http.StatusNotFound:
		return "", fmt.Errorf("bitbucket has no %s visible to this connection", key)
	}
	return "", fmt.Errorf("bitbucket answered %d for the merge: %s", resp.StatusCode, bitbucketMergeRefusal(out))
}

// bitbucketMergeRefusal words Bitbucket's own error text for the panel, bounded like
// mergeRefusal: a member sees it and it must not be able to carry a page.
func bitbucketMergeRefusal(body []byte) string {
	msg := strings.TrimSpace(gitx.BitbucketErrText(body))
	if msg == "" {
		return "Bitbucket did not accept the merge"
	}
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	return "Bitbucket did not accept the merge: " + msg
}
