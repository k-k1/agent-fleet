package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func jiraProjectsServer(t *testing.T, total int, hits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		if r.URL.Path != "/rest/api/3/project/search" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
		max, _ := strconv.Atoi(r.URL.Query().Get("maxResults"))
		var vals []string
		for i := start; i < start+max && i < total; i++ {
			// name/lead are present upstream and must not travel on.
			vals = append(vals, fmt.Sprintf(`{"key":"P%03d","name":"NAME_SENTINEL","lead":{"displayName":"LEAD_SENTINEL"}}`, i))
		}
		fmt.Fprintf(w, `{"values":[%s],"isLast":%v}`, strings.Join(vals, ","), start+max >= total)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestJiraProjectKeysCarriesKeysOnly(t *testing.T) {
	hits := 0
	srv := jiraProjectsServer(t, 3, &hits)
	c := &secrets.JiraCreds{Site: srv.URL, Email: "a@example.com", Token: "t"}
	keys, truncated, err := jiraProjectKeys(c)
	if err != nil || truncated || strings.Join(keys, ",") != "P000,P001,P002" {
		t.Fatalf("keys=%v truncated=%v err=%v", keys, truncated, err)
	}
	b, _ := json.Marshal(jiraProjectsOut{Connected: true, Keys: keys})
	if strings.Contains(string(b), "SENTINEL") {
		t.Fatalf("wire carries more than keys: %s", b)
	}
}

func TestJiraProjectKeysIsBounded(t *testing.T) {
	hits := 0
	srv := jiraProjectsServer(t, 5000, &hits)
	c := &secrets.JiraCreds{Site: srv.URL, Email: "a@example.com", Token: "t"}
	keys, truncated, err := jiraProjectKeys(c)
	if err != nil || !truncated {
		t.Fatalf("truncated=%v err=%v", truncated, err)
	}
	if want := jiraProjectMaxKeys; len(keys) != want || hits != jiraProjectMaxKeys/jiraProjectPageSize {
		t.Fatalf("keys=%d hits=%d, want %d keys in %d reads", len(keys), hits, want, jiraProjectMaxKeys/jiraProjectPageSize)
	}
}

func TestJiraProjectKeysDropsMalformedKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"values":[{"key":"OK"},{"key":"a-b"},{"key":"X"},{"key":"TOOLONGKEY12"},{"key":"OK"},{"key":"A_1"}],"isLast":true}`))
	}))
	defer srv.Close()
	keys, _, err := jiraProjectKeys(&secrets.JiraCreds{Site: srv.URL, Email: "a@example.com", Token: "t"})
	if err != nil || strings.Join(keys, ",") != "OK,A_1" {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
}

func TestHandleJiraProjectsNotConnectedAnswersEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	w := httptest.NewRecorder()
	handleWorkItemsJiraProjects(w, httptest.NewRequest("POST", "/work-items/jira-projects", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"connected":false,"keys":[],"truncated":false}` {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestHandleJiraProjectsErrorCarriesNoUpstreamText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`UPSTREAM_SENTINEL`))
	}))
	defer srv.Close()
	s, _ := secrets.Load()
	s.Jira = &secrets.JiraCreds{Site: srv.URL, Email: "a@example.com", Token: "t"}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handleWorkItemsJiraProjects(w, httptest.NewRequest("POST", "/work-items/jira-projects", nil))
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "UPSTREAM_SENTINEL") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

// Jira may cap a page below what was asked for; the walk must continue from what it received.
func TestJiraProjectKeysFollowsAShortPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
		all := []string{"AA", "BB", "CC", "DD", "EE"}
		end := start + 2 // the site's own maximum
		if end > len(all) {
			end = len(all)
		}
		var vals []string
		for _, k := range all[start:end] {
			vals = append(vals, `{"key":"`+k+`"}`)
		}
		// isLast is absent on purpose: total alone must end the walk.
		fmt.Fprintf(w, `{"values":[%s],"total":%d}`, strings.Join(vals, ","), len(all))
	}))
	defer srv.Close()
	keys, truncated, err := jiraProjectKeys(&secrets.JiraCreds{Site: srv.URL, Email: "a@example.com", Token: "t"})
	if err != nil || truncated || strings.Join(keys, ",") != "AA,BB,CC,DD,EE" {
		t.Fatalf("keys=%v truncated=%v err=%v", keys, truncated, err)
	}
}

// A page past the cap, or the same page again, ends the walk as truncated instead of growing the
// list or looping.
func TestJiraProjectKeysEnforcesTheCapPerPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var vals []string
		for i := 0; i < jiraProjectMaxKeys+1; i++ {
			vals = append(vals, fmt.Sprintf(`{"key":"Q%03d"}`, i))
		}
		fmt.Fprintf(w, `{"values":[%s],"isLast":true}`, strings.Join(vals, ","))
	}))
	defer srv.Close()
	keys, truncated, err := jiraProjectKeys(&secrets.JiraCreds{Site: srv.URL, Email: "a@example.com", Token: "t"})
	if err != nil || !truncated || len(keys) != jiraProjectMaxKeys {
		t.Fatalf("keys=%d truncated=%v err=%v", len(keys), truncated, err)
	}
}

func TestJiraProjectKeysStopsOnARepeatedPage(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"values":[{"key":"AA"},{"key":"BB"}],"isLast":false}`))
	}))
	defer srv.Close()
	keys, truncated, err := jiraProjectKeys(&secrets.JiraCreds{Site: srv.URL, Email: "a@example.com", Token: "t"})
	if err != nil || !truncated || strings.Join(keys, ",") != "AA,BB" || hits != 2 {
		t.Fatalf("keys=%v truncated=%v hits=%d err=%v", keys, truncated, hits, err)
	}
}
