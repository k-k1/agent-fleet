package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func TestJiraJQLStringEscapes(t *testing.T) {
	got := jiraJQLString("a \"quoted\" back\\slash\nnew")
	if want := `a \"quoted\" back\\slash\nnew`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	q := jiraFindJQL("KEY", `x" OR project = "Z`, true)
	if !strings.Contains(q, `summary ~ "x\" OR project = \"Z"`) {
		t.Errorf("quote not escaped: %s", q)
	}
	if !strings.Contains(q, "created >= -15m") || !strings.Contains(q, "reporter = currentUser()") {
		t.Errorf("window or reporter missing: %s", q)
	}
	if strings.Contains(jiraFindJQL("KEY", "s", false), "summary") {
		t.Error("fallback JQL still has the summary clause")
	}
}

func TestJiraFindMatchesIsExact(t *testing.T) {
	body := []byte(`{"issues":[
 {"key":"K-3","fields":{"summary":"Fix login"}},
 {"key":"K-2","fields":{"summary":"Fix login page"}},
 {"key":"K-1","fields":{"summary":"fix login"}},
 {"key":"K-0","fields":{"summary":" Fix login "}}]}`)
	got, err := jiraFindMatches(body, "https://acme.atlassian.net/", "Fix login")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"K-3 https://acme.atlassian.net/browse/K-3", "K-0 https://acme.atlassian.net/browse/K-0"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, _ := jiraFindMatches([]byte(`{"issues":[]}`), "https://x", "s"); len(got) != 0 {
		t.Errorf("empty result gave %v", got)
	}
	if _, err := jiraFindMatches([]byte(`nope`), "https://x", "s"); err == nil {
		t.Error("bad JSON accepted")
	}
}

func seedFindStore(t *testing.T, site string) {
	t.Helper()
	seedJiraStore(t, func(s *secrets.Data) {
		s.Jira = &secrets.JiraCreds{Site: site, Email: "u@example.com", Token: "fake-api-token"}
	})
}

func TestJiraFindCreatedRunPrintsCandidates(t *testing.T) {
	var mu sync.Mutex
	var jqls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		jqls = append(jqls, r.URL.Query().Get("jql"))
		mu.Unlock()
		if r.URL.Path != "/rest/api/3/search/jql" || r.Method != "GET" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"issues":[{"key":"AB-7","fields":{"summary":"say \"hi\""}},{"key":"AB-6","fields":{"summary":"say \"hi\" twice"}}]}`))
	}))
	defer srv.Close()
	seedFindStore(t, srv.URL)
	var out bytes.Buffer
	if err := jiraFindCreatedRun(&out, "AB", `say "hi"`); err != nil {
		t.Fatal(err)
	}
	if want := "AB-7 " + srv.URL + "/browse/AB-7\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if len(jqls) != 1 || !strings.Contains(jqls[0], `project = "AB"`) || !strings.Contains(jqls[0], `summary ~ "say \"hi\""`) || !strings.Contains(jqls[0], "created >= -15m") {
		t.Errorf("jql = %v", jqls)
	}
}

func TestJiraFindCreatedRunEmptyResultIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"issues":[]}`))
	}))
	defer srv.Close()
	seedFindStore(t, srv.URL)
	var out bytes.Buffer
	if err := jiraFindCreatedRun(&out, "AB", "nothing"); err != nil || out.Len() != 0 {
		t.Errorf("err=%v out=%q", err, out.String())
	}
}

func TestJiraFindCreatedFallsBackOnGoneAndOnBadJQL(t *testing.T) {
	var paths, jqls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		jqls = append(jqls, r.URL.Query().Get("jql"))
		switch {
		case r.URL.Path == "/rest/api/3/search/jql":
			w.WriteHeader(http.StatusGone)
		case strings.Contains(r.URL.Query().Get("jql"), "summary ~"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errorMessages":["bad"]}`))
		default:
			_, _ = w.Write([]byte(`{"issues":[{"key":"AB-1","fields":{"summary":"a+b"}}]}`))
		}
	}))
	defer srv.Close()
	seedFindStore(t, srv.URL)
	var out bytes.Buffer
	if err := jiraFindCreatedRun(&out, "AB", "a+b"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "AB-1 ") {
		t.Errorf("stdout = %q (paths %v)", out.String(), paths)
	}
	if last := jqls[len(jqls)-1]; strings.Contains(last, "summary ~") {
		t.Errorf("last query still fuzzy: %s", last)
	}
}

// An error carries no token, from the server body or the credentials.
func TestJiraFindCreatedErrorsCarryNoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`fake-api-token`))
	}))
	defer srv.Close()
	seedFindStore(t, srv.URL)
	var out bytes.Buffer
	err := jiraFindCreatedRun(&out, "AB", "s")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "fake-api-token") || strings.Contains(out.String(), "fake-api-token") {
		t.Errorf("token leaked: %v %q", err, out.String())
	}
}

func TestJiraFindCreatedRunRejectsBadInput(t *testing.T) {
	var out bytes.Buffer
	if jiraFindCreatedRun(&out, "A B", "s") == nil || jiraFindCreatedRun(&out, "AB", "  ") == nil {
		t.Error("bad key or empty summary accepted")
	}
	seedJiraStore(t, func(s *secrets.Data) {})
	if jiraFindCreatedRun(&out, "AB", "s") == nil {
		t.Error("no connection accepted")
	}
}

func TestJiraFindCreatedHonoursTheDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	seedFindStore(t, srv.URL)
	old := jiraFindDeadline
	jiraFindDeadline = 300 * time.Millisecond
	defer func() { jiraFindDeadline = old }()
	start := time.Now()
	err := jiraFindCreatedRun(&bytes.Buffer{}, "AB", "s")
	if err == nil || time.Since(start) > 3*time.Second {
		t.Errorf("err=%v after %s", err, time.Since(start))
	}
}
