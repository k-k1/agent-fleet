package branchpr

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// bbServer is a stub of api.bitbucket.org. pulls answers the PR list of a repository by its
// path; statuses answers /pullrequests/{id}/statuses; main answers the repository's main branch.
type bbServer struct {
	mu       sync.Mutex
	paths    []string
	filters  []string
	auth     []string
	main     map[string]string // repo path → main branch; missing → 404
	pulls    map[string]string // repo path → body
	statuses map[string]string // ".../commit/<hash>/statuses" → body
	status   int               // non-zero: every call answers this
	// badBranch makes any filter naming it answer 400; byBranch answers a filter naming exactly
	// that one branch.
	badBranch string
	byBranch  map[string]string
	notFound  string // a path that answers 404
}

func (b *bbServer) handler(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paths = append(b.paths, r.URL.Path)
	b.auth = append(b.auth, r.Header.Get("Authorization"))
	if b.status != 0 {
		w.WriteHeader(b.status)
		return
	}
	if r.URL.Path == b.notFound {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/statuses"):
		if body, ok := b.statuses[p]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	case strings.HasSuffix(p, "/pullrequests"):
		q := r.URL.Query().Get("q")
		b.filters = append(b.filters, q)
		if b.badBranch != "" && strings.Contains(q, `"`+b.badBranch+`"`) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if strings.Count(q, "source.branch.name") == 1 {
			for br, body := range b.byBranch {
				if strings.Contains(q, `source.branch.name = "`+br+`"`) {
					_, _ = w.Write([]byte(body))
					return
				}
			}
		}
		_, _ = w.Write([]byte(b.pulls[strings.TrimSuffix(p, "/pullrequests")]))
	default:
		m, ok := b.main[p]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"mainbranch":{"name":"` + m + `"}}`))
	}
}

func newBBCache(t *testing.T, b *bbServer) *Cache {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(b.handler))
	t.Cleanup(srv.Close)
	c := NewBitbucket(func() (string, error) { return "Bearer bb", nil })
	c.Endpoint = srv.URL
	c.Async = func(f func()) { f() }
	return c
}

func bbPR(id int, state string, draft bool, branch, source string) string {
	d := "false"
	if draft {
		d = "true"
	}
	return `{"id":` + itoa(id) + `,"state":"` + state + `","draft":` + d +
		`,"links":{"html":{"href":"https://bitbucket.org/w/r/pull-requests/` + itoa(id) + `"}},` +
		`"source":{"branch":{"name":"` + branch + `"},"repository":{"full_name":"` + source + `"},` +
		`"commit":{"hash":"c0ffee` + itoa(id) + `"}}}`
}

func TestBitbucketAsksOncePerRepositoryAndPicksEachBranchsOwnPR(t *testing.T) {
	b := &bbServer{
		main: map[string]string{"/2.0/repositories/w/r": "main", "/2.0/repositories/w/other": "develop"},
		pulls: map[string]string{
			"/2.0/repositories/w/r": `{"values":[` +
				// Newest first: a fork's PR from a branch of the same name comes before ours.
				bbPR(12, "OPEN", false, "feature/a", "someone/r") + "," +
				bbPR(11, "OPEN", true, "feature/a", "W/R") + "," +
				bbPR(10, "DECLINED", false, "feature/a", "w/r") + "," +
				bbPR(9, "MERGED", false, "fix/b", "w/r") + "," +
				bbPR(8, "SUPERSEDED", false, "old", "w/r") + "]}",
			"/2.0/repositories/w/other": `{"values":[]}`,
		},
		statuses: map[string]string{
			"/2.0/repositories/w/r/commit/c0ffee11/statuses": `{"values":[{"state":"SUCCESSFUL"},{"state":"INPROGRESS"}]}`,
		},
	}
	c := newBBCache(t, b)
	keys := []Key{
		{"w/r", "feature/a"}, {"w/r", "fix/b"}, {"w/r", "old"}, {"w/r", "main"}, {"w/r", "none"},
		{"w/other", "x"}, {"w/gone", "y"},
	}
	c.Lookup(keys, time.Now())
	got := c.Lookup(keys, time.Now())

	if pr := got[keys[0]]; pr == nil || pr.Number != 11 || pr.State != "open" || !pr.Draft || pr.Checks != "pending" ||
		pr.URL != "https://bitbucket.org/w/r/pull-requests/11" {
		t.Errorf("feature/a = %+v, want #11 open draft pending (the fork's #12 and the older #10 skipped)", pr)
	}
	if pr := got[keys[1]]; pr == nil || pr.Number != 9 || pr.State != "merged" || pr.Checks != "" {
		t.Errorf("fix/b = %+v, want #9 merged with no status call", pr)
	}
	if pr := got[keys[2]]; pr == nil || pr.State != "closed" {
		t.Errorf("old = %+v, want a superseded PR read as closed", pr)
	}
	for _, k := range keys[3:] {
		if pr := got[k]; pr != nil {
			t.Errorf("%v = %+v, want none", k, pr)
		}
	}
	// One PR list per visible repository, the main branch left out of its filter; a status call
	// only for the open PR.
	if len(b.filters) != 2 {
		t.Fatalf("PR list calls = %d (%v), want one per visible repository", len(b.filters), b.paths)
	}
	f := b.filters[0]
	for _, want := range []string{`source.branch.name = "feature/a"`, `source.branch.name = "none"`, `state = "MERGED"`} {
		if !strings.Contains(f, want) {
			t.Errorf("filter %q lacks %q", f, want)
		}
	}
	if strings.Contains(f, `"main"`) {
		t.Errorf("filter %q asks about the main branch", f)
	}
	statusCalls := 0
	for _, p := range b.paths {
		if strings.HasSuffix(p, "/statuses") {
			statusCalls++
		}
	}
	if statusCalls != 1 {
		t.Errorf("status calls = %d, want 1 (open PRs only)", statusCalls)
	}
	for _, a := range b.auth {
		if a != "Bearer bb" {
			t.Fatalf("Authorization = %q, want the connection's value as given", a)
		}
	}
}

func TestBitbucketMainBranchIsAskedOncePerHour(t *testing.T) {
	b := &bbServer{
		main:  map[string]string{"/2.0/repositories/w/r": "main"},
		pulls: map[string]string{"/2.0/repositories/w/r": `{"values":[]}`},
	}
	c := newBBCache(t, b)
	keys := []Key{{"w/r", "a"}}
	now := time.Now()
	c.Lookup(keys, now)
	c.Lookup(keys, now.Add(settledTTL+time.Second))
	mains := 0
	for _, p := range b.paths {
		if p == "/2.0/repositories/w/r" {
			mains++
		}
	}
	if mains != 1 || len(b.filters) != 2 {
		t.Fatalf("main calls = %d, list calls = %d; want the main branch once and the list twice", mains, len(b.filters))
	}
}

func TestBitbucketFilterQuotesTheBranchName(t *testing.T) {
	f := bitbucketFilter([]Key{{"w/r", `a"b\c`}})
	if !strings.Contains(f, `source.branch.name = "a\"b\\c"`) {
		t.Fatalf("filter = %q, want the quote and backslash escaped", f)
	}
}

func TestBitbucketSplitsManyBranchesIntoBoundedFilters(t *testing.T) {
	b := &bbServer{
		main:  map[string]string{"/2.0/repositories/w/r": "main"},
		pulls: map[string]string{"/2.0/repositories/w/r": `{"values":[]}`},
	}
	c := newBBCache(t, b)
	var keys []Key
	for i := 0; i < bitbucketBranchesPerCall+1; i++ {
		keys = append(keys, Key{"w/r", "b" + itoa(i)})
	}
	c.Lookup(keys, time.Now())
	if len(b.filters) != 2 {
		t.Fatalf("list calls = %d, want 2 for %d branches", len(b.filters), len(keys))
	}
}

func TestBitbucketRefusalHoldsOffAndKeepsTheHeldAnswers(t *testing.T) {
	b := &bbServer{
		main:     map[string]string{"/2.0/repositories/w/r": "main"},
		pulls:    map[string]string{"/2.0/repositories/w/r": `{"values":[` + bbPR(4, "OPEN", false, "a", "w/r") + `]}`},
		statuses: map[string]string{"/2.0/repositories/w/r/commit/c0ffee4/statuses": `{"values":[]}`},
	}
	c := newBBCache(t, b)
	keys := []Key{{"w/r", "a"}}
	now := time.Now()
	c.Lookup(keys, now)
	// The connection loses the pull request scope: the row keeps what it showed, and the
	// provider is not asked again on the next poll.
	b.status = http.StatusForbidden
	later := now.Add(bitbucketOpenTTL + time.Second)
	c.Lookup(keys, later)
	n := len(b.paths)
	// Held past failBackoff too: a missing scope does not come back on its own.
	got := c.Lookup(keys, now.Add(failBackoff+2*time.Minute))
	if pr := got[keys[0]]; pr == nil || pr.Number != 4 {
		t.Fatalf("after a 403: %+v, want the held #4", pr)
	}
	if len(b.paths) != n {
		t.Fatalf("asked again right after a 403 (%d → %d calls)", n, len(b.paths))
	}
}

func TestBitbucket401RenewsOnce(t *testing.T) {
	b := &bbServer{status: http.StatusUnauthorized}
	c := newBBCache(t, b)
	renewed := 0
	c.Renew = func(rejected string) (string, error) {
		renewed++
		if rejected != "Bearer bb" {
			t.Errorf("rejected = %q", rejected)
		}
		return "Bearer fresh", nil
	}
	c.Lookup([]Key{{"w/r", "a"}}, time.Now())
	if renewed != 1 || len(b.auth) != 2 || b.auth[1] != "Bearer fresh" {
		t.Fatalf("renewed=%d auth=%v, want one renewal and one retry with the new value", renewed, b.auth)
	}
}

func TestBitbucketRollup(t *testing.T) {
	for body, want := range map[string]string{
		`{"values":[]}`:                       "",
		`{"values":[{"state":"SUCCESSFUL"}]}`: "success",
		`{"values":[{"state":"SUCCESSFUL"},{"state":"INPROGRESS"}]}`: "pending",
		`{"values":[{"state":"INPROGRESS"},{"state":"STOPPED"}]}`:    "failure",
		`{"values":[{"state":"FAILED"}]}`:                            "failure",
		`not json`:                                                   "",
	} {
		if got := bitbucketRollup([]byte(body), ""); got != want {
			t.Errorf("rollup(%s) = %q, want %q", body, got, want)
		}
	}
}

func TestRepoPathEscapesEachPart(t *testing.T) {
	if got := repoPath("w s/r?x"); got != "/2.0/repositories/"+url.PathEscape("w s")+"/"+url.PathEscape("r?x") {
		t.Fatalf("repoPath = %q", got)
	}
}

func TestBitbucketReadsChecksOfTheHeadCommitOnly(t *testing.T) {
	b := &bbServer{
		main:  map[string]string{"/2.0/repositories/w/r": "main"},
		pulls: map[string]string{"/2.0/repositories/w/r": `{"values":[` + bbPR(4, "OPEN", false, "a", "w/r") + `]}`},
		statuses: map[string]string{
			"/2.0/repositories/w/r/commit/c0ffee4/statuses": `{"values":[{"state":"SUCCESSFUL"}]}`,
			// The PR's own list spans its old commits too; it must not be what the row reads.
			"/2.0/repositories/w/r/pullrequests/4/statuses": `{"values":[{"state":"FAILED"}]}`,
		},
	}
	c := newBBCache(t, b)
	keys := []Key{{"w/r", "a"}}
	c.Lookup(keys, time.Now())
	if pr := c.Lookup(keys, time.Now())[keys[0]]; pr == nil || pr.Checks != "success" {
		t.Fatalf("pr = %+v, want the head commit's success", pr)
	}
}

func TestBitbucketAsksAnOpenPRLessOftenThanGitHub(t *testing.T) {
	b := &bbServer{
		main:     map[string]string{"/2.0/repositories/w/r": "main"},
		pulls:    map[string]string{"/2.0/repositories/w/r": `{"values":[` + bbPR(4, "OPEN", false, "a", "w/r") + `]}`},
		statuses: map[string]string{"/2.0/repositories/w/r/commit/c0ffee4/statuses": `{"values":[]}`},
	}
	c := newBBCache(t, b)
	keys := []Key{{"w/r", "a"}}
	now := time.Now()
	c.Lookup(keys, now)
	c.Lookup(keys, now.Add(openTTL+time.Second))
	if len(b.filters) != 1 {
		t.Fatalf("list calls = %d after GitHub's openTTL, want 1: an open Bitbucket PR is held for bitbucketOpenTTL", len(b.filters))
	}
	c.Lookup(keys, now.Add(bitbucketOpenTTL+time.Second))
	if len(b.filters) != 2 {
		t.Fatalf("list calls = %d after bitbucketOpenTTL, want 2", len(b.filters))
	}
}

func TestBitbucketBadFilterFailsOnlyItsOwnBranch(t *testing.T) {
	b := &bbServer{
		main: map[string]string{"/2.0/repositories/w/r": "main"},
		pulls: map[string]string{"/2.0/repositories/w/r": `{"values":[` +
			bbPR(5, "MERGED", false, "good", "w/r") + `]}`},
		badBranch: "odd",
	}
	c := newBBCache(t, b)
	keys := []Key{{"w/r", "good"}, {"w/r", "odd"}}
	c.Lookup(keys, time.Now())
	got := c.Lookup(keys, time.Now())
	if pr := got[keys[0]]; pr == nil || pr.Number != 5 {
		t.Fatalf("good = %+v, want #5 despite its neighbour's 400", pr)
	}
	if got[keys[1]] != nil {
		t.Fatalf("odd = %+v, want none", got[keys[1]])
	}
	// The odd branch is answered (as none), not failed: the provider is not held off.
	if !c.holdUntil.IsZero() {
		t.Fatalf("held until %v after a 400 of one branch", c.holdUntil)
	}
}

func TestBitbucketAsksAgainForABranchAFullPageLeftOut(t *testing.T) {
	var page []string
	for i := 0; i < bitbucketPageLen; i++ {
		page = append(page, bbPR(100+i, "MERGED", false, "busy", "w/r"))
	}
	b := &bbServer{
		main:     map[string]string{"/2.0/repositories/w/r": "main"},
		pulls:    map[string]string{"/2.0/repositories/w/r": `{"values":[` + strings.Join(page, ",") + `]}`},
		byBranch: map[string]string{"quiet": `{"values":[` + bbPR(7, "MERGED", false, "quiet", "w/r") + `]}`},
	}
	c := newBBCache(t, b)
	keys := []Key{{"w/r", "busy"}, {"w/r", "quiet"}}
	c.Lookup(keys, time.Now())
	got := c.Lookup(keys, time.Now())
	if pr := got[keys[1]]; pr == nil || pr.Number != 7 {
		t.Fatalf("quiet = %+v, want #7 from a call of its own", pr)
	}
	if pr := got[keys[0]]; pr == nil || pr.Number != 100 {
		t.Fatalf("busy = %+v, want its newest #100", pr)
	}
	if len(b.filters) != 2 {
		t.Fatalf("list calls = %d, want the chunk and one for quiet", len(b.filters))
	}
}

func TestBitbucketFallsBackToThePRsStatusesOfItsHeadWhenTheCommitIsNotResolved(t *testing.T) {
	commit := func(hash, state string) string {
		return `{"state":"` + state + `","links":{"commit":{"href":"https://api.bitbucket.org/2.0/repositories/w/r/commit/` + hash + `"}}}`
	}
	b := &bbServer{
		main:  map[string]string{"/2.0/repositories/w/r": "main"},
		pulls: map[string]string{"/2.0/repositories/w/r": `{"values":[` + bbPR(4, "OPEN", false, "a", "w/r") + `]}`},
		// The commit endpoint does not resolve the short hash (notFound below).
		statuses: map[string]string{
			"/2.0/repositories/w/r/pullrequests/4/statuses": `{"values":[` +
				commit("c0ffee4aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "SUCCESSFUL") + "," +
				commit("01d0000000000000000000000000000000000000", "FAILED") + `]}`,
		},
	}
	c := newBBCache(t, b)
	b.notFound = "/2.0/repositories/w/r/commit/c0ffee4/statuses"
	keys := []Key{{"w/r", "a"}}
	c.Lookup(keys, time.Now())
	if pr := c.Lookup(keys, time.Now())[keys[0]]; pr == nil || pr.Checks != "success" {
		t.Fatalf("pr = %+v, want success from the head's statuses only (the old commit's failure ignored)", pr)
	}
}

func TestBitbucketReadsThePRTitleForLinksOnly(t *testing.T) {
	body := `{"values":[{"id":3,"title":"PROJ-12 tidy","state":"MERGED","links":{"html":{"href":"https://bitbucket.org/w/r/pull-requests/3"}},` +
		`"source":{"branch":{"name":"a"},"repository":{"full_name":"w/r"}}}]}`
	got, _, err := parseBitbucketPulls([]byte(body), "w/r", []Key{{"w/r", "a"}})
	if err != nil || got[Key{"w/r", "a"}].pr.Title != "PROJ-12 tidy" {
		t.Fatalf("got %+v err %v, want the title read", got, err)
	}
}
