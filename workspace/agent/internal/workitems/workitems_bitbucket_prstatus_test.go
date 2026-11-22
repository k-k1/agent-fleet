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
	dst   string // "" = d0
}

// bbStatusStub answers PR lists plus per-PR statuses and mergeability, and records every call.
type bbStatusStub struct {
	mu        sync.Mutex
	calls     []string // "<id>/statuses" or "<id>/mergeability"
	listQuery []url.Values
	prs       []bbListPR
	// override may answer a status read itself; it returns true when it did.
	override func(w http.ResponseWriter, id int, kind string) bool
	listNear bool
	statuses string
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
		near := st.listNear
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
			vals = append(vals, fmt.Sprintf(`{"id":%d,"title":"t","state":%q,"updated_on":"2026-08-%02dT07:00:00+00:00",`+
				`"source":{"commit":{"hash":"s%d"}},"destination":{"repository":{"full_name":%q},"commit":{"hash":%q}}}`,
				pr.id, state, pr.id, pr.id, pr.repo, d))
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

// bbRefresh does what HandleWorkItemsFetch does for Bitbucket: every query's list, then one
// enrichment over all rows.
func bbRefresh(t *testing.T, s *secrets.Data, queries ...string) map[string]workItemOut {
	t.Helper()
	started := time.Now()
	var items []workItemOut
	for i, q := range queries {
		rows, err := bitbucketSearchWorkItems(s, fmt.Sprint("q", i), q)
		if err != nil {
			t.Fatalf("list %q: %v", q, err)
		}
		items = append(items, rows...)
	}
	bitbucketEnrichItems(s, items, started)
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

func TestBitbucketListNearLimitStartsNoReads(t *testing.T) {
	st := newBBStatusStub(t, web(1, 2)...)
	st.set(func() { st.listNear = true })
	rows := bbRefresh(t, bbTokenStore(), "acme/web")
	if len(st.snapshot()) != 0 || len(rows) != 2 {
		t.Fatalf("calls=%v rows=%d", st.snapshot(), len(rows))
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
