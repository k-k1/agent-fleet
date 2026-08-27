package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
	// ★ labels を持たない課題でも nil を載せない。nil スライスは JSON の null になり、
	// 受け手が配列として扱えない（Console を真っ白にした形と同じ）。
	if rows[2].Labels == nil {
		t.Error("a label-less issue produced a nil slice, which marshals as null")
	}
	if enc, _ := json.Marshal(rows[2]); strings.Contains(string(enc), `"labels":null`) {
		t.Errorf("row wire carries a null array: %s", enc)
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

// --- OAuth（docs/80 §80.17）-------------------------------------------------

// ★ 3LO のトークンはサイトのホストでは通らない。API のベースが認証方式で変わることを
// 固定する —— ここを間違えると症状は 401 になり、「トークンが違う」と読めてしまう。
func TestJiraAPIBaseSwitchesWithAuthKind(t *testing.T) {
	tokenAuth := &secrets.JiraCreds{Site: "https://example.atlassian.net", Email: "a@example.com", Token: "t"}
	if got := jiraAPIBase(tokenAuth); got != "https://example.atlassian.net" {
		t.Errorf("token base = %q", got)
	}
	oauth := &secrets.JiraCreds{AuthKind: "oauth", Site: "https://example.atlassian.net", CloudID: "cid-1", AccessToken: "at"}
	if got := jiraAPIBase(oauth); got != "https://api.atlassian.com/ex/jira/cid-1" {
		t.Errorf("oauth base = %q", got)
	}
	// 認証ヘッダも切り替わる。
	if h := jiraAuthHeader(oauth); h != "Bearer at" {
		t.Errorf("oauth header = %q", h)
	}
	if h := jiraAuthHeader(tokenAuth); !strings.HasPrefix(h, "Basic ") {
		t.Errorf("token header = %q", h)
	}
	// Site は人が見る URL のまま（browse リンクは api.atlassian.com ではない）。
	if oauth.Site != "https://example.atlassian.net" {
		t.Errorf("oauth site = %q", oauth.Site)
	}
}

// 「接続済み」の判定に Token 欄を使うと、OAuth 接続が未接続に見える。
func TestJiraConnected(t *testing.T) {
	if jiraConnected(nil) {
		t.Error("nil is connected")
	}
	if jiraConnected(&secrets.JiraCreds{Site: "https://x"}) {
		t.Error("a site alone is not a connection")
	}
	if !jiraConnected(&secrets.JiraCreds{Token: "t"}) {
		t.Error("api token path not recognised")
	}
	if !jiraConnected(&secrets.JiraCreds{AuthKind: "oauth", AccessToken: "at"}) {
		t.Error("oauth path not recognised")
	}
	// アクセストークンが切れていても、更新トークンがあるなら接続は生きている。
	if !jiraConnected(&secrets.JiraCreds{AuthKind: "oauth", RefreshToken: "rt"}) {
		t.Error("an expired-but-refreshable connection reads as disconnected")
	}
}

// 認可のあと何が起きるかを丸ごと固定する。★ サイトの解決は「保存の一部」——
// cloud id が無い 3LO 接続は API を 1 本も叩けないので、トークンだけ保存して
// あとで解決する形にすると「カードは接続済み・レールは 401」という一番分かりにくい
// 状態になる。
func TestJiraOAuthStoreResolvesSitesAndPicksOne(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var authSeen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authSeen = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/oauth/token/accessible-resources":
			_, _ = w.Write([]byte(`[{"id":"cid-1","url":"https://one.atlassian.net/","name":"One"},
			                        {"id":"cid-2","url":"https://two.atlassian.net","name":"Two"}]`))
		case "/ex/jira/cid-1/rest/api/3/myself":
			_, _ = w.Write([]byte(`{"displayName":"山田 太郎"}`))
		case "/ex/jira/cid-2/rest/api/3/myself":
			_, _ = w.Write([]byte(`{"displayName":"Taro on Two"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	old := jiraCloudAPIBase
	jiraCloudAPIBase = srv.URL
	defer func() { jiraCloudAPIBase = old }()

	w := httptest.NewRecorder()
	handleJiraOAuthStore(w, httptest.NewRequest("PUT", "/connections/jira/oauth",
		strings.NewReader(`{"access_token":"at","refresh_token":"rt","expires_in":3600}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("store = %d (%s)", w.Code, w.Body.String())
	}
	if authSeen != "Bearer at" {
		t.Errorf("site resolution used %q, want a Bearer token", authSeen)
	}
	s, _ := secrets.Load()
	if s.Jira == nil || s.Jira.AuthKind != "oauth" {
		t.Fatalf("stored = %+v", s.Jira)
	}
	if len(s.Jira.Sites) != 2 {
		t.Fatalf("sites = %+v", s.Jira.Sites)
	}
	// 既定は先頭。URL の末尾スラッシュは落とす（browse リンクが // になる）。
	if s.Jira.CloudID != "cid-1" || s.Jira.Site != "https://one.atlassian.net" {
		t.Errorf("default site = %q / %q", s.Jira.CloudID, s.Jira.Site)
	}
	if s.Jira.Account != "山田 太郎" {
		t.Errorf("account = %q", s.Jira.Account)
	}
	if s.Jira.Expiry <= time.Now().Unix() {
		t.Errorf("expiry not stamped: %d", s.Jira.Expiry)
	}

	// サイトの切り替え —— 認可に含まれるものだけ。
	w = httptest.NewRecorder()
	handlePutJiraSite(w, httptest.NewRequest("PUT", "/connections/jira/site", strings.NewReader(`{"cloudId":"cid-2"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("site switch = %d (%s)", w.Code, w.Body.String())
	}
	s, _ = secrets.Load()
	if s.Jira.CloudID != "cid-2" || s.Jira.Site != "https://two.atlassian.net" {
		t.Errorf("after switch = %q / %q", s.Jira.CloudID, s.Jira.Site)
	}
	if s.Jira.Account != "Taro on Two" {
		t.Errorf("account not re-resolved for the new site: %q", s.Jira.Account)
	}
	w = httptest.NewRecorder()
	handlePutJiraSite(w, httptest.NewRequest("PUT", "/connections/jira/site", strings.NewReader(`{"cloudId":"cid-999"}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("a site outside the authorization was accepted: %d", w.Code)
	}
}

// 認可は通ったのにサイトが 0 件 —— スコープ不足かサイト未所属。接続済みにしない。
func TestJiraOAuthStoreRefusesWhenNoSites(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	old := jiraCloudAPIBase
	jiraCloudAPIBase = srv.URL
	defer func() { jiraCloudAPIBase = old }()

	w := httptest.NewRecorder()
	handleJiraOAuthStore(w, httptest.NewRequest("PUT", "/connections/jira/oauth",
		strings.NewReader(`{"access_token":"at","refresh_token":"rt"}`)))
	if w.Code == http.StatusOK {
		t.Fatalf("stored a connection that cannot call anything: %s", w.Body.String())
	}
	s, _ := secrets.Load()
	if s.Jira != nil {
		t.Error("a refused authorization was written to the store")
	}
}

func TestJiraStatusHidesSecretsAndNamesTheAuthKind(t *testing.T) {
	s := &secrets.Data{Jira: &secrets.JiraCreds{
		AuthKind: "oauth", AccessToken: "super-secret", RefreshToken: "also-secret",
		Site: "https://example.atlassian.net", CloudID: "cid-1", Account: "山田 太郎",
		Sites: []secrets.JiraSite{{CloudID: "cid-1", URL: "https://example.atlassian.net", Name: "Example"}},
	}}
	st := jiraStatus(s)
	enc, _ := json.Marshal(st)
	for _, secret := range []string{"super-secret", "also-secret"} {
		if strings.Contains(string(enc), secret) {
			t.Fatalf("status leaked a token: %s", enc)
		}
	}
	if st["authKind"] != "oauth" {
		t.Errorf("authKind = %v", st["authKind"])
	}
	if st["cloudId"] != "cid-1" {
		t.Errorf("cloudId = %v", st["cloudId"])
	}
	// ⚠️ OAuth 側にメールは無い。空文字を出すと「メールが消えた」と読める。
	if _, ok := st["email"]; ok {
		t.Errorf("oauth status carries an email field: %v", st)
	}
	// 旧ストア（authKind 無し）は token 扱いのまま動く。
	old := &secrets.Data{Jira: &secrets.JiraCreds{Site: "https://x", Email: "a@example.com", Token: "t"}}
	if got := jiraStatus(old)["authKind"]; got != "token" {
		t.Errorf("legacy store authKind = %v, want token", got)
	}
}

// 期限が近ければ要求の前に更新する。⚠️ 更新の結果は保存する —— Atlassian は更新トークンを
// ローテートするので、保存を落とすと次の期限で接続が詰む。
func TestJiraEnsureFreshOnlyWhenOAuthAndDue(t *testing.T) {
	// token 認証は期限を持たないので、何もしない（ブリッジが無くてもエラーにしない）。
	tokenAuth := &secrets.JiraCreds{Site: "https://x", Token: "t"}
	if err := jiraEnsureFresh(tokenAuth); err != nil {
		t.Errorf("token path tried to refresh: %v", err)
	}
	// 期限が十分先なら触らない。
	future := &secrets.JiraCreds{AuthKind: "oauth", AccessToken: "at", Expiry: time.Now().Add(time.Hour).Unix()}
	if err := jiraEnsureFresh(future); err != nil {
		t.Errorf("fresh token refreshed anyway: %v", err)
	}
	// 期限切れならブリッジを探しに行き、無ければその旨のエラー（黙って古い token で叩かない）。
	t.Setenv("HOME", t.TempDir())
	expired := &secrets.JiraCreds{AuthKind: "oauth", AccessToken: "at", RefreshToken: "rt", Expiry: 1}
	if err := jiraEnsureFresh(expired); err == nil {
		t.Error("an expired token was used without a refresh")
	}
}

// ★ ローテートする更新トークンは 1 回きり。使い終わったものをもう一度出すのは
// Atlassian が「盗用」とみなす操作で、認可ごと取り消されうる（＝利用者からは
// 「Jira が勝手に切れた」に見える）。同時に 2 か所が期限切れに気づいても、交換は
// 1 回だけ走ることを固定する。
func TestJiraRefreshIsSerializedAndNotRepeated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var mu sync.Mutex
	var seen []string // 受け取った refresh token の並び
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, body.RefreshToken)
		n := len(seen)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond) // 交換に時間がかかる状況を作る
		_, _ = w.Write([]byte(fmt.Sprintf(`{"access_token":"at%d","refresh_token":"rt%d","expires_in":3600}`, n, n)))
	}))
	defer srv.Close()

	// ブリッジをストアに置く（CP 経由の更新経路）。
	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	s.GitOAuthBridge = &secrets.CPBridge{BaseURL: srv.URL, Token: "afo_x"}
	s.Jira = &secrets.JiraCreds{AuthKind: "oauth", AccessToken: "old", RefreshToken: "rt0", Expiry: 1,
		CloudID: "cid", Site: "https://x.atlassian.net"}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	// レールの取得とコメント投稿が同時に期限切れに気づいた、という形。
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cur, _ := secrets.Load()
			if cur == nil || cur.Jira == nil {
				return
			}
			c := *cur.Jira
			_ = jiraEnsureFresh(&c)
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("refresh grant ran %d times (%v) — a rotated token must not be presented twice", len(seen), seen)
	}
	if seen[0] != "rt0" {
		t.Errorf("exchanged %q, want the stored refresh token", seen[0])
	}
	after, _ := secrets.Load()
	if after.Jira.RefreshToken != "rt1" || after.Jira.AccessToken != "at1" {
		t.Errorf("rotation not persisted: %+v", after.Jira)
	}
	if after.Jira.Expiry <= time.Now().Unix() {
		t.Errorf("expiry not moved forward: %d", after.Jira.Expiry)
	}
}

// docs/80 §80.18.6 — 取得は 1 クエリ 50 件で切るので、並び順が無い JQL では
// 「どの 50 件が残るか」が Jira 任せになる。レールは「新しい順の上位 N 件」を
// 名乗っているので、そこを不定にしたままにはできない。
func TestJiraOrderedJQL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"assignee = currentUser() AND statusCategory != Done",
			"assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC"},
		// 利用者が書いた並び順はそのまま（大小文字・空白の揺れも同じ扱い）。
		{"project = G3M ORDER BY priority DESC", "project = G3M ORDER BY priority DESC"},
		{"project = G3M order by created", "project = G3M order by created"},
		{"project = G3M ORDER   BY created", "project = G3M ORDER   BY created"},
		// 語の一部に order を含むだけの JQL は「並び順あり」ではない。
		{`summary ~ "reorder"`, `summary ~ "reorder" ORDER BY updated DESC`},
		{"  ", ""},
	}
	for _, c := range cases {
		if got := jiraOrderedJQL(c.in); got != c.want {
			t.Errorf("jiraOrderedJQL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
