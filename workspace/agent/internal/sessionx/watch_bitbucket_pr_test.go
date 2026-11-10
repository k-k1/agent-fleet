package sessionx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
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
