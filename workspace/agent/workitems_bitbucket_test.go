package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// docs/log/80 §80.19 / ADR 0061 decisions 17-19 — the Bitbucket-side mapping and the "where
// to look" that heads a query. There is no real Bitbucket account in this environment or in
// CI, so the parts that need no auth (response shape, ordering, how q behaves) are pinned to
// values measured against a public repository, and the parts that do (cross-workspace search,
// scope refusal) are pinned with a stub.

func TestParseBitbucketWorkItemQuery(t *testing.T) {
	cases := []struct {
		in               string
		ws, repo, filter string
		wantErr          bool
	}{
		{in: "acme/web", ws: "acme", repo: "web"},
		{in: `acme/web reviewers.uuid="@me" AND state="OPEN"`, ws: "acme", repo: "web", filter: `reviewers.uuid="@me" AND state="OPEN"`},
		{in: "acme", ws: "acme"},
		{in: `  acme   state="MERGED"  `, ws: "acme", filter: `state="MERGED"`},
		// The leading token is nothing but "where". Forgetting or mistyping it yields a row
		// that saves fine but can never be fetched, so refuse it at fetch time.
		{in: "", wantErr: true},
		{in: `state="OPEN"`, ws: "state=\"OPEN\""}, // indistinguishable: Bitbucket refuses the content
		{in: "acme/", wantErr: true},
		{in: "acme/web/extra", wantErr: true},
		{in: "/web", wantErr: true},
	}
	for _, c := range cases {
		got, err := parseBitbucketWorkItemQuery(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: want an error, got %+v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got.Workspace != c.ws || got.Repo != c.repo || got.Filter != c.filter {
			t.Errorf("%q -> %+v, want ws=%q repo=%q filter=%q", c.in, got, c.ws, c.repo, c.filter)
		}
	}
}

// The response is a shortened copy of one taken from the real api.bitbucket.org (the public
// atlassian/aui repository). The "+00:00" on updated_on in particular is genuine, and if that
// handling breaks the ordering falls apart the moment these rows mix with GitHub / Jira ones.
func TestParseBitbucketPullRequests(t *testing.T) {
	body := []byte(`{"values":[
	  {"id":5319,"title":"Update button styles","state":"OPEN","draft":false,
	   "updated_on":"2026-08-24T07:10:55.049604+00:00",
	   "author":{"display_name":"Filip Nowakowski"},
	   "destination":{"repository":{"full_name":"atlassian/aui"}},
	   "links":{"html":{"href":"https://bitbucket.org/atlassian/aui/pull-requests/5319"}}},
	  {"id":5320,"title":"draft one","state":"OPEN","draft":true,
	   "updated_on":"2026-08-23T00:00:00+09:00",
	   "author":{"nickname":"taro"},
	   "destination":{"repository":{"full_name":"atlassian/aui"}},
	   "links":{"html":{"href":"https://bitbucket.org/atlassian/aui/pull-requests/5320"}}},
	  {"id":5321,"title":"declined one","state":"DECLINED","updated_on":"2026-08-22T00:00:00+00:00"}
	]}`)
	rows, err := parseBitbucketPullRequests(body, "q7")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	got := rows[0]
	if got.QueryID != "q7" || got.Provider != "bitbucket" || got.Kind != "pr" {
		t.Errorf("query/provider/kind not stamped: %+v", got)
	}
	// With the same key shape as GitHub, shortKey works as-is and the row reads "#5319".
	if got.Key != "atlassian/aui#5319" || got.Repo != "atlassian/aui" {
		t.Errorf("key/repo = %q / %q", got.Key, got.Repo)
	}
	if got.State != "open" {
		t.Errorf("state = %q, want open", got.State)
	}
	if got.Assignee != "Filip Nowakowski" {
		t.Errorf("assignee = %q, want the author's display name", got.Assignee)
	}
	if got.URL != "https://bitbucket.org/atlassian/aui/pull-requests/5319" {
		t.Errorf("url = %q", got.URL)
	}
	// Break this and the ordering of a mixed list goes with it: sortWorkItems compares strings.
	if got.UpdatedAt != "2026-08-24T07:10:55Z" {
		t.Errorf("updatedAt = %q, want UTC RFC3339", got.UpdatedAt)
	}
	if rows[1].UpdatedAt != "2026-08-22T15:00:00Z" {
		t.Errorf("offset stamp not normalised to UTC: %q", rows[1].UpdatedAt)
	}
	// A draft is not waiting on anyone's reply, matching how GitHub draft PRs are treated.
	if rows[1].State != "in_progress" {
		t.Errorf("draft state = %q, want in_progress", rows[1].State)
	}
	if rows[1].Assignee != "taro" {
		t.Errorf("author fallback = %q, want the nickname", rows[1].Assignee)
	}
	if rows[2].State != "done" {
		t.Errorf("DECLINED = %q, want done", rows[2].State)
	}
	if rows[2].Key != "#5321" {
		t.Errorf("repo-less key = %q, want #5321", rows[2].Key)
	}
	// A nil slice marshals to JSON null and leaves the Console blank (docs/log/80 §80.17.5).
	for i, r := range rows {
		if r.Labels == nil {
			t.Errorf("row %d: labels must be an empty slice, not nil", i)
		}
	}
}

func TestNormalizeBitbucketPRState(t *testing.T) {
	for _, c := range []struct{ state, want string }{
		{"OPEN", "open"}, {"MERGED", "done"}, {"DECLINED", "done"},
		{"SUPERSEDED", "done"}, {"", "other"}, {"WHATEVER", "other"},
	} {
		if got := normalizeBitbucketPRState(c.state, false); got != c.want {
			t.Errorf("%q -> %q, want %q", c.state, got, c.want)
		}
	}
	if got := normalizeBitbucketPRState("OPEN", true); got != "in_progress" {
		t.Errorf("draft -> %q, want in_progress", got)
	}
}

// bbStub stands in for api.bitbucket.org and records what the adapter asked for.
type bbStub struct {
	srv     *httptest.Server
	paths   []string
	queries []url.Values
	status  int
	body    string
}

func newBBStub(t *testing.T, status int, body string) *bbStub {
	t.Helper()
	st := &bbStub{status: status, body: body}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.paths = append(st.paths, r.URL.Path)
		st.queries = append(st.queries, r.URL.Query())
		if r.URL.Path == "/2.0/user" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"uuid":"{11111111-2222-3333-4444-555555555555}","username":"taro"}`))
			return
		}
		w.WriteHeader(st.status)
		_, _ = w.Write([]byte(st.body))
	}))
	prev := bitbucketAPIBase
	bitbucketAPIBase = st.srv.URL
	t.Cleanup(func() {
		bitbucketAPIBase = prev
		st.srv.Close()
		bbSelfCache.auth, bbSelfCache.uuid = "", ""
	})
	return st
}

func bbTokenStore() *secrets.Data {
	return &secrets.Data{Git: map[string]secrets.GitEntry{
		"bitbucket.org": {User: "taro@example.com", Token: "app-token"},
	}}
}

// A repository-qualified query takes exactly one request; never N walks (ADR 0061
// decision 17).
func TestBitbucketSearchRepoQueryIsOneCall(t *testing.T) {
	st := newBBStub(t, http.StatusOK, `{"values":[{"id":7,"title":"t","state":"OPEN",
	  "updated_on":"2026-08-24T07:10:55.049604+00:00",
	  "destination":{"repository":{"full_name":"acme/web"}},
	  "links":{"html":{"href":"https://bitbucket.org/acme/web/pull-requests/7"}}}]}`)
	rows, err := bitbucketSearchWorkItems(bbTokenStore(), "q1", `acme/web state="OPEN"`)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "acme/web#7" {
		t.Fatalf("rows = %+v", rows)
	}
	if len(st.paths) != 1 {
		t.Fatalf("want exactly 1 provider call, got %v", st.paths)
	}
	if st.paths[0] != "/2.0/repositories/acme/web/pullrequests" {
		t.Errorf("path = %q", st.paths[0])
	}
	q := st.queries[0]
	if q.Get("q") != `state="OPEN"` {
		t.Errorf("filter not passed through verbatim: %q", q.Get("q"))
	}
	// Since the list is cut at 50, which 50 is not left to the provider (ADR 0061 decision 15).
	if q.Get("sort") != "-updated_on" {
		t.Errorf("sort = %q, want -updated_on", q.Get("sort"))
	}
	// The body is never requested: ADR 0061 decision 2, expressed in the request itself.
	if f := q.Get("fields"); f == "" || strings.Contains(f, "description") {
		t.Errorf("fields = %q, must project away the description", f)
	}
}

// @me expands to the connected account's UUID. Without it nobody can write "PRs I am a
// reviewer on": Bitbucket's filtering has no currentUser().
func TestBitbucketSearchExpandsMe(t *testing.T) {
	st := newBBStub(t, http.StatusOK, `{"values":[]}`)
	if _, err := bitbucketSearchWorkItems(bbTokenStore(), "q1", `acme/web reviewers.uuid="@me"`); err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(st.paths) != 2 || st.paths[0] != "/2.0/user" {
		t.Fatalf("want /2.0/user then the search, got %v", st.paths)
	}
	if got := st.queries[1].Get("q"); got != `reviewers.uuid="{11111111-2222-3333-4444-555555555555}"` {
		t.Errorf("q = %q, @me not expanded", got)
	}
}

// The form without a repository is the cross-workspace search, the only cross-cutting query
// Bitbucket offers, and it is limited to PRs the account itself created.
func TestBitbucketSearchWorkspaceFormUsesTheWorkspaceEndpoint(t *testing.T) {
	st := newBBStub(t, http.StatusOK, `{"values":[]}`)
	if _, err := bitbucketSearchWorkItems(bbTokenStore(), "q1", "acme"); err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(st.paths) != 2 {
		t.Fatalf("paths = %v", st.paths)
	}
	want := "/2.0/workspaces/acme/pullrequests/{11111111-2222-3333-4444-555555555555}"
	if st.paths[1] != want {
		t.Errorf("path = %q, want %q", st.paths[1], want)
	}
}

// A 403 does not mean the token is bad; it means the app lacks permission to read PRs.
// Re-pasting the token fixes nothing, so name the scope to add and who can add it.
func TestBitbucketScopeRefusalNamesThePermission(t *testing.T) {
	newBBStub(t, http.StatusForbidden, `{"type":"error","error":{"message":"Your credentials lack one or more required privilege scopes."}}`)
	_, err := bitbucketSearchWorkItems(bbTokenStore(), "q1", "acme/web")
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	for _, want := range []string{"pullrequest", "read:pullrequest:bitbucket", "re-connect"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}

// A syntax error surfaces Bitbucket's own wording verbatim, as JQL's 400 does.
func TestBitbucketBadFilterShowsBitbucketsOwnWords(t *testing.T) {
	newBBStub(t, http.StatusBadRequest, `{"type":"error","error":{"message":"Invalid filter query expression: Syntax error in input at '=' (type _ANON_2) line 1 col 7"}}`)
	_, err := bitbucketSearchWorkItems(bbTokenStore(), "q1", "acme/web bogus===")
	if err == nil || !strings.Contains(err.Error(), "Syntax error in input") {
		t.Fatalf("err = %v", err)
	}
}

func TestBitbucketNotConnected(t *testing.T) {
	_, err := bitbucketSearchWorkItems(&secrets.Data{}, "q1", "acme/web")
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("err = %v", err)
	}
}

// An unsupported provider is refused on the fetch side too, so no row can be saved and then
// never fetched again.
func TestFetchWorkItemQueryRoutesBitbucket(t *testing.T) {
	newBBStub(t, http.StatusOK, `{"values":[]}`)
	if _, err := fetchWorkItemQuery(bbTokenStore(), workItemQueryIn{ID: "q1", Provider: "bitbucket", Query: "acme/web"}); err != nil {
		t.Fatalf("bitbucket must be routed: %v", err)
	}
	if _, err := fetchWorkItemQuery(bbTokenStore(), workItemQueryIn{ID: "q1", Provider: "gitlab", Query: "x"}); err == nil {
		t.Fatal("unknown provider must be refused")
	}
}
