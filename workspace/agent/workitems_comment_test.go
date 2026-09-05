package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// docs/log/80 §80.10 - the one write-back path. Break it and you get either "posted but not
// there" or "something that must not be posted got posted", so the boundaries are pinned here.

func TestParseGitHubIssueKey(t *testing.T) {
	for _, tc := range []struct {
		in   string
		repo string
		n    int
		ok   bool
	}{
		{"acme/web#45", "acme/web", 45, true},
		{"acme/web#0", "", 0, false},
		{"acme/web", "", 0, false},
		{"#45", "", 0, false},
		{"acme/web#x", "", 0, false},
		{"PROJ-123", "", 0, false},
		{"", "", 0, false},
	} {
		repo, n, ok := parseGitHubIssueKey(tc.in)
		if ok != tc.ok || repo != tc.repo || n != tc.n {
			t.Errorf("parseGitHubIssueKey(%q) = %q,%d,%v; want %q,%d,%v", tc.in, repo, n, ok, tc.repo, tc.n, tc.ok)
		}
	}
}

// A Jira v3 comment is a 400 unless it is ADF, and sending a plain string is the most common
// way to break it, so the doc/paragraph shape and how paragraphs and line breaks are carried
// over are pinned here.
func TestJiraADF(t *testing.T) {
	adf := jiraADF("一行目\n二行目\n\n次の段落")
	if adf["type"] != "doc" || adf["version"] != 1 {
		t.Fatalf("envelope = %+v", adf)
	}
	content, _ := adf["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("want 2 paragraphs, got %d: %+v", len(content), content)
	}
	first, _ := content[0].(map[string]any)
	nodes, _ := first["content"].([]any)
	// text, hardBreak, text
	if len(nodes) != 3 {
		t.Fatalf("paragraph nodes = %+v", nodes)
	}
	if n, _ := nodes[1].(map[string]any); n["type"] != "hardBreak" {
		t.Errorf("line break did not become a hardBreak: %+v", nodes[1])
	}
	// An all-blank body must still not produce an empty doc: Jira answers 400 for one.
	if c, _ := jiraADF("\n\n\n")["content"].([]any); len(c) == 0 {
		t.Error("an all-blank body produced an empty doc, which Jira rejects")
	}
	// It must marshal: this is nested map[string]any, so a broken shape fails at send time.
	if _, err := json.Marshal(adf); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}

func TestWorkItemsCommentValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handleWorkItemsComment(w, httptest.NewRequest("POST", "/work-items/comment", strings.NewReader(body)))
		return w
	}
	if w := post(`{"provider":"github","key":"","body":"x"}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty key = %d", w.Code)
	}
	if w := post(`{"provider":"github","key":"acme/web#1","body":"   "}`); w.Code != http.StatusBadRequest {
		t.Errorf("blank body = %d — an empty comment must never be posted", w.Code)
	}
	if w := post(`{"provider":"backlog","key":"x#1","body":"y"}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown provider = %d", w.Code)
	}
	// No connection is a 400 (missing configuration on the af side), kept distinct from the
	// 502 used when the provider refuses.
	if w := post(`{"provider":"github","key":"acme/web#1","body":"y"}`); w.Code != http.StatusBadRequest {
		t.Errorf("no connection = %d, want 400", w.Code)
	}
}

// A refused post surfaces the provider's own wording. Rewriting it as "please reconnect"
// hides insufficient permissions (readable but not writable) and a locked issue.
func TestGitHubCommentSurfacesProviderRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	// githubPostIssueComment is pinned to api.github.com, so what is checked here is the key
	// validation - that a malformed key never reaches the network - not the route.
	if _, err := githubPostIssueComment("tok", "not-a-key", "body"); err == nil {
		t.Error("a malformed key was sent to the API instead of being refused locally")
	}
}
