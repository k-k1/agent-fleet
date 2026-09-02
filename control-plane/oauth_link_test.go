package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// docs/log/61 §61.16 + ADR0043 決定 37 — 本人の同意で 2 つ目のサインイン方法を紐づける。
// 何を固定しているか:
//   - 紐づけはログイン済みの本人からしか始まらない（/oauth2/ は authGate の除外なので
//     ハンドラ自身が門になっている＝ここが抜けると無認証で identity 行を書ける）
//   - その方式自身の門（org・ドメイン）を必ず通る（紐づけは迂回路ではない）
//   - 主張されたアドレスが自分のものでなければ拒否（決定 37・別 email は結合しない）
//   - 相手の IdP アカウントが誰かのものなら拒否（付け替えも結合もしない）
//   - identity 行を触らない＝役割は動かない（決定 31）
//   - 紐づけの往復でセッションが発行/差し替えされない

// --- store layer -------------------------------------------------------------

// 紐づけたあと、その方式でのログインが規則 1 で同じ identity に着地すること。そして
// identity 行（email・user_key・role）が紐づけで動かないこと。
func TestAttachProviderAddsAMethodWithoutTouchingTheIdentity(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	const email = "yamada@acme.co.jp"

	me, _, err := st.LinkIdentity(ctx, linkOf(auth.GoogleProviderID, "g-1", email, true))
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	if err := st.AttachProvider(ctx, me.ID, store.IdentityLink{
		Provider: "t:sub:github", Subject: "gh-1", Realm: auth.GithubWebBase, Email: email,
	}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// 押し間違いで 2 回押しても同じ（冪等）。
	if err := st.AttachProvider(ctx, me.ID, store.IdentityLink{
		Provider: "t:sub:github", Subject: "gh-1", Realm: auth.GithubWebBase, Email: email,
	}); err != nil {
		t.Fatalf("attach twice: %v", err)
	}
	if n := countRows(t, st, "identity"); n != 1 {
		t.Fatalf("identity rows = %d, want 1", n)
	}
	after, _, _ := st.GetIdentityByID(ctx, me.ID)
	if after.UserKey != me.UserKey || after.Email != me.Email || after.Role != me.Role {
		t.Fatalf("the identity row must not move: %+v -> %+v", me, after)
	}
	// 紐づけた方式で実際にサインインすると、規則 1 で同じ人。
	// ★ emailJoin=false（テナント定義）のまま通ることが要点 — 規則 2' は使っていない。
	got, isNew, err := st.LinkIdentity(ctx, linkOf("t:sub:github", "gh-1", email, false))
	if err != nil || isNew || got.ID != me.ID {
		t.Fatalf("login with the linked method: %+v isNew=%v err=%v", got, isNew, err)
	}
	lp, err := st.ListLinkedProviders(ctx, me.ID)
	if err != nil || len(lp) != 2 {
		t.Fatalf("linked = %+v (%v), want 2", lp, err)
	}
}

// ★ 付け替えも結合もしない。3 つの経路（同じ対を他人が持つ／規則 1.5 で他人に当たる／
// アドレスが他人のもの）はどれも不可逆なので、拒否だけが正しい答え。
func TestAttachProviderRefusesAnAccountThatBelongsToSomebody(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	me, _, err := st.LinkIdentity(ctx, linkOf(auth.GoogleProviderID, "g-1", "yamada@acme.co.jp", true))
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	other, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "github", Subject: "gh-9", Realm: auth.GithubWebBase,
		Email: "suzuki@acme.co.jp", FallbackKey: "suzuki-acme-co-jp", EmailJoin: true,
	})
	if err != nil {
		t.Fatalf("other: %v", err)
	}

	// 1. その対そのものが他人のもの。
	if err := st.AttachProvider(ctx, me.ID, store.IdentityLink{
		Provider: "github", Subject: "gh-9", Realm: auth.GithubWebBase, Email: "yamada@acme.co.jp",
	}); !errors.Is(err, store.ErrLinkTaken) {
		t.Fatalf("err = %v, want errLinkTaken", err)
	}
	// 2. 規則 1.5 — 別のボタン（テナントの GitHub）だが同じ GitHub アカウント。
	//    そのアカウントでログインすると other に着地するので、ここで紐づけると
	//    1 つのアカウントを 2 人が名乗ることになる。
	if err := st.AttachProvider(ctx, me.ID, store.IdentityLink{
		Provider: "t:sub:github", Subject: "gh-9", Realm: auth.GithubWebBase, Email: "yamada@acme.co.jp",
	}); !errors.Is(err, store.ErrLinkTaken) {
		t.Fatalf("rule 1.5 err = %v, want errLinkTaken", err)
	}
	// 3. アドレスが他人のもの（呼び出し側の同一アドレス規則の後ろで、なお効く層）。
	if err := st.AttachProvider(ctx, me.ID, store.IdentityLink{
		Provider: "t:sub:entra", Subject: "e-1", Email: "suzuki@acme.co.jp",
	}); !errors.Is(err, store.ErrLinkTaken) {
		t.Fatalf("email err = %v, want errLinkTaken", err)
	}
	if n := countRows(t, st, "identity_provider"); n != 2 {
		t.Fatalf("identity_provider rows = %d — a refusal must write nothing", n)
	}
	if lp, _ := st.ListLinkedProviders(ctx, other.ID); len(lp) != 1 {
		t.Fatalf("the other account's method must be untouched: %+v", lp)
	}
}

// --- the flow ----------------------------------------------------------------

// linkTestConfig は「Google で入っている人が、2 つ目の方式（entra）を足す」最小構成。
func linkTestConfig(t *testing.T, idp *stubIdP) (config, *store.SQL) {
	t.Helper()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	cfg := oauthTestConfig(t, stubProvider("entra", idp, auth.TrustEmailVerified))
	cfg.mgr = mgr
	// oauthTestConfig wires deployAllowed before mgr exists, so re-point the DB term
	// the way main.go does — the link flow must run the SAME Allowed() as a login.
	for _, p := range cfg.providers {
		if op, ok := p.(*auth.OIDCProvider); ok {
			op.DeployAllowed, op.DBAllowed = cfg.emailAllowed, cfg.tenantEmailAllowed
		}
	}
	return cfg, st
}

// seedSignedIn creates the person and the session cookie they are holding.
func seedSignedIn(t *testing.T, cfg config, st *store.SQL, email string) (store.Identity, *http.Cookie) {
	t.Helper()
	ident, _, err := st.LinkIdentity(t.Context(), linkOf(auth.GoogleProviderID, "g-1", email, true))
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	b, _ := json.Marshal(sessionClaims{
		Email: email, Exp: time.Now().Add(time.Hour).Unix(), Prov: auth.GoogleProviderID, Sub: "g-1",
	})
	return ident, &http.Cookie{Name: sessionCookie, Value: cfg.signCookie(b)}
}

// startLink drives GET /oauth2/link with the session cookie set.
func startLink(t *testing.T, cfg config, session *http.Cookie, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/oauth2/link"+query, nil)
	if session != nil {
		r.AddCookie(session)
	}
	w := httptest.NewRecorder()
	cfg.handleOAuthLink(w, r)
	return w
}

func stateCookieOf(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, ck := range w.Result().Cookies() {
		if ck.Name == stateCookie && ck.Value != "" {
			return ck
		}
	}
	return nil
}

// linkCallback replays the IdP redirect with BOTH cookies — the state of the link
// flow and the session that started it.
func linkCallback(t *testing.T, cfg config, state, session *http.Cookie, nonce string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet,
		"/oauth2/callback?code=the-code&state="+url.QueryEscape(nonce), nil)
	if state != nil {
		r.AddCookie(state)
	}
	if session != nil {
		r.AddCookie(session)
	}
	w := httptest.NewRecorder()
	cfg.handleOAuthCallback(w, r)
	return w
}

// nonceOf reads the nonce back out of the signed state cookie (the browser would
// have got it from the authorize URL).
func nonceOf(t *testing.T, cfg config, state *http.Cookie) string {
	t.Helper()
	payload, ok := cfg.verifyCookie(state.Value)
	if !ok {
		t.Fatal("state cookie does not verify")
	}
	var st oauthState
	if err := json.Unmarshal(payload, &st); err != nil {
		t.Fatalf("state: %v", err)
	}
	return st.Nonce
}

// ★ 門はここにしかない。/oauth2/ は authGate の除外プレフィックスなので、この
// チェックが抜けると「セッション不要で identity_provider を書けるエンドポイント」になる。
func TestLinkRequiresASignedInSession(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{})
	cfg, st := linkTestConfig(t, idp)

	w := startLink(t, cfg, nil, "?provider=entra")
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/login") {
		t.Fatalf("want a redirect to /login, got %d %q", w.Code, w.Header().Get("Location"))
	}
	if stateCookieOf(t, w) != nil {
		t.Fatal("no session must mean no flow at all — a state cookie was minted")
	}
	if n := countRows(t, st, "identity"); n != 0 {
		t.Fatalf("identity rows = %d, want 0", n)
	}
}

// 通常経路。往復のあとで対が記録され、セッションは発行も差し替えもされないこと。
func TestLinkAddsTheMethodAndLeavesTheSessionAlone(t *testing.T) {
	const email = "yamada@acme.co.jp"
	idp := newStubIdP(t, &stubIdP{
		idTokenClaims:  map[string]any{"sub": "e-1", "email": email, "email_verified": true},
		userinfoClaims: map[string]any{"sub": "e-1", "email": email, "email_verified": true},
	})
	cfg, st := linkTestConfig(t, idp)
	me, session := seedSignedIn(t, cfg, st, email)

	w := startLink(t, cfg, session, "?provider=entra&next=%2Fsessions")
	if w.Code != http.StatusFound {
		t.Fatalf("link start: %d %s", w.Code, w.Body.String())
	}
	state := stateCookieOf(t, w)
	if state == nil {
		t.Fatal("link start: no state cookie")
	}
	w = linkCallback(t, cfg, state, session, nonceOf(t, cfg, state))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "追加しました") {
		t.Fatalf("callback: %d %s", w.Code, w.Body.String())
	}
	if sessionCookieOf(t, w) != nil {
		t.Fatal("a link flow must not issue or replace the session cookie")
	}
	lp, err := st.ListLinkedProviders(t.Context(), me.ID)
	if err != nil || len(lp) != 2 {
		t.Fatalf("linked = %+v (%v), want google + entra", lp, err)
	}
	// realm はアダプタが名乗った値で押されていること（規則 1.5 が動く条件）。
	for _, r := range lp {
		if r.Provider == "entra" && r.Realm != idp.URL {
			t.Fatalf("realm = %q, want %q", r.Realm, idp.URL)
		}
	}
	// そして次からはその方式でも同じワークスペースに入れる。
	got, isNew, err := st.LinkIdentity(t.Context(), linkOf("entra", "e-1", email, true))
	if err != nil || isNew || got.ID != me.ID {
		t.Fatalf("sign-in with the linked method: %+v isNew=%v err=%v", got, isNew, err)
	}
}

// ★ 決定 37: 追加できるのは同じアドレスを名乗る方式だけ。別アドレスの結合は §61.5 の
// 「両方にサインインできることは、同一人物であることの証明ではない」に当たる。
func TestLinkRefusesADifferentAddress(t *testing.T) {
	const email = "yamada@acme.co.jp"
	idp := newStubIdP(t, &stubIdP{
		idTokenClaims:  map[string]any{"sub": "e-1", "email": "tanaka@acme.co.jp", "email_verified": true},
		userinfoClaims: map[string]any{"sub": "e-1", "email": "tanaka@acme.co.jp", "email_verified": true},
	})
	cfg, st := linkTestConfig(t, idp)
	me, session := seedSignedIn(t, cfg, st, email)

	w := startLink(t, cfg, session, "?provider=entra")
	state := stateCookieOf(t, w)
	w = linkCallback(t, cfg, state, session, nonceOf(t, cfg, state))
	if !strings.Contains(w.Body.String(), "違います") {
		t.Fatalf("want the different-address refusal, got: %s", w.Body.String())
	}
	if lp, _ := st.ListLinkedProviders(t.Context(), me.ID); len(lp) != 1 {
		t.Fatalf("a refusal must write nothing: %+v", lp)
	}
}

// ★ 紐づけは門の迂回路ではない。その方式でサインインできない人は、紐づけもできない。
func TestLinkRunsTheMethodsOwnGate(t *testing.T) {
	const email = "yamada@acme.co.jp"
	idp := newStubIdP(t, &stubIdP{
		idTokenClaims:  map[string]any{"sub": "e-1", "email": email, "email_verified": true},
		userinfoClaims: map[string]any{"sub": "e-1", "email": email, "email_verified": true},
	})
	cfg, st := linkTestConfig(t, idp)
	// この provider だけの許可リスト（別ドメイン）＝ この人は入れない。
	cfg.providers[0].(*auth.OIDCProvider).AllowDomains = domainSet("sub.co.jp")
	me, session := seedSignedIn(t, cfg, st, email)

	w := startLink(t, cfg, session, "?provider=entra")
	state := stateCookieOf(t, w)
	w = linkCallback(t, cfg, state, session, nonceOf(t, cfg, state))
	if !strings.Contains(w.Body.String(), "許可されていません") {
		t.Fatalf("want the gate refusal, got: %s", w.Body.String())
	}
	if lp, _ := st.ListLinkedProviders(t.Context(), me.ID); len(lp) != 1 {
		t.Fatalf("a refusal must write nothing: %+v", lp)
	}
}

// ★ 署名済みの state は「CP が書いた」しか言わない。2 本目の脚でブラウザが別人に
// なっていたら（別タブでサインインし直した）、その別人のアカウントに紐づいてしまう。
func TestLinkRefusesWhenTheSessionChangedMidFlow(t *testing.T) {
	const email = "yamada@acme.co.jp"
	idp := newStubIdP(t, &stubIdP{
		idTokenClaims:  map[string]any{"sub": "e-1", "email": email, "email_verified": true},
		userinfoClaims: map[string]any{"sub": "e-1", "email": email, "email_verified": true},
	})
	cfg, st := linkTestConfig(t, idp)
	me, session := seedSignedIn(t, cfg, st, email)

	w := startLink(t, cfg, session, "?provider=entra")
	state := stateCookieOf(t, w)
	// 別人のセッションで戻ってくる。
	b, _ := json.Marshal(sessionClaims{
		Email: "suzuki@acme.co.jp", Exp: time.Now().Add(time.Hour).Unix(),
		Prov: auth.GoogleProviderID, Sub: "g-2",
	})
	other := &http.Cookie{Name: sessionCookie, Value: cfg.signCookie(b)}

	w = linkCallback(t, cfg, state, other, nonceOf(t, cfg, state))
	if !strings.Contains(w.Body.String(), "確認できませんでした") {
		t.Fatalf("want the session refusal, got: %s", w.Body.String())
	}
	if n := countRows(t, st, "identity_provider"); n != 1 {
		t.Fatalf("identity_provider rows = %d — nothing may have been written", n)
	}
	if lp, _ := st.ListLinkedProviders(t.Context(), me.ID); len(lp) != 1 {
		t.Fatalf("linked = %+v", lp)
	}
}

// ★ テナント定義の方式は、そのテナントの名簿に載っている人にだけ差し出す（決定 32-4 と
// 同じ理由 — 子会社の一覧をデプロイ全体に見せない）。一覧を絞るだけでなく、開始側でも
// 同じ規則を効かせる（決定 14: 表示は門ではない）。
func TestLinkToATenantMethodNeedsMembership(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	cfg := config{mgr: mgr}

	mine, err := st.CreateTenant(ctx, "sub", "子会社")
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	if _, err := st.CreateTenant(ctx, "other", "よそ"); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	me, err := st.UpsertIdentity(ctx, "yamada@acme.co.jp", "yamada-acme-co-jp", "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	if _, err := st.EnsureMembership(ctx, me.ID, mine.ID, "member"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	if !cfg.linkableFor(ctx, me, "t:sub:github") {
		t.Fatal("a member must be able to link their own tenant's method")
	}
	if cfg.linkableFor(ctx, me, "t:other:github") {
		t.Fatal("another tenant's method must not be offered")
	}
	if !cfg.linkableFor(ctx, me, "entra") {
		t.Fatal("an env provider is the deployment's own door and stays open")
	}
}

// アカウント面の一覧: 紐づけ済みと、次に足せるもの。すでに持っている方式は候補に出ない。
func TestLoginMethodsListsLinkedAndLinkable(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{})
	cfg, st := linkTestConfig(t, idp)
	me, _ := seedSignedIn(t, cfg, st, "yamada@acme.co.jp")
	if err := st.AttachProvider(t.Context(), me.ID, store.IdentityLink{
		Provider: "entra", Subject: "e-1", Realm: idp.URL, Email: "yamada@acme.co.jp",
	}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	api := newAccountAPI(cfg)
	r := httptest.NewRequest(http.MethodGet, "/api/me/login-methods", nil)
	r = r.WithContext(withLoginRef(r.Context(), loginRef{auth.GoogleProviderID, "g-1"}))
	w := httptest.NewRecorder()
	api.loginMethods(w, r, me)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var got struct {
		Enabled bool `json:"enabled"`
		Linked  []struct {
			Provider string `json:"provider"`
			Current  bool   `json:"current"`
			LabelJA  string `json:"label_ja"`
		} `json:"linked"`
		Linkable []struct {
			Provider string `json:"provider"`
		} `json:"linkable"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body: %v", err)
	}
	if len(got.Linked) != 2 {
		t.Fatalf("linked = %+v, want google + entra", got.Linked)
	}
	for _, l := range got.Linked {
		if l.Provider == auth.GoogleProviderID && !l.Current {
			t.Error("the method the session was minted with must be marked current")
		}
		if l.Provider == "entra" && l.LabelJA == "" {
			t.Error("a configured provider must carry its button label")
		}
	}
	// entra は紐づけ済みなので候補から消え、google は env にあるが未紐づけ扱いではない。
	for _, c := range got.Linkable {
		if c.Provider == "entra" {
			t.Fatalf("an already-linked method must not be offered again: %+v", got.Linkable)
		}
	}
}

// --- 解除（docs/log/61 §61.16.4）--------------------------------------------------

// detachReq は DELETE /api/me/login-methods を 1 回叩く。provider / subject は
// パスではなくクエリ（テナントの provider id は ":" を含む）。
func detachReq(t *testing.T, api accountAPI, me store.Identity, cur loginRef, provider, subject string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{"provider": {provider}, "subject": {subject}}
	r := httptest.NewRequest(http.MethodDelete, "/api/me/login-methods?"+q.Encode(), nil)
	r = r.WithContext(withLoginRef(r.Context(), cur))
	w := httptest.NewRecorder()
	api.detachLoginMethod(w, r, me)
	return w
}

// ★ 3 つのガードはどれか 1 つでも抜けると別々の壊れ方をする:
//   - 残り 1 つを外す → 二度と入れないアカウントができる（復旧経路が無い）
//   - いま使っている方式を外す → そのセッションで自分の足元を消す
//   - 他人の行 → identity_id を条件から落とすと、対を当てるだけで他人の方式を消せる
func TestDetachRefusesTheLastMethodTheCurrentOneAndSomebodyElses(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{})
	cfg, st := linkTestConfig(t, idp)
	me, _ := seedSignedIn(t, cfg, st, "yamada@acme.co.jp")
	api := newAccountAPI(cfg)
	cur := loginRef{auth.GoogleProviderID, "g-1"}

	// 1. まだ 1 つしか無い。★ 現セッションの方式のガードと重なるので、ここでは別の
	//    方式で入っていることにして「残数」のガード単体を見る — 2 つは独立に効く。
	if w := detachReq(t, api, me, loginRef{"entra", "e-1"}, auth.GoogleProviderID, "g-1"); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "last_login_method") {
		t.Fatalf("最後の 1 つは外せてはいけない: %d %s", w.Code, w.Body.String())
	}
	// ★ SQL 層でも数えている（API のチェックと DELETE の間にタブが 1 枚挟まる）。
	if err := st.DetachProvider(t.Context(), me.ID, auth.GoogleProviderID, "g-1"); !errors.Is(err, store.ErrLastLoginMethod) {
		t.Fatalf("store 層の残数ガードが効いていない: %v", err)
	}

	if err := st.AttachProvider(t.Context(), me.ID, store.IdentityLink{
		Provider: "entra", Subject: "e-1", Realm: idp.URL, Email: "yamada@acme.co.jp",
	}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// 2. 2 つになっても、いま入っている方式は外せない。
	if w := detachReq(t, api, me, cur, auth.GoogleProviderID, "g-1"); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "current_login_method") {
		t.Fatalf("現セッションの方式は外せてはいけない: %d %s", w.Code, w.Body.String())
	}
	// 3. 他人の行。対を知っていても届かない。
	other, _, err := st.LinkIdentity(t.Context(), store.IdentityLink{
		Provider: "entra", Subject: "e-9", Realm: idp.URL, Email: "suzuki@acme.co.jp",
		FallbackKey: "suzuki-acme-co-jp", EmailJoin: true,
	})
	if err != nil {
		t.Fatalf("other: %v", err)
	}
	if w := detachReq(t, api, me, cur, "entra", "e-9"); w.Code != http.StatusNotFound {
		t.Fatalf("他人の行に届いてはいけない: %d %s", w.Code, w.Body.String())
	}
	if lp, _ := st.ListLinkedProviders(t.Context(), other.ID); len(lp) != 1 {
		t.Fatalf("他人の方式が消えている: %+v", lp)
	}

	// そして正しい 1 件は外せる。identity 行は動かない（解除もログインではない）。
	before, _, _ := st.GetIdentityByID(t.Context(), me.ID)
	if w := detachReq(t, api, me, cur, "entra", "e-1"); w.Code != http.StatusOK {
		t.Fatalf("外せるはずの 1 件が外せない: %d %s", w.Code, w.Body.String())
	}
	lp, _ := st.ListLinkedProviders(t.Context(), me.ID)
	if len(lp) != 1 || lp[0].Provider != auth.GoogleProviderID {
		t.Fatalf("linked = %+v, want google だけ", lp)
	}
	after, _, _ := st.GetIdentityByID(t.Context(), me.ID)
	if after.UserKey != before.UserKey || after.Email != before.Email || after.Role != before.Role {
		t.Fatalf("解除で identity 行が動いた: %+v -> %+v", before, after)
	}
	// 台帳に残る（いつからその扉が開いていた／閉じたかを後から読めること）。
	rows, err := st.ListAuditByTenant(t.Context(), "", 50)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	seen := ""
	for _, row := range rows {
		if row.Action == "identity_provider.detach" && row.Target == "entra" {
			seen = row.Detail
		}
	}
	if !strings.Contains(seen, "e-1") {
		t.Fatalf("解除が監査に残っていない: %+v", rows)
	}
}

// ★ 一覧は「外せるかどうか」もサーバが答える。UI はその写しで、判断はしない
// （決定 14）— そして解除 API は同じ規則を自分でもう一度見る。
func TestLoginMethodsSaysWhichRowsCanBeRemoved(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{})
	cfg, st := linkTestConfig(t, idp)
	me, _ := seedSignedIn(t, cfg, st, "yamada@acme.co.jp")
	api := newAccountAPI(cfg)

	read := func() []struct {
		Provider  string `json:"provider"`
		Subject   string `json:"subject"`
		Current   bool   `json:"current"`
		Removable bool   `json:"removable"`
	} {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/me/login-methods", nil)
		r = r.WithContext(withLoginRef(r.Context(), loginRef{auth.GoogleProviderID, "g-1"}))
		w := httptest.NewRecorder()
		api.loginMethods(w, r, me)
		var got struct {
			Linked []struct {
				Provider  string `json:"provider"`
				Subject   string `json:"subject"`
				Current   bool   `json:"current"`
				Removable bool   `json:"removable"`
			} `json:"linked"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("body: %v", err)
		}
		return got.Linked
	}

	// 1 つだけのときは、それが現セッションの方式でもあるので二重に外せない。
	one := read()
	if len(one) != 1 || one[0].Removable {
		t.Fatalf("最後の 1 つが removable になっている: %+v", one)
	}
	if one[0].Subject != "g-1" {
		t.Fatalf("行を名指しする subject が返っていない: %+v", one)
	}

	if err := st.AttachProvider(t.Context(), me.ID, store.IdentityLink{
		Provider: "entra", Subject: "e-1", Realm: idp.URL, Email: "yamada@acme.co.jp",
	}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	for _, l := range read() {
		switch l.Provider {
		case auth.GoogleProviderID:
			if !l.Current || l.Removable {
				t.Fatalf("現セッションの方式は removable ではない: %+v", l)
			}
		case "entra":
			if l.Current || !l.Removable {
				t.Fatalf("もう一方は外せるはず: %+v", l)
			}
		}
	}
}
