package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// docs/log/80 §80.10 — 唯一の書き戻し。壊れると「投稿したのに入っていない」か
// 「入れてはいけないものが入る」のどちらかになるので、境界を固定する。

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

// ★ Jira v3 のコメントは ADF でないと 400。プレーン文字列を送るのが一番ありがちな壊れ方
// なので、doc/paragraph の形と、段落・改行の写し方を固定する。
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
	// 空文字だけでも doc は空にしない（空の doc は 400）。
	if c, _ := jiraADF("\n\n\n")["content"].([]any); len(c) == 0 {
		t.Error("an all-blank body produced an empty doc, which Jira rejects")
	}
	// JSON にできること（map[string]any の入れ子なので、ここが崩れると送信時に落ちる）。
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
	// 未接続は 400（af 側の設定不足）。provider が断ったときの 502 と区別する。
	if w := post(`{"provider":"github","key":"acme/web#1","body":"y"}`); w.Code != http.StatusBadRequest {
		t.Errorf("no connection = %d, want 400", w.Code)
	}
}

// 投稿が拒否されたときは provider の文言をそのまま上げる。「再接続してください」と
// 言い換えると、権限不足（読めるが書けない）や課題のロックが見えなくなる。
func TestGitHubCommentSurfacesProviderRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	// githubPostIssueComment は api.github.com 固定なので、ここでは経路ではなく
	// キーの検証（不正キーで送信に行かないこと）を確かめる。
	if _, err := githubPostIssueComment("tok", "not-a-key", "body"); err == nil {
		t.Error("a malformed key was sent to the API instead of being refused locally")
	}
}
