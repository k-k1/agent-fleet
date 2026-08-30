package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// docs/log/80 / ADR 0061 — 一覧に載るのは provider の応答をどう写したかで決まる。
// ここが本体なので、ネットワーク無しで固定する。

func TestParseGitHubSearchItems(t *testing.T) {
	body := []byte(`{"items":[
	  {"number":45,"title":"ログイン後に一覧が空になる","state":"open",
	   "html_url":"https://github.com/acme/web/issues/45",
	   "repository_url":"https://api.github.com/repos/acme/web",
	   "updated_at":"2026-08-25T01:02:03Z",
	   "assignees":[{"login":"taro"},{"login":"hanako"}],
	   "labels":[{"name":"bug"},{"name":"p1"}]},
	  {"number":46,"title":"draft pr","state":"open","draft":true,
	   "html_url":"https://github.com/acme/web/pull/46",
	   "repository_url":"https://api.github.com/repos/acme/web",
	   "updated_at":"2026-08-24T00:00:00Z",
	   "pull_request":{"url":"https://api.github.com/repos/acme/web/pulls/46"}},
	  {"number":47,"title":"closed one","state":"closed",
	   "html_url":"https://github.com/acme/web/issues/47",
	   "repository_url":"https://api.github.com/repos/acme/web"}
	]}`)
	rows, err := parseGitHubSearchItems(body, "q1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	got := rows[0]
	if got.QueryID != "q1" || got.Provider != "github" {
		t.Errorf("query/provider not stamped: %+v", got)
	}
	if got.Key != "acme/web#45" {
		t.Errorf("key = %q, want acme/web#45", got.Key)
	}
	if got.Repo != "acme/web" {
		t.Errorf("repo = %q", got.Repo)
	}
	if got.Kind != "issue" || got.State != "open" {
		t.Errorf("kind/state = %q/%q", got.Kind, got.State)
	}
	// 担当者は先頭 1 人だけ（行は 1 行で、全員は入らない）。
	if got.Assignee != "taro" {
		t.Errorf("assignee = %q, want taro", got.Assignee)
	}
	if strings.Join(got.Labels, ",") != "bug,p1" {
		t.Errorf("labels = %v", got.Labels)
	}
	if rows[1].Kind != "pr" || rows[1].State != "in_progress" {
		t.Errorf("draft PR = %q/%q, want pr/in_progress", rows[1].Kind, rows[1].State)
	}
	if rows[2].State != "done" {
		t.Errorf("closed = %q, want done", rows[2].State)
	}
	// ★ 本文・コメントは返さない（ADR 0061 決定 2）。応答に body が現れないことを
	// 構造ではなく実際の JSON で確かめる —— フィールドを足した瞬間にここが落ちる。
	enc, _ := json.Marshal(rows)
	for _, forbidden := range []string{`"body"`, `"comments"`, `"description"`} {
		if strings.Contains(string(enc), forbidden) {
			t.Errorf("row JSON leaks %s: %s", forbidden, enc)
		}
	}
}

func TestNormalizeGitHubState(t *testing.T) {
	for _, tc := range []struct {
		state string
		draft bool
		want  string
	}{
		{"open", false, "open"},
		{"OPEN", false, "open"},
		{"open", true, "in_progress"},
		{"closed", false, "done"},
		{"closed", true, "done"},
		{"", false, "other"},
		{"merged", false, "other"},
	} {
		if got := normalizeGitHubState(tc.state, tc.draft); got != tc.want {
			t.Errorf("normalizeGitHubState(%q,%v) = %q, want %q", tc.state, tc.draft, got, tc.want)
		}
	}
}

func TestRepoFromGitHubAPIURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://api.github.com/repos/acme/web", "acme/web"},
		{"https://api.github.com/repos/acme/web/", "acme/web"},
		{"https://api.github.com/repos/acme", ""},
		{"https://api.github.com/repos/acme/web/extra", ""},
		{"", ""},
	} {
		if got := repoFromGitHubAPIURL(tc.in); got != tc.want {
			t.Errorf("repoFromGitHubAPIURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// 未接続・未知の provider は「その 1 本のクエリのエラー」として返り、リクエスト全体は
// 200 で通る —— 1 本の失敗で棚全体が空になってはいけない（docs/log/80 §80.6）。
func TestWorkItemsFetchPerQueryErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	body := `{"queries":[{"id":"q1","provider":"github","query":"assignee:@me"},
	                     {"id":"q2","provider":"jira","query":"assignee = currentUser()"},
	                     {"id":"q3","provider":"github","query":"  "},
	                     {"id":"q4","provider":"backlog","query":"x"}]}`
	req := httptest.NewRequest("POST", "/work-items/fetch", strings.NewReader(body))
	w := httptest.NewRecorder()
	handleWorkItemsFetch(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (per-query failures are not request failures)", w.Code)
	}
	var out struct {
		Items  []workItemOut    `json:"items"`
		Errors []workItemErrOut `json:"errors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if len(out.Errors) != 4 {
		t.Fatalf("want 4 per-query errors, got %d (%s)", len(out.Errors), w.Body.String())
	}
	byID := map[string]string{}
	for _, e := range out.Errors {
		byID[e.QueryID] = e.Message
	}
	if !strings.Contains(byID["q1"], "not connected") {
		t.Errorf("q1 (no GitHub connection) = %q", byID["q1"])
	}
	// jira は P1 で対応済み。未接続は「未接続」と言う —— ここが "unsupported" のままだと、
	// 接続すれば直る話を「af が Jira に対応していない」と読ませてしまう。
	if !strings.Contains(byID["q2"], "Jira is not connected") {
		t.Errorf("q2 (jira, no connection) = %q", byID["q2"])
	}
	if !strings.Contains(byID["q3"], "empty") {
		t.Errorf("q3 (blank query) = %q", byID["q3"])
	}
	if !strings.Contains(byID["q4"], "unsupported provider") {
		t.Errorf("q4 (unknown provider) = %q", byID["q4"])
	}
}

func TestWorkItemsFetchEmptyRequest(t *testing.T) {
	req := httptest.NewRequest("POST", "/work-items/fetch", strings.NewReader(`{"queries":[]}`))
	w := httptest.NewRecorder()
	handleWorkItemsFetch(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	// 空でも配列を返す（null は Console 側で「壊れている」と見分けがつかない）。
	if got := strings.TrimSpace(w.Body.String()); !strings.Contains(got, `"items":[]`) {
		t.Errorf("body = %s, want an empty items array", got)
	}
}
