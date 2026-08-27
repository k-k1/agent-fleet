package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// docs/80 P1 — Jira 側の写像。実 Jira はこの環境にもアカウントも無いので、
// 応答の形を固定して「行に何が出るか」を押さえる。

func TestParseJiraSearchIssues(t *testing.T) {
	body := []byte(`{"issues":[
	  {"key":"PROJ-123","fields":{
	     "summary":"ログイン後に一覧が空になる",
	     "updated":"2026-08-26T10:11:12.000+0900",
	     "status":{"name":"進行中","statusCategory":{"key":"indeterminate"}},
	     "assignee":{"displayName":"山田 太郎"},
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
	// ★ Jira はリポジトリを持たない。起動先はクエリの repoHint が決めるので、
	//   ここで何かを推測して埋めてはいけない。
	if got.Repo != "" {
		t.Errorf("repo = %q, want empty (Jira has no repository)", got.Repo)
	}
	// Jira の時刻は "+0900" 形式。RFC3339(UTC) に寄せないと行の並びが GitHub と混ざらない。
	if got.UpdatedAt != "2026-08-26T01:11:12Z" {
		t.Errorf("updatedAt = %q, want the UTC RFC3339 form", got.UpdatedAt)
	}
	if rows[1].State != "done" || rows[1].Assignee != "" {
		t.Errorf("row2 = %q / %q", rows[1].State, rows[1].Assignee)
	}
	if rows[2].State != "open" {
		t.Errorf("row3 = %q, want open", rows[2].State)
	}
	// 本文（description）は取りにも行かないし返しもしない（ADR 0061 決定 2）。
	enc, _ := json.Marshal(rows)
	if strings.Contains(string(enc), `"description"`) {
		t.Errorf("row JSON leaks a description: %s", enc)
	}
}

// ★ ステータス「名前」ではなく「カテゴリ」で判定する。名前はプロジェクトごとに
// 自由に変えられる（レビュー中 / 検証待ち …）ので、名前で判定すると最初の
// カスタムワークフローで壊れる。
func TestNormalizeJiraState(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"new", "open"},
		{"undefined", "open"},
		{"indeterminate", "in_progress"},
		{"done", "done"},
		{"DONE", "done"},
		{"", "other"},
		{"進行中", "other"}, // 名前が来たら other（カテゴリではないと分かる）
	} {
		if got := normalizeJiraState(tc.in); got != tc.want {
			t.Errorf("normalizeJiraState(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeJiraSite(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		bad      bool
	}{
		{in: "https://example.atlassian.net", want: "https://example.atlassian.net"},
		{in: " example.atlassian.net ", want: "https://example.atlassian.net"},
		// 見ていた画面の URL をそのまま貼る人が多い。パスは落とす。
		{in: "https://example.atlassian.net/jira/software/projects/PROJ/boards/1", want: "https://example.atlassian.net"},
		{in: "http://example.atlassian.net", bad: true}, // Basic 認証が平文で飛ぶ
		{in: "", bad: true},
		{in: "https://", bad: true},
	} {
		got, err := normalizeJiraSite(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("normalizeJiraSite(%q) = %q, want an error", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("normalizeJiraSite(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

// ★ 2 つのエンドポイントを順に試す（Atlassian が /search → /search/jql へ移行中で、
// どちらを答えるかはサイト次第）。新しい方が 404 なら古い方へ落ちること、そして
// 401 のような**本物のエラーでは落ちない**ことを固定する。
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

// 保存前に /rest/api/3/myself で検証する。通らない資格情報は**保存しない**
// （保存してしまうと、最初の異常はレール行のエラーになり「機能が壊れている」と読める）。
func TestJiraConnectVerifiesBeforeSaving(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/myself" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
			t.Errorf("missing basic auth header")
		}
		_, _ = w.Write([]byte(`{"displayName":"山田 太郎"}`))
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer bad.Close()

	put := func(site string) *httptest.ResponseRecorder {
		body := `{"site":"` + site + `","email":"a@example.com","token":"tok"}`
		w := httptest.NewRecorder()
		handlePutJiraConn(w, httptest.NewRequest("PUT", "/connections/jira", strings.NewReader(body)))
		return w
	}

	// ⚠️ normalizeJiraSite は https を要求するので、httptest（http）を通すために
	//    ここでは検証部分だけを直に確かめる。
	if _, err := jiraAccount(&secrets.JiraCreds{Site: bad.URL, Email: "a@example.com", Token: "t"}); err == nil {
		t.Error("bad credentials were accepted")
	}
	name, err := jiraAccount(&secrets.JiraCreds{Site: ok.URL, Email: "a@example.com", Token: "t"})
	if err != nil || name != "山田 太郎" {
		t.Errorf("jiraAccount = %q, %v", name, err)
	}
	// http:// は入口で弾く（Basic 認証が平文で飛ぶ）。
	if w := put(bad.URL); w.Code != http.StatusBadRequest {
		t.Errorf("http site accepted: %d", w.Code)
	}
	s, _ := secrets.Load()
	if s.Jira != nil {
		t.Error("a rejected connection was still written to the store")
	}
}
