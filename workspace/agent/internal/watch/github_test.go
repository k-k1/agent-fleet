package watch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
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

	evs, cur, hint, err := g.Poll(context.Background(), "github:o/r#7", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// clean, but a non-required check failed: not ready.
	if got := types(evs); got != "pr.checks.completed,pr.checks.failed,pr.mergeable" {
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
	evs2, _, _, err := g.Poll(context.Background(), "github:o/r#7", cur, nil)
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
	evs3, _, _, _ := g.Poll(context.Background(), "github:o/r#7", cur, nil)
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
	evs, _, hint, err := g.Poll(context.Background(), "github:o/r#7", nil, nil)
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
	evs, _, hint, err := g.Poll(context.Background(), "github:o/r#7", nil, nil)
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
	_, _, _, err := g.Poll(context.Background(), "github:o/r#7", nil, nil)
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

func TestGitHubReadsEveryPageOfChecks(t *testing.T) {
	f := newFakeGitHub()
	f.bodies[pullPath] = openPull(boolp(true), "clean")
	var first, second []any
	for i := 1; i <= 150; i++ {
		run := map[string]any{"id": i, "name": "job" + strconv.Itoa(i), "status": "completed", "conclusion": "success", "completed_at": "t"}
		if i == 120 {
			run["conclusion"] = "failure"
		}
		if i <= 100 {
			first = append(first, run)
		} else {
			second = append(second, run)
		}
	}
	f.bodies[runsPath] = map[string]any{"total_count": 150, "check_runs": first}
	f.bodies["/repos/o/r/commits/abc1234def/check-runs?per_page=100&page=2"] = map[string]any{"total_count": 150, "check_runs": second}
	f.bodies[statusPath] = map[string]any{"total_count": 0, "statuses": []any{}}
	f.bodies[reviewsPath] = []any{}
	g := newTestGitHub(t, f)
	evs, cur, _, err := g.Poll(context.Background(), "github:o/r#7", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := types(evs); got != "pr.checks.completed,pr.checks.failed,pr.mergeable" {
		t.Fatalf("a failure on the second page was missed: %s", got)
	}
	if !strings.Contains(evs[0].Summary, "failed (1 of 150)") || evs[0].Data[0].Text != "job120" {
		t.Fatalf("summary %q data %+v", evs[0].Summary, evs[0].Data)
	}
	// Nothing changed: the second page is asked with its ETag too, and costs nothing.
	spent := g.spent
	evs2, _, _, err := g.Poll(context.Background(), "github:o/r#7", cur, nil)
	if err != nil || types(evs2) != types(evs) {
		t.Fatalf("second poll: %v %s", err, types(evs2))
	}
	if g.spent != spent || f.served["/repos/o/r/commits/abc1234def/check-runs?per_page=100&page=2"] != 1 {
		t.Fatalf("an unchanged later page was counted: spent %d -> %d, page 2 served %d times",
			spent, g.spent, f.served["/repos/o/r/commits/abc1234def/check-runs?per_page=100&page=2"])
	}
}

func TestGitHubReadyNeedsEveryCheckGreen(t *testing.T) {
	f := newFakeGitHub()
	f.bodies[pullPath] = openPull(boolp(true), "clean")
	f.bodies[runsPath] = map[string]any{"total_count": 1, "check_runs": []any{
		map[string]any{"id": 1, "name": "build", "status": "completed", "conclusion": "success", "completed_at": "t1"},
	}}
	f.bodies[statusPath] = map[string]any{"total_count": 1, "statuses": []any{map[string]any{"context": "ci", "state": "pending"}}}
	f.bodies[reviewsPath] = []any{}
	g := newTestGitHub(t, f)
	evs, cur, _, _ := g.Poll(context.Background(), "github:o/r#7", nil, nil)
	if got := types(evs); got != "pr.mergeable" {
		t.Fatalf("pending status: %s", got)
	}
	f.bodies[statusPath] = map[string]any{"total_count": 1, "statuses": []any{map[string]any{"context": "ci", "state": "success", "updated_at": "u"}}}
	evs, _, _, _ = g.Poll(context.Background(), "github:o/r#7", cur, nil)
	if got := types(evs); got != "pr.checks.completed,pr.mergeable,pr.ready" {
		t.Fatalf("all green: %s", got)
	}
}

func TestGitHubDismissedReviewLiftsARequest(t *testing.T) {
	id, who := changesRequested([]ghReview{
		{ID: 1, State: "CHANGES_REQUESTED", User: struct {
			Login string `json:"login"`
		}{"rev"}},
		{ID: 2, State: "DISMISSED", User: struct {
			Login string `json:"login"`
		}{"rev"}},
	})
	if id != 0 || len(who) != 0 {
		t.Fatalf("dismissed request still reported: %d %v", id, who)
	}
}

func TestGitHubForbiddenWithoutRateLimitIsAnError(t *testing.T) {
	f := newFakeGitHub()
	f.status[pullPath] = http.StatusForbidden
	g := newTestGitHub(t, f)
	_, _, _, err := g.Poll(context.Background(), "github:o/r#7", nil, nil)
	var rl *ErrRateLimited
	if err == nil || errors.As(err, &rl) {
		t.Fatalf("plain 403: %v", err)
	}
	f.headers.Set("Retry-After", "30")
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	if _, _, _, err := g.Poll(context.Background(), "github:o/r#7", nil, nil); !errors.As(err, &rl) || !rl.Until.Equal(now.Add(30*time.Second)) {
		t.Fatalf("secondary limit: %v", err)
	}
}

func TestGitHubRejectedTokenAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	t.Cleanup(srv.Close)
	g := NewGitHub(func() (string, error) { return "stale", nil }, func(string) (string, error) { return "stale", nil })
	g.API = srv.URL
	var ref *ErrRefused
	if _, err := g.Resolve(context.Background(), "o/r#7", ""); !errors.As(err, &ref) {
		t.Fatalf("no fresh token: %v, want a refusal", err)
	}
	g.Renew = func(string) (string, error) { return "", errors.New("store locked") }
	if _, err := g.Resolve(context.Background(), "o/r#7", ""); err == nil || !strings.Contains(err.Error(), "store locked") {
		t.Fatalf("renew failure lost its cause: %v", err)
	}
}

func TestParseGitHubRefEdges(t *testing.T) {
	for _, c := range []struct {
		ref, def, repo string
		n              int
	}{
		{"O/R#7", "", "o/r", 7},
		{"https://github.com/Owner/Repo/pull/9", "", "owner/repo", 9},
		{"#3", "a/b", "a/b", 3},
	} {
		repo, n, ok := ParseGitHubRef(c.ref, c.def)
		if !ok || repo != c.repo || n != c.n {
			t.Errorf("ParseGitHubRef(%q) = %q %d %v", c.ref, repo, n, ok)
		}
	}
	for _, bad := range []string{"o/r7", "../x#1", "o/..#1", "./r#1", "o/r#0", "o/r#7 x", "o/r##7"} {
		if _, _, ok := ParseGitHubRef(bad, "a/b"); ok {
			t.Errorf("ParseGitHubRef(%q) accepted", bad)
		}
	}
}

func TestSafeURLRefusesWhitespaceAndControls(t *testing.T) {
	for _, u := range []string{"https://x.test/a b", "https://x.test/a\nb", "https://x.test/\u0085", "http://x.test/", "https://u:p@x.test/"} {
		if got := SafeURL(u); got != "" {
			t.Errorf("SafeURL(%q) = %q", u, got)
		}
	}
	if SafeURL("https://x.test/run/1?a=b") == "" {
		t.Error("a plain https URL was refused")
	}
}

func TestGitHubThreadsOnlyWhenAskedAndOnlyOnARise(t *testing.T) {
	f := newFakeGitHub()
	f.bodies[pullPath] = openPull(boolp(true), "blocked")
	f.bodies[runsPath] = map[string]any{"total_count": 0, "check_runs": []any{}}
	f.bodies[statusPath] = map[string]any{"total_count": 0, "statuses": []any{}}
	f.bodies[reviewsPath] = []any{}
	g := newTestGitHub(t, f)
	if strings.Contains(strings.Join(g.Types(), ","), typeThreads) {
		t.Fatal("threads offered without a reader")
	}
	count, reads := 0, 0
	g.Threads = func(_ context.Context, repo string, n int) (int, error) {
		reads++
		if repo != "o/r" || n != 7 {
			t.Errorf("threads asked for %s#%d", repo, n)
		}
		return count, nil
	}
	if !strings.Contains(strings.Join(g.Types(), ","), typeThreads) {
		t.Fatal("threads not offered with a reader")
	}
	_, cur, _, _ := g.Poll(context.Background(), "github:o/r#7", nil, []string{"pr.merged"})
	if reads != 0 {
		t.Fatal("threads read though nobody asked for them")
	}
	versions := []string{}
	for _, c := range []int{1, 2, 1, 0, 1} {
		count = c
		var evs []Event
		evs, cur, _, _ = g.Poll(context.Background(), "github:o/r#7", cur, []string{"pr.review.*"})
		v := "-"
		for _, e := range evs {
			if e.Type == typeThreads {
				v = e.Version
			}
		}
		versions = append(versions, v)
	}
	// 1 and 2 are news; falling back to 1 is not; a new episode after zero is.
	if got := strings.Join(versions, ","); got != "0.1,0.2,0.2,-,1.1" {
		t.Fatalf("versions = %s", got)
	}
}

func TestGitHubThreadsFailureKeepsTheRestOfThePoll(t *testing.T) {
	f := newFakeGitHub()
	f.bodies[pullPath] = openPull(boolp(false), "dirty")
	f.bodies[runsPath] = map[string]any{"total_count": 0, "check_runs": []any{}}
	f.bodies[statusPath] = map[string]any{"total_count": 0, "statuses": []any{}}
	f.bodies[reviewsPath] = []any{}
	g := newTestGitHub(t, f)
	g.Threads = func(context.Context, string, int) (int, error) { return 0, errors.New("graphql down") }
	evs, cur, _, err := g.Poll(context.Background(), "github:o/r#7", nil, []string{"pr.*"})
	if err != nil || types(evs) != "pr.conflict" || len(cur) == 0 {
		t.Fatalf("a threads failure lost the REST answers: %v %s", err, types(evs))
	}
}

func TestGitHubPersistentThreadsFailureFailsTheRead(t *testing.T) {
	f := newFakeGitHub()
	f.bodies[pullPath] = openPull(boolp(true), "clean")
	f.bodies[runsPath] = map[string]any{"total_count": 0, "check_runs": []any{}}
	f.bodies[statusPath] = map[string]any{"total_count": 0, "statuses": []any{}}
	f.bodies[reviewsPath] = []any{}
	g := newTestGitHub(t, f)
	fail := true
	g.Threads = func(context.Context, string, int) (int, error) {
		if fail {
			return 0, errors.New("graphql down")
		}
		return 0, nil
	}
	var cur json.RawMessage
	for i := 1; i < ghThreadFailLimit; i++ {
		var err error
		if _, cur, _, err = g.Poll(context.Background(), "github:o/r#7", cur, []string{typeThreads}); err != nil {
			t.Fatalf("failure %d failed the read: %v", i, err)
		}
	}
	if _, _, _, err := g.Poll(context.Background(), "github:o/r#7", cur, []string{typeThreads}); err == nil {
		t.Fatal("a persistent threads failure stayed invisible")
	}
	// The failed read did not keep its cursor; one success resets the count.
	fail = false
	if _, cur, _, err := g.Poll(context.Background(), "github:o/r#7", cur, []string{typeThreads}); err != nil || strings.Contains(string(cur), "thread_fails") {
		t.Fatalf("a success did not reset: %v %s", err, cur)
	}
}
