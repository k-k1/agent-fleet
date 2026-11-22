package workitems

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// bbStatusStub answers a PR list plus per-PR statuses and mergeability, and records every call.
type bbStatusStub struct {
	mu       sync.Mutex
	calls    []string
	dst      map[int]string // PR id -> destination hash
	mergeSt  int            // status for mergeability (0 = 200)
	nearAt   string         // path suffix whose answer carries X-RateLimit-NearLimit: true
	tooMany  string         // path suffix answered 429
	nPRs     int
	statuses string
}

func newBBStatusStub(t *testing.T, n int) *bbStatusStub {
	t.Helper()
	st := &bbStatusStub{nPRs: n, dst: map[int]string{}, statuses: `{"values":[{"state":"SUCCESSFUL"}]}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		defer st.mu.Unlock()
		p := r.URL.Path
		if strings.HasSuffix(p, "/pullrequests") {
			var vals []string
			for i := 1; i <= st.nPRs; i++ {
				d := st.dst[i]
				if d == "" {
					d = "d0"
				}
				vals = append(vals, fmt.Sprintf(`{"id":%d,"title":"t","state":"OPEN","updated_on":"2026-08-%02dT07:00:00+00:00",`+
					`"source":{"commit":{"hash":"s%d"}},"destination":{"repository":{"full_name":"acme/web"},"commit":{"hash":"%s"}}}`, i, i, i, d))
			}
			_, _ = w.Write([]byte(`{"values":[` + strings.Join(vals, ",") + `]}`))
			return
		}
		st.calls = append(st.calls, p)
		if st.tooMany != "" && strings.HasSuffix(p, st.tooMany) {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if st.nearAt != "" && strings.HasSuffix(p, st.nearAt) {
			w.Header().Set("X-RateLimit-NearLimit", "true")
		}
		if strings.HasSuffix(p, "/statuses") {
			_, _ = w.Write([]byte(st.statuses))
			return
		}
		if st.mergeSt != 0 {
			w.WriteHeader(st.mergeSt)
			return
		}
		_, _ = w.Write([]byte(`{"values":[{"type":"git_mergeability_check","status":"PASSED","reason":"clean"}]}`))
	}))
	prev := BitbucketAPIBase
	BitbucketAPIBase = srv.URL
	bitbucketStatusCache.Lock()
	bitbucketStatusCache.m = map[string]bitbucketStatusEntry{}
	bitbucketStatusCache.Unlock()
	t.Cleanup(func() { BitbucketAPIBase = prev; srv.Close() })
	return st
}

func (st *bbStatusStub) count() int { st.mu.Lock(); defer st.mu.Unlock(); return len(st.calls) }
func (st *bbStatusStub) reset()     { st.mu.Lock(); st.calls = nil; st.mu.Unlock() }

func TestBitbucketRowsGetChecksAndMergeable(t *testing.T) {
	st := newBBStatusStub(t, 1)
	rows, err := bitbucketSearchWorkItems(bbTokenStore(), "q", "acme/web")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Checks.State != "success" || rows[0].Mergeable != "clean" {
		t.Fatalf("row = %+v", rows[0])
	}
	if st.count() != 2 {
		t.Fatalf("calls = %v", st.calls)
	}
}

func TestBitbucketUnchangedHashesCauseNoCalls(t *testing.T) {
	st := newBBStatusStub(t, 3)
	s := bbTokenStore()
	if _, err := bitbucketSearchWorkItems(s, "q", "acme/web"); err != nil {
		t.Fatal(err)
	}
	st.reset()
	rows, _ := bitbucketSearchWorkItems(s, "q", "acme/web")
	if st.count() != 0 {
		t.Fatalf("calls = %v", st.calls)
	}
	if rows[0].Checks.State != "success" || rows[0].Mergeable != "clean" {
		t.Fatalf("cached answer not applied: %+v", rows[0])
	}
}

func TestBitbucketChangedDestinationRereadsMergeabilityOnly(t *testing.T) {
	st := newBBStatusStub(t, 1)
	s := bbTokenStore()
	_, _ = bitbucketSearchWorkItems(s, "q", "acme/web")
	st.reset()
	st.mu.Lock()
	st.dst[1] = "d1"
	st.mu.Unlock()
	rows, _ := bitbucketSearchWorkItems(s, "q", "acme/web")
	if st.count() != 1 || !strings.HasSuffix(st.calls[0], "/mergeability/checks") {
		t.Fatalf("calls = %v", st.calls)
	}
	if rows[0].Checks.State != "success" {
		t.Fatalf("checks lost: %+v", rows[0])
	}
}

func TestBitbucketBudgetCapsPullRequestsPerRefresh(t *testing.T) {
	st := newBBStatusStub(t, 14)
	s := bbTokenStore()
	rows, _ := bitbucketSearchWorkItems(s, "q", "acme/web")
	if st.count() != 20 {
		t.Fatalf("want 10 PRs x 2 calls, got %d", st.count())
	}
	read := 0
	for _, r := range rows {
		if r.Mergeable != "" {
			read++
		}
	}
	if read != 10 {
		t.Fatalf("read rows = %d", read)
	}
	// The most recently updated (ids 14..5) were read; the next refresh reaches the rest.
	for _, r := range rows {
		if (r.Key == "acme/web#14") != (r.Mergeable != "") && r.Key == "acme/web#14" {
			t.Fatalf("newest row unread")
		}
	}
	st.reset()
	rows, _ = bitbucketSearchWorkItems(s, "q", "acme/web")
	if st.count() != 8 {
		t.Fatalf("second refresh calls = %d", st.count())
	}
	for _, r := range rows {
		if r.Mergeable != "clean" {
			t.Fatalf("row still unread: %+v", r)
		}
	}
}

func TestBitbucketStopsOnTooManyRequests(t *testing.T) {
	st := newBBStatusStub(t, 8)
	st.tooMany = "/statuses"
	rows, err := bitbucketSearchWorkItems(bbTokenStore(), "q", "acme/web")
	if err != nil || len(rows) != 8 {
		t.Fatalf("list lost: %v %d", err, len(rows))
	}
	// At most the three in-flight reads can have started before the first 429 lands.
	if st.count() > bitbucketStatusConcurrency {
		t.Fatalf("calls after 429 = %d", st.count())
	}
	for _, r := range rows {
		if r.Mergeable != "" || r.Checks.State != "" {
			t.Fatalf("row drew status after 429: %+v", r)
		}
	}
}

func TestBitbucketStopsOnNearLimit(t *testing.T) {
	st := newBBStatusStub(t, 8)
	st.nearAt = "/statuses"
	rows, _ := bitbucketSearchWorkItems(bbTokenStore(), "q", "acme/web")
	if st.count() > bitbucketStatusConcurrency {
		t.Fatalf("calls after near-limit = %d", st.count())
	}
	for _, r := range rows {
		if r.Mergeable != "" {
			t.Fatalf("mergeability read after near-limit: %+v", r)
		}
	}
}

func TestBitbucketFailedReadLeavesRowUnread(t *testing.T) {
	st := newBBStatusStub(t, 1)
	st.mergeSt = http.StatusInternalServerError
	s := bbTokenStore()
	rows, err := bitbucketSearchWorkItems(s, "q", "acme/web")
	if err != nil || rows[0].Mergeable != "" {
		t.Fatalf("err=%v row=%+v", err, rows[0])
	}
	// Not cached: the next refresh tries the mergeability again, and does not repeat the statuses.
	st.reset()
	st.mu.Lock()
	st.mergeSt = 0
	st.mu.Unlock()
	rows, _ = bitbucketSearchWorkItems(s, "q", "acme/web")
	if st.count() != 1 || rows[0].Mergeable != "clean" {
		t.Fatalf("calls=%v row=%+v", st.calls, rows[0])
	}
}
