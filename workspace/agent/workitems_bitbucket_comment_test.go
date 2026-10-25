package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// #2709 — posting a comment on a Bitbucket pull request, and the scope gate that decides whether
// the Console offers it. Everything runs against httptest stubs; no real Bitbucket is contacted.

// bbCommentStub answers the PR comments endpoint with status/body and records the request.
type bbCommentStub struct {
	srv    *httptest.Server
	path   string
	auth   string
	posted map[string]any
}

func newBBCommentStub(t *testing.T, status int, body string) *bbCommentStub {
	t.Helper()
	st := &bbCommentStub{}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.path, st.auth = r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &st.posted)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	prev := bitbucketAPIBase
	bitbucketAPIBase = st.srv.URL
	t.Cleanup(func() { bitbucketAPIBase = prev; st.srv.Close() })
	return st
}

func TestBitbucketPostCommentSuccess(t *testing.T) {
	st := newBBCommentStub(t, http.StatusCreated,
		`{"links":{"html":{"href":"https://bitbucket.org/acme/web/pull-requests/7/_/diff#comment-9"}}}`)
	link, err := bitbucketPostPullRequestComment(bbTokenStore(), "acme/web#7", "hello")
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if st.path != "/2.0/repositories/acme/web/pullrequests/7/comments" {
		t.Errorf("path = %q", st.path)
	}
	content, _ := st.posted["content"].(map[string]any)
	if content["raw"] != "hello" {
		t.Errorf("payload = %+v, want content.raw", st.posted)
	}
	if !strings.HasPrefix(st.auth, "Basic ") {
		t.Errorf("auth header not sent: %q", st.auth)
	}
	if !strings.Contains(link, "comment-9") {
		t.Errorf("link = %q", link)
	}
}

// A 403 is a refusal to be reported, never a success: the connection lacks pullrequest:write.
func TestBitbucketPostCommentRefusalsAreErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   []string
	}{
		{http.StatusForbidden, []string{"403", "pullrequest:write", "write:pullrequest:bitbucket"}},
		{http.StatusUnauthorized, []string{"401", "re-connect"}},
		{http.StatusNotFound, []string{"acme/web#7"}},
		{http.StatusInternalServerError, []string{"500"}},
	} {
		newBBCommentStub(t, tc.status, `{"type":"error","error":{"message":"nope"}}`)
		link, err := bitbucketPostPullRequestComment(bbTokenStore(), "acme/web#7", "hello")
		if err == nil || link != "" {
			t.Fatalf("status %d: err=%v link=%q, want an error", tc.status, err, link)
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("status %d: %q lacks %q", tc.status, err.Error(), w)
			}
		}
	}
}

func TestBitbucketPostCommentRejectsBadKeyAndNoConnection(t *testing.T) {
	st := newBBCommentStub(t, http.StatusCreated, `{}`)
	if _, err := bitbucketPostPullRequestComment(bbTokenStore(), "not-a-key", "x"); err == nil {
		t.Error("a malformed key was sent instead of refused")
	}
	if _, err := bitbucketPostPullRequestComment(&secrets.Data{}, "acme/web#7", "x"); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("no connection: err = %v", err)
	}
	if st.path != "" {
		t.Errorf("a request reached the API: %q", st.path)
	}
}

// The handler no longer answers 400 bad_provider for bitbucket.
func TestWorkItemsCommentHandlerRoutesBitbucket(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := newBBCommentStub(t, http.StatusCreated, `{"links":{"html":{"href":"https://bitbucket.org/x"}}}`)
	s := bbTokenStore()
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handleWorkItemsComment(w, httptest.NewRequest("POST", "/work-items/comment",
		strings.NewReader(`{"provider":"bitbucket","key":"acme/web#7","body":"hi"}`)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "https://bitbucket.org/x") {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if st.path == "" {
		t.Fatal("the comment never reached Bitbucket")
	}
	st2 := newBBCommentStub(t, http.StatusForbidden, `{}`)
	_ = st2
	w = httptest.NewRecorder()
	handleWorkItemsComment(w, httptest.NewRequest("POST", "/work-items/comment",
		strings.NewReader(`{"provider":"bitbucket","key":"acme/web#7","body":"hi"}`)))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "provider_error") {
		t.Fatalf("a refused post must be a 502 provider_error, got %d %s", w.Code, w.Body.String())
	}
}

// --- scope gate -------------------------------------------------------------------------

const bbOnePR = `{"values":[{"id":7,"title":"t","state":"OPEN","destination":{"repository":{"full_name":"acme/web"}}}]}`

// newBBScopeStub serves one PR and reports `scopes` in X-OAuth-Scopes ("" = no header).
func newBBScopeStub(t *testing.T, scopes string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if scopes != "" {
			w.Header().Set("X-OAuth-Scopes", scopes)
		}
		_, _ = w.Write([]byte(bbOnePR))
	}))
	prev := bitbucketAPIBase
	bitbucketAPIBase = srv.URL
	t.Cleanup(func() { bitbucketAPIBase = prev; srv.Close() })
}

func TestBitbucketRowsCanCommentFollowGrantedScope(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recorded string // BitbucketCreds.Scopes
		header   string // X-OAuth-Scopes on the fetch itself
		want     bool
	}{
		{"recorded oauth write", "account repository pullrequest:write", "", true},
		{"recorded oauth read only", "account repository pullrequest", "", false},
		{"recorded wins over a header that grants more", "pullrequest", "pullrequest:write", false},
		{"unknown everywhere", "", "", false},
		{"older store learns write from the response", "", "read:pullrequest:bitbucket, write:pullrequest:bitbucket", true},
		{"older store learns read only from the response", "", "read:pullrequest:bitbucket,write:repository:bitbucket", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			newBBScopeStub(t, tc.header)
			s := &secrets.Data{Bitbucket: &secrets.BitbucketCreds{AccessToken: "a", RefreshToken: "r",
				Expiry: 1 << 40, Scopes: tc.recorded}}
			rows, err := bitbucketSearchWorkItems(s, "q", "acme/web")
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows=%v err=%v", rows, err)
			}
			if rows[0].CanComment != tc.want {
				t.Errorf("CanComment = %v, want %v", rows[0].CanComment, tc.want)
			}
		})
	}
}

// A pasted API token's scopes are recorded the same way, under the granular names.
func TestBitbucketTokenRowsCanComment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	newBBScopeStub(t, "")
	for scopes, want := range map[string]bool{
		"read:pullrequest:bitbucket write:pullrequest:bitbucket": true,
		"read:pullrequest:bitbucket":                             false,
		"":                                                       false,
	} {
		s := bbTokenStore()
		e := s.Git["bitbucket.org"]
		e.Scopes = scopes
		s.Git["bitbucket.org"] = e
		rows, err := bitbucketSearchWorkItems(s, "q", "acme/web")
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows=%v err=%v", rows, err)
		}
		if rows[0].CanComment != want {
			t.Errorf("scopes %q: CanComment = %v, want %v", scopes, rows[0].CanComment, want)
		}
	}
}

// What a response teaches an older store is persisted, so a later fetch whose response names
// nothing still knows; and a store without the field (every existing one) loads fine.
func TestBitbucketObservedScopesArePersisted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &secrets.Data{Bitbucket: &secrets.BitbucketCreds{AccessToken: "a", Expiry: 1 << 40}}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	newBBScopeStub(t, "pullrequest:write")
	if _, err := bitbucketSearchWorkItems(s, "q", "acme/web"); err != nil {
		t.Fatal(err)
	}
	loaded, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Bitbucket == nil || loaded.Bitbucket.Scopes != "pullrequest:write" {
		t.Fatalf("scopes not persisted: %+v", loaded.Bitbucket)
	}
	// The raw JSON of an old store has no scopes key and must still decode.
	var old secrets.BitbucketCreds
	if err := json.Unmarshal([]byte(`{"access_token":"a","refresh_token":"r","expiry":1}`), &old); err != nil || old.Scopes != "" {
		t.Fatalf("old store: %+v %v", old, err)
	}
}
