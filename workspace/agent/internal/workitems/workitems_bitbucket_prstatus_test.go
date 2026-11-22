package workitems

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

type bbListPR struct {
	id    int
	repo  string // "acme/web"
	state string // "" = OPEN
	draft bool
	dst   string // "" = d0
}

// bbStatusStub answers PR lists plus per-PR statuses and mergeability, and records every call.
type bbStatusStub struct {
	mu        sync.Mutex
	calls     []string // "<id>/statuses" or "<id>/mergeability"
	listQuery []url.Values
	prs       []bbListPR
	// override may answer a status read itself; it returns true when it did.
	override    func(w http.ResponseWriter, id int, kind string) bool
	listNear    bool
	nearRepo    map[string]bool
	list429     map[string]bool
	list429Once map[string]bool // the first list of the repository answers 429, then 200
	lists       map[string]int
	srcShift    bool // lists after the first report another source commit
	statuses    string
}

func newBBStatusStub(t *testing.T, prs ...bbListPR) *bbStatusStub {
	t.Helper()
	st := &bbStatusStub{prs: prs, statuses: `{"values":[{"state":"SUCCESSFUL"}]}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if i := strings.Index(p, "/pullrequests/"); i >= 0 {
			rest := strings.Split(p[i+len("/pullrequests/"):], "/")
			id, _ := strconv.Atoi(rest[0])
			kind := rest[1]
			st.mu.Lock()
			st.calls = append(st.calls, fmt.Sprintf("%d/%s", id, kind))
			ov := st.override
			body := st.statuses
			st.mu.Unlock()
			if ov != nil && ov(w, id, kind) {
				return
			}
			if kind == "statuses" {
				_, _ = w.Write([]byte(body))
				return
			}
			_, _ = w.Write([]byte(`{"values":[{"type":"git_mergeability_check","status":"PASSED","reason":"clean"}]}`))
			return
		}
		repo := strings.TrimSuffix(strings.TrimPrefix(p, "/2.0/repositories/"), "/pullrequests")
		st.mu.Lock()
		st.listQuery = append(st.listQuery, r.URL.Query())
		near := st.listNear || st.nearRepo[repo]
		if st.lists == nil {
			st.lists = map[string]int{}
		}
		st.lists[repo]++
		nth := st.lists[repo]
		if st.list429[repo] || (st.list429Once[repo] && nth == 1) {
			st.mu.Unlock()
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var vals []string
		for _, pr := range st.prs {
			if pr.repo != repo {
				continue
			}
			d, state := pr.dst, pr.state
			if d == "" {
				d = "d0"
			}
			if state == "" {
				state = "OPEN"
			}
			src := fmt.Sprintf("s%d", pr.id)
			if st.srcShift && nth > 1 {
				src += "b"
			}
			vals = append(vals, fmt.Sprintf(`{"id":%d,"title":"t","state":%q,"draft":%t,"updated_on":"2026-08-%02dT07:00:00+00:00",`+
				`"source":{"commit":{"hash":%q}},"destination":{"repository":{"full_name":%q},"commit":{"hash":%q}}}`,
				pr.id, state, pr.draft, pr.id, src, pr.repo, d))
		}
		st.mu.Unlock()
		if near {
			w.Header().Set("X-RateLimit-NearLimit", "true")
		}
		_, _ = w.Write([]byte(`{"values":[` + strings.Join(vals, ",") + `]}`))
	}))
	prev, prevWin := BitbucketAPIBase, bitbucketStatusWindow
	BitbucketAPIBase = srv.URL
	bitbucketStatusCache.Lock()
	bitbucketStatusCache.m = map[string]bitbucketStatusEntry{}
	bitbucketStatusCache.Unlock()
	t.Cleanup(func() { BitbucketAPIBase = prev; bitbucketStatusWindow = prevWin; srv.Close() })
	return st
}

func (st *bbStatusStub) snapshot() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]string(nil), st.calls...)
}
func (st *bbStatusStub) reset()       { st.mu.Lock(); st.calls = nil; st.mu.Unlock() }
func (st *bbStatusStub) set(f func()) { st.mu.Lock(); f(); st.mu.Unlock() }

// bbRefresh runs the handler's own fetch path (fetchWorkItems): every query's list, then one
// enrichment over all rows.
func bbRefresh(t *testing.T, s *secrets.Data, queries ...string) map[string]workItemOut {
	t.Helper()
	var qs []workItemQueryIn
	for i, q := range queries {
		qs = append(qs, workItemQueryIn{ID: fmt.Sprint("q", i), Provider: "bitbucket", Query: q})
	}
	items, errs, _ := fetchWorkItems(s, qs)
	_ = errs
	out := map[string]workItemOut{}
	for _, r := range items {
		out[r.Key] = r
	}
	return out
}

func web(ids ...int) []bbListPR {
	var o []bbListPR
	for _, id := range ids {
		o = append(o, bbListPR{id: id, repo: "acme/web"})
	}
	return o
}

func hasCall(calls []string, c string) bool {
	for _, x := range calls {
		if x == c {
			return true
		}
	}
	return false
}

func TestBitbucketListAsksForCommitHashesAndRowsGetStatus(t *testing.T) {
	st := newBBStatusStub(t, web(1)...)
	rows := bbRefresh(t, bbTokenStore(), "acme/web")
	if f := st.listQuery[0].Get("fields"); !strings.Contains(f, "values.source.commit.hash") || !strings.Contains(f, "values.destination.commit.hash") {
		t.Fatalf("fields = %q", f)
	}
	r := rows["acme/web#1"]
	if r.Checks.State != "success" || r.Mergeable != "clean" || len(st.snapshot()) != 2 {
		t.Fatalf("row=%+v calls=%v", r, st.snapshot())
	}
}

func TestBitbucketUnchangedHashesCauseNoCalls(t *testing.T) {
	st := newBBStatusStub(t, web(1, 2, 3)...)
	s := bbTokenStore()
	bbRefresh(t, s, "acme/web")
	st.reset()
	rows := bbRefresh(t, s, "acme/web")
	if len(st.snapshot()) != 0 {
		t.Fatalf("calls = %v", st.snapshot())
	}
	if r := rows["acme/web#1"]; r.Checks.State != "success" || r.Mergeable != "clean" {
		t.Fatalf("cached answer not applied: %+v", r)
	}
}

func TestBitbucketChangedHashesRereadOnlyTheirHalf(t *testing.T) {
	st := newBBStatusStub(t, web(1)...)
	s := bbTokenStore()
	bbRefresh(t, s, "acme/web")
	st.reset()
	st.set(func() { st.prs[0].dst = "d1" })
	rows := bbRefresh(t, s, "acme/web")
	if c := st.snapshot(); len(c) != 1 || c[0] != "1/mergeability" {
		t.Fatalf("destination change: calls = %v", c)
	}
	if rows["acme/web#1"].Checks.State != "success" {
		t.Fatalf("checks lost: %+v", rows["acme/web#1"])
	}
	// A source change moves both halves (its hash is in both keys); the list reports the new one.
	st.reset()
	bitbucketStatusCache.Lock()
	for k, e := range bitbucketStatusCache.m {
		e.checksSrc, e.mergeSrc = "older", "older"
		bitbucketStatusCache.m[k] = e
	}
	bitbucketStatusCache.Unlock()
	bbRefresh(t, s, "acme/web")
	if c := st.snapshot(); len(c) != 2 {
		t.Fatalf("source change: calls = %v", c)
	}
}

func TestBitbucketBudgetIsPerRefreshAcrossQueries(t *testing.T) {
	prs := web(1, 2, 3, 4, 5, 6)
	for id := 7; id <= 12; id++ {
		prs = append(prs, bbListPR{id: id, repo: "acme/api"})
	}
	st := newBBStatusStub(t, prs...)
	rows := bbRefresh(t, bbTokenStore(), "acme/web", "acme/api")
	if len(rows) != 12 {
		t.Fatalf("rows = %d", len(rows))
	}
	calls := st.snapshot()
	if len(calls) != 20 {
		t.Fatalf("want 10 PRs x 2 calls across both queries, got %d", len(calls))
	}
	// Newest first: 12..3 are read, 1 and 2 wait.
	for _, id := range []int{12, 3} {
		if !hasCall(calls, fmt.Sprintf("%d/statuses", id)) {
			t.Errorf("#%d not read: %v", id, calls)
		}
	}
	for _, id := range []int{1, 2} {
		if hasCall(calls, fmt.Sprintf("%d/statuses", id)) || rows[fmt.Sprintf("acme/web#%d", id)].Mergeable != "" {
			t.Errorf("#%d should wait for the next refresh", id)
		}
	}
}

func TestBitbucketTooManyRequestsStopsTheWholeRefresh(t *testing.T) {
	prs := web(1, 2, 3, 4)
	for id := 5; id <= 8; id++ {
		prs = append(prs, bbListPR{id: id, repo: "acme/api"})
	}
	st := newBBStatusStub(t, prs...)
	st.set(func() {
		st.override = func(w http.ResponseWriter, id int, kind string) bool {
			w.WriteHeader(http.StatusTooManyRequests)
			return true
		}
	})
	rows := bbRefresh(t, bbTokenStore(), "acme/web", "acme/api")
	if len(rows) != 8 {
		t.Fatalf("list lost: %d rows", len(rows))
	}
	if n := len(st.snapshot()); n > bitbucketStatusConcurrency {
		t.Fatalf("reads after the 429 = %d", n)
	}
	for _, r := range rows {
		if r.Mergeable != "" || r.Checks.State != "" {
			t.Fatalf("row drew status after 429: %+v", r)
		}
	}
}

func TestBitbucketNearLimitHeaderStopsBeforeTheBodyArrives(t *testing.T) {
	st := newBBStatusStub(t, web(1, 2, 3, 4, 5, 6, 7, 8)...)
	release := make(chan struct{})
	var once sync.Once
	st.set(func() {
		st.override = func(w http.ResponseWriter, id int, kind string) bool {
			if kind != "statuses" {
				return false
			}
			w.Header().Set("X-RateLimit-NearLimit", "true")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-release
			_, _ = w.Write([]byte(`{"values":[]}`))
			return true
		}
	})
	go func() { time.Sleep(300 * time.Millisecond); once.Do(func() { close(release) }) }()
	rows := bbRefresh(t, bbTokenStore(), "acme/web")
	once.Do(func() { close(release) })
	if n := len(st.snapshot()); n > bitbucketStatusConcurrency {
		t.Fatalf("reads started after the near-limit headers = %d", n)
	}
	if len(rows) != 8 {
		t.Fatalf("list lost")
	}
}

func TestBitbucketAListRateLimitStopsReadsForEveryQuery(t *testing.T) {
	prs := append(web(1, 2), bbListPR{id: 3, repo: "acme/api"}, bbListPR{id: 4, repo: "acme/done", state: "MERGED"})
	for name, arrange := range map[string]func(st *bbStatusStub){
		"near-limit on an empty list": func(st *bbStatusStub) { st.nearRepo = map[string]bool{"acme/empty": true} },
		"near-limit on finished rows": func(st *bbStatusStub) { st.nearRepo = map[string]bool{"acme/done": true} },
		"429 on one of the lists":     func(st *bbStatusStub) { st.list429 = map[string]bool{"acme/empty": true} },
	} {
		t.Run(name, func(t *testing.T) {
			st := newBBStatusStub(t, prs...)
			arrange(st)
			rows := bbRefresh(t, bbTokenStore(), "acme/empty", "acme/done", "acme/web", "acme/api")
			if len(st.snapshot()) != 0 {
				t.Fatalf("reads after a list warning: %v", st.snapshot())
			}
			if len(rows) == 0 {
				t.Fatal("lists lost")
			}
		})
	}
}

func TestBitbucketRateLimitedPastRefreshDoesNotStopTheNextOne(t *testing.T) {
	st := newBBStatusStub(t, web(1)...)
	st.set(func() { st.listNear = true })
	s := bbTokenStore()
	bbRefresh(t, s, "acme/web")
	st.set(func() { st.listNear = false })
	rows := bbRefresh(t, s, "acme/web")
	if rows["acme/web#1"].Mergeable != "clean" {
		t.Fatalf("row = %+v", rows["acme/web#1"])
	}
}

func TestBitbucketHalfFailureShowsNothingAndKeepsTheOtherHalf(t *testing.T) {
	for _, failing := range []string{"mergeability", "statuses"} {
		t.Run(failing, func(t *testing.T) {
			st := newBBStatusStub(t, web(1)...)
			st.set(func() {
				st.override = func(w http.ResponseWriter, id int, kind string) bool {
					if kind == failing {
						w.WriteHeader(http.StatusInternalServerError)
						return true
					}
					return false
				}
			})
			s := bbTokenStore()
			r := bbRefresh(t, s, "acme/web")["acme/web#1"]
			if r.Mergeable != "" || r.Checks.State != "" {
				t.Fatalf("a row with a failed half drew %+v", r)
			}
			st.reset()
			st.set(func() { st.override = nil })
			r = bbRefresh(t, s, "acme/web")["acme/web#1"]
			if c := st.snapshot(); len(c) != 1 || !strings.HasSuffix(c[0], failing) {
				t.Fatalf("only the failed half is read again: %v", c)
			}
			if r.Mergeable != "clean" || r.Checks.State != "success" {
				t.Fatalf("row = %+v", r)
			}
		})
	}
}

func TestBitbucketBrokenOKBodiesAreNotKept(t *testing.T) {
	bodies := map[string]func(w http.ResponseWriter) bool{
		"truncated": func(w http.ResponseWriter) bool {
			w.Header().Set("Content-Length", "500")
			_, _ = w.Write([]byte(`{"values":[{"state":"SUCCESSFUL"}]}`))
			return true
		},
		"not json":  func(w http.ResponseWriter) bool { _, _ = w.Write([]byte(`<html>`)); return true },
		"no values": func(w http.ResponseWriter) bool { _, _ = w.Write([]byte(`{"type":"error"}`)); return true },
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			st := newBBStatusStub(t, web(1)...)
			st.set(func() {
				st.override = func(w http.ResponseWriter, id int, kind string) bool {
					return kind == "statuses" && body(w)
				}
			})
			s := bbTokenStore()
			if r := bbRefresh(t, s, "acme/web")["acme/web#1"]; r.Checks.State != "" || r.Mergeable != "" {
				t.Fatalf("broken body drew %+v", r)
			}
			st.reset()
			st.set(func() { st.override = nil })
			r := bbRefresh(t, s, "acme/web")["acme/web#1"]
			if !hasCall(st.snapshot(), "1/statuses") || r.Checks.State != "success" {
				t.Fatalf("not re-read at the same hash: calls=%v row=%+v", st.snapshot(), r)
			}
		})
	}
}

func TestBitbucketStatusesWithNextPage(t *testing.T) {
	st := newBBStatusStub(t, web(1)...)
	st.set(func() { st.statuses = `{"values":[{"state":"SUCCESSFUL"}],"next":"https://x/page2"}` })
	r := bbRefresh(t, bbTokenStore(), "acme/web")["acme/web#1"]
	if r.Checks.State != "" || r.Mergeable != "" {
		t.Fatalf("all-green first page with a next page must not read green: %+v", r)
	}
	if got := st.listQuery; len(got) != 1 {
		t.Fatalf("lists = %d", len(got))
	}
	// A failure on the first page is a failure whatever follows.
	st.set(func() { st.statuses = `{"values":[{"state":"FAILED"}],"next":"https://x/page2"}` })
	r = bbRefresh(t, bbTokenStore(), "acme/web")["acme/web#1"]
	if r.Checks.State != "failure" {
		t.Fatalf("row = %+v", r)
	}
}

func TestBitbucketProvisionalAnswersAreShownButNotKept(t *testing.T) {
	st := newBBStatusStub(t, web(1)...)
	st.set(func() { st.statuses = `{"values":[{"state":"INPROGRESS"}]}` })
	s := bbTokenStore()
	if r := bbRefresh(t, s, "acme/web")["acme/web#1"]; r.Checks.State != "pending" {
		t.Fatalf("row = %+v", r)
	}
	st.reset()
	st.set(func() { st.statuses = `{"values":[{"state":"SUCCESSFUL"}]}` })
	r := bbRefresh(t, s, "acme/web")["acme/web#1"]
	if !hasCall(st.snapshot(), "1/statuses") || r.Checks.State != "success" {
		t.Fatalf("calls=%v row=%+v", st.snapshot(), r)
	}
	// An unknown conflict verdict is read again at the same hashes too.
	st.set(func() {
		st.override = func(w http.ResponseWriter, id int, kind string) bool {
			if kind == "mergeability" {
				_, _ = w.Write([]byte(`{"values":[{"type":"git_mergeability_check","status":"UNKNOWN"}]}`))
				return true
			}
			return false
		}
	})
	s2 := bbTokenStore()
	s2.Git["bitbucket.org"] = secrets.GitEntry{User: "u2", Token: "t2"}
	if r := bbRefresh(t, s2, "acme/web")["acme/web#1"]; r.Mergeable != "unknown" {
		t.Fatalf("row = %+v", r)
	}
	st.reset()
	st.set(func() { st.override = nil })
	if r := bbRefresh(t, s2, "acme/web")["acme/web#1"]; r.Mergeable != "clean" || !hasCall(st.snapshot(), "1/mergeability") {
		t.Fatalf("unknown was kept: calls=%v row=%+v", st.snapshot(), r)
	}
}

func TestBitbucketFailingNewestDoNotStarveTheRest(t *testing.T) {
	st := newBBStatusStub(t, web(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14)...)
	st.set(func() {
		st.override = func(w http.ResponseWriter, id int, kind string) bool {
			if kind == "mergeability" {
				w.WriteHeader(http.StatusInternalServerError)
				return true
			}
			return false
		}
	})
	s := bbTokenStore()
	bbRefresh(t, s, "acme/web")
	st.reset()
	bbRefresh(t, s, "acme/web")
	calls := st.snapshot()
	for id := 1; id <= 4; id++ {
		if !hasCall(calls, fmt.Sprintf("%d/statuses", id)) {
			t.Fatalf("#%d never reached on the second refresh: %v", id, calls)
		}
	}
}

func TestBitbucketStuckReadCostsNoMoreThanTheWindow(t *testing.T) {
	st := newBBStatusStub(t, web(1, 2)...)
	bitbucketStatusWindow = 300 * time.Millisecond
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	st.set(func() {
		st.override = func(w http.ResponseWriter, id int, kind string) bool { <-block; return true }
	})
	began := time.Now()
	rows := bbRefresh(t, bbTokenStore(), "acme/web")
	if time.Since(began) > 5*time.Second || len(rows) != 2 {
		t.Fatalf("took %v, rows %d", time.Since(began), len(rows))
	}
}

func TestBitbucketCacheIsPerConnectionAndSkipsFinishedPullRequests(t *testing.T) {
	st := newBBStatusStub(t, bbListPR{id: 1, repo: "acme/web"}, bbListPR{id: 2, repo: "acme/web", state: "MERGED"},
		bbListPR{id: 3, repo: "acme/web"})
	a := bbTokenStore()
	bbRefresh(t, a, "acme/web")
	calls := st.snapshot()
	if hasCall(calls, "2/statuses") || !hasCall(calls, "1/statuses") {
		t.Fatalf("calls = %v", calls)
	}
	st.reset()
	b := bbTokenStore()
	b.Git["bitbucket.org"] = secrets.GitEntry{User: "taro@example.com", Token: "rotated"}
	bbRefresh(t, b, "acme/web")
	if len(st.snapshot()) == 0 {
		t.Fatal("a replaced token reused the previous connection's answers")
	}
}

func TestBitbucketConnKeyTellsAccessTokenOnlyConnectionsApart(t *testing.T) {
	mk := func(access string) *secrets.Data {
		return &secrets.Data{Bitbucket: &secrets.BitbucketCreds{AccessToken: access}}
	}
	if bitbucketConnKey(mk("a")) == bitbucketConnKey(mk("b")) {
		t.Fatal("two access-token-only connections share a key")
	}
	if bitbucketConnKey(mk("a")) != bitbucketConnKey(mk("a")) {
		t.Fatal("key is not stable")
	}
	c1 := &secrets.Data{Bitbucket: &secrets.BitbucketCreds{AccessToken: "x", RefreshToken: "r"}}
	c2 := &secrets.Data{Bitbucket: &secrets.BitbucketCreds{AccessToken: "y", RefreshToken: "r"}}
	if bitbucketConnKey(c1) != bitbucketConnKey(c2) {
		t.Fatal("a rotated access token must not drop the cache of the same connection")
	}
}

func TestBitbucketMalformedEntriesAreFailedReads(t *testing.T) {
	cases := map[string]struct{ kind, body string }{
		"statuses entry without state": {"statuses", `{"values":[{}]}`},
		"statuses null state":          {"statuses", `{"values":[{"state":null}]}`},
		"merge entry not an object":    {"mergeability", `{"values":[42]}`},
		"merge entry without status":   {"mergeability", `{"values":[{"type":"git_mergeability_check"}]}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			st := newBBStatusStub(t, web(1)...)
			st.set(func() {
				st.override = func(w http.ResponseWriter, id int, kind string) bool {
					if kind != c.kind {
						return false
					}
					_, _ = w.Write([]byte(c.body))
					return true
				}
			})
			s := bbTokenStore()
			if r := bbRefresh(t, s, "acme/web")["acme/web#1"]; r.Checks.State != "" || r.Mergeable != "" {
				t.Fatalf("malformed answer drew %+v", r)
			}
			st.reset()
			bbRefresh(t, s, "acme/web")
			if !hasCall(st.snapshot(), "1/"+c.kind) {
				t.Fatalf("not re-read at the same hash: %v", st.snapshot())
			}
		})
	}
	// A legitimately empty page and an explicit UNKNOWN are answers.
	if _, final, ok := parseBitbucketStatusesPage([]byte(`{"values":[]}`)); !ok || !final {
		t.Fatal("empty statuses page rejected")
	}
	if !bitbucketValidMergeChecks([]byte(`{"values":[{"type":"git_mergeability_check","status":"UNKNOWN"}]}`)) {
		t.Fatal("explicit UNKNOWN rejected")
	}
}

func TestBitbucketOnePullRequestSeenAtTwoCommitsIsReadOnce(t *testing.T) {
	st := newBBStatusStub(t, web(1)...)
	st.set(func() { st.srcShift = true })
	s := bbTokenStore()
	// The same repository twice, as two overlapping saved queries; the second list sees a newer source.
	rows := bbRefresh(t, s, "acme/web", "acme/web state=\"OPEN\"")
	if n := len(st.snapshot()); n > 2 {
		t.Fatalf("one pull request read %d times: %v", n, st.snapshot())
	}
	_ = rows
	bitbucketStatusCache.Lock()
	var kept string
	for _, e := range bitbucketStatusCache.m {
		kept = e.checksSrc
	}
	bitbucketStatusCache.Unlock()
	if kept != "s1b" {
		t.Fatalf("cache holds %q, want the newest listing's source", kept)
	}
}

func TestBitbucketRetriesRotateThroughEveryPullRequest(t *testing.T) {
	st := newBBStatusStub(t, web(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14)...)
	st.set(func() {
		st.override = func(w http.ResponseWriter, id int, kind string) bool {
			if kind == "mergeability" {
				w.WriteHeader(http.StatusInternalServerError)
				return true
			}
			return false
		}
	})
	s := bbTokenStore()
	for i := 0; i < 4; i++ {
		bbRefresh(t, s, "acme/web")
	}
	merges := map[string]int{}
	for _, c := range st.snapshot() {
		if strings.HasSuffix(c, "/mergeability") {
			merges[c]++
		}
	}
	for id := 1; id <= 14; id++ {
		if merges[fmt.Sprintf("%d/mergeability", id)] < 2 {
			t.Fatalf("#%d retried %d times in 4 refreshes: %v", id, merges[fmt.Sprintf("%d/mergeability", id)], merges)
		}
	}
}

func TestBitbucketOnlyTheSlowHeaderMattersForTheStop(t *testing.T) {
	st := newBBStatusStub(t, web(1, 2, 3, 4, 5, 6, 7, 8)...)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	st.set(func() {
		st.override = func(w http.ResponseWriter, id int, kind string) bool {
			if kind != "statuses" {
				return false
			}
			if id == 8 { // the newest, read first: warns at once, body much later
				w.Header().Set("X-RateLimit-NearLimit", "true")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-release
				_, _ = w.Write([]byte(`{"values":[]}`))
				return true
			}
			time.Sleep(150 * time.Millisecond) // finishes after the header, with no warning of its own
			_, _ = w.Write([]byte(`{"values":[]}`))
			return true
		}
	})
	go func() { time.Sleep(600 * time.Millisecond); close(release) }()
	bbRefresh(t, bbTokenStore(), "acme/web")
	if n := len(st.snapshot()); n > bitbucketStatusConcurrency {
		t.Fatalf("reads started after another read's near-limit header: %v", st.snapshot())
	}
}

func TestBitbucketDraftsAreReadAndSourceChangesRereadBothHalves(t *testing.T) {
	st := newBBStatusStub(t, bbListPR{id: 1, repo: "acme/web", draft: true})
	s := bbTokenStore()
	r := bbRefresh(t, s, "acme/web")["acme/web#1"]
	if r.State != "in_progress" || r.Mergeable != "clean" || len(st.snapshot()) != 2 {
		t.Fatalf("draft row = %+v calls=%v", r, st.snapshot())
	}
	st.reset()
	st.set(func() { st.srcShift = true; st.lists = map[string]int{"acme/web": 1} })
	bbRefresh(t, s, "acme/web")
	if len(st.snapshot()) != 2 {
		t.Fatalf("a new source commit must re-read both halves: %v", st.snapshot())
	}
}

func TestBitbucketStaleRefreshCannotOverwriteANewerOne(t *testing.T) {
	bitbucketStatusCache.Lock()
	bitbucketStatusCache.m = map[string]bitbucketStatusEntry{}
	bitbucketStatusCache.Unlock()
	newer := &bbGroup{cacheKey: "k", srcHash: "sB", dstHash: "d", newChecks: true, checks: workItemChecksOut{State: "success", Total: 1}}
	older := &bbGroup{cacheKey: "k", srcHash: "sA", dstHash: "d", newChecks: true, unsettled: true}
	bitbucketStatusCache.Lock()
	bitbucketStoreGroup(newer, 5)
	bitbucketStoreGroup(older, 4)
	e := bitbucketStatusCache.m["k"]
	bitbucketStatusCache.Unlock()
	if e.checksSrc != "sB" || e.unsettled {
		t.Fatalf("older refresh overwrote the newer one: %+v", e)
	}
}

func TestBitbucketUnattemptedRowsNeverTouchAFullCache(t *testing.T) {
	st := newBBStatusStub(t, web(1, 2)...)
	st.set(func() { st.listNear = true })
	bitbucketStatusCache.Lock()
	for i := 0; i < bitbucketStatusCacheMax; i++ {
		bitbucketStatusCache.m[fmt.Sprint("k", i)] = bitbucketStatusEntry{checksOK: true}
	}
	bitbucketStatusCache.Unlock()
	bbRefresh(t, bbTokenStore(), "acme/web")
	bitbucketStatusCache.Lock()
	n := len(bitbucketStatusCache.m)
	_, ok := bitbucketStatusCache.m["k0"]
	unsettled := 0
	for k, e := range bitbucketStatusCache.m {
		if e.unsettled || strings.Contains(k, "|") {
			unsettled++
		}
	}
	bitbucketStatusCache.Unlock()
	if n != bitbucketStatusCacheMax || !ok || unsettled != 0 {
		t.Fatalf("cache changed by rows that were never read: %d entries, k0 present=%v, %d attempt records", n, ok, unsettled)
	}
}

func TestBitbucketA429ThatTheListRetryAbsorbedStillStopsTheReads(t *testing.T) {
	st := newBBStatusStub(t, web(1, 2)...)
	st.set(func() { st.list429Once = map[string]bool{"acme/web": true} })
	rows := bbRefresh(t, bbTokenStore(), "acme/web")
	if len(rows) != 2 {
		t.Fatalf("the retried list was lost: %d rows", len(rows))
	}
	if len(st.snapshot()) != 0 {
		t.Fatalf("reads after a 429 on the list: %v", st.snapshot())
	}
}

func TestBitbucketUnsupportedStateAndBadlyTypedFieldsAreFailedReads(t *testing.T) {
	cases := map[string]struct{ kind, body string }{
		"unsupported state": {"statuses", `{"values":[{"state":"IN_PROGRESS"}]}`},
		"numeric reason":    {"mergeability", `{"values":[{"type":"git_mergeability_check","status":"PASSED","reason":7}]}`},
		"string blocking":   {"mergeability", `{"values":[{"type":"x","status":"PASSED","blocking":"no"}]}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			st := newBBStatusStub(t, web(1)...)
			st.set(func() {
				st.override = func(w http.ResponseWriter, id int, kind string) bool {
					if kind != c.kind {
						return false
					}
					_, _ = w.Write([]byte(c.body))
					return true
				}
			})
			s := bbTokenStore()
			if r := bbRefresh(t, s, "acme/web")["acme/web#1"]; r.Checks.State != "" || r.Mergeable != "" {
				t.Fatalf("drew %+v", r)
			}
			st.reset()
			bbRefresh(t, s, "acme/web")
			if !hasCall(st.snapshot(), "1/"+c.kind) {
				t.Fatalf("not re-read: %v", st.snapshot())
			}
		})
	}
}

func TestBitbucketOlderListingNeverBorrowsTheNewerOnesAnswer(t *testing.T) {
	st := newBBStatusStub(t, web(1)...)
	s := bbTokenStore()
	// Cache the newer commit (what a second, newer listing reports) first.
	st.set(func() { st.srcShift = true; st.lists = map[string]int{"acme/web": 1} })
	bbRefresh(t, s, "acme/web")
	st.reset()
	st.set(func() { st.lists = nil })
	items, _, _ := fetchWorkItems(s, []workItemQueryIn{
		{ID: "old", Provider: "bitbucket", Query: "acme/web"},
		{ID: "new", Provider: "bitbucket", Query: `acme/web state="OPEN"`},
	})
	if len(st.snapshot()) != 0 {
		t.Fatalf("calls = %v", st.snapshot())
	}
	for _, r := range items {
		switch r.QueryID {
		case "old":
			if r.srcHash != "s1" || r.Mergeable != "" || r.Checks.State != "" {
				t.Fatalf("the older listing got an answer read at another commit: %+v", r)
			}
		case "new":
			if r.srcHash != "s1b" || r.Mergeable != "clean" {
				t.Fatalf("the newer listing lost its cached answer: %+v", r)
			}
		}
	}
}

func TestBitbucketJobsThatNeverStartedLeaveNoAttemptRecord(t *testing.T) {
	st := newBBStatusStub(t, web(1, 2, 3, 4, 5, 6, 7, 8)...)
	st.set(func() {
		st.override = func(w http.ResponseWriter, id int, kind string) bool {
			time.Sleep(100 * time.Millisecond)
			w.WriteHeader(http.StatusTooManyRequests)
			return true
		}
	})
	bbRefresh(t, bbTokenStore(), "acme/web")
	bitbucketStatusCache.Lock()
	n := len(bitbucketStatusCache.m)
	bitbucketStatusCache.Unlock()
	if n > bitbucketStatusConcurrency {
		t.Fatalf("%d attempt records for at most %d reads that started", n, bitbucketStatusConcurrency)
	}
}
