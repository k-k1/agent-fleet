package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	clock := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	jiraProjects = &jiraProjectsCache{m: map[string]jiraProjectsEntry{}, now: func() time.Time { return clock }}
	t.Cleanup(func() { jiraProjects = &jiraProjectsCache{m: map[string]jiraProjectsEntry{}, now: time.Now} })
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
	c := &jiraProjectsCache{m: map[string]jiraProjectsEntry{}, now: time.Now}
	for i := 0; i < jiraProjectsMax+50; i++ {
		c.put(string(rune('a'+i%26))+strings.Repeat("x", i), jiraProjectsWire{})
	}
	if len(c.m) > jiraProjectsMax {
		t.Fatalf("cache holds %d entries, cap %d", len(c.m), jiraProjectsMax)
	}
}
