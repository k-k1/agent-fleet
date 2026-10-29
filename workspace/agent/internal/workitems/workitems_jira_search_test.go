package workitems

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// docs/log/80 P1 — the Jira-side mapping. There is no real Jira and no account in this
// environment, so the response shapes are pinned here to fix what ends up in a row.

func TestParseJiraSearchIssues(t *testing.T) {
	body := []byte(`{"issues":[
	  {"key":"PROJ-123","fields":{
	     "summary":"ログイン後に一覧が空になる",
	     "updated":"2026-08-26T10:11:12.000+0900",
	     "status":{"name":"進行中","statusCategory":{"key":"indeterminate"}},
	     "assignee":{"displayName":"山田 太郎"},
	     "issuetype":{"name":"Bug"},
	     "labels":["bug","checkout"]}},
	  {"key":"PROJ-124","fields":{
	     "summary":"done one",
	     "updated":"2026-08-25T00:00:00.000+0000",
	     "status":{"name":"完了","statusCategory":{"key":"done"}},
	     "assignee":null,
	     "labels":[]}},
	  {"key":"PROJ-125","fields":{
	     "summary":"todo one",
	     "status":{"name":"未対応","statusCategory":{"key":"new"}}}}
	]}`)
	rows, err := parseJiraSearchIssues(body, "https://example.atlassian.net", "q9")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	got := rows[0]
	if got.QueryID != "q9" || got.Provider != "jira" || got.Kind != "issue" {
		t.Errorf("stamp: %+v", got)
	}
	if got.Key != "PROJ-123" || got.Title != "ログイン後に一覧が空になる" {
		t.Errorf("key/title: %q %q", got.Key, got.Title)
	}
	if got.URL != "https://example.atlassian.net/browse/PROJ-123" {
		t.Errorf("url = %q", got.URL)
	}
	if got.State != "in_progress" {
		t.Errorf("state = %q, want in_progress", got.State)
	}
	if got.Assignee != "山田 太郎" {
		t.Errorf("assignee = %q", got.Assignee)
	}
	if strings.Join(got.Labels, ",") != "bug,checkout" {
		t.Errorf("labels = %v", got.Labels)
	}
	// The issue type feeds the branch-name resolver's kind (ADR 0103 decision 4).
	if got.Type != "Bug" || rows[2].Type != "" {
		t.Errorf("type = %q / %q, want Bug / empty", got.Type, rows[2].Type)
	}
	// Jira has no repository. The launch target comes from the query's repoHint, so nothing may
	// be guessed and filled in here.
	if got.Repo != "" {
		t.Errorf("repo = %q, want empty (Jira has no repository)", got.Repo)
	}
	// Jira timestamps come in the "+0900" form. Without normalizing to RFC3339 (UTC), these rows
	// cannot be ordered together with GitHub's.
	if got.UpdatedAt != "2026-08-26T01:11:12Z" {
		t.Errorf("updatedAt = %q, want the UTC RFC3339 form", got.UpdatedAt)
	}
	if rows[1].State != "done" || rows[1].Assignee != "" {
		t.Errorf("row2 = %q / %q", rows[1].State, rows[1].Assignee)
	}
	if rows[2].State != "open" {
		t.Errorf("row3 = %q, want open", rows[2].State)
	}
	// An issue without labels must still not carry nil: a nil slice marshals as JSON null, which
	// the receiver cannot treat as an array (the shape that once blanked the Console).
	if rows[2].Labels == nil {
		t.Error("a label-less issue produced a nil slice, which marshals as null")
	}
	if enc, _ := json.Marshal(rows[2]); strings.Contains(string(enc), `"labels":null`) || strings.Contains(string(enc), `"labelColors":null`) {
		t.Errorf("row wire carries a null array: %s", enc)
	}
	// The body (description) is neither fetched nor returned (ADR 0061 decision 2).
	enc, _ := json.Marshal(rows)
	if strings.Contains(string(enc), `"description"`) {
		t.Errorf("row JSON leaks a description: %s", enc)
	}
}

// The decision is made on the status CATEGORY, not the status NAME. Names are freely renamed per
// project ("in review", "awaiting verification", …), so deciding on the name breaks on the first
// custom workflow.
func TestNormalizeJiraState(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"new", "open"},
		{"undefined", "open"},
		{"indeterminate", "in_progress"},
		{"done", "done"},
		{"DONE", "done"},
		{"", "other"},
		{"進行中", "other"}, // a name, not a category: it must fall through to other
	} {
		if got := normalizeJiraState(tc.in); got != tc.want {
			t.Errorf("normalizeJiraState(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The two endpoints are tried in order: Atlassian is migrating /search → /search/jql and which
// one a site answers on varies. This pins that a 404 on the newer one falls back to the older
// one, and that a genuine error such as 401 does NOT fall back.
func TestJiraSearchFallsBackToClassicEndpoint(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		if r.URL.Path == "/rest/api/3/search/jql" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[{"key":"PROJ-1","fields":{"summary":"x","status":{"statusCategory":{"key":"new"}}}}]}`))
	}))
	defer srv.Close()

	c := &secrets.JiraCreds{Site: srv.URL, Email: "a@example.com", Token: "t"}
	rows, err := jiraSearchWorkItems(c, "q1", "assignee = currentUser()")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "PROJ-1" {
		t.Fatalf("rows = %+v", rows)
	}
	if len(hits) != 2 || hits[0] != "/rest/api/3/search/jql" || hits[1] != "/rest/api/3/search" {
		t.Errorf("endpoint order = %v", hits)
	}
}

func TestJiraSearchDoesNotFallBackOnAuthError(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &secrets.JiraCreds{Site: srv.URL, Email: "a@example.com", Token: "t"}
	_, err := jiraSearchWorkItems(c, "q1", "x")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Errorf("error = %q, want it to name the credentials", err)
	}
	if hits != 1 {
		t.Errorf("hits = %d — a 401 must not be retried against the other endpoint", hits)
	}
}

// docs/log/80 §80.18.6 — a fetch is cut at 50 issues per query, so with an unordered JQL it is
// up to Jira WHICH 50 survive. The rail claims to show "the newest N", and that cannot be left
// undefined.
func TestJiraOrderedJQL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"assignee = currentUser() AND statusCategory != Done",
			"assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC"},
		// An ordering the user wrote is left alone, case and spacing variants included.
		{"project = G3M ORDER BY priority DESC", "project = G3M ORDER BY priority DESC"},
		{"project = G3M order by created", "project = G3M order by created"},
		{"project = G3M ORDER   BY created", "project = G3M ORDER   BY created"},
		// A JQL that merely contains "order" inside a word does not count as ordered.
		{`summary ~ "reorder"`, `summary ~ "reorder" ORDER BY updated DESC`},
		{"  ", ""},
	}
	for _, c := range cases {
		if got := jiraOrderedJQL(c.in); got != c.want {
			t.Errorf("jiraOrderedJQL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
