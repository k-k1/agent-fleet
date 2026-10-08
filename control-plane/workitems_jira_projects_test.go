package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func jiraProjectsEnv(t *testing.T, state, answer string, status int) (workItemsAPI, func(mid string) *resolved, *int32, *time.Time) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/work-items/jira-projects" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		atomic.AddInt32(&hits, 1)
		time.Sleep(60 * time.Millisecond) // long enough for concurrent callers to overlap
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	clock := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	jiraProjects = newJiraProjectsCache()
	jiraProjects.now = func() time.Time { return clock }
	t.Cleanup(func() { jiraProjects = newJiraProjectsCache() })
	res := func(mid string) *resolved {
		return &resolved{rt: stubRuntime{endpoint: srv.URL, token: "tok", state: state}, mv: store.MembershipView{MembershipID: mid}}
	}
	return workItemsAPI{}, res, &hits, &clock
}

func getJiraProjects(a workItemsAPI, res *resolved) string {
	w := httptest.NewRecorder()
	a.jiraProjectsList(w, httptest.NewRequest("GET", "/api/work-items/jira-projects", nil), res)
	return strings.TrimSpace(w.Body.String())
}

const projAnswer = `{"connected":true,"keys":["ABC","XYZ"],"truncated":false}`

func TestJiraProjectsFreshEntryIsServedWithoutTheAgent(t *testing.T) {
	a, res, hits, clock := jiraProjectsEnv(t, "running", projAnswer, 200)
	if got := getJiraProjects(a, res("m1")); got != projAnswer {
		t.Fatalf("got %s", got)
	}
	*clock = clock.Add(jiraProjectsFresh - time.Minute)
	getJiraProjects(a, res("m1"))
	if *hits != 1 {
		t.Fatalf("agent reads = %d, want 1 inside the fresh window", *hits)
	}
	*clock = clock.Add(2 * time.Minute)
	getJiraProjects(a, res("m1"))
	if *hits != 2 {
		t.Fatalf("agent reads = %d, want a re-read past the fresh window", *hits)
	}
}

func TestJiraProjectsAreScopedPerMembership(t *testing.T) {
	a, res, _, _ := jiraProjectsEnv(t, "running", projAnswer, 200)
	getJiraProjects(a, res("m1"))
	// Another member whose workspace is stopped must not be handed m1's projects.
	_, res2, _, _ := jiraProjectsEnv(t, "stopped", projAnswer, 200)
	jiraProjects.put("m1", jiraProjectsWire{Connected: true, Keys: []string{"ABC"}})
	if got := getJiraProjects(a, res2("m2")); got != `{"connected":false,"keys":[],"truncated":false}` {
		t.Fatalf("m2 got %s", got)
	}
}

func TestJiraProjectsStoppedServesStaleButNeverStartsOrReads(t *testing.T) {
	a, res, hits, clock := jiraProjectsEnv(t, "running", projAnswer, 200)
	getJiraProjects(a, res("m1"))
	stopped := res("m1")
	stopped.rt = stubRuntime{endpoint: stopped.rt.Endpoint(), token: "tok", state: "stopped"}

	*clock = clock.Add(3 * time.Hour)
	if got := getJiraProjects(a, stopped); got != projAnswer {
		t.Fatalf("stale entry not served: %s", got)
	}
	*clock = clock.Add(jiraProjectsStale)
	if got := getJiraProjects(a, stopped); got != `{"connected":false,"keys":[],"truncated":false}` {
		t.Fatalf("too-old entry served: %s", got)
	}
	if *hits != 1 {
		t.Fatalf("a stopped workspace was read (%d reads)", *hits)
	}
}

func TestJiraProjectsAgentFailureKeepsTheStaleEntryOrAnswersEmpty(t *testing.T) {
	a, res, _, clock := jiraProjectsEnv(t, "running", `{"error":{"code":"provider_error"}}`, 502)
	if got := getJiraProjects(a, res("m1")); got != `{"connected":false,"keys":[],"truncated":false}` {
		t.Fatalf("got %s", got)
	}
	jiraProjects.put("m1", jiraProjectsWire{Connected: true, Keys: []string{"OLD"}})
	*clock = clock.Add(2 * time.Hour)
	if got := getJiraProjects(a, res("m1")); !strings.Contains(got, `"OLD"`) {
		t.Fatalf("stale entry dropped on an Agent failure: %s", got)
	}
}

func TestJiraProjectsCacheIsBounded(t *testing.T) {
	c := newJiraProjectsCache()
	for i := 0; i < jiraProjectsMax+50; i++ {
		c.put(string(rune('a'+i%26))+strings.Repeat("x", i), jiraProjectsWire{})
	}
	if len(c.m) > jiraProjectsMax {
		t.Fatalf("cache holds %d entries, cap %d", len(c.m), jiraProjectsMax)
	}
}

func concurrently(n int, f func()) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	wg.Wait()
}

func TestJiraProjectsConcurrentColdReadsShareOneAgentRead(t *testing.T) {
	a, res, hits, _ := jiraProjectsEnv(t, "running", projAnswer, 200)
	got := make([]string, 8)
	var mu sync.Mutex
	i := 0
	concurrently(8, func() {
		out := getJiraProjects(a, res("m1"))
		mu.Lock()
		got[i] = out
		i++
		mu.Unlock()
	})
	if *hits != 1 {
		t.Fatalf("agent reads = %d, want 1 for 8 concurrent cold requests", *hits)
	}
	for _, g := range got {
		if g != projAnswer {
			t.Fatalf("a waiter got %s", g)
		}
	}
	// Another membership is independent.
	getJiraProjects(a, res("m2"))
	if *hits != 2 {
		t.Fatalf("agent reads = %d, want 2 after a second membership", *hits)
	}
}

func TestJiraProjectsConcurrentStaleReadsShareOneAgentRead(t *testing.T) {
	a, res, hits, clock := jiraProjectsEnv(t, "running", projAnswer, 200)
	getJiraProjects(a, res("m1"))
	*clock = clock.Add(2 * time.Hour)
	concurrently(8, func() { getJiraProjects(a, res("m1")) })
	if *hits != 2 {
		t.Fatalf("agent reads = %d, want 1 initial + 1 shared re-read", *hits)
	}
}

func TestJiraProjectsFailureBacksOffPerMembership(t *testing.T) {
	a, res, hits, clock := jiraProjectsEnv(t, "running", `{"error":{"code":"provider_error"}}`, 502)
	getJiraProjects(a, res("m1"))
	getJiraProjects(a, res("m1"))
	concurrently(4, func() { getJiraProjects(a, res("m1")) })
	if *hits != 1 {
		t.Fatalf("agent reads = %d, want 1 inside the backoff", *hits)
	}
	getJiraProjects(a, res("m2")) // someone else's failure is not mine
	if *hits != 2 {
		t.Fatalf("agent reads = %d, want 2 (m2 independent)", *hits)
	}
	*clock = clock.Add(jiraProjectsBackoff + time.Second)
	getJiraProjects(a, res("m1"))
	if *hits != 3 {
		t.Fatalf("agent reads = %d, want a retry after the backoff", *hits)
	}
}

func TestJiraProjectsRefuseAnOversizedOrMalformedAgentAnswer(t *testing.T) {
	var keys []string
	for i := 0; i <= jiraProjectsKeyCap; i++ {
		keys = append(keys, fmt.Sprintf(`"K%03d"`, i))
	}
	big := `{"connected":true,"keys":[` + strings.Join(keys, ",") + `],"truncated":false}`
	for name, answer := range map[string]string{"oversized": big, "malformed": `{"connected":true,"keys":["ok","x y"]}`} {
		a, res, _, _ := jiraProjectsEnv(t, "running", answer, 200)
		if got := getJiraProjects(a, res("m1")); got != `{"connected":false,"keys":[],"truncated":false}` {
			t.Errorf("%s answer was kept: %.80s", name, got)
		}
		if _, ok := jiraProjects.get("m1"); ok {
			t.Errorf("%s answer was cached", name)
		}
	}
}

// gateRuntime holds the first State call until released, so the order "A misses the cache, B
// completes a whole read, A continues" is decided by channels, not by timing.
type gateRuntime struct {
	stubRuntime
	entered chan struct{}
	release chan struct{}
	first   int32
}

func (g *gateRuntime) State(ctx context.Context) string {
	if atomic.AddInt32(&g.first, 1) == 1 {
		close(g.entered)
		<-g.release
	}
	return g.stubRuntime.State(ctx)
}

func TestJiraProjectsAReadFinishedWhileAnotherCallerWasInStateIsNotRepeated(t *testing.T) {
	a, res, hits, _ := jiraProjectsEnv(t, "running", projAnswer, 200)
	base := res("m1")
	g := &gateRuntime{stubRuntime: base.rt.(stubRuntime), entered: make(chan struct{}), release: make(chan struct{})}
	ra := &resolved{rt: g, mv: base.mv}

	done := make(chan string)
	go func() { done <- getJiraProjects(a, ra) }() // A: cache miss, parked inside State
	<-g.entered
	if got := getJiraProjects(a, res("m1")); got != projAnswer { // B: the whole read
		t.Fatalf("B got %s", got)
	}
	close(g.release)
	if got := <-done; got != projAnswer {
		t.Fatalf("A got %s", got)
	}
	if *hits != 1 {
		t.Fatalf("agent reads = %d, want 1", *hits)
	}
}
