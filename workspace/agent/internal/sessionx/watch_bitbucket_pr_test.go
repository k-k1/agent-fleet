package sessionx

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

// watchBitbucketPR is the seam the Bitbucket watch source relies on: only a definite "no such
// pull request" (404) may come back as (nil, nil), which ends the watch; every failed read must
// be an error, or a transient outage would end a healthy watch after three reads.
func TestWatchBitbucketPRSeparatesGoneFromFailed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		wantNil bool // (nil, nil): the PR is gone
	}{
		{"404 is gone", http.StatusNotFound, true},
		{"500 is a failed read", http.StatusInternalServerError, false},
		{"429 is a failed read", http.StatusTooManyRequests, false},
		{"403 is a failed read", http.StatusForbidden, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			c := branchpr.NewBitbucket(func() (string, error) { return "Bearer t", nil })
			c.Endpoint = srv.URL
			old := sessionBBPRs
			sessionBBPRs = c
			defer func() { sessionBBPRs = old }()
			pr, err := watchBitbucketPR(context.Background(), "w/r", 4)
			if tc.wantNil && (pr != nil || err != nil) {
				t.Fatalf("got (%v, %v), want (nil, nil)", pr, err)
			}
			if !tc.wantNil && (pr != nil || err == nil) {
				t.Fatalf("got (%v, %v), want an error", pr, err)
			}
		})
	}
}

// A cached "open" answer is fresh for minutes; a read a poke brought forward must still reach
// Bitbucket, or the declined PR is reported only when the entry expires.
func TestWatchBitbucketPRAfterAPokeSkipsTheCachedAnswer(t *testing.T) {
	var state atomic.Value
	state.Store("OPEN")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprintf(w, `{"id":4,"title":"t","state":%q,"links":{"html":{"href":"https://bitbucket.org/w/r/pull-requests/4"}}}`, state.Load())
	}))
	defer srv.Close()
	c := branchpr.NewBitbucket(func() (string, error) { return "Bearer t", nil })
	c.Endpoint = srv.URL
	old := sessionBBPRs
	sessionBBPRs = c
	defer func() { sessionBBPRs = old }()

	pr, err := watchBitbucketPR(context.Background(), "w/r", 4)
	if err != nil || pr == nil || pr.State != "open" {
		t.Fatalf("first read = %+v, %v", pr, err)
	}
	state.Store("DECLINED")
	if pr, _ = watchBitbucketPR(context.Background(), "w/r", 4); pr == nil || pr.State != "open" || calls.Load() != 1 {
		t.Fatalf("a scheduled read should still use the cache: %+v calls=%d", pr, calls.Load())
	}
	time.Sleep(2 * time.Millisecond)
	ctx := watch.WithPokedSince(context.Background(), time.Now())
	pr, err = watchBitbucketPR(ctx, "w/r", 4)
	if err != nil || pr == nil || pr.State == "open" || calls.Load() != 2 {
		t.Fatalf("poked read = %+v, %v, calls=%d: answered from the cache", pr, err, calls.Load())
	}
}
