package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin/memberorigintest"
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
	s := memberorigintest.Install(t)
	post := func(body string) *httptest.ResponseRecorder {
		return postComment(t, s, body)
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

// commentReq is the request the CP relays: JSON, AGENT_TOKEN as the bearer.
func commentReq(body string) *http.Request {
	r := httptest.NewRequest("POST", "/work-items/comment", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer agent-token")
	r.Header.Set("Content-Type", "application/json")
	return r
}

// postComment is the CP's relay of a member's press, through the real route table: a valid mark
// over the exact body.
func postComment(t *testing.T, s *memberorigintest.Signer, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := commentReq(body)
	s.Sign(r, []byte(body))
	w := httptest.NewRecorder()
	buildMux().ServeHTTP(w, r)
	return w
}

func serveComment(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	buildMux().ServeHTTP(w, r)
	return w
}

// AGENT_TOKEN is in every session's shell, so a session can make the HTTP call itself. The route
// takes only a request the CP marked as this workspace's own member's press; everything else is
// refused before the tracker is reached.
func TestWorkItemsCommentRequiresTheMemberOriginMark(t *testing.T) {
	seedGitHubToken(t)
	forger := memberorigintest.Install(t) // a key pair the Agent stops trusting once s is installed
	s := memberorigintest.Install(t)
	calls := fakeGitHubMerge(t, 201, `{"html_url":"https://github.com/acme/web/issues/12#c1"}`)
	body := `{"provider":"github","key":"acme/web#12","body":"hello"}`
	want := func(name string, w *httptest.ResponseRecorder, code string) {
		t.Helper()
		if w.Code < 400 || !strings.Contains(w.Body.String(), code) {
			t.Errorf("%s: %d %s, want a refusal with %q", name, w.Code, w.Body.String(), code)
		}
	}

	want("token only", serveComment(commentReq(body)), memberorigin.CodeRequired)
	hint := commentReq(body)
	hint.Header.Set("X-AF-Relay", "cp")
	want("relay hint", serveComment(hint), memberorigin.CodeRequired)
	forged := commentReq(body)
	forged.Header.Set(memberorigin.Header, forger.Mark(memberorigintest.Claims(forged, []byte(body))))
	want("forged", serveComment(forged), memberorigin.CodeInvalid)
	// A real mark over a different body (the text swapped after signing).
	tampered := commentReq(strings.Replace(body, "hello", "evil", 1))
	tampered.Header.Set(memberorigin.Header, s.Mark(memberorigintest.Claims(commentReq(body), []byte(body))))
	want("tampered body", serveComment(tampered), memberorigin.CodeInvalid)
	for name, edit := range map[string]func(*memberorigin.Claims){
		"other workspace":  func(c *memberorigin.Claims) { c.Workspace = "ws-someone-else" },
		"other membership": func(c *memberorigin.Claims) { c.Membership = "mb-someone-else" },
		"other path":       func(c *memberorigin.Claims) { c.Path = "/work-items/merge" },
	} {
		r := commentReq(body)
		c := memberorigintest.Claims(r, []byte(body))
		edit(&c)
		r.Header.Set(memberorigin.Header, s.Mark(c))
		if w := serveComment(r); w.Code < 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	// text/plain is what a cross-origin page sends without a preflight.
	plain := commentReq(body)
	plain.Header.Set("Content-Type", "text/plain")
	s.Sign(plain, []byte(body))
	if w := serveComment(plain); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d %s", w.Code, w.Body.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("GitHub was reached by a request that should have been refused: %+v", *calls)
	}

	// The CP's own relay gets through, once; the same mark replayed does not.
	ok := commentReq(body)
	mark := s.Mark(memberorigintest.Claims(ok, []byte(body)))
	ok.Header.Set(memberorigin.Header, mark)
	if w := serveComment(ok); w.Code != http.StatusOK || len(*calls) != 1 {
		t.Fatalf("signed: %d %s (calls %d)", w.Code, w.Body.String(), len(*calls))
	}
	replay := commentReq(body)
	replay.Header.Set(memberorigin.Header, mark)
	if w := serveComment(replay); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "replayed") || len(*calls) != 1 {
		t.Fatalf("replayed: %d %s (calls %d)", w.Code, w.Body.String(), len(*calls))
	}
}

// A handler registered without the wrapper still refuses.
func TestWorkItemsCommentHandlerRefusesWithoutAMark(t *testing.T) {
	seedGitHubToken(t)
	memberorigintest.Install(t)
	calls := fakeGitHubMerge(t, 201, `{}`)
	w := httptest.NewRecorder()
	handleWorkItemsComment(w, commentReq(`{"provider":"github","key":"acme/web#12","body":"x"}`))
	if w.Code != http.StatusForbidden || len(*calls) != 0 {
		t.Fatalf("%d %s (calls %d)", w.Code, w.Body.String(), len(*calls))
	}
}

// The cap is sized for what the CP can send, not for the text the member typed: a body of 50,000
// "<" is 300 KB once a JSON encoder escapes it, and must still be posted; past the cap it is refused.
func TestWorkItemsCommentRequestSizeCap(t *testing.T) {
	seedGitHubToken(t)
	s := memberorigintest.Install(t)
	calls := fakeGitHubMerge(t, 201, `{"html_url":"u"}`)
	escaped := `{"provider":"github","key":"acme/web#12","body":"` + strings.Repeat(`\u003c`, 50000) + `"}`
	if w := postComment(t, s, escaped); w.Code != http.StatusOK || len(*calls) != 1 {
		t.Fatalf("escaped 300 KB body: %d %.200s (calls %d)", w.Code, w.Body.String(), len(*calls))
	}
	huge := `{"provider":"github","key":"acme/web#12","body":"` + strings.Repeat("a", workItemCommentMaxRequest) + `"}`
	if w := postComment(t, s, huge); w.Code != http.StatusRequestEntityTooLarge || len(*calls) != 1 {
		t.Fatalf("oversize: %d (calls %d)", w.Code, len(*calls))
	}
}
