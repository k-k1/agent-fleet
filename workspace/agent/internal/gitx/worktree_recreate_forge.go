package gitx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// githubToken is read under the plan's own deadline (ctx), so a renewal that has to
	// wait cannot make the plan wait longer than forgeLookupTimeout.
	githubToken = func(ctx context.Context) string {
		s, err := secrets.Load()
		if err != nil {
			return ""
		}
		// A renewal failure leaves the token empty: the forge lookup then runs
		// anonymously, which is how it already degrades.
		tok, _ := GitHubTokenContext(ctx, s)
		return tok
	}
	// githubRenew replaces a token the API rejected with a 401; replaced in tests.
	githubRenew = RenewRejectedGitHubToken
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
// there is none or the forge cannot be asked. With after set (the offline merge commit's
// date), the latest merged pull request counts only when it was merged strictly later: the
// merge commit's own pull request carries its date, and an unreadable merged_at is no reason
// to override a good offline answer.
func (f *mergedPRFinder) head(branch string, after time.Time) (string, int) {
	if !f.resolved {
		f.resolved = true
		if f.repo = githubRepoOf(f.dir); f.repo != "" {
			f.ctx, f.cancel = context.WithTimeout(context.Background(), forgeLookupTimeout)
			if f.token = githubToken(f.ctx); f.token == "" {
				f.cancel()
				f.ctx = nil
			}
		}
	}
	if f.ctx == nil || f.ctx.Err() != nil {
		return "", 0
	}
	sha, pr, mergedAt := githubMergedPR(f.ctx, f.token, f.repo, branch)
	if sha == "" || (!after.IsZero() && !mergedAt.After(after)) {
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

// githubPullsPerPage and githubPullsPages bound the listing: a branch with more closed pull
// requests than this is not worth the plan's time.
const (
	githubPullsPerPage = 100
	githubPullsPages   = 5
)

// githubHTTPClient refuses any redirect that leaves the first request's scheme and host. Go's
// default forwards Authorization to the same hostname whatever the scheme or port, so a
// redirect to http:// would carry the token in the clear.
var githubHTTPClient = &http.Client{CheckRedirect: sameOriginRedirect}

func sameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	if o := via[0].URL; req.URL.Scheme != o.Scheme || !strings.EqualFold(req.URL.Host, o.Host) {
		return fmt.Errorf("redirect off %s://%s refused", o.Scheme, o.Host)
	}
	return nil
}

// githubMergedPR finds the most recently merged pull request whose head is branch in repo
// itself (a fork's branch, or one whose repository is gone and so cannot be shown to be
// repo's, is not the one this working copy pushed), and when it was merged. GitHub lists by
// creation, and a pull request opened earlier can be merged later, so every page is read
// rather than stopping at the first merged one. An unreadable merged_at ranks below every
// readable one and comes back as the zero time. A page that fails ends the scan with what the
// earlier pages found.
func githubMergedPR(ctx context.Context, token, repo, branch string) (string, int, time.Time) {
	owner, _, ok := splitRepo(repo)
	if !ok {
		return "", 0, time.Time{}
	}
	var sha string
	var num int
	var at time.Time
	renewed := false
	for page := 1; page <= githubPullsPages; page++ {
		prs, ok, unauthorized := githubClosedPullsPage(ctx, token, repo, owner+":"+branch, page)
		if unauthorized && !renewed {
			// The recorded expiry can be wrong (clock skew, a renewal by another process):
			// renew once and ask this page again.
			renewed = true
			if nt, err := githubRenew(token); err == nil && nt != "" && nt != token {
				token = nt
				prs, ok, _ = githubClosedPullsPage(ctx, token, repo, owner+":"+branch, page)
			}
		}
		if !ok {
			break
		}
		for _, p := range prs {
			if p.MergedAt == nil || *p.MergedAt == "" || p.Number <= 0 || p.Head.Ref != branch || !isHexSHA(p.Head.SHA) {
				continue
			}
			if p.Head.Repo == nil || !strings.EqualFold(p.Head.Repo.FullName, repo) {
				continue
			}
			t, _ := time.Parse(time.RFC3339, *p.MergedAt)
			if sha == "" || t.After(at) {
				sha, num, at = p.Head.SHA, p.Number, t
			}
		}
		if len(prs) < githubPullsPerPage {
			break
		}
	}
	return sha, num, at
}

type githubPull struct {
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

func githubClosedPullsPage(ctx context.Context, token, repo, head string, page int) (prs []githubPull, ok, unauthorized bool) {
	q := url.Values{"state": {"closed"}, "head": {head},
		"per_page": {strconv.Itoa(githubPullsPerPage)}, "page": {strconv.Itoa(page)}}
	req, err := http.NewRequestWithContext(ctx, "GET", githubAPIBase+"/repos/"+EscapeRepoPath(repo)+"/pulls?"+q.Encode(), nil)
	if err != nil {
		return nil, false, false
	}
	GithubHeaders(req, token)
	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		return nil, false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, resp.StatusCode == http.StatusUnauthorized
	}
	var out []githubPull
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return nil, false, false
	}
	return out, true, false
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
