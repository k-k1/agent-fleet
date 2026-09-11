// cli_release_watch.go — is the upstream CLI release watcher still running?
//
// `.github/workflows/cli-release-watch.yml` asks once a day whether a public agent-CLI
// version moved, and records what it saw in one tracking issue of the public repository
// (deploy/local/cli-release-state.sh is the authority on that format). Two namespaces
// matter here:
//
//	tested   one comment per CLI per version, the version its contract last passed
//	watcher  the run's own liveness, rewritten in the issue BODY: `ok` (the last run
//	         where every release source was readable), `failed` (the rows the last run
//	         could not read, or `none`) and `at` (when that run was)
//
// Without the watcher half, a `tested` version that stops moving reads exactly the same
// whether upstream went quiet or the job fell over. On 2026-09-09 it was the latter —
// rtk's release call failed, the job aborted, claude 2.1.267 was dropped on the floor —
// and nobody noticed until the contract was run by hand the next morning. This is the
// half that lets the Console tell those two apart.
//
// Three rules this obeys, and what breaks without each.
//
//   - **Read anonymously.** The data is public, and a token here would be one more
//     secret in the CP for something that needs none. The cost is the anonymous budget:
//     60 requests per hour per IP. One refresh spends 1 request for the issue plus at
//     most cliReleaseCommentPages for its comments — 6 — and the TTL is an hour, so a CP
//     process uses at most 10% of that budget. Never make this fetch per-workspace: the
//     watcher is one job for the whole deployment, and so is this cache.
//   - **Never block a request on GitHub.** The block rides on GET /api/env/ws-settings,
//     which the Console loads whenever a workspace is opened. A read returns whatever was
//     last fetched and starts the refresh on its own goroutine; it never waits.
//   - **When in doubt, say nothing** (the workspace_stale.go stance). A deployment with
//     no outbound network never gets an answer, so the block is simply absent and the
//     Console draws no row — neither "fine" nor a warning. A failed refresh keeps the
//     previous answer rather than downgrading a live watcher to "unknown".
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// cliReleaseGitHubAPI is the real API root. The cache carries it as a field so tests
	// can point an instance at an httptest server instead (see TestMain: the package's
	// own instance is redirected for the whole suite, because a handler test must never
	// dial the public internet).
	cliReleaseGitHubAPI = "https://api.github.com"
	// The repository the watcher workflow runs in, and the title cli-release-state.sh
	// gives its one tracking issue. The script finds the issue by exact title — it
	// carries no label — so this must stay in step with it.
	cliReleaseRepo       = "k-k1/agent-fleet"
	cliReleaseIssueTitle = "CLI release watcher state"

	cliReleaseTTL     = time.Hour
	cliReleaseTimeout = 15 * time.Second
	// GitHub's maximum page size, and how many of the LAST comment pages are read. The
	// tracking issue grows by a comment per release forever (128 by 2026-09), so reading
	// all of it would cost more of the anonymous budget every month. Five pages is 500
	// comments — roughly a year of releases at the current rate — and the markers are
	// read last-wins, so an older page can only ever hold an older version.
	cliReleasePerPage      = 100
	cliReleaseCommentPages = 5
	// Response cap. The comments are one HTML comment per line; 4 MB is far past any
	// real page and stops a mirror that answers with something enormous.
	cliReleaseMaxBody = 4 << 20
)

// cliReleaseWire is the `cliRelease` block of GET /api/env/ws-settings, mirrored by the
// Console's `CLIRelease` (console/src/features/settings/cliRelease.ts).
//
// Nothing here is omitempty on purpose: "the watcher never wrote this" and "the field is
// missing from the response" must not look the same to the Console, which decides what to
// draw from WatcherOkAt being a readable timestamp. The whole block is what goes absent
// when the answer is unknown.
type cliReleaseWire struct {
	WatcherOkAt   string            `json:"watcherOkAt"`
	WatcherFailed []string          `json:"watcherFailed"`
	WatcherAt     string            `json:"watcherAt"`
	Tested        map[string]string `json:"tested"`
	FetchedAt     string            `json:"fetchedAt"`
}

// cliReleaseCache holds the last answer read from GitHub for the whole deployment.
type cliReleaseCache struct {
	base string // GitHub API root
	// spawn runs a refresh. Production hands it to a goroutine, so no request waits on
	// GitHub; tests substitute a synchronous call so a refresh has finished by the time
	// snapshot returns.
	spawn func(func())
	http  *http.Client

	mu   sync.Mutex
	out  *cliReleaseWire
	at   time.Time // when the last refresh ATTEMPT finished, successful or not
	busy bool
}

func newCLIReleaseCache(base string) *cliReleaseCache {
	return &cliReleaseCache{
		base:  base,
		spawn: func(f func()) { go f() },
		http:  &http.Client{Timeout: cliReleaseTimeout},
	}
}

// cliRelease is the deployment-wide instance. A var so TestMain can replace it with one
// that cannot reach the public internet.
var cliRelease = newCLIReleaseCache(cliReleaseGitHubAPI)

// snapshot returns the last state read from GitHub — nil until one has been read at all —
// and starts a refresh when the cache is stale. It never touches the network itself.
func (c *cliReleaseCache) snapshot() *cliReleaseWire {
	c.mu.Lock()
	start := !c.busy && time.Since(c.at) >= cliReleaseTTL
	if start {
		c.busy = true
	}
	c.mu.Unlock()
	if start {
		c.spawn(c.refresh)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out
}

// refresh reads GitHub once. The timestamp moves whether or not the read worked — a
// deployment with no route out must not re-dial on every settings read — but the answer
// is replaced only on success, so a rate-limit 403 or a network blip keeps showing the
// watcher state it last managed to read.
func (c *cliReleaseCache) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), cliReleaseTimeout)
	defer cancel()
	out, err := c.fetch(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.busy = false
	c.at = time.Now()
	if err == nil {
		c.out = out
	}
}

// fetch reads the tracking issue and its recent comments and parses the state out of them.
func (c *cliReleaseCache) fetch(ctx context.Context) (*cliReleaseWire, error) {
	iss, err := c.findIssue(ctx)
	if err != nil {
		return nil, err
	}
	out := &cliReleaseWire{WatcherFailed: []string{}, Tested: map[string]string{}}
	// The issue body carries the watcher block, and cli-release-state.sh reads `tested`
	// markers out of the body too — so parse both halves of it.
	parseCLIReleaseWatcher(iss.Body, out)
	parseCLIReleaseTested(iss.Body, out.Tested)
	bodies, err := c.commentBodies(ctx, iss.Number, iss.Comments)
	if err != nil {
		return nil, err
	}
	for _, b := range bodies {
		parseCLIReleaseTested(b, out.Tested)
	}
	out.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	return out, nil
}

// ghIssue is the slice of GitHub's issue JSON this needs. PullRequest is present only on
// pull requests, which /issues returns alongside real issues; `gh issue list` filters them
// out and so must this, or a PR that happened to be titled the same would be read instead.
type ghIssue struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	Comments    int             `json:"comments"`
	PullRequest json.RawMessage `json:"pull_request"`
}

// findIssue locates the tracking issue by exact title, the way cli-release-state.sh does.
//
// Sorted by creation ASCENDING and read one page deep: the tracking issue is among the
// oldest open issues of the repository and new issues pile up after it, so this is the one
// ordering under which page 1 keeps containing it — the default (newest first) would push
// it off the page once the repository has 100 open issues, and the block would go silently
// blank on a day nothing was wrong.
func (c *cliReleaseCache) findIssue(ctx context.Context) (*ghIssue, error) {
	var list []ghIssue
	url := fmt.Sprintf("%s/repos/%s/issues?state=open&per_page=%d&sort=created&direction=asc",
		c.base, cliReleaseRepo, cliReleasePerPage)
	if err := c.getJSON(ctx, url, &list); err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Title == cliReleaseIssueTitle && len(list[i].PullRequest) == 0 {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("no open issue titled %q in %s", cliReleaseIssueTitle, cliReleaseRepo)
}

// ghComment is the slice of a GitHub issue comment this needs.
type ghComment struct {
	Body string `json:"body"`
}

// commentBodies reads the last cliReleaseCommentPages pages of comments, oldest page
// first so that last-wins parsing sees them in the order they were written.
func (c *cliReleaseCache) commentBodies(ctx context.Context, issue, total int) ([]string, error) {
	if total <= 0 {
		return nil, nil
	}
	last := (total + cliReleasePerPage - 1) / cliReleasePerPage
	first := last - cliReleaseCommentPages + 1
	if first < 1 {
		first = 1
	}
	var out []string
	for page := first; page <= last; page++ {
		var items []ghComment
		url := fmt.Sprintf("%s/repos/%s/issues/%d/comments?per_page=%d&page=%d",
			c.base, cliReleaseRepo, issue, cliReleasePerPage, page)
		if err := c.getJSON(ctx, url, &items); err != nil {
			return nil, err
		}
		for _, it := range items {
			out = append(out, it.Body)
		}
	}
	return out, nil
}

// getJSON performs one anonymous GitHub read. No Authorization header, deliberately: see
// the file header. A non-200 is an error and the caller keeps its previous answer — 403
// (the hourly budget is spent) and 404 (the issue was closed or renamed) both land here.
func (c *cliReleaseCache) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "agent-fleet")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("github HTTP %d for %s", res.StatusCode, url)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, cliReleaseMaxBody))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// The two shapes cli-release-state.sh writes. The watcher block is a visible Markdown list
// item (so that whoever opens the issue to ask "why is tested old?" can read the answer
// without viewing the source); `tested` is an invisible HTML comment appended once per
// version. Both are anchored to the whole line, exactly like the `sed` that reads them
// back — a marker quoted inside prose must not count as state.
var (
	cliReleaseWatcherRe = regexp.MustCompile("^- `cli-release-state watcher ([a-z0-9-]+)=([^ `]*)`$")
	cliReleaseTestedRe  = regexp.MustCompile(`^<!-- cli-release-state tested ([A-Za-z0-9_-]+)=([^ ]*) -->$`)
)

// parseCLIReleaseWatcher fills the watcher fields from the issue body. Anything it does
// not find stays empty, which the Console reads as "unknown" and draws nothing for.
func parseCLIReleaseWatcher(body string, out *cliReleaseWire) {
	for _, line := range strings.Split(body, "\n") {
		m := cliReleaseWatcherRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		switch m[1] {
		case "ok":
			out.WatcherOkAt = m[2]
		case "at":
			out.WatcherAt = m[2]
		case "failed":
			out.WatcherFailed = parseCLIReleaseFailed(m[2])
		}
	}
}

// parseCLIReleaseFailed splits the `failed` value into rows. The watcher writes the
// literal `none` for a clean run, which is an empty list here — the Console warns on a
// non-empty one, so letting "none" through would warn on every healthy day.
func parseCLIReleaseFailed(v string) []string {
	out := []string{}
	if v == "" || v == "none" {
		return out
	}
	for _, row := range strings.Split(v, ",") {
		if row = strings.TrimSpace(row); row != "" {
			out = append(out, row)
		}
	}
	return out
}

// parseCLIReleaseTested collects `tested` markers into dst, last one winning — the same
// `tail -1` rule cli-release-state.sh reads them back with.
func parseCLIReleaseTested(body string, dst map[string]string) {
	for _, line := range strings.Split(body, "\n") {
		if m := cliReleaseTestedRe.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			dst[m[1]] = m[2]
		}
	}
}

// addCLIRelease appends the release-watcher block to a settings response, when there is
// one. The key is ABSENT until GitHub has answered at least once: on a deployment with no
// route out the Console must draw nothing here — not a watcher that reads as broken.
func addCLIRelease(out map[string]any) {
	if st := cliRelease.snapshot(); st != nil {
		out["cliRelease"] = st
	}
}
