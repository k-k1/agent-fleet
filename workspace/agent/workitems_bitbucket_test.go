package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// docs/log/80 §80.19 / ADR 0061 決定 17〜19 — Bitbucket 側の写像と、クエリの先頭に置く
// 「どこを見るか」。実 Bitbucket アカウントはこの環境にも CI にも無いので、
// 認証が要らない部分（応答の形・並び・q の効き方）は公開リポジトリで実測した値を、
// 認証が要る部分（ワークスペース横断・スコープ拒否）はスタブで固定する。

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
		// 先頭のトークンは「どこ」でしかない。書き忘れ・書き損じは保存できても
		// 取得できない行になるので、取得の時点で断る。
		{in: "", wantErr: true},
		{in: `state="OPEN"`, ws: "state=\"OPEN\""}, // 区別できない: 中身は Bitbucket が断る
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

// 応答は実 api.bitbucket.org（公開リポジトリ atlassian/aui）から取ったものを縮めた形。
// 特に updated_on の "+00:00" は本物で、ここが崩れると GitHub / Jira の行と混ざった
// 瞬間に並び順が壊れる。
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
	// GitHub と同型のキーにしておくと shortKey がそのまま効いて行は "#5319" になる。
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
	// ⚠️ ここが崩れると混在した一覧の並びが壊れる（sortWorkItems は文字列比較）。
	if got.UpdatedAt != "2026-08-24T07:10:55Z" {
		t.Errorf("updatedAt = %q, want UTC RFC3339", got.UpdatedAt)
	}
	if rows[1].UpdatedAt != "2026-08-22T15:00:00Z" {
		t.Errorf("offset stamp not normalised to UTC: %q", rows[1].UpdatedAt)
	}
	// draft は「誰かの返事待ち」ではない（GitHub の draft PR と揃える）。
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
	// ⚠️ nil スライスは JSON の null になり、Console が真っ白になる（docs/log/80 §80.17.5）。
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

// レポジトリ指定は 1 リクエストで済む。★ N 個舐めない（ADR 0061 決定 17）。
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
	// 50 件で切る以上、どの 50 件かを provider に委ねない（ADR 0061 決定 15）。
	if q.Get("sort") != "-updated_on" {
		t.Errorf("sort = %q, want -updated_on", q.Get("sort"))
	}
	// ★ 本文は要求しない（ADR 0061 決定 2 を、リクエストに書いた形）。
	if f := q.Get("fields"); f == "" || strings.Contains(f, "description") {
		t.Errorf("fields = %q, must project away the description", f)
	}
}

// @me は接続アカウントの UUID に置き換わる。これが無いと「自分がレビューアの PR」を
// 人が書けない（Bitbucket の絞り込みに currentUser() は無い）。
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

// リポジトリを書かない形はワークスペース横断（＝Bitbucket が唯一提供する横断）で、
// 相手は「自分が作った PR」に限られる。
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

// ★ 403 は「トークンが悪い」ではなく「そのアプリに PR を読む権限が無い」。
// 貼り直させても直らないので、足すべき権限と、足せる人を名指しする。
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

// 文法エラーは Bitbucket の説明文をそのまま出す（JQL の 400 と同じ扱い）。
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

// 未対応の provider は「保存できたのに永久に取れない行」を作らないよう、取得側でも断る。
func TestFetchWorkItemQueryRoutesBitbucket(t *testing.T) {
	newBBStub(t, http.StatusOK, `{"values":[]}`)
	if _, err := fetchWorkItemQuery(bbTokenStore(), workItemQueryIn{ID: "q1", Provider: "bitbucket", Query: "acme/web"}); err != nil {
		t.Fatalf("bitbucket must be routed: %v", err)
	}
	if _, err := fetchWorkItemQuery(bbTokenStore(), workItemQueryIn{ID: "q1", Provider: "gitlab", Query: "x"}); err == nil {
		t.Fatal("unknown provider must be refused")
	}
}
