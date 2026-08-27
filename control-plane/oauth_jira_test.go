// oauth_jira_test.go — Jira 3LO（docs/80 §80.17）。実 Atlassian は CI にも
// この環境にも無いので、固定するのは「af が組み立てるもの」と「秘密の置き場所」。
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ★ 認可 URL の 4 点を固定する。どれが欠けても症状は「連携したのに動かない」で、
// 原因が認可 URL だとは分からない形になる:
//   - audience=api.atlassian.com（無いと 3LO のトークンが API 用にならない）
//   - offline_access（無いと refresh token が返らず 1 時間で死ぬ）
//   - prompt=consent（無いと再認可で refresh token が返らない）
//   - write:jira-work（利用者の選択でコメント投稿まで含める・docs/80 §80.10）
func TestJiraAuthorizeURLShape(t *testing.T) {
	au := jiraAuthorizeURL +
		"?audience=api.atlassian.com" +
		"&client_id=" + url.QueryEscape("cid") +
		"&scope=" + url.QueryEscape(jiraScopes) +
		"&redirect_uri=" + url.QueryEscape("https://af.example/api/oauth/jira/callback") +
		"&state=abc&response_type=code&prompt=consent"
	u, err := url.Parse(au)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "auth.atlassian.com" {
		t.Errorf("host = %q — Jira does not authorize at bitbucket.org", u.Host)
	}
	if q.Get("audience") != "api.atlassian.com" {
		t.Error("missing audience")
	}
	if q.Get("prompt") != "consent" {
		t.Error("missing prompt=consent")
	}
	if q.Get("response_type") != "code" {
		t.Error("missing response_type")
	}
	for _, want := range []string{"read:jira-work", "read:jira-user", "write:jira-work", "offline_access"} {
		if !strings.Contains(q.Get("scope"), want) {
			t.Errorf("scope %q is missing %q", q.Get("scope"), want)
		}
	}
}

// トークン交換は JSON ボディ（Bitbucket の form+Basic とは違う）。ここを Bitbucket と
// 同じ形で送ると 400 になり、「アプリの設定ミス」と読める。
func TestJiraExchangeCodeUsesJSONBody(t *testing.T) {
	var gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600}`))
	}))
	defer srv.Close()
	old := jiraTokenURL
	jiraTokenURL = srv.URL
	defer func() { jiraTokenURL = old }()

	tok, err := jiraExchangeCode("cid", "csecret", "the-code", "https://af.example/cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if tok.AccessToken != "at" || tok.RefreshToken != "rt" || tok.ExpiresIn != 3600 {
		t.Errorf("token = %+v", tok)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("body was not JSON: %s", gotBody)
	}
	// client_secret はボディで運ぶ（Basic ではない）。
	if sent["client_secret"] != "csecret" || sent["grant_type"] != "authorization_code" {
		t.Errorf("body = %v", sent)
	}
	if sent["redirect_uri"] != "https://af.example/cb" {
		t.Errorf("redirect_uri = %q", sent["redirect_uri"])
	}
}

func TestJiraExchangeCodeReportsRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	old := jiraTokenURL
	jiraTokenURL = srv.URL
	defer func() { jiraTokenURL = old }()

	if _, err := jiraExchangeCode("cid", "s", "bad", "https://af.example/cb"); err == nil {
		t.Fatal("a refused exchange was reported as success")
	} else if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error = %q, want the provider's own words", err)
	}
}

// リフレッシュも JSON。★ 4xx は invalid_grant（＝再接続が要る）として即返し、
// 5xx / 429 だけ再試行する —— 取り消された認可を毎回叩き続けないため。
func TestJiraRefreshGrant(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"at2","refresh_token":"rt2","expires_in":3600}`))
	}))
	defer srv.Close()
	old := jiraTokenURL
	jiraTokenURL = srv.URL
	defer func() { jiraTokenURL = old }()

	tok, aerr := jiraRefreshGrant("cid", "s", "rt1")
	if aerr != nil {
		t.Fatalf("refresh: %v", aerr)
	}
	// ⚠️ Atlassian は refresh token をローテートする。返ってきた新しい方を運ばないと、
	// Agent は古い方を保存し続けて次の期限で詰む。
	if tok.AccessToken != "at2" || tok.RefreshToken != "rt2" {
		t.Errorf("token = %+v", tok)
	}
	if hits != 2 {
		t.Errorf("hits = %d, want the 5xx to be retried once", hits)
	}

	hits = 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv2.Close()
	jiraTokenURL = srv2.URL
	_, aerr = jiraRefreshGrant("cid", "s", "revoked")
	if aerr == nil || aerr.code != "invalid_grant" {
		t.Fatalf("aerr = %+v, want invalid_grant", aerr)
	}
	if hits != 1 {
		t.Errorf("hits = %d — a revoked refresh token must not be retried", hits)
	}
}

// provider の閉じた集合に jira が入り、コールバック URL が provider ごとに出る。
// ⚠️ GitHub は device flow なので URL を持たない（持たせると管理画面が「登録しろ」と
// 言えない値を表示することになる）。
func TestJiraIsAGitOAuthProvider(t *testing.T) {
	if !validGitOAuthProvider(gitOAuthJira) {
		t.Fatal("jira is not in the closed provider set")
	}
	if !gitOAuthNeedsSecret(gitOAuthJira) {
		t.Error("jira's code grant needs a client secret")
	}
	m := &manager{publicBaseURL: "https://af.example/"}
	if got := m.gitOAuthRedirectURI(gitOAuthJira); got != "https://af.example/api/oauth/jira/callback" {
		t.Errorf("jira redirect = %q", got)
	}
	if got := m.gitOAuthRedirectURI(gitOAuthBitbucket); got != "https://af.example/api/oauth/bitbucket/callback" {
		t.Errorf("bitbucket redirect = %q", got)
	}
	if got := m.gitOAuthRedirectURI(gitOAuthGitHub); got != "" {
		t.Errorf("github redirect = %q, want empty (device flow)", got)
	}
	none := &manager{}
	if got := none.gitOAuthRedirectURI(gitOAuthJira); got != "" {
		t.Errorf("no PUBLIC_BASE_URL should yield no redirect, got %q", got)
	}
}

// コールバックの文言は provider 名で差し替わる（Bitbucket と Jira で 9 本ずつ
// 手書きすると必ずずれる）。
func TestOAuthCallbackTextNamesTheProvider(t *testing.T) {
	ja := oauthCallbackText("ja", "Jira")
	if !strings.Contains(ja.notConfigured, "Jira") || !strings.Contains(ja.success, "Jira") {
		t.Errorf("ja text does not name the provider: %+v", ja)
	}
	en := oauthCallbackText("en", "Bitbucket")
	if !strings.Contains(en.success, "Bitbucket") {
		t.Errorf("en text does not name the provider: %+v", en)
	}
}
