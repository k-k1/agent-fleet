package watch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub serves the four endpoints the source reads, with ETags, and counts what it served.
type fakeGitHub struct {
	mu      sync.Mutex
	bodies  map[string]any // path (with query) -> JSON body
	status  map[string]int
	headers http.Header
	served  map[string]int // path -> 200s
	n304    int
	tokens  []string
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{bodies: map[string]any{}, status: map[string]int{}, served: map[string]int{}, headers: http.Header{}}
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, r.Header.Get("Authorization"))
	key := r.URL.RequestURI()
	for k, v := range f.headers {
		w.Header()[k] = v
	}
	if st, ok := f.status[key]; ok {
		w.WriteHeader(st)
		return
	}
	body, ok := f.bodies[key]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	b, _ := json.Marshal(body)
	etag := `"` + shortHash(b) + `"`
	if r.Header.Get("If-None-Match") == etag {
		f.n304++
		w.WriteHeader(http.StatusNotModified)
		return
	}
	f.served[key]++
	w.Header().Set("ETag", etag)
	_, _ = w.Write(b)
}

func shortHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

const (
	pullPath    = "/repos/o/r/pulls/7"
	runsPath    = "/repos/o/r/commits/abc1234def/check-runs?per_page=100"
	statusPath  = "/repos/o/r/commits/abc1234def/status?per_page=100"
	reviewsPath = "/repos/o/r/pulls/7/reviews?per_page=100"
)

func openPull(mergeable *bool, state string) map[string]any {
	return map[string]any{
		"state": "open", "merged": false, "draft": false, "mergeable": mergeable, "mergeable_state": state,
		"html_url": "https://github.com/o/r/pull/7", "head": map[string]any{"sha": "abc1234def"}, "base": map[string]any{"sha": "base1"},
	}
}

func boolp(b bool) *bool { return &b }

func newTestGitHub(t *testing.T, f *fakeGitHub) *GitHub {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	g := NewGitHub(func() (string, error) { return "tok", nil }, nil)
	g.API = srv.URL
	return g
}

func types(evs []Event) string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return strings.Join(out, ",")
}

func TestGitHubPollReportsChecksAndUses304(t *testing.T) {
	f := newFakeGitHub()
	f.bodies[pullPath] = openPull(boolp(true), "clean")
	f.bodies[runsPath] = map[string]any{"total_count": 2, "check_runs": []any{
		map[string]any{"id": 1, "name": "build", "status": "completed", "conclusion": "success", "completed_at": "t1"},
		map[string]any{"id": 2, "name": "lint\nfoo", "status": "completed", "conclusion": "failure", "completed_at": "t2", "html_url": "https://github.com/o/r/runs/2"},
	}}
	f.bodies[statusPath] = map[string]any{"statuses": []any{}}
	f.bodies[reviewsPath] = []any{}
	g := newTestGitHub(t, f)

	evs, cur, hint, err := g.Poll(context.Background(), "github:o/r#7", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := types(evs); got != "pr.checks.completed,pr.checks.failed,pr.mergeable,pr.ready" {
		t.Fatalf("events = %s", got)
	}
	if !strings.Contains(evs[0].Summary, "failed (1 of 2)") || len(evs[0].Data) != 1 || evs[0].Data[0].Text != "lint\nfoo" {
		t.Fatalf("checks event = %+v", evs[0])
	}
	if hint.After != ghOpenAfter {
		t.Fatalf("hint = %v", hint)
	}
	v1 := evs[0].Version

	// Nothing changed: every endpoint answers 304, the events are the same.
	evs2, _, _, err := g.Poll(context.Background(), "github:o/r#7", cur)
	if err != nil || f.n304 != 4 || types(evs2) != types(evs) || evs2[0].Version != v1 {
		t.Fatalf("second poll: err=%v 304s=%d events=%s", err, f.n304, types(evs2))
	}
	if g.spent != 4 {
		t.Fatalf("304s were counted against the budget: spent=%d", g.spent)
	}

	// A re-run on the same commit is a new version.
	f.bodies[runsPath] = map[string]any{"total_count": 2, "check_runs": []any{
		map[string]any{"id": 1, "name": "build", "status": "completed", "conclusion": "success", "completed_at": "t1"},
		map[string]any{"id": 3, "name": "lint", "status": "completed", "conclusion": "success", "completed_at": "t3"},
	}}
	evs3, _, _, _ := g.Poll(context.Background(), "github:o/r#7", cur)
	if types(evs3) != "pr.checks.completed,pr.mergeable,pr.ready" || evs3[0].Version == v1 {
		t.Fatalf("re-run: %s %v", types(evs3), evs3)
	}
}

func TestGitHubPollRunningChecksConflictAndReviews(t *testing.T) {
	f := newFakeGitHub()
	f.bodies[pullPath] = openPull(boolp(false), "dirty")
	f.bodies[runsPath] = map[string]any{"total_count": 1, "check_runs": []any{
		map[string]any{"id": 1, "name": "build", "status": "in_progress"},
	}}
	f.bodies[statusPath] = map[string]any{"statuses": []any{}}
	f.bodies[reviewsPath] = []any{
		map[string]any{"id": 10, "state": "CHANGES_REQUESTED", "user": map[string]any{"login": "rev"}},
		map[string]any{"id": 11, "state": "COMMENTED", "user": map[string]any{"login": "rev"}},
		map[string]any{"id": 12, "state": "APPROVED", "user": map[string]any{"login": "other"}},
	}
	g := newTestGitHub(t, f)
	evs, _, hint, err := g.Poll(context.Background(), "github:o/r#7", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := types(evs); got != "pr.conflict,pr.review.changes_requested" {
		t.Fatalf("events = %s", got)
	}
	if evs[0].Version != "abc1234def:base1" || evs[1].Version != "10" || evs[1].Data[0].Text != "rev" {
		t.Fatalf("versions = %+v", evs)
	}
	if hint.After != ghBusyAfter {
		t.Fatalf("running checks should poll fast, hint %v", hint)
	}
}

func TestGitHubPollMergedIsSettled(t *testing.T) {
	f := newFakeGitHub()
	f.bodies[pullPath] = map[string]any{"state": "closed", "merged": true, "head": map[string]any{"sha": "abc1234def"}}
	g := newTestGitHub(t, f)
	evs, _, hint, err := g.Poll(context.Background(), "github:o/r#7", nil)
	if err != nil || types(evs) != "pr.merged" || !hint.Settled {
		t.Fatalf("merged: %v %s %+v", err, types(evs), hint)
	}
	if f.served[runsPath] != 0 {
		t.Fatal("a merged PR's checks were read")
	}
}

func TestGitHubResolve(t *testing.T) {
	f := newFakeGitHub()
	f.bodies[pullPath] = openPull(nil, "unknown")
	g := newTestGitHub(t, f)
	ctx := context.Background()
	for _, ref := range []string{"o/r#7", "#7", "7", "https://github.com/o/r/pull/7/files"} {
		res, err := g.Resolve(ctx, ref, "o/r")
		if err != nil || res.Subject != "github:o/r#7" || res.URL != "https://github.com/o/r/pull/7" {
			t.Errorf("Resolve(%q) = %+v, %v", ref, res, err)
		}
	}
	var ref *ErrRefused
	for _, bad := range []string{"o/r#8", "#7", "o/r", "../x#1", "o/r#7 reply=x"} {
		def := "o/r"
		if bad == "#7" {
			def = ""
		}
		if _, err := g.Resolve(ctx, bad, def); !errors.As(err, &ref) {
			t.Errorf("Resolve(%q) = %v, want a refusal", bad, err)
		}
	}
}

func TestGitHubRateLimitAndBudget(t *testing.T) {
	f := newFakeGitHub()
	f.status[pullPath] = http.StatusForbidden
	f.headers.Set("X-RateLimit-Remaining", "0")
	f.headers.Set("X-RateLimit-Reset", "2000000000")
	g := newTestGitHub(t, f)
	_, _, _, err := g.Poll(context.Background(), "github:o/r#7", nil)
	var rl *ErrRateLimited
	if !errors.As(err, &rl) || rl.Until.Unix() != 2000000000 {
		t.Fatalf("primary limit: %v", err)
	}

	f2 := newFakeGitHub()
	f2.bodies[pullPath] = openPull(boolp(true), "clean")
	f2.headers.Set("X-RateLimit-Limit", "40") // 10% = 4 counted requests an hour
	g2 := newTestGitHub(t, f2)
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	g2.now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		_, _, _ = g2.get(context.Background(), pullPath, "", &ghPull{})
	}
	if _, _, err := g2.get(context.Background(), pullPath, "", &ghPull{}); !errors.As(err, &rl) || !rl.Until.Equal(now.Add(time.Hour)) {
		t.Fatalf("budget: %v", err)
	}
	now = now.Add(61 * time.Minute)
	if _, _, err := g2.get(context.Background(), pullPath, "", &ghPull{}); err != nil {
		t.Fatalf("a new hour should reset the budget: %v", err)
	}
}

func TestGitHubRenewsRejectedTokenOnce(t *testing.T) {
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"state":"open","html_url":"https://github.com/o/r/pull/7"}`))
	}))
	t.Cleanup(srv.Close)
	renewed := 0
	g := NewGitHub(func() (string, error) { return "stale", nil }, func(rejected string) (string, error) {
		renewed++
		if rejected != "stale" {
			t.Errorf("renew got %q", rejected)
		}
		return "fresh", nil
	})
	g.API = srv.URL
	if _, err := g.Resolve(context.Background(), "o/r#7", ""); err != nil || renewed != 1 {
		t.Fatalf("resolve: %v renewed=%d", err, renewed)
	}
}

func TestGitHubNotConnectedIsARefusal(t *testing.T) {
	g := NewGitHub(func() (string, error) { return "", nil }, nil)
	var ref *ErrRefused
	if _, err := g.Resolve(context.Background(), "o/r#7", ""); !errors.As(err, &ref) {
		t.Fatalf("not connected: %v", err)
	}
}
