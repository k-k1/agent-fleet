package memconsol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/redirectpolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// githubAPI is the REST base.
const githubAPI = "https://api.github.com"

var errRefNotConnected = errors.New("github is not connected")

// githubRefState reads one issue or pull request through the member's own github.com connection
// (the one the work-item and watch reads use; gitx renews a rejected token once). It asks the
// issues endpoint, which answers for both kinds: a pull request carries pull_request.merged_at.
// Any failure (not connected, a 404 for a private repo the token cannot see, a rate limit, a
// network error) is an error, which the caller treats as "unknown", never as "closed".
func githubRefState(ctx context.Context, repo string, number int) (RefState, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") || number < 1 {
		return "", fmt.Errorf("bad reference")
	}
	s, err := secrets.Load()
	if err != nil {
		return "", err
	}
	var state RefState
	err = gitx.WithGitHubToken(s, errRefNotConnected, func(tok string) error {
		u := fmt.Sprintf("%s/repos/%s/%s/issues/%d", githubAPI, url.PathEscape(owner), url.PathEscape(name), number)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		resp, err := redirectpolicy.Client(0).Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		st, err := refStateOf(resp.StatusCode, resp.Body)
		state = st
		return err
	})
	return state, err
}

// refStateOf maps an issues-endpoint answer to a state. Only a 200 with a recognised state is an
// answer; a 401 is the token's rejection (gitx renews it), everything else is an error.
func refStateOf(status int, body io.Reader) (RefState, error) {
	switch {
	case status == http.StatusUnauthorized:
		return "", gitx.NewGitHubUnauthorized("github rejected the token")
	case status == http.StatusNotFound || status == http.StatusGone:
		return "", ErrRefGone
	case status != http.StatusOK:
		return "", fmt.Errorf("github answered %d", status)
	}
	var is struct {
		State       string `json:"state"`
		PullRequest *struct {
			MergedAt *string `json:"merged_at"`
		} `json:"pull_request"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&is); err != nil {
		return "", err
	}
	switch is.State {
	case "open":
		return RefOpen, nil
	case "closed":
		if is.PullRequest != nil && is.PullRequest.MergedAt != nil {
			return RefMerged, nil
		}
		return RefClosed, nil
	}
	return "", fmt.Errorf("unknown state")
}
