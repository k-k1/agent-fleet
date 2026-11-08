package watch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GitHub event types this source produces over REST. pr.review.threads_unresolved needs
// GraphQL (REST has no resolved state for a thread); it is offered only when Threads is set.
var githubTypes = []string{
	"pr.checks.completed", "pr.checks.failed",
	"pr.conflict", "pr.mergeable", "pr.ready",
	"pr.merged", "pr.closed",
	"pr.review.changes_requested",
}

const typeThreads = "pr.review.threads_unresolved"

// Poll intervals the source suggests.
const (
	ghBusyAfter    = 15 * time.Second // checks running, or mergeability still being computed
	ghOpenAfter    = time.Minute
	ghSettledAfter = 30 * time.Minute // merged or closed: only a reopen can change it
)

// ghBudgetShare is the share of the token's hourly limit this source may spend (ADR 0133 rule
// 6.2). A 304 is not counted by GitHub and is not counted here.
const ghBudgetShare = 10

// GitHub reads pull requests over REST with conditional requests: a 304 costs nothing against
// the primary rate limit, so a watched PR that did not change is nearly free to ask about.
type GitHub struct {
	// Token returns the member's github.com token, "" when GitHub is not connected.
	Token func() (string, error)
	// Renew returns a fresh token after GitHub rejected the given one.
	Renew func(rejected string) (string, error)
	// Threads counts a PR's unresolved review threads. It is read only for a subject whose
	// watches ask for pr.review.threads_unresolved, and each read is one counted request.
	Threads func(ctx context.Context, repo string, number int) (int, error)
	// API is the REST base; tests point it at a local server.
	API    string
	Client *http.Client

	mu          sync.Mutex
	windowStart time.Time
	spent       int // counted requests in the current hour
	limit       int // the token's hourly limit as GitHub last stated it
	now         func() time.Time
}

// NewGitHub returns the github.com source.
func NewGitHub(token func() (string, error), renew func(string) (string, error)) *GitHub {
	return &GitHub{Token: token, Renew: renew, API: "https://api.github.com", Client: &http.Client{Timeout: 20 * time.Second}}
}

func (g *GitHub) Kind() string { return "github" }

func (g *GitHub) Types() []string {
	t := append([]string(nil), githubTypes...)
	if g.Threads != nil {
		t = append(t, typeThreads)
	}
	return t
}

var (
	ghRepoRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	ghRefRE  = regexp.MustCompile(`^(?:([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#|#)?([1-9][0-9]{0,9})$`)
	ghURLRE  = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/([1-9][0-9]{0,9})(?:[/?#].*)?$`)
)

// ParseGitHubRef turns "owner/repo#12", "#12" / "12" (with defaultRepo) or a pull request URL
// into the repository and number.
func ParseGitHubRef(ref, defaultRepo string) (repo string, number int, ok bool) {
	ref = strings.TrimSpace(ref)
	if m := ghURLRE.FindStringSubmatch(ref); m != nil {
		repo, number = m[1], atoi(m[2])
	} else if m := ghRefRE.FindStringSubmatch(ref); m != nil {
		repo, number = m[1], atoi(m[2])
		if repo == "" {
			repo = defaultRepo
		}
	} else {
		return "", 0, false
	}
	if !validRepo(repo) || number <= 0 {
		return "", 0, false
	}
	// GitHub names are case-insensitive; one spelling keeps one subject per PR.
	return strings.ToLower(repo), number, true
}

// validRepo is owner/repo with neither part "." or "..", which would change the API path.
func validRepo(repo string) bool {
	if !ghRepoRE.MatchString(repo) {
		return false
	}
	owner, name, _ := strings.Cut(repo, "/")
	return owner != "." && owner != ".." && name != "." && name != ".."
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func ghSubject(repo string, n int) SubjectID { return SubjectID(fmt.Sprintf("github:%s#%d", repo, n)) }

func parseGHSubject(id SubjectID) (string, int, bool) {
	rest, ok := strings.CutPrefix(string(id), "github:")
	if !ok {
		return "", 0, false
	}
	repo, num, ok := strings.Cut(rest, "#")
	if !ok || !validRepo(repo) {
		return "", 0, false
	}
	n := atoi(num)
	return repo, n, n > 0
}

func (g *GitHub) Resolve(ctx context.Context, ref, defaultRepo string) (Resolved, error) {
	repo, n, ok := ParseGitHubRef(ref, defaultRepo)
	if !ok {
		if defaultRepo == "" && ghRefRE.MatchString(strings.TrimSpace(ref)) {
			return Resolved{}, &ErrRefused{"a bare #N needs a GitHub repository in this session's working copy; give owner/repo#N"}
		}
		return Resolved{}, &ErrRefused{"give a pull request as owner/repo#N, #N in this session's repository, or its https://github.com/… URL"}
	}
	var pr ghPull
	status, err := g.getJSON(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, n), "", &pr)
	switch {
	case err != nil:
		return Resolved{}, err
	case status == http.StatusNotFound:
		return Resolved{}, &ErrRefused{fmt.Sprintf("%s#%d is not a pull request your GitHub connection can read", repo, n)}
	case status != http.StatusOK:
		return Resolved{}, fmt.Errorf("GitHub answered %d", status)
	}
	return Resolved{Subject: ghSubject(repo, n), URL: SafeURL(pr.HTMLURL)}, nil
}

// ghCursor is what one Poll keeps for the next: each endpoint's ETag and the part of its last
// 200 answer this source uses, so a 304 can be answered from it.
type ghCursor struct {
	Pull    ghCached[ghPull]      `json:"pull"`
	Runs    ghCached[ghCheckRuns] `json:"runs"`
	Status  ghCached[ghStatus]    `json:"status"`
	Reviews ghCached[[]ghReview]  `json:"reviews"`
	// ThreadEpisode counts the times the unresolved threads went back to zero, and ThreadHigh is
	// the most seen since: the version of pr.review.threads_unresolved, so a rise is news and a
	// fall is not.
	ThreadEpisode int `json:"thread_episode,omitempty"`
	ThreadHigh    int `json:"thread_high,omitempty"`
	// Later pages, by path, with their own ETags: a PR with many checks would otherwise spend
	// counted requests on every read even when nothing changed.
	RunPages    map[string]ghCached[ghCheckRuns] `json:"run_pages,omitempty"`
	StatusPages map[string]ghCached[ghStatus]    `json:"status_pages,omitempty"`
	ReviewPages map[string]ghCached[[]ghReview]  `json:"review_pages,omitempty"`
}

type ghCached[T any] struct {
	Key  string `json:"key,omitempty"` // the path asked; a new head SHA is a new key
	ETag string `json:"etag,omitempty"`
	Val  T      `json:"val"`
}

type ghPull struct {
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	Draft          bool   `json:"draft"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	HTMLURL        string `json:"html_url"`
	ClosedAt       string `json:"closed_at"`
	MergedAt       string `json:"merged_at"`
	Head           struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
	} `json:"base"`
}

type ghCheckRuns struct {
	Total int          `json:"total_count"`
	Runs  []ghCheckRun `json:"check_runs"`
}

type ghCheckRun struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	CompletedAt string `json:"completed_at"`
	HTMLURL     string `json:"html_url"`
}

type ghStatus struct {
	Total    int            `json:"total_count"`
	Statuses []ghStatusItem `json:"statuses"`
}

type ghStatusItem struct {
	Context   string `json:"context"`
	State     string `json:"state"`
	UpdatedAt string `json:"updated_at"`
	TargetURL string `json:"target_url"`
}

type ghReview struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
	User  struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (g *GitHub) Poll(ctx context.Context, subject SubjectID, raw json.RawMessage, want []string) ([]Event, json.RawMessage, Hint, error) {
	repo, n, ok := parseGHSubject(subject)
	if !ok {
		return nil, raw, Hint{}, fmt.Errorf("not a github subject: %s", subject)
	}
	var cur ghCursor
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cur)
	}
	if err := g.cached(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, n), &cur.Pull); err != nil {
		return nil, raw, Hint{}, err
	}
	pr := cur.Pull.Val
	at := g.clock()
	ev := func(typ, version, summary string, data []DataItem) Event {
		return Event{Source: "github", Subject: subject, Type: typ, Version: version, Summary: summary, Data: data, At: at.UTC()}
	}
	ref := fmt.Sprintf("PR %s#%d", repo, n)
	if pr.Merged || pr.State == "closed" {
		out, _ := json.Marshal(cur)
		if pr.Merged {
			return []Event{ev("pr.merged", "merged", ref+" was merged.", nil)}, out, Hint{After: ghSettledAfter, Settled: true}, nil
		}
		return []Event{ev("pr.closed", "closed:"+pr.ClosedAt, ref+" was closed without merging.", nil)}, out, Hint{After: ghSettledAfter, Settled: true}, nil
	}

	head := pr.Head.SHA
	busy := pr.Mergeable == nil
	checksOK := true
	var events []Event
	if head != "" {
		if err := g.cached(ctx, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", repo, head), &cur.Runs); err != nil {
			return nil, raw, Hint{}, err
		}
		if err := g.cached(ctx, fmt.Sprintf("/repos/%s/commits/%s/status?per_page=100", repo, head), &cur.Status); err != nil {
			return nil, raw, Hint{}, err
		}
		runs, status, err := g.allChecks(ctx, repo, head, &cur)
		if err != nil {
			return nil, raw, Hint{}, err
		}
		cs := summarizeChecks(runs, status)
		switch {
		case cs.pending:
			busy, checksOK = true, false
		case cs.total > 0:
			version := head + ":" + cs.fingerprint
			result := "passed"
			if len(cs.failed) > 0 {
				result = fmt.Sprintf("failed (%d of %d)", len(cs.failed), cs.total)
			}
			sum := fmt.Sprintf("Checks on %s finished on %s: %s.", ref, short(head), result)
			events = append(events, ev("pr.checks.completed", version, sum, cs.failed))
			if len(cs.failed) > 0 {
				checksOK = false
				events = append(events, ev("pr.checks.failed", version, sum, cs.failed))
			}
		}
	}
	pair := head + ":" + pr.Base.SHA
	if pr.Mergeable != nil {
		if *pr.Mergeable {
			events = append(events, ev("pr.mergeable", pair, ref+" can be merged into its base.", nil))
		} else if pr.MergeableState == "dirty" {
			events = append(events, ev("pr.conflict", pair, ref+" has a merge conflict with its base.", nil))
		}
	}
	// "clean" is GitHub's own verdict (mergeable, required checks and reviews satisfied), but it
	// ignores checks that are not required, so ready also needs every check finished and none
	// failed.
	if !pr.Draft && pr.MergeableState == "clean" && checksOK {
		events = append(events, ev("pr.ready", pair, ref+" is ready to merge: checks passed, mergeable, reviews satisfied.", nil))
	}
	if err := g.cached(ctx, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repo, n), &cur.Reviews); err != nil {
		return nil, raw, Hint{}, err
	}
	reviews, err := g.allReviews(ctx, repo, n, &cur)
	if err != nil {
		return nil, raw, Hint{}, err
	}
	if id, who := changesRequested(reviews); id != 0 {
		data := make([]DataItem, 0, len(who))
		for _, w := range who {
			data = append(data, DataItem{Text: w})
		}
		events = append(events, ev("pr.review.changes_requested", strconv.FormatInt(id, 10),
			fmt.Sprintf("Changes were requested on %s.", ref), data))
	}
	if g.Threads != nil && selects(want, typeThreads) {
		count, err := g.Threads(ctx, repo, n)
		if err != nil {
			// The REST answers and their ETags stay good: only the threads are unknown this
			// round, and a threads watch stays pending until a later read answers.
			log.Printf("watch: unresolved threads of %s: %v", ref, err)
			goto done
		}
		g.charge()
		switch {
		case count == 0 && cur.ThreadHigh > 0:
			cur.ThreadEpisode++
			cur.ThreadHigh = 0
		case count > cur.ThreadHigh:
			cur.ThreadHigh = count
		}
		if count > 0 {
			events = append(events, ev(typeThreads, fmt.Sprintf("%d.%d", cur.ThreadEpisode, cur.ThreadHigh),
				fmt.Sprintf("%s has %d unresolved review thread(s).", ref, count), nil))
		}
	}
done:
	out, _ := json.Marshal(cur)
	hint := Hint{After: ghOpenAfter}
	if busy {
		hint.After = ghBusyAfter
	}
	return events, out, hint, nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

type checkSummary struct {
	total       int
	pending     bool
	failed      []DataItem
	fingerprint string
}

var failedConclusions = map[string]bool{
	"failure": true, "timed_out": true, "cancelled": true, "action_required": true, "startup_failure": true,
}

// summarizeChecks combines check runs and commit statuses. The fingerprint covers every check's
// identity, state and completion time, so a re-run on the same commit is a new version.
func summarizeChecks(runs ghCheckRuns, st ghStatus) checkSummary {
	var cs checkSummary
	var parts []string
	for _, r := range runs.Runs {
		cs.total++
		if r.Status != "completed" {
			cs.pending = true
		} else if failedConclusions[r.Conclusion] {
			cs.failed = append(cs.failed, DataItem{Text: r.Name, URL: r.HTMLURL})
		}
		parts = append(parts, fmt.Sprintf("r%d|%s|%s|%s", r.ID, r.Status, r.Conclusion, r.CompletedAt))
	}
	for _, s := range st.Statuses {
		cs.total++
		switch s.State {
		case "pending":
			cs.pending = true
		case "failure", "error":
			cs.failed = append(cs.failed, DataItem{Text: s.Context, URL: s.TargetURL})
		}
		parts = append(parts, fmt.Sprintf("s%s|%s|%s", s.Context, s.State, s.UpdatedAt))
	}
	sort.Strings(parts)
	h := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	cs.fingerprint = hex.EncodeToString(h[:6])
	return cs
}

// changesRequested is the newest review id among reviewers whose latest decisive review asks for
// changes, and their logins. A comment-only review does not undo a request.
func changesRequested(reviews []ghReview) (int64, []string) {
	latest := map[string]ghReview{}
	for _, r := range reviews {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			if p, ok := latest[r.User.Login]; !ok || r.ID > p.ID {
				latest[r.User.Login] = r
			}
		}
	}
	var id int64
	var who []string
	for login, r := range latest {
		if r.State != "CHANGES_REQUESTED" {
			continue
		}
		who = append(who, login)
		id = max(id, r.ID)
	}
	sort.Strings(who)
	return id, who
}

// cached GETs path with the stored ETag and refreshes c on a 200; a 304 keeps it.
func cachedDecode[T any](g *GitHub, ctx context.Context, path string, c *ghCached[T]) error {
	etag := ""
	if c.Key == path {
		etag = c.ETag
	}
	var v T
	status, newTag, err := g.get(ctx, path, etag, &v)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusNotModified:
		return nil
	case http.StatusOK:
		c.Key, c.ETag, c.Val = path, newTag, v
		return nil
	case http.StatusNotFound:
		return errors.New("GitHub answered 404: the pull request is gone or no longer readable with your connection")
	default:
		return fmt.Errorf("GitHub answered %d for %s", status, path)
	}
}

func (g *GitHub) cached(ctx context.Context, path string, c any) error {
	switch c := c.(type) {
	case *ghCached[ghPull]:
		return cachedDecode(g, ctx, path, c)
	case *ghCached[ghCheckRuns]:
		return cachedDecode(g, ctx, path, c)
	case *ghCached[ghStatus]:
		return cachedDecode(g, ctx, path, c)
	case *ghCached[[]ghReview]:
		return cachedDecode(g, ctx, path, c)
	}
	return fmt.Errorf("watch: no decoder for %T", c)
}

func (g *GitHub) getJSON(ctx context.Context, path, etag string, v any) (int, error) {
	status, _, err := g.get(ctx, path, etag, v)
	return status, err
}

// get performs one GET, renewing the token once on a 401, and decodes a 200 into v. It
// enforces the source's share of the hourly limit and turns GitHub's rate-limit answers into
// ErrRateLimited.
func (g *GitHub) get(ctx context.Context, path, etag string, v any) (int, string, error) {
	if until, ok := g.overBudget(); ok {
		return 0, "", &ErrRateLimited{Until: until}
	}
	tok, err := g.Token()
	if err != nil {
		return 0, "", err
	}
	if tok == "" {
		return 0, "", &ErrRefused{"GitHub is not connected (Settings > Connections)"}
	}
	resp, err := g.do(ctx, path, etag, tok)
	if err == nil && resp.StatusCode == http.StatusUnauthorized && g.Renew != nil {
		resp.Body.Close()
		fresh, rerr := g.Renew(tok)
		switch {
		case rerr != nil:
			return 0, "", fmt.Errorf("GitHub rejected the connection's token and renewing it failed: %w", rerr)
		case fresh == "" || fresh == tok:
			return 0, "", errRejected
		}
		resp, err = g.do(ctx, path, etag, fresh)
	}
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	g.account(resp)
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return resp.StatusCode, etag, nil
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		if until, ok := rateLimitUntil(resp, g.clock()); ok {
			return 0, "", &ErrRateLimited{Until: until}
		}
		return resp.StatusCode, "", nil
	case resp.StatusCode == http.StatusUnauthorized:
		return 0, "", errRejected
	case resp.StatusCode != http.StatusOK:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return resp.StatusCode, "", nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v); err != nil {
		return 0, "", fmt.Errorf("GitHub answer for %s could not be read: %w", path, err)
	}
	return resp.StatusCode, resp.Header.Get("ETag"), nil
}

// errRejected is a refusal: only the member can fix it, by reconnecting.
var errRejected = &ErrRefused{"GitHub rejected the connection's token; reconnect GitHub in Settings > Connections"}

// ghMaxPages bounds how far a list is followed: 1,000 checks or reviews on one PR.
const ghMaxPages = 10

// allChecks completes the first pages of check runs and statuses with the pages after them,
// each read conditionally with its own ETag, so a PR is never judged on its first hundred
// checks and an unchanged page stays free.
func (g *GitHub) allChecks(ctx context.Context, repo, head string, cur *ghCursor) (ghCheckRuns, ghStatus, error) {
	runs := ghCheckRuns{Total: cur.Runs.Val.Total, Runs: append([]ghCheckRun(nil), cur.Runs.Val.Runs...)}
	runPages := map[string]ghCached[ghCheckRuns]{}
	for page := 2; len(runs.Runs) < runs.Total && page <= ghMaxPages; page++ {
		path := fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100&page=%d", repo, head, page)
		c := cur.RunPages[path]
		if err := cachedDecode(g, ctx, path, &c); err != nil {
			return ghCheckRuns{}, ghStatus{}, err
		}
		runPages[path] = c
		if len(c.Val.Runs) == 0 {
			break
		}
		runs.Runs = append(runs.Runs, c.Val.Runs...)
	}
	st := ghStatus{Total: cur.Status.Val.Total, Statuses: append([]ghStatusItem(nil), cur.Status.Val.Statuses...)}
	statusPages := map[string]ghCached[ghStatus]{}
	for page := 2; len(st.Statuses) < st.Total && page <= ghMaxPages; page++ {
		path := fmt.Sprintf("/repos/%s/commits/%s/status?per_page=100&page=%d", repo, head, page)
		c := cur.StatusPages[path]
		if err := cachedDecode(g, ctx, path, &c); err != nil {
			return ghCheckRuns{}, ghStatus{}, err
		}
		statusPages[path] = c
		if len(c.Val.Statuses) == 0 {
			break
		}
		st.Statuses = append(st.Statuses, c.Val.Statuses...)
	}
	cur.RunPages, cur.StatusPages = runPages, statusPages
	return runs, st, nil
}

// allReviews follows the reviews list past its first page (it has no total, so a full page
// means there may be more).
func (g *GitHub) allReviews(ctx context.Context, repo string, n int, cur *ghCursor) ([]ghReview, error) {
	out := append([]ghReview(nil), cur.Reviews.Val...)
	pages := map[string]ghCached[[]ghReview]{}
	last := len(out)
	for page := 2; last == 100 && page <= ghMaxPages; page++ {
		path := fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100&page=%d", repo, n, page)
		c := cur.ReviewPages[path]
		if err := cachedDecode(g, ctx, path, &c); err != nil {
			return nil, err
		}
		pages[path] = c
		out = append(out, c.Val...)
		last = len(c.Val)
	}
	cur.ReviewPages = pages
	return out, nil
}

func (g *GitHub) do(ctx context.Context, path, etag, tok string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.API, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

func (g *GitHub) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// account counts a request that GitHub counts (anything but a 304) against this source's share,
// and learns the token's hourly limit from the answer.
func (g *GitHub) account(resp *http.Response) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()
	if now.Sub(g.windowStart) >= time.Hour {
		g.windowStart, g.spent = now, 0
	}
	if l, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Limit")); err == nil && l > 0 {
		g.limit = l
	}
	if resp.StatusCode != http.StatusNotModified {
		g.spent++
	}
}

// charge counts one request made outside get (the threads read) against the source's share.
func (g *GitHub) charge() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.clock().Sub(g.windowStart) >= time.Hour {
		g.windowStart, g.spent = g.clock(), 0
	}
	g.spent++
}

func (g *GitHub) overBudget() (time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()
	if now.Sub(g.windowStart) >= time.Hour {
		return time.Time{}, false
	}
	limit := g.limit
	if limit <= 0 {
		limit = 5000
	}
	if g.spent >= limit*ghBudgetShare/100 {
		return g.windowStart.Add(time.Hour), true
	}
	return time.Time{}, false
}

// rateLimitUntil reads GitHub's primary (X-RateLimit-Remaining 0 + Reset) and secondary
// (Retry-After) rate-limit answers.
func rateLimitUntil(resp *http.Response, now time.Time) (time.Time, bool) {
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s >= 0 {
		return now.Add(time.Duration(s) * time.Second), true
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if r, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return time.Unix(r, 0), true
		}
	}
	return time.Time{}, false
}
