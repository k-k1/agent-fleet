package branchpr

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
	// conflicts answers ".../file-conflicts/<spec>"; ".../pullrequests/<id>/conflicts" redirects
	// there with the spec "c0ffee<id>..main", as Bitbucket does. Missing → 404.
	conflicts map[string]string
	fields    string // the last PR list's fields
	status    int    // non-zero: every call answers this
	// badBranch makes any filter naming it answer 400; byBranch answers a filter naming exactly
	// that one branch.
	badBranch string
	byBranch  map[string]string
	notFound  string // a path that answers 404
	// one answers ".../pullrequests/<id>", a PR asked by number; missing → 404. oneFields is the
	// fields it was last asked for.
	one       map[string]string
	oneFields string
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
	case strings.HasSuffix(p, "/conflicts") && strings.Contains(p, "/pullrequests/"):
		repo, id, _ := strings.Cut(strings.TrimSuffix(p, "/conflicts"), "/pullrequests/")
		http.Redirect(w, r, repo+"/file-conflicts/c0ffee"+id+"..main", http.StatusFound)
	case strings.Contains(p, "/file-conflicts/"):
		if body, ok := b.conflicts[p]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case strings.HasSuffix(p, "/statuses"):
		if pg := r.URL.Query().Get("page"); pg != "" {
			p += "?page=" + pg // a later page, keyed on its own
		}
		if body, ok := b.statuses[p]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	case strings.HasSuffix(p, "/pullrequests"):
		q := r.URL.Query().Get("q")
		b.filters = append(b.filters, q)
		b.fields = r.URL.Query().Get("fields")
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
	case strings.Contains(p, "/pullrequests/"):
		b.oneFields = r.URL.Query().Get("fields")
		if body, ok := b.one[p]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
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
		{Repo: "w/r", Branch: "feature/a"}, {Repo: "w/r", Branch: "fix/b"}, {Repo: "w/r", Branch: "old"}, {Repo: "w/r", Branch: "main"}, {Repo: "w/r", Branch: "none"},
		{Repo: "w/other", Branch: "x"}, {Repo: "w/gone", Branch: "y"},
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
	keys := []Key{{Repo: "w/r", Branch: "a"}}
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
	f := bitbucketFilter([]Key{{Repo: "w/r", Branch: `a"b\c`}})
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
		keys = append(keys, Key{Repo: "w/r", Branch: "b" + itoa(i)})
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
	keys := []Key{{Repo: "w/r", Branch: "a"}}
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
	c.Lookup([]Key{{Repo: "w/r", Branch: "a"}}, time.Now())
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
		if got, _, _ := bitbucketRollup([]byte(body), ""); got != want {
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
	keys := []Key{{Repo: "w/r", Branch: "a"}}
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
	keys := []Key{{Repo: "w/r", Branch: "a"}}
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
	keys := []Key{{Repo: "w/r", Branch: "good"}, {Repo: "w/r", Branch: "odd"}}
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
	keys := []Key{{Repo: "w/r", Branch: "busy"}, {Repo: "w/r", Branch: "quiet"}}
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
	keys := []Key{{Repo: "w/r", Branch: "a"}}
	c.Lookup(keys, time.Now())
	if pr := c.Lookup(keys, time.Now())[keys[0]]; pr == nil || pr.Checks != "success" {
		t.Fatalf("pr = %+v, want success from the head's statuses only (the old commit's failure ignored)", pr)
	}
}

func TestBitbucketReadsThePRTitleForLinksOnly(t *testing.T) {
	body := `{"values":[{"id":3,"title":"PROJ-12 tidy","state":"MERGED","links":{"html":{"href":"https://bitbucket.org/w/r/pull-requests/3"}},` +
		`"source":{"branch":{"name":"a"},"repository":{"full_name":"w/r"}}}]}`
	got, _, err := parseBitbucketPulls([]byte(body), "w/r", []Key{{Repo: "w/r", Branch: "a"}})
	if err != nil || got[Key{Repo: "w/r", Branch: "a"}].pr.Title != "PROJ-12 tidy" {
		t.Fatalf("got %+v err %v, want the title read", got, err)
	}
}

func TestBitbucketReadsReviewConflictAndFailingBuilds(t *testing.T) {
	pr := strings.TrimSuffix(bbPR(4, "OPEN", false, "a", "w/r"), "}") +
		`,"destination":{"repository":{"uuid":"{u-1}"}},"participants":[{"state":null},{"state":"approved"},{"state":"changes_requested"}]}`
	b := &bbServer{
		main:  map[string]string{"/2.0/repositories/w/r": "main"},
		pulls: map[string]string{"/2.0/repositories/w/r": `{"values":[` + pr + `]}`},
		statuses: map[string]string{"/2.0/repositories/w/r/commit/c0ffee4/statuses": `{"values":[` +
			`{"state":"FAILED","name":"build","url":"https://ci/b"},{"state":"STOPPED","key":"deploy-key"},{"state":"SUCCESSFUL","name":"lint"}]}`},
		conflicts: map[string]string{"/2.0/repositories/w/r/file-conflicts/c0ffee4..main": `{"pagelen":500,"size":1,"values":[{"path":"a.go","scenario":"content"}]}`},
	}
	c := newBBCache(t, b)
	keys := []Key{{Repo: "w/r", Branch: "a"}}
	c.Lookup(keys, time.Now())
	got := c.Lookup(keys, time.Now())[keys[0]]
	if got == nil || got.NodeID != "{u-1}#4" || !got.ChangesRequested || !got.Conflict || got.Checks != "failure" {
		t.Fatalf("pr = %+v, want node id, changes requested, conflict and failure", got)
	}
	if len(got.FailingChecks) != 2 || got.FailingChecks[0] != (Check{"build", "https://ci/b"}) || got.FailingChecks[1].Name != "deploy-key" {
		t.Fatalf("failing = %+v, want build and the stopped status by its key", got.FailingChecks)
	}
	if !strings.Contains(b.fields, "values.participants.state") || !strings.Contains(b.fields, "values.destination.repository.uuid") {
		t.Fatalf("fields = %q, want participants and the destination uuid", b.fields)
	}
}

func TestBitbucketReadsTheConflictOncePerTTLAndAgainForANewHead(t *testing.T) {
	b := &bbServer{
		main:      map[string]string{"/2.0/repositories/w/r": "main"},
		pulls:     map[string]string{"/2.0/repositories/w/r": `{"values":[` + bbPR(4, "OPEN", false, "a", "w/r") + `]}`},
		statuses:  map[string]string{"/2.0/repositories/w/r/commit/c0ffee4/statuses": `{"values":[]}`, "/2.0/repositories/w/r/commit/beef/statuses": `{"values":[]}`},
		conflicts: map[string]string{"/2.0/repositories/w/r/file-conflicts/c0ffee4..main": `{"size":1,"values":[{"path":"a.go"}]}`},
	}
	c := newBBCache(t, b)
	bb := &bitbucket{c: c, main: map[string]mainEntry{}, conflicts: map[string]conflictEntry{}}
	count := func() int {
		b.mu.Lock()
		defer b.mu.Unlock()
		n := 0
		for _, p := range b.paths {
			if strings.HasSuffix(p, "/pullrequests/4/conflicts") {
				n++
			}
		}
		return n
	}
	if !bb.conflict("Bearer bb", "w/r", 4, "c0ffee4") || !bb.conflict("Bearer bb", "w/r", 4, "c0ffee4") || count() != 1 {
		t.Fatalf("conflicts calls = %d, want 1 within the TTL", count())
	}
	b.mu.Lock()
	b.conflicts["/2.0/repositories/w/r/file-conflicts/c0ffee4..main"] = `{"size":0,"values":[]}`
	b.mu.Unlock()
	if bb.conflict("Bearer bb", "w/r", 4, "beef") || count() != 2 {
		t.Fatalf("a new head must be read again (calls %d)", count())
	}
	// A failed read keeps what this head showed rather than inventing a conflict.
	b.mu.Lock()
	b.conflicts = nil
	bb.conflicts["w/r#4"] = conflictEntry{head: "beef", conflict: true, at: time.Now().Add(-time.Hour)}
	b.mu.Unlock()
	if !bb.conflict("Bearer bb", "w/r", 4, "beef") || bb.conflict("Bearer bb", "w/r", 4, "other") {
		t.Fatal("a failed read must keep this head's last state, and claim nothing for another head")
	}
}

func TestBitbucketApprovalsAloneRequestNoChanges(t *testing.T) {
	pr := strings.TrimSuffix(bbPR(4, "OPEN", false, "a", "w/r"), "}") + `,"participants":[{"state":"approved"},{"state":null}]}`
	got, _, err := parseBitbucketPulls([]byte(`{"values":[`+pr+`]}`), "w/r", []Key{{Repo: "w/r", Branch: "a"}})
	if err != nil || got[Key{Repo: "w/r", Branch: "a"}].pr.ChangesRequested || got[Key{Repo: "w/r", Branch: "a"}].pr.NodeID != "" {
		t.Fatalf("got %+v err %v: approvals request no changes, and no uuid gives no node id", got[Key{Repo: "w/r", Branch: "a"}].pr, err)
	}
}

func TestBitbucketConflictFollowsTheRedirectWithTheConnection(t *testing.T) {
	b := &bbServer{conflicts: map[string]string{"/2.0/repositories/w/r/file-conflicts/c0ffee4..main": `{"values":[{"path":"a.go"}]}`}}
	c := newBBCache(t, b)
	bb := &bitbucket{c: c, main: map[string]mainEntry{}, conflicts: map[string]conflictEntry{}}
	if !bb.conflict("Bearer bb", "w/r", 4, "c0ffee4") {
		t.Fatal("a listed file conflict must read as a conflict")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.paths) != 2 || b.paths[1] != "/2.0/repositories/w/r/file-conflicts/c0ffee4..main" || b.auth[1] != "Bearer bb" {
		t.Fatalf("paths %v auth %v: want the redirect followed with the same Authorization", b.paths, b.auth)
	}
}

func TestBitbucketConflictKeepsAnActivePRsStateAcrossAnotherPRsAnswer(t *testing.T) {
	b := &bbServer{conflicts: map[string]string{"/2.0/repositories/w/r/file-conflicts/c0ffee1..main": `{"values":[]}`}}
	c := newBBCache(t, b)
	bb := &bitbucket{c: c, main: map[string]mainEntry{}, conflicts: map[string]conflictEntry{}}
	stale := time.Now().Add(-bitbucketConflictTTL - time.Minute)
	bb.conflicts["w/r#1"] = conflictEntry{head: "h1", conflict: false, at: stale, asked: time.Now()}
	bb.conflicts["w/r#2"] = conflictEntry{head: "h2", conflict: true, at: stale, asked: time.Now()}
	_ = bb.conflict("Bearer bb", "w/r", 1, "h1") // answers; must not prune #2, which is merely past its TTL
	if !bb.conflict("Bearer bb", "w/r", 2, "h2") {
		t.Fatal("#2's read failed (404) and must fall back to the conflict it last showed for this head")
	}
	// #2's reads have failed for longer than forgetAfter, but it is still asked about: its last
	// good read must outlive #1's next answer.
	long := time.Now().Add(-forgetAfter - time.Minute)
	bb.conflicts["w/r#1"] = conflictEntry{head: "h1", at: stale, asked: time.Now()}
	bb.conflicts["w/r#2"] = conflictEntry{head: "h2", conflict: true, at: long, asked: long}
	if !bb.conflict("Bearer bb", "w/r", 2, "h2") { // fails again, and is asked now
		t.Fatal("#2 must fall back to its known conflict")
	}
	_ = bb.conflict("Bearer bb", "w/r", 1, "h1")
	if !bb.conflict("Bearer bb", "w/r", 2, "h2") {
		t.Fatal("#1's answer pruned #2, which was asked a moment ago")
	}
	// A PR nobody asked about for forgetAfter does go.
	bb.conflicts["w/r#3"] = conflictEntry{head: "h3", conflict: true, at: long, asked: long}
	bb.conflicts["w/r#1"] = conflictEntry{head: "h1", at: stale, asked: time.Now()}
	_ = bb.conflict("Bearer bb", "w/r", 1, "h1")
	if _, kept := bb.conflicts["w/r#3"]; kept {
		t.Fatal("an entry unasked for forgetAfter must be pruned")
	}
}

func TestBitbucketConflictTakesAnUnreadableAnswerAsAFailedRead(t *testing.T) {
	b := &bbServer{conflicts: map[string]string{"/2.0/repositories/w/r/file-conflicts/c0ffee4..main": `not json`}}
	c := newBBCache(t, b)
	bb := &bitbucket{c: c, main: map[string]mainEntry{}, conflicts: map[string]conflictEntry{}}
	past := time.Now().Add(-bitbucketConflictTTL - time.Minute)
	bb.conflicts["w/r#4"] = conflictEntry{head: "h", conflict: true, at: past}
	if !bb.conflict("Bearer bb", "w/r", 4, "h") {
		t.Fatal("an unreadable answer must keep this head's known conflict")
	}
	if e := bb.conflicts["w/r#4"]; !e.at.Equal(past) {
		t.Fatalf("entry %+v: an unreadable answer must not count as a fresh read", e)
	}
	if bb.conflict("Bearer bb", "w/r", 4, "other") {
		t.Fatal("another head has no known state, so no conflict")
	}
	b.mu.Lock()
	b.conflicts["/2.0/repositories/w/r/file-conflicts/c0ffee4..main"] = `{"pagelen":10}`
	b.mu.Unlock()
	bb.conflicts["w/r#4"] = conflictEntry{head: "h", conflict: true, at: past}
	if !bb.conflict("Bearer bb", "w/r", 4, "h") {
		t.Fatal("an answer with neither values nor size says nothing; keep the known state")
	}
}

func TestBitbucketReadsAPRByNumberWithoutTheMainBranch(t *testing.T) {
	b := &bbServer{
		one: map[string]string{
			"/2.0/repositories/w/r/pullrequests/4": bbPR(4, "OPEN", false, "main", "w/fork"),
			"/2.0/repositories/w/r/pullrequests/6": bbPR(6, "WEIRD", false, "x", "w/r"),
			"/2.0/repositories/w/r/pullrequests/8": bbPR(3, "OPEN", false, "x", "w/r"),
		},
		statuses: map[string]string{
			"/2.0/repositories/w/r/commit/c0ffee4/statuses": `{"values":[{"state":"FAILED","name":"unit"},{"state":"INPROGRESS","name":"e2e"}]}`,
		},
		conflicts: map[string]string{"/2.0/repositories/w/r/file-conflicts/c0ffee4..main": `{"size":1,"values":[{"path":"a.go"}]}`},
	}
	c := newBBCache(t, b)
	keys := []Key{{Repo: "w/r", Number: 4}, {Repo: "w/r", Number: 5}, {Repo: "w/r", Number: 6}, {Repo: "w/r", Number: 8}}
	c.Lookup(keys, time.Now())
	got := c.Lookup(keys, time.Now())
	pr := got[keys[0]]
	if pr == nil || pr.Number != 4 || pr.State != "open" || pr.Checks != "failure" || !pr.ChecksPending || !pr.Conflict {
		t.Fatalf("pr #4 = %+v: a PR held by number is read whatever its branch, with its builds and conflict", pr)
	}
	if len(pr.FailingChecks) != 1 || pr.FailingChecks[0].Name != "unit" {
		t.Errorf("failing = %v", pr.FailingChecks)
	}
	if got[keys[1]] != nil || got[keys[2]] != nil || got[keys[3]] != nil {
		t.Errorf("a PR not found, in a state the row does not know, or answered for another number answers none: %v %v %v",
			got[keys[1]], got[keys[2]], got[keys[3]])
	}
	for _, p := range b.paths {
		if p == "/2.0/repositories/w/r" {
			t.Errorf("asked the main branch for number keys only: %v", b.paths)
		}
	}
	for _, f := range []string{"id", "source.commit.hash", "participants.state", "destination.repository.uuid"} {
		if !strings.Contains(b.oneFields, f) || strings.Contains(b.oneFields, "values.") {
			t.Errorf("fields %q: want %s, without the list's values. prefix", b.oneFields, f)
		}
	}
}

func TestBitbucketRollupTellsAFailureWithBuildsStillRunning(t *testing.T) {
	for body, want := range map[string]bool{
		`{"values":[{"state":"FAILED"},{"state":"INPROGRESS"}]}`: true,
		`{"values":[{"state":"FAILED"},{"state":"SUCCESSFUL"}]}`: false,
		`{"values":[{"state":"INPROGRESS"}]}`:                    true,
	} {
		if _, _, pending := bitbucketRollup([]byte(body), ""); pending != want {
			t.Errorf("rollup(%s) pending = %v, want %v", body, pending, want)
		}
	}
}

func TestBitbucketConflictWaitsOutTheTTLAfterAFailedRead(t *testing.T) {
	b := &bbServer{} // every conflicts read answers 404
	c := newBBCache(t, b)
	bb := &bitbucket{c: c, main: map[string]mainEntry{}, conflicts: map[string]conflictEntry{}}
	past := time.Now().Add(-bitbucketConflictTTL - time.Minute)
	bb.conflicts["w/r#4"] = conflictEntry{head: "h", conflict: true, at: past, asked: time.Now()}
	for range 3 {
		if !bb.conflict("Bearer bb", "w/r", 4, "h") {
			t.Fatal("a failed read keeps this head's known conflict")
		}
	}
	reads := 0
	for _, p := range b.paths {
		if strings.HasSuffix(p, "/conflicts") {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("read the conflicts %d times (%v): a failed read waits out the TTL", reads, b.paths)
	}
	if e := bb.conflicts["w/r#4"]; !e.at.Equal(past) {
		t.Fatalf("entry %+v: a failed read is not a fresh one", e)
	}
	// The wait ends with the TTL.
	e := bb.conflicts["w/r#4"]
	e.tried = past
	bb.conflicts["w/r#4"] = e
	_ = bb.conflict("Bearer bb", "w/r", 4, "h")
	if n := strings.Count(strings.Join(b.paths, " "), "/conflicts"); n != 2 {
		t.Fatalf("read %d times, want a second read once the TTL ran out", n)
	}
}

func TestBitbucketGivesThePRItsHeadAndHoldsAFailureWithMorePages(t *testing.T) {
	b := &bbServer{
		main:  map[string]string{"/2.0/repositories/w/r": "main"},
		pulls: map[string]string{"/2.0/repositories/w/r": `{"values":[` + bbPR(4, "OPEN", false, "a", "w/r") + `]}`},
		statuses: map[string]string{
			"/2.0/repositories/w/r/commit/c0ffee4/statuses": `{"next":"https://api.bitbucket.org/page2","values":[{"state":"FAILED","name":"unit"}]}`,
		},
	}
	c := newBBCache(t, b)
	keys := []Key{{Repo: "w/r", Branch: "a"}}
	c.Lookup(keys, time.Now())
	pr := c.Lookup(keys, time.Now())[keys[0]]
	if pr == nil || pr.Head != "c0ffee4" {
		t.Fatalf("pr = %+v: the PR carries its head commit, which the CI notice is keyed on", pr)
	}
	if pr.Checks != "failure" || !pr.ChecksPending {
		t.Fatalf("pr = %+v: a failure with another page of statuses may still have builds running", pr)
	}
	if got, _, pending := bitbucketRollup([]byte(`{"next":"x","values":[{"state":"SUCCESSFUL"}]}`), ""); got != "success" || pending {
		t.Errorf("rollup = %q pending %v: another page does not turn a green page pending", got, pending)
	}
}

func TestBitbucketFollowsTheStatusPagesToTheEnd(t *testing.T) {
	b := &bbServer{statuses: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(b.handler))
	t.Cleanup(srv.Close)
	c := NewBitbucket(func() (string, error) { return "Bearer bb", nil })
	c.Endpoint = srv.URL
	bb := &bitbucket{c: c, main: map[string]mainEntry{}, conflicts: map[string]conflictEntry{}}
	commit := "/2.0/repositories/w/r/commit/c0ffee4/statuses"
	next := func(path string, n int) string { return srv.URL + path + "?page=" + strconv.Itoa(n) }
	// More than one page, every build finished, one failed on page 1: told, with page 2 read.
	b.statuses[commit] = `{"next":"` + next(commit, 2) + `","values":[{"state":"FAILED","name":"unit"}]}`
	b.statuses[commit+"?page=2"] = `{"values":[{"state":"SUCCESSFUL","name":"e2e"},{"state":"FAILED","name":"lint"}]}`
	state, failing, pending := bb.checks("Bearer bb", "w/r", 4, "c0ffee4")
	if state != "failure" || pending || len(failing) != 2 {
		t.Fatalf("checks = %q %v pending %v: every page read, all finished", state, failing, pending)
	}
	// Still running on page 2: pending.
	b.statuses[commit+"?page=2"] = `{"values":[{"state":"INPROGRESS","name":"e2e"}]}`
	if _, _, pending := bb.checks("Bearer bb", "w/r", 4, "c0ffee4"); !pending {
		t.Fatal("a build running on a later page holds the failure")
	}
	// The PR's own list (the commit not found): a later page's build of this head counts too.
	delete(b.statuses, commit)
	delete(b.statuses, commit+"?page=2")
	pull := "/2.0/repositories/w/r/pullrequests/4/statuses"
	b.statuses[pull] = `{"next":"` + next(pull, 2) + `","values":[{"state":"FAILED","name":"unit","links":{"commit":{"href":"x/commit/c0ffee4"}}}]}`
	b.statuses[pull+"?page=2"] = `{"values":[{"state":"INPROGRESS","name":"e2e","links":{"commit":{"href":"x/commit/c0ffee4"}}},` +
		`{"state":"INPROGRESS","name":"old","links":{"commit":{"href":"x/commit/0ld"}}}]}`
	b.notFound = commit
	if _, _, pending := bb.checks("Bearer bb", "w/r", 4, "c0ffee4"); !pending {
		t.Fatal("the fallback's later page holds this head's running build")
	}
	// Past the bound, unread statuses count as running.
	b.statuses[pull] = `{"next":"` + next(pull, 1) + `","values":[{"state":"FAILED","name":"unit","links":{"commit":{"href":"x/commit/c0ffee4"}}}]}`
	b.statuses[pull+"?page=1"] = b.statuses[pull]
	if _, _, pending := bb.checks("Bearer bb", "w/r", 4, "c0ffee4"); !pending {
		t.Fatal("pages past the bound are unknown")
	}
}
