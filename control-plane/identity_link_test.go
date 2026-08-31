package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// docs/log/61 P1 — 同一人物の判定。何を固定しているか:
//   - 既存 Google デプロイの初回ログインが (google, sub) 行を現 identity に書き、
//     user_key を動かさないこと（移行ゼロ）
//   - IdP 側で email が変わっても identity が増えず、表示用の email だけ変わること
//   - 別 IdP でも email が同じなら同じ identity（同じ workspace / home / secrets）
//   - email が一致しなければ新規 identity で、それが本人に見えること
//   - AUTH=proxy / AUTH=dev は何も変わらないこと（IdP subject が無いモード）

func newLinkStore(t *testing.T) *sqlStore {
	t.Helper()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.migrate(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func countRows(t *testing.T, st *sqlStore, table string) int {
	t.Helper()
	var n int
	if err := st.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// linkOf は「realm を持たない、ごく普通のログイン 1 回」。realm（規則 1.5・
// docs/log/61 §61.15）を試すテストは IdentityLink を直に組み立てる — 既定で空にして
// あるのは、realm 無しの行が従来どおりに振る舞うことこそ移行の要件だから。
func linkOf(provider, subject, email string, emailJoin bool) IdentityLink {
	return IdentityLink{
		Provider: provider, Subject: subject, Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: emailJoin,
	}
}

// ★ 受入条件 6 の移行面: Google 専用で動いてきたデプロイの人が、アップグレード後の
// 初回ログインで別人にならないこと。そして IdP 側の姓変更（email 変更）でも
// user_key＝home ディレクトリ名が動かないこと。
func TestLinkIdentityKeepsUserKeyAcrossEmailChange(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	const email = "yamada@acme.co.jp"

	// P1 より前に存在していた行（email から作られた identity）。
	seed, err := st.UpsertIdentity(ctx, email, sanitizeUser(email), "")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, isNew, err := st.LinkIdentity(ctx, linkOf(googleProviderID, "g-sub-1", email, true))
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	if isNew {
		t.Fatal("既存デプロイの初回ログインが新規アカウント扱いになっている")
	}
	if got.ID != seed.ID || got.UserKey != seed.UserKey {
		t.Fatalf("identity moved: %+v want id=%s key=%s", got, seed.ID, seed.UserKey)
	}
	if n := countRows(t, st, "identity_provider"); n != 1 {
		t.Fatalf("identity_provider rows = %d, want 1", n)
	}

	// 同じ (provider, subject) の再ログインで identity は増えない。
	again, isNew, err := st.LinkIdentity(ctx, linkOf(googleProviderID, "g-sub-1", email, true))
	if err != nil || isNew || again.ID != seed.ID {
		t.Fatalf("re-login: id=%s isNew=%v err=%v", again.ID, isNew, err)
	}
	if n := countRows(t, st, "identity"); n != 1 {
		t.Fatalf("identity rows after re-login = %d, want 1", n)
	}

	// IdP 側で email が変わった（姓変更・ドメイン統合）。同じ人のまま、
	// user_key は据え置き、表示用の email だけ新しくなる。
	const renamed = "yamada-hanako@acme.co.jp"
	moved, isNew, err := st.LinkIdentity(ctx, linkOf(googleProviderID, "g-sub-1", renamed, true))
	if err != nil {
		t.Fatalf("after rename: %v", err)
	}
	switch {
	case isNew:
		t.Fatal("email 変更で新規アカウントになっている")
	case moved.ID != seed.ID:
		t.Fatalf("email 変更で identity が変わった: %s -> %s", seed.ID, moved.ID)
	case moved.UserKey != seed.UserKey:
		t.Fatalf("user_key が動いた: %q -> %q（home ディレクトリ名なので不変が要件）", seed.UserKey, moved.UserKey)
	case moved.Email != renamed:
		t.Fatalf("表示用 email が更新されていない: %q", moved.Email)
	}
	if n := countRows(t, st, "identity"); n != 1 {
		t.Fatalf("identity rows after rename = %d, want 1", n)
	}
}

// 別の IdP から入っても email が同じなら同じ人 — 押したボタンで workspace が
// 変わらないこと（§61.5 の 2 行目）。
func TestLinkIdentityJoinsSameEmailFromAnotherProvider(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	const email = "yamada@acme.co.jp"

	first, _, err := st.LinkIdentity(ctx, linkOf(googleProviderID, "g-1", email, true))
	if err != nil {
		t.Fatalf("google: %v", err)
	}
	second, isNew, err := st.LinkIdentity(ctx, linkOf("entra", "e-1", email, true))
	if err != nil {
		t.Fatalf("entra: %v", err)
	}
	if isNew || second.ID != first.ID || second.UserKey != first.UserKey {
		t.Fatalf("別 IdP・同 email が別人になった: %+v want %+v (isNew=%v)", second, first, isNew)
	}
	if n := countRows(t, st, "identity"); n != 1 {
		t.Fatalf("identity rows = %d, want 1", n)
	}
	if n := countRows(t, st, "identity_provider"); n != 2 {
		t.Fatalf("identity_provider rows = %d, want 2", n)
	}
}

// email が一致しなければ新規 identity。isNew はログイン直後の通知の唯一の根拠なので
// ここで固定する（受入条件 3）。招待で先に作られた行を本人が引き取るのは「新規」では
// ないことも合わせて固定する。
func TestLinkIdentityNewAccountWhenEmailIsUnknown(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	const known = "yamada@acme.co.jp"
	first, _, err := st.LinkIdentity(ctx, linkOf(googleProviderID, "g-1", known, true))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	const other = "tanaka@acme.co.jp"
	got, isNew, err := st.LinkIdentity(ctx, linkOf("entra", "e-2", other, true))
	if err != nil {
		t.Fatalf("new person: %v", err)
	}
	if !isNew {
		t.Fatal("未知の email が新規アカウントとして報告されていない")
	}
	if got.ID == first.ID {
		t.Fatal("別 email が既存 identity に合流した（結合はしない設計）")
	}
	if got.UserKey != sanitizeUser(other) {
		t.Fatalf("user_key = %q, want %q", got.UserKey, sanitizeUser(other))
	}
	if n := countRows(t, st, "identity"); n != 2 {
		t.Fatalf("identity rows = %d, want 2", n)
	}

	// 管理者が email 未確定のまま user_key で作った招待行（tenants.go）を
	// 本人が初ログインで引き取るのは、新規アカウントではない。
	const invited = "suzuki@acme.co.jp"
	inv, err := st.UpsertIdentity(ctx, "", sanitizeUser(invited), "")
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	claimed, isNew, err := st.LinkIdentity(ctx, linkOf(googleProviderID, "g-3", invited, true))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if isNew || claimed.ID != inv.ID {
		t.Fatalf("招待行の引き取り: id=%s want %s isNew=%v", claimed.ID, inv.ID, isNew)
	}
}

// ★ AUTH=proxy と AUTH=dev には provider も subject も無い。P1 はそこでは何もせず、
// email だけで解決する現行の契約を保つ（ここで fail-closed に倒すと既存の proxy
// デプロイと dev が壊れる）。
func TestIdentityResolutionUnchangedWithoutAnIdPSubject(t *testing.T) {
	st := newLinkStore(t)
	const email = "yamada@acme.co.jp"

	proxy := &manager{store: st, authMode: "proxy", emailHeader: "X-Forwarded-Email"}
	r := httptest.NewRequest(http.MethodGet, "/api/tenants", nil)
	r.Header.Set("X-Forwarded-Email", email)
	ident, aerr := proxy.identityFor(r.Context(), r)
	if aerr != nil {
		t.Fatalf("proxy: %v", aerr.message)
	}
	if ident.UserKey != sanitizeUser(email) {
		t.Fatalf("proxy user_key = %q, want %q", ident.UserKey, sanitizeUser(email))
	}

	dev := &manager{store: st, authMode: "dev", devUser: "dev"}
	ident, aerr = dev.identityFor(t.Context(), httptest.NewRequest(http.MethodGet, "/api/tenants", nil))
	if aerr != nil {
		t.Fatalf("dev: %v", aerr.message)
	}
	if ident.UserKey != "dev" {
		t.Fatalf("dev user_key = %q, want dev", ident.UserKey)
	}
	if n := countRows(t, st, "identity_provider"); n != 0 {
		t.Fatalf("subject の無いモードで identity_provider に %d 行書かれている", n)
	}
}

// ★ authGate が prov/sub を下流へ渡していること。P0 では email ヘッダしか渡って
// おらず、resolveIdentity はそれだけを読んでいた — 渡し損ねると email 変更で
// home が変わる（この経路が壊れても email が同じ間はテストが通ってしまうので、
// 改名後に何が返るかで判定する）。
func TestAuthGateCarriesTheIdPSubjectIntoIdentityResolution(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	const email = "yamada@acme.co.jp"
	seed, err := st.UpsertIdentity(ctx, email, sanitizeUser(email), "")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	idp := newStubIdP(t, &stubIdP{})
	cfg := oauthTestConfig(t, stubProvider(googleProviderID, idp, trustEmailVerified))
	cfg.mgr.store = st
	cfg.mgr.authMode = "oauth"
	gate := cfg.authGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ident, aerr := cfg.mgr.identityFor(r.Context(), r)
		if aerr != nil {
			http.Error(w, aerr.message, aerr.status)
			return
		}
		_, _ = w.Write([]byte(ident.UserKey))
	}))
	call := func(sessionEmail string) string {
		b, _ := json.Marshal(sessionClaims{
			Email: sessionEmail, Exp: time.Now().Add(time.Hour).Unix(),
			Prov: googleProviderID, Sub: "g-sub-1",
		})
		r := httptest.NewRequest(http.MethodGet, "/api/tenants", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cfg.signCookie(b)})
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("gate: %d %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	if got := call(email); got != seed.UserKey {
		t.Fatalf("user_key = %q, want %q", got, seed.UserKey)
	}
	// 同じ subject・違う email。email 由来のキーに落ちたら home が変わっている。
	const renamed = "yamada-hanako@acme.co.jp"
	if got := call(renamed); got != seed.UserKey {
		t.Fatalf("改名後の user_key = %q, want %q（%q に落ちていれば prov/sub が届いていない）",
			got, seed.UserKey, sanitizeUser(renamed))
	}
	if n := countRows(t, st, "identity"); n != 1 {
		t.Fatalf("identity rows = %d, want 1", n)
	}
}

// 新規アカウントは本人に見える形で示す（受入条件 3: 黙って 2 つ目の workspace を
// 作らない）。IdP が 1 つだけのデプロイでは新規＝新しい同僚でしかないので、
// 既存デプロイの体験は変えない。
func TestNewAccountNoticeIsShownOnceOnMultiIdPDeployments(t *testing.T) {
	st := newLinkStore(t)
	newcomer := func(t *testing.T, email string) *stubIdP {
		return newStubIdP(t, &stubIdP{
			idTokenClaims:  map[string]any{"sub": "sub-" + email, "email": email, "email_verified": true},
			userinfoClaims: map[string]any{"sub": "sub-" + email, "email": email, "email_verified": true},
		})
	}

	idp := newcomer(t, "tanaka@acme.co.jp")
	cfg := oauthTestConfig(t, stubProvider(googleProviderID, idp, trustEmailVerified),
		stubProvider("okta", idp, trustEmailVerified))
	cfg.mgr.store = st

	st1, au := startLogin(t, cfg, "?provider=okta&next=%2Fsessions")
	w := callback(t, cfg, st1, "code", au.Query().Get("state"))
	if w.Code != http.StatusOK {
		t.Fatalf("新規アカウントで通知が出ていない: %d -> %s", w.Code, w.Header().Get("Location"))
	}
	page := w.Body.String()
	if !strings.Contains(page, "新しいワークスペース") || !strings.Contains(page, "tanaka@acme.co.jp") {
		t.Fatalf("通知の中身:\n%s", page)
	}
	if sessionCookieOf(t, w) == nil {
		t.Fatal("通知を出すために session を落としてはいけない")
	}

	// 2 回目は同じ (provider, subject) なので新規ではない — 素通りする。
	st2, au := startLogin(t, cfg, "?provider=okta&next=%2Fsessions")
	w = callback(t, cfg, st2, "code", au.Query().Get("state"))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/sessions" {
		t.Fatalf("再ログイン: %d -> %q", w.Code, w.Header().Get("Location"))
	}

	// IdP が 1 つだけのデプロイ（＝既存の Google 専用）は、新しい人でも素通り。
	single := oauthTestConfig(t, stubProvider(googleProviderID, newcomer(t, "sato@acme.co.jp"), trustEmailVerified))
	single.mgr.store = st
	st3, au := startLogin(t, single, "?next=%2Fsessions")
	w = callback(t, single, st3, "code", au.Query().Get("state"))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/sessions" {
		t.Fatalf("単一 IdP デプロイ: %d -> %q", w.Code, w.Header().Get("Location"))
	}
}
