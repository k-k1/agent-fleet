package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// docs/log/61 §61.15.10 + ADR0043 決定 38 — 規則 1.5 の 2 本目の鍵（安定クレーム）。
//
// 何のためか: Entra の `sub` は (アプリ登録, 人) のペアワイズなので、同じ Entra
// テナントでもアプリ登録が違えば同じ人が別 subject になる。規則 1.5 は当たらず、
// テナント定義の行は規則 2' で email_taken に落ちる＝既存利用者が締め出される。
//
// ここで固定するのは、その修正が「事故る形」で入っていないこと:
//   - `subject` は今までどおり `sub` のまま（差し替えると既存行の鍵が変わる）
//   - 照合にはクレーム名も含む（値がたまたま一致しても、別のクレームなら当たらない）
//   - テナントが名乗れるのは既知の安定クレームだけ（email を書けたら共有 realm の
//     中に email 結合を作れる）
//   - 検証は API 側と実行時側の両方にある（片方だけだと保存できて承認後に落ちる）
//   - 値はトークンからしか来ない（行から書けるのはクレーム名だけ）

const entraIssuer = "https://login.microsoftonline.com/guid-1/v2.0"

// oidLink は「同じ Entra アカウントを、別のアプリ登録から」を作る。sub は違い、
// oid は同じ — 実物の Entra がそう振る舞う。
func oidLink(provider, sub, oid, email string, emailJoin bool) store.IdentityLink {
	return store.IdentityLink{
		Provider: provider, Subject: sub, Realm: entraIssuer,
		RealmClaim: "oid", RealmSubject: oid, Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: emailJoin,
	}
}

// ★ 本題。アプリ登録が違う 2 つのボタンでも、oid が同じなら同じ人。
func TestRule15JoinsAcrossAppRegistrationsByStableClaim(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	const email = "yamada@acme.co.jp"

	first, _, err := st.LinkIdentity(ctx, oidLink("entra", "pairwise-A", "oid-1", email, true))
	if err != nil {
		t.Fatalf("head office: %v", err)
	}
	// テナント定義の行なので EmailJoin=false — 規則 2 は使えず、規則 1.5 だけが頼り。
	second, isNew, err := st.LinkIdentity(ctx, oidLink("t:sub:entra", "pairwise-B", "oid-1", email, false))
	if err != nil {
		t.Fatalf("subsidiary must not be refused: %v", err)
	}
	if isNew || second.ID != first.ID {
		t.Fatalf("同じ Entra アカウントが別人になった: %+v vs %+v (isNew=%v)", first, second, isNew)
	}
	if n := countRows(t, st, "identity"); n != 1 {
		t.Fatalf("identity rows = %d, want 1", n)
	}
	// ★ subject は sub のまま。差し替えていたら規則 1（対の一致）が既存行から外れる。
	lp, _ := st.ListLinkedProviders(ctx, first.ID)
	subs := map[string]bool{}
	for _, r := range lp {
		subs[r.Subject] = true
	}
	if !subs["pairwise-A"] || !subs["pairwise-B"] {
		t.Fatalf("subject が sub 以外に書き換わっている: %+v", lp)
	}
	// 2 回目のログインは規則 1 で当たる（鍵が動いていないことの裏取り）。
	again, isNew, err := st.LinkIdentity(ctx, oidLink("t:sub:entra", "pairwise-B", "oid-1", email, false))
	if err != nil || isNew || again.ID != first.ID {
		t.Fatalf("2 回目のログイン: %+v isNew=%v err=%v", again, isNew, err)
	}
}

// ★ 照合にはクレーム名も入る。片方が oid、片方が別のクレームのとき、値が
// たまたま一致しても当ててはいけない — 同じ問いの答えではないため。
func TestRule15DoesNotJoinWhenTheClaimNameDiffers(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()

	me, _, err := st.LinkIdentity(ctx, oidLink("entra", "s-1", "shared-value", "yamada@acme.co.jp", true))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	other := oidLink("t:sub:entra", "s-2", "shared-value", "suzuki@acme.co.jp", false)
	other.RealmClaim = "employee_id" // 別のクレームに、たまたま同じ値
	got, _, err := st.LinkIdentity(ctx, other)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if got.ID == me.ID {
		t.Fatal("クレーム名が違うのに結合した — 値の衝突で他人になる")
	}
}

// 既存行（realm_subject が空）は今までどおり。空が空に当たって全員が 1 人に
// なる、が一番起こしやすい壊し方。
func TestRule15IgnoresRowsWithoutAStableClaim(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()

	a, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "entra", Subject: "s-1", Realm: entraIssuer,
		Email: "yamada@acme.co.jp", FallbackKey: "yamada-acme-co-jp", EmailJoin: true,
	})
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	b, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "okta", Subject: "s-2", Realm: entraIssuer,
		Email: "suzuki@acme.co.jp", FallbackKey: "suzuki-acme-co-jp", EmailJoin: true,
	})
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	if a.ID == b.ID {
		t.Fatal("realm_claim / realm_subject が空同士で結合した")
	}
	// realm+subject の従来の規則 1.5 も生きていること（GitHub の経路）。
	c, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "t:sub:entra", Subject: "s-1", Realm: entraIssuer,
		Email: "yamada@acme.co.jp", FallbackKey: "yamada-acme-co-jp", EmailJoin: false,
	})
	if err != nil || c.ID != a.ID {
		t.Fatalf("realm+subject の規則 1.5 が壊れた: %+v err=%v", c, err)
	}
}

// ★ 紐づけ（§61.16）の拒否条件にも 2 本目の鍵が要る。realm+subject だけ見ていると、
// ペアワイズ sub のせいで「対は空いている」ように見えて、実際にその方式でサインイン
// すると他人に着地する。
func TestAttachRefusesAnAccountFoundByTheStableClaim(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()

	me, _, err := st.LinkIdentity(ctx, linkOf(googleProviderID, "g-1", "yamada@acme.co.jp", true))
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	if _, _, err := st.LinkIdentity(ctx, oidLink("entra", "pairwise-A", "oid-9", "suzuki@acme.co.jp", true)); err != nil {
		t.Fatalf("other: %v", err)
	}
	err = st.AttachProvider(ctx, me.ID, store.IdentityLink{
		Provider: "t:sub:entra", Subject: "pairwise-B", Realm: entraIssuer,
		RealmClaim: "oid", RealmSubject: "oid-9", Email: "yamada@acme.co.jp",
	})
	if !errors.Is(err, store.ErrLinkTaken) {
		t.Fatalf("err = %v, want errLinkTaken", err)
	}
}

// --- 行が名乗れるクレーム（ホワイトリスト）------------------------------------

// ★ 検証は 2 箇所ある。API 側だけ直すと「保存はできたのに承認後に落ちる」行が
// 作れるし、実行時側だけだと保存できてしまう。
func TestTenantLinkClaimIsWhitelistedOnSaveAndAtRuntime(t *testing.T) {
	ctx := context.Background()
	stt := p3Store(t)
	mgr := p4Manager(t, stt)
	tn, _ := stt.CreateTenant(ctx, "sub", "子会社")
	if _, err := stt.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin"); err != nil {
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
	const base = `"kind":"oidc","issuer":"https://login.microsoftonline.com/guid-1/v2.0","client_id":"c","client_secret":"s","trust":"issuer","allowed_domains":"@sub.co.jp"`

	// ★ email / upn / preferred_username は「主張されるもの」。共有 realm の中で
	// これを鍵にできると、別の権威で作られたアカウントに届く（決定 32 の乗っ取り）。
	for _, claim := range []string{"email", "upn", "preferred_username", "name"} {
		w := post(`{"name":"entra-` + strings.ReplaceAll(claim, "_", "") + `",` + base + `,"link_claim":"` + claim + `"}`)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "tenant_idp_link_claim_invalid") {
			t.Fatalf("link_claim=%q は保存できてはいけない: %d %s", claim, w.Code, w.Body.String())
		}
	}
	if w := post(`{"name":"entra",` + base + `,"link_claim":"oid"}`); w.Code != http.StatusOK {
		t.Fatalf("oid は保存できるはず: %d %s", w.Code, w.Body.String())
	}
	rows, _ := stt.ListTenantIdPs(ctx, tn.ID)
	if len(rows) != 1 || rows[0].LinkClaim != "oid" {
		t.Fatalf("rows = %+v", rows)
	}
	// 実行時側。API を通さずに書かれた行（古いバイナリ・許可リスト変更後）でも
	// プロバイダに組まれない。
	bad := rows[0]
	bad.LinkClaim = "email"
	if _, err := buildTenantProvider(bad, store.TenantRef{Slug: "sub", Name: "子会社"}, "s"); err == nil {
		t.Fatal("実行時側の検証が無い — 保存できてしまえば承認後に効いてしまう")
	}
	if _, err := buildTenantProvider(rows[0], store.TenantRef{Slug: "sub", Name: "子会社"}, "s"); err != nil {
		t.Fatalf("oid の行は組めるはず: %v", err)
	}
	// ★ github 行は 2 本目の鍵を持たない（subject が最初から全アプリ共通）。
	if w := post(`{"name":"github","kind":"github","client_id":"c","client_secret":"s","allowed_orgs":"acme-sub","allowed_domains":"@sub2.co.jp","link_claim":"oid"}`); w.Code != http.StatusOK {
		t.Fatalf("github 行: %d %s", w.Code, w.Body.String())
	}
	rows, _ = stt.ListTenantIdPs(ctx, tn.ID)
	for _, row := range rows {
		if row.Kind == tenantIdPKindGitHub && row.LinkClaim != "" {
			t.Fatalf("github 行に link_claim が残っている: %+v", row)
		}
	}
}

// ★ link_claim の変更は承認をやり直す。誰が入れるかは変わらないが、誰に着地するか
// が変わる — 既存アカウントに届くボタンが増えるので、承認者が見るべき変更。
func TestLinkClaimChangeRepends(t *testing.T) {
	active := store.TenantIdP{
		Kind: tenantIdPKindOIDC, Status: "active", ClientID: "c", Issuer: entraIssuer,
		Trust: trustIssuer, AllowedDomains: "sub.co.jp",
	}
	next := active
	next.LinkClaim = "oid"
	if !repend(active, next) {
		t.Fatal("link_claim を足したら承認をやり直す")
	}
	back := next
	back.LinkClaim = ""
	if !repend(next, back) {
		t.Fatal("外すのも同じ — 着地先が変わることに変わりはない")
	}
}

// --- トークンからしか値は来ない ------------------------------------------------

// ★ 行（と env）が名乗れるのはクレーム「名」だけで、値は必ずトークンから読む。
// ここが逆になると、テナントが他人の oid を書けて規則 1.5 が偽造できる。
func TestLinkClaimValueComesFromTheToken(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{
		idTokenClaims: map[string]any{
			"sub": "pairwise-A", "email": "yamada@acme.co.jp", "email_verified": true,
			"oid": "oid-1",
		},
		userinfoClaims: map[string]any{"sub": "pairwise-A", "email": "yamada@acme.co.jp", "email_verified": true},
	})
	p := stubProvider("entra", idp, trustEmailVerified)
	p.linkClaim = "oid"
	pr, err := p.Exchange(t.Context(), "code", "https://af.example.com/oauth2/callback")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if pr.Subject != "pairwise-A" {
		t.Fatalf("subject = %q — sub のままでなければ既存行の鍵が変わる", pr.Subject)
	}
	if pr.RealmClaim != "oid" || pr.RealmSubject != "oid-1" {
		t.Fatalf("realm claim = %q / %q", pr.RealmClaim, pr.RealmSubject)
	}
	// クレームを出さない IdP では両方空 — 空同士で当たらないための前提。
	p2 := stubProvider("okta", idp, trustEmailVerified)
	p2.linkClaim = "employee_id"
	pr2, err := p2.Exchange(t.Context(), "code", "https://af.example.com/oauth2/callback")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if pr2.RealmClaim != "" || pr2.RealmSubject != "" {
		t.Fatalf("出ていないクレームで値が入った: %q / %q", pr2.RealmClaim, pr2.RealmSubject)
	}
	// 名乗らなければ何も読まない。
	p3 := stubProvider("plain", idp, trustEmailVerified)
	pr3, err := p3.Exchange(t.Context(), "code", "https://af.example.com/oauth2/callback")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if pr3.RealmClaim != "" || pr3.RealmSubject != "" {
		t.Fatalf("link_claim 未設定で値が入った: %q / %q", pr3.RealmClaim, pr3.RealmSubject)
	}
}
