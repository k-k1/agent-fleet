package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// docs/log/61 §61.15 / ADR0043 決定 34 / 35 — テナントが GitHub をサインイン方法にする。
// P4（OIDC）と違うのは信頼の根拠だけで、ここで固定するのはその差が実装から消えて
// いないこと:
//
//   - github 行は org と受け入れドメインの両方が要る（issuer は共有なので、org が
//     テナントの境界そのもの）
//   - 1 ドメイン 1 テナントの台帳は kind を問わず効く
//   - org の追加は承認のやり直し（承認は「この org のメンバーを」に対して与えた）
//   - 規則 1.5: 同じ GitHub アカウントなら、デプロイのボタンでもテナントのボタンでも
//     同じ人。email 一致ではないので、テナントが他人を名乗る道は開かない

// --- 行 → アダプタ ------------------------------------------------------------

func TestTenantGitHubRowBuildsTheGitHubAdapter(t *testing.T) {
	base := store.TenantIdP{
		Name: "github", Kind: tenantIdPKindGitHub, ClientID: "c",
		AllowedOrgs: "Acme-Sub, other", AllowedDomains: "@sub.co.jp",
	}
	p, err := buildTenantProvider(base, store.TenantRef{Slug: "sub"}, "s")
	if err != nil {
		t.Fatalf("the valid row must build: %v", err)
	}
	gh, ok := p.(*githubProvider)
	if !ok {
		t.Fatalf("kind=github must build the GitHub adapter, got %T", p)
	}
	if gh.ID() != "t:sub:github" {
		t.Fatalf("id = %q", gh.ID())
	}
	// org は小文字化して突合する（GitHub の org 名は大文字小文字を区別しない）。
	if strings.Join(gh.allowedOrgs, ",") != "acme-sub,other" {
		t.Fatalf("orgs = %v", gh.allowedOrgs)
	}
	if !gh.allowDomains["sub.co.jp"] {
		t.Fatalf("domains = %v", gh.allowDomains)
	}
	// ★ 行から github.com を差し替えられないこと。ここが動かせると、テナントが
	// 自分のサーバを立てて任意の subject を名乗れる＝規則 1.5 の鍵が偽造できる。
	if gh.web() != "https://github.com" || gh.api() != "https://api.github.com" {
		t.Fatalf("row must not be able to move the endpoints: %s / %s", gh.web(), gh.api())
	}
	// realm は「どこで身元が証明されたか」。これが env の GitHub と一致することが
	// 規則 1.5 の前提（一致しなければ別人になる）。
	if providerRealm(gh) != githubWebBase {
		t.Fatalf("realm = %q", providerRealm(gh))
	}
	// ★ デプロイ共通の許可リストにも名簿にもフォールバックしない（決定 32-3）。
	if gh.deployAllowed != nil || gh.dbAllowed != nil || gh.deployHasList {
		t.Fatal("a tenant row must not inherit the deployment-wide entry gate")
	}

	bad := map[string]func(*store.TenantIdP){
		"no orgs":    func(r *store.TenantIdP) { r.AllowedOrgs = "" },
		"no domains": func(r *store.TenantIdP) { r.AllowedDomains = "" },
		"no client":  func(r *store.TenantIdP) { r.ClientID = "" },
		"bad name":   func(r *store.TenantIdP) { r.Name = "Git Hub" },
		"other kind": func(r *store.TenantIdP) { r.Kind = "saml" },
	}
	for label, mutate := range bad {
		row := base
		mutate(&row)
		if _, err := buildTenantProvider(row, store.TenantRef{Slug: "sub"}, "s"); err == nil {
			t.Fatalf("%s: must be refused", label)
		}
	}
	// 秘密が空（復号できなかった等）でも組めてはいけない。
	if _, err := buildTenantProvider(base, store.TenantRef{Slug: "sub"}, ""); err == nil {
		t.Fatal("an empty client_secret must be refused")
	}
}

// --- 保存時の検証（API 側と実行時側で同じ規則） --------------------------------

func TestTenantGitHubSaveTimeValidation(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sub", "子会社")
	other, _ := st.CreateTenant(ctx, "sibling", "別会社")
	seedTenantIdP(t, st, other.ID, "entra", "sibling.co.jp", "active")
	if _, err := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("super admin: %v", err)
	}
	api := newTenantIdPAPI(mgr, nil)
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/admin/tenants/sub/idp", strings.NewReader(body))
		r.SetPathValue("slug", "sub")
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		w := httptest.NewRecorder()
		api.upsert(w, r)
		return w
	}

	if w := post(`{"name":"github","kind":"github","client_id":"c","client_secret":"s","allowed_domains":"@sub.co.jp"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no orgs must be refused — membership is what authorizes the login: %d %s", w.Code, w.Body.String())
	}
	if w := post(`{"name":"github","kind":"github","client_id":"c","client_secret":"s","allowed_orgs":"acme-sub"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no domains must be refused: %d %s", w.Code, w.Body.String())
	}
	// ★ 1 ドメイン 1 テナントは kind を問わない。GitHub は他社ドメインの verified
	// email を偽造できないが、台帳の枠を先に取ること自体が他社の登録を塞ぐ。
	if w := post(`{"name":"github","kind":"github","client_id":"c","client_secret":"s","allowed_orgs":"acme-sub","allowed_domains":"sibling.co.jp"}`); w.Code != http.StatusConflict {
		t.Fatalf("a domain another tenant claims must be refused: %d %s", w.Code, w.Body.String())
	}
	if w := post(`{"name":"github","kind":"saml","client_id":"c","client_secret":"s","allowed_domains":"@sub.co.jp"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("an unknown kind must be refused: %d %s", w.Code, w.Body.String())
	}
	if w := post(`{"name":"github","kind":"github","client_id":"c","client_secret":"s","allowed_orgs":"Acme-Sub","allowed_domains":"@SUB.co.jp","issuer":"https://evil.example/","trust":"issuer"}`); w.Code != http.StatusOK {
		t.Fatalf("the valid row must be accepted: %d %s", w.Code, w.Body.String())
	}
	rows, _ := st.ListTenantIdPs(ctx, tn.ID)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	// ★ issuer と trust はフォームから来ない。github 行の身元の出どころは 1 つしか
	// なく、そこを行が名乗れると登録簿と監査ログが嘘をつく。
	switch row := rows[0]; {
	case row.Issuer != githubWebBase:
		t.Fatalf("issuer = %q, want the constant %q", row.Issuer, githubWebBase)
	case row.Trust != trustAPI:
		t.Fatalf("trust = %q, want %q", row.Trust, trustAPI)
	case row.AllowedOrgs != "acme-sub":
		t.Fatalf("orgs = %q (must be normalized on save)", row.AllowedOrgs)
	case row.AllowedDomains != "sub.co.jp":
		t.Fatalf("domains = %q (must be normalized on save)", row.AllowedDomains)
	case row.Status != "pending":
		t.Fatalf("status = %q — a new row is never born active (決定 30)", row.Status)
	}
}

// ★ 承認は「この org のメンバーを、このドメイン範囲で」に対して与えたもの。org が
// 増えれば対象の人の集合が変わるので、承認をやり直す。減るのは戻さない。
func TestGitHubRowRepends(t *testing.T) {
	active := store.TenantIdP{
		Kind: tenantIdPKindGitHub, Status: "active", ClientID: "c", Issuer: githubWebBase,
		Trust: trustAPI, AllowedOrgs: "acme-sub", AllowedDomains: "sub.co.jp",
	}
	widened := active
	widened.AllowedOrgs = "acme-sub,acme-parent"
	if !repend(active, widened) {
		t.Fatal("adding an organization must send the row back for approval")
	}
	narrowed := active
	narrowed.AllowedOrgs = ""
	if repend(active, narrowed) {
		t.Fatal("removing an organization admits fewer people and must not repend")
	}
	rotated := active
	rotated.SecretEnc = "new"
	if repend(active, rotated) {
		t.Fatal("a client_secret rotation must not repend (or nobody rotates)")
	}
	switched := active
	switched.Kind = tenantIdPKindOIDC
	if !repend(active, switched) {
		t.Fatal("changing the kind changes what was approved")
	}
}

// --- 規則 1.5（決定 35）--------------------------------------------------------

// ★ 同じ GitHub アカウントを、デプロイのボタンとテナントのボタンから押した場合。
// P4 のままだと 2 つ目が email_taken で拒否され、兼務の人が締め出される。realm と
// subject が一致する＝ GitHub が同一アカウントだと言っている、が根拠。
func TestRule15JoinsTheSameIdPAccountAcrossButtons(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	const email = "yamada@acme.co.jp"

	first, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: githubProviderID, Subject: "42", Realm: githubWebBase, Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: true,
	})
	if err != nil {
		t.Fatalf("deployment github: %v", err)
	}
	// テナント定義の行なので EmailJoin=false — つまり規則 2 は使えない。
	second, isNew, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "t:sub:github", Subject: "42", Realm: githubWebBase, Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: false,
	})
	if err != nil {
		t.Fatalf("tenant github must not be refused: %v", err)
	}
	if isNew || second.ID != first.ID || second.UserKey != first.UserKey {
		t.Fatalf("同じ GitHub アカウントが別人になった: %+v want %+v (isNew=%v)", second, first, isNew)
	}
	if n := countRows(t, st, "identity"); n != 1 {
		t.Fatalf("identity rows = %d, want 1", n)
	}

	// ★ realm が違えば、subject がたまたま同じでも別物。数値 subject は IdP を
	// またぐと衝突し得るので、ここを緩めると他人の workspace に着地する。
	if _, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "t:sub:keycloak", Subject: "42", Realm: "https://idp.sub.co.jp/realms/x",
		Email: email, FallbackKey: sanitizeUser(email), EmailJoin: false,
	}); err == nil {
		t.Fatal("別 realm・同 subject は結合してはいけない（email はすでに他人のもの）")
	}

	// realm を持たない行（0041 以前・proxy 経由）は従来どおり拒否のまま。
	if _, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "t:sub:entra", Subject: "99", Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: false,
	}); err == nil {
		t.Fatal("realm 無しで email だけ一致する行は拒否のまま（決定 32）")
	}
}

// 起動時の埋め戻し。0041 より前に書かれた行は realm が空で、そのままだと
// 「デプロイの GitHub で入っていた人」がテナントのボタンで拒否される。
func TestFillProviderRealmMakesOldRowsJoinable(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	const email = "yamada@acme.co.jp"

	// realm を知らなかった頃のログイン。
	first, _, err := st.LinkIdentity(ctx, linkOf(githubProviderID, "42", email, true))
	if err != nil {
		t.Fatalf("legacy login: %v", err)
	}
	if _, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "t:sub:github", Subject: "42", Realm: githubWebBase, Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: false,
	}); err == nil {
		t.Fatal("埋め戻す前は拒否される（この状態を直すのが FillProviderRealm）")
	}

	if err := st.FillProviderRealm(ctx, githubProviderID, githubWebBase); err != nil {
		t.Fatalf("fill: %v", err)
	}
	joined, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "t:sub:github", Subject: "42", Realm: githubWebBase, Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: false,
	})
	if err != nil {
		t.Fatalf("after fill: %v", err)
	}
	if joined.ID != first.ID {
		t.Fatalf("埋め戻し後も別人のまま: %s want %s", joined.ID, first.ID)
	}

	// ★ 既に記録された realm は上書きしない（別の provider id に付け替えられた
	// デプロイで、過去のログインの事実を書き換えないため）。
	if err := st.FillProviderRealm(ctx, "t:sub:github", "https://idp.example/"); err != nil {
		t.Fatalf("re-fill: %v", err)
	}
	var realm string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT realm FROM identity_provider WHERE provider='t:sub:github'`).Scan(&realm); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if realm != githubWebBase {
		t.Fatalf("realm was overwritten: %q", realm)
	}
}

// --- 受け入れるが出さない（§61.15.9）------------------------------------------

// ★ 子会社が「自社の GitHub だけ」で運用したいのに、本社から来ている兼務の人は
// 本社の方式でしか入れない、という形。受け入れる方式から本社の分を外すとその人が
// 締め出されるので、外すのは**ボタンだけ**にできる必要がある。
//
// ここで固定するのは「隠しても入れる」— 表示は表示であって、門ではない（決定 14）。
func TestHiddenProvidersHideTheButtonButNotTheDoor(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sub", "子会社")
	seedTenantIdP(t, st, tn.ID, "entra", "sub.co.jp", "active")
	// 受け入れるのは自社の方式＋本社の google。ボタンに出すのは自社の方式だけ。
	if err := st.SetTenantLogin(ctx, tn.ID, "t:sub:entra,google", "", "", "google"); err != nil {
		t.Fatalf("set login rules: %v", err)
	}

	cfg := config{
		publicBaseURL: "https://af.example.com",
		cookieSecret:  []byte("0123456789abcdef0123456789abcdef"),
		mgr:           mgr,
	}
	cfg.setProviders([]loginProvider{&oidcProvider{id: "google", labelJA: "Google でサインイン"}})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login/{slug}", cfg.handleLogin)
	body := func(path string) string {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w.Body.String()
	}

	page := body("/login/sub")
	if strings.Contains(page, "provider=google") {
		t.Fatalf("隠した方式のボタンが出ている:\n%s", page)
	}
	if !strings.Contains(page, "t%3Asub%3Aentra") {
		t.Fatalf("自社の方式のボタンは出ていないといけない:\n%s", page)
	}
	// ★ ここが本題。隠しても、その方式で入った人はこのテナントを使える。
	ok, _ := mgr.tenantLogin.providerAllowed(ctx, tn.ID, "google")
	if !ok {
		t.Fatal("隠した方式が門でも閉じている — 兼務の人が締め出される（表示と強制を混ぜている）")
	}

	// 全部隠したら、隠す指定の方を無視する。ボタンの無いログイン画面は行き止まりで、
	// テナント側の設定ミスがそれを作れてはいけない。
	if err := st.SetTenantLogin(ctx, tn.ID, "", "", "", "google,t:sub:entra"); err != nil {
		t.Fatalf("set login rules: %v", err)
	}
	mgr.tenantLogin.invalidate()
	if page := body("/login/sub"); !strings.Contains(page, "provider=google") {
		t.Fatalf("全部隠したときはボタンを出す（行き止まりを作らない）:\n%s", page)
	}
}

// --- ボタン文言の書き分け（docs/log/61 §61.15.10）---------------------------------

// env の GitHub とテナントの GitHub は同じ画面（/login/<slug>）に並ぶ。id は衝突
// しないが、id はボタンに出ない — 既定ラベルが同じ「GitHub でサインイン」のままだと、
// 見分けのつかない 2 つのボタンが別々の OAuth アプリへ人を送ることになる。
//
// ★ 固定するのは 3 点:
//   - テナント行の既定ラベルにテナント名が入り、env のボタンと文字列が違う
//   - 行が label_ja / label_en を書いていればそちらが勝つ（優先順位は変えない）
//   - 二言語とも効く（日本語だけ直すと英語の面で元の衝突が残る）
func TestTenantGitHubButtonSaysWhichCompanyItIs(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sub", "子会社")
	row := store.TenantIdP{
		ID: store.NewID(), TenantID: tn.ID, Name: "github", Kind: tenantIdPKindGitHub,
		Issuer: githubWebBase, ClientID: "c", SecretEnc: "s", Trust: trustAPI,
		AllowedOrgs: "acme-sub", AllowedDomains: "@sub.co.jp",
		Status: "active", CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
	}
	if err := st.CreateTenantIdP(ctx, row); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg := config{
		publicBaseURL: "https://af.example.com",
		cookieSecret:  []byte("0123456789abcdef0123456789abcdef"),
		mgr:           mgr,
	}
	// デプロイ自身の GitHub ボタン（env 由来）。既定の文言を持っている。
	cfg.setProviders([]loginProvider{&githubProvider{
		id: githubProviderID, labelJA: "GitHub でサインイン", labelEN: "Sign in with GitHub",
	}})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login/{slug}", cfg.handleLogin)
	body := func(accept string) string {
		r := httptest.NewRequest(http.MethodGet, "/login/sub", nil)
		r.Header.Set("Accept-Language", accept)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Body.String()
	}

	for _, tc := range []struct{ lang, accept, env, tenant string }{
		{"ja", "ja,en;q=0.8", "GitHub でサインイン", "GitHub でサインイン（子会社）"},
		{"en", "en-US,en;q=0.9", "Sign in with GitHub", "Sign in with GitHub (子会社)"},
	} {
		page := body(tc.accept)
		if !strings.Contains(page, tc.tenant) {
			t.Fatalf("%s: テナント行のボタンに会社名が入っていない（%q が無い）:\n%s", tc.lang, tc.tenant, page)
		}
		// env のボタンは元の文言のまま。テナント行の文言はそれを含むので、
		// 「同じ文字列のボタンが 2 つ」でないことは出現回数で見る。
		if n := strings.Count(page, tc.env); n != 2 {
			t.Fatalf("%s: %q の出現が %d（env の 1 つ＋テナント行の接頭辞 1 つ = 2 のはず）:\n%s",
				tc.lang, tc.env, n, page)
		}
		if !strings.Contains(page, "provider="+githubProviderID) || !strings.Contains(page, "t%3Asub%3Agithub") {
			t.Fatalf("%s: 両方のボタンが出ていない:\n%s", tc.lang, page)
		}
	}

	// ★ 行が自分でラベルを書いていればそちらが勝つ。既定を補うのが目的で、
	// 管理者が選んだ文言を上書きするのは別のこと。
	row.LabelJA, row.LabelEN = "子会社の GitHub", "Subsidiary GitHub"
	p, err := buildTenantProvider(row, store.TenantRef{Slug: "sub", Name: "子会社"}, "s")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if p.Label("ja") != "子会社の GitHub" || p.Label("en") != "Subsidiary GitHub" {
		t.Fatalf("行が書いたラベルが勝っていない: %q / %q", p.Label("ja"), p.Label("en"))
	}
}

// 表示名の無いテナント（name が空）では slug で代用する。会社名が読めなくても、
// 「どちらのボタンか」が分かることのほうが大事。
func TestTenantLabelFallsBackToTheSlug(t *testing.T) {
	if got := tenantLabelSuffix("GitHub でサインイン", store.TenantRef{Slug: "sub"}, "ja"); got != "GitHub でサインイン（sub）" {
		t.Fatalf("ja = %q", got)
	}
	if got := tenantLabelSuffix("Sign in with GitHub", store.TenantRef{Slug: "sub"}, "en"); got != "Sign in with GitHub (sub)" {
		t.Fatalf("en = %q", got)
	}
	// slug も名前も無いのは実際には起きないが、そのときは接尾辞を足さない。
	if got := tenantLabelSuffix("x", store.TenantRef{}, "ja"); got != "x" {
		t.Fatalf("empty tenant = %q", got)
	}
}
