package gitx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// The online half of finding a merged branch's head (#1043). A squash or rebase merge leaves
// no merge commit naming the branch, so only the forge still knows which commit the branch
// was at: the head of the pull request it was merged through. GitHub only — the token is the
// tenant's github.com connection, and the other forges keep the offline answer.
//
// Best effort throughout: no connection, no network, a refusal or a slow answer all mean
// "not found", and the plan falls through to a new branch exactly as it did before.

// githubAPIBase and githubToken are variables so tests can point them at an httptest server
// and a fake connection.
var (
	githubAPIBase = "https://api.github.com"
	githubToken   = func() string {
		s, err := secrets.Load()
		if err != nil {
			return ""
		}
		return s.Git["github.com"].Token
	}
)

// forgeLookupTimeout bounds the forge part of one plan — the API calls and any fetch of a
// pull request head together — so a slow forge costs the plan seconds, never the recreate.
const forgeLookupTimeout = 15 * time.Second

// githubRepoOf answers "owner/name" when dir's origin is on github.com. The configured URL is
// read rather than `remote get-url`, which expands url.<base>.insteadOf and would hide the
// forge behind a mirror.
func githubRepoOf(dir string) string {
	raw, err := Run(dir, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	if _, host := gitProviderHost(raw); host != "github.com" {
		return ""
	}
	repo := gitRemotePath(raw)
	if !ValidRemoteRepo(repo) {
		return ""
	}
	return repo
}

// mergedPRFinder asks the forge once per plan: the repository and the token are resolved on
// the first branch that needs them, and every lookup shares one deadline.
type mergedPRFinder struct {
	dir      string
	resolved bool
	repo     string
	token    string
	ctx      context.Context
	cancel   context.CancelFunc
}

func newMergedPRFinder(dir string) *mergedPRFinder { return &mergedPRFinder{dir: dir} }

func (f *mergedPRFinder) close() {
	if f.cancel != nil {
		f.cancel()
	}
}

// head returns the head commit of the newest merged pull request from branch, fetching it
// from origin when the object is not in the repository, plus the pull request number; "" when
// there is none or the forge cannot be asked.
func (f *mergedPRFinder) head(branch string) (string, int) {
	if !f.resolved {
		f.resolved = true
		if f.repo = githubRepoOf(f.dir); f.repo != "" {
			f.token = githubToken()
		}
		if f.repo != "" && f.token != "" {
			f.ctx, f.cancel = context.WithTimeout(context.Background(), forgeLookupTimeout)
		}
	}
	if f.ctx == nil || f.ctx.Err() != nil {
		return "", 0
	}
	sha, pr := githubMergedPR(f.ctx, f.token, f.repo, branch)
	if sha == "" {
		return "", 0
	}
	if !commitExists(f.dir, sha) {
		// The branch was never fetched here, or its objects went with it. GitHub keeps every
		// pull request's head under refs/pull/N/head; nothing is written to a ref, the commit
		// only has to exist for `worktree add -b` to start at it.
		_ = CmdContext(f.ctx, f.dir, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head",
			"origin", "refs/pull/"+strconv.Itoa(pr)+"/head").Run()
		if !commitExists(f.dir, sha) {
			return "", 0
		}
	}
	return sha, pr
}

// githubMergedPR finds the newest merged pull request whose head is branch in repo itself (a
// fork's branch is not the one this working copy pushed). GitHub lists newest first.
func githubMergedPR(ctx context.Context, token, repo, branch string) (string, int) {
	owner, _, ok := splitRepo(repo)
	if !ok {
		return "", 0
	}
	q := url.Values{"state": {"closed"}, "head": {owner + ":" + branch}, "per_page": {"30"}}
	req, err := http.NewRequestWithContext(ctx, "GET", githubAPIBase+"/repos/"+EscapeRepoPath(repo)+"/pulls?"+q.Encode(), nil)
	if err != nil {
		return "", 0
	}
	GithubHeaders(req, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0
	}
	var prs []struct {
		Number   int     `json:"number"`
		MergedAt *string `json:"merged_at"`
		Head     struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo *struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&prs); err != nil {
		return "", 0
	}
	for _, p := range prs {
		if p.MergedAt == nil || *p.MergedAt == "" || p.Number <= 0 || p.Head.Ref != branch || !isHexSHA(p.Head.SHA) {
			continue
		}
		if p.Head.Repo != nil && !strings.EqualFold(p.Head.Repo.FullName, repo) {
			continue
		}
		return p.Head.SHA, p.Number
	}
	return "", 0
}

func isHexSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
