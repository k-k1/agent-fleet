package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// docs/log/61 §61.15.10 + ADR0043 decision 38 — rule 1.5's second key (stable claim).
//
// Why it exists: Entra's `sub` is pairwise over (app registration, person), so inside one
// Entra tenant the same person arrives with a different subject from a different app
// registration. Rule 1.5 misses, a tenant-defined row falls through to rule 2' and
// email_taken, and an existing user is locked out.
//
// What these tests pin down is that the fix is not built in a way that backfires:
//   - `subject` stays `sub` (replacing it would move the key of every existing row)
//   - the claim NAME is part of the match (a value that collides under a different
//     claim must not join)
//   - a tenant may only name a known stable claim (allowing email would recreate an
//     email join inside a shared realm)
//   - validation exists on the API side AND the runtime side (either alone leaves a row
//     that saves and then fails after approval)
//   - values come only from the token (a row may name the claim, never its value)

const entraIssuer = "https://login.microsoftonline.com/guid-1/v2.0"

// oidLink builds "the same Entra account, seen from a different app registration": a
// different sub, the same oid — which is how real Entra behaves.
func oidLink(provider, sub, oid, email string, emailJoin bool) store.IdentityLink {
	return store.IdentityLink{
		Provider: provider, Subject: sub, Realm: entraIssuer,
		RealmClaim: "oid", RealmSubject: oid, Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: emailJoin,
	}
}

// TestRule15JoinsAcrossAppRegistrationsByStableClaim is the main case: two buttons on
// different app registrations are still one person when the oid matches.
func TestRule15JoinsAcrossAppRegistrationsByStableClaim(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	const email = "yamada@acme.co.jp"

	first, _, err := st.LinkIdentity(ctx, oidLink("entra", "pairwise-A", "oid-1", email, true))
	if err != nil {
		t.Fatalf("head office: %v", err)
	}
	// A tenant-defined row, so EmailJoin=false: rule 2 is unavailable and rule 1.5 is
	// the only way through.
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
	// subject stays `sub`. Replacing it would take rule 1 (the pair match) off every
	// existing row.
	lp, _ := st.ListLinkedProviders(ctx, first.ID)
	subs := map[string]bool{}
	for _, r := range lp {
		subs[r.Subject] = true
	}
	if !subs["pairwise-A"] || !subs["pairwise-B"] {
		t.Fatalf("subject が sub 以外に書き換わっている: %+v", lp)
	}
	// A second login hits rule 1 — the evidence that the key did not move.
	again, isNew, err := st.LinkIdentity(ctx, oidLink("t:sub:entra", "pairwise-B", "oid-1", email, false))
	if err != nil || isNew || again.ID != first.ID {
		t.Fatalf("2 回目のログイン: %+v isNew=%v err=%v", again, isNew, err)
	}
}

// TestRule15DoesNotJoinWhenTheClaimNameDiffers pins that the claim name is part of the
// match. When one side reads oid and the other reads a different claim, a value that
// happens to collide must not join — the two are not answers to the same question.
func TestRule15DoesNotJoinWhenTheClaimNameDiffers(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()

	me, _, err := st.LinkIdentity(ctx, oidLink("entra", "s-1", "shared-value", "yamada@acme.co.jp", true))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	other := oidLink("t:sub:entra", "s-2", "shared-value", "suzuki@acme.co.jp", false)
	other.RealmClaim = "employee_id" // a different claim, the same value by coincidence
	got, _, err := st.LinkIdentity(ctx, other)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if got.ID == me.ID {
		t.Fatal("クレーム名が違うのに結合した — 値の衝突で他人になる")
	}
}

// TestRule15IgnoresRowsWithoutAStableClaim: existing rows (empty realm_subject) behave as
// before. Empty matching empty, folding everybody into one person, is the easiest way to
// break this.
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
	// The original realm+subject form of rule 1.5 still holds (the GitHub path).
	c, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "t:sub:entra", Subject: "s-1", Realm: entraIssuer,
		Email: "yamada@acme.co.jp", FallbackKey: "yamada-acme-co-jp", EmailJoin: false,
	})
	if err != nil || c.ID != a.ID {
		t.Fatalf("realm+subject の規則 1.5 が壊れた: %+v err=%v", c, err)
	}
}

// TestAttachRefusesAnAccountFoundByTheStableClaim: attach's refusal (§61.16) needs the
// second key as well. Looking at realm+subject alone, a pairwise sub makes the pair look
// unclaimed — and actually signing in that way then lands on somebody else's account.
func TestAttachRefusesAnAccountFoundByTheStableClaim(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()

	me, _, err := st.LinkIdentity(ctx, linkOf(auth.GoogleProviderID, "g-1", "yamada@acme.co.jp", true))
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

// --- the claims a row may name (the allowlist) --------------------------------

// TestTenantLinkClaimIsWhitelistedOnSaveAndAtRuntime: the validation lives in two places.
// Fixing only the API side still allows a row that saves and then fails after approval;
// fixing only the runtime side lets such a row be saved at all.
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

	// email / upn / preferred_username are ASSERTED values. Keying on one inside a
	// shared realm reaches an account created by a different authority — the takeover
	// of decision 32.
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
	// The runtime side: a row written without going through the API — an older binary,
	// or one saved before the allowlist changed — still cannot be built into a provider.
	bad := rows[0]
	bad.LinkClaim = "email"
	if _, err := auth.BuildTenantProvider(bad, store.TenantRef{Slug: "sub", Name: "子会社"}, "s"); err == nil {
		t.Fatal("実行時側の検証が無い — 保存できてしまえば承認後に効いてしまう")
	}
	if _, err := auth.BuildTenantProvider(rows[0], store.TenantRef{Slug: "sub", Name: "子会社"}, "s"); err != nil {
		t.Fatalf("oid の行は組めるはず: %v", err)
	}
	// A github row has no second key: its subject is the same across every app already.
	if w := post(`{"name":"github","kind":"github","client_id":"c","client_secret":"s","allowed_orgs":"acme-sub","allowed_domains":"@sub2.co.jp","link_claim":"oid"}`); w.Code != http.StatusOK {
		t.Fatalf("github 行: %d %s", w.Code, w.Body.String())
	}
	rows, _ = stt.ListTenantIdPs(ctx, tn.ID)
	for _, row := range rows {
		if row.Kind == auth.TenantIdPKindGitHub && row.LinkClaim != "" {
			t.Fatalf("github 行に link_claim が残っている: %+v", row)
		}
	}
}

// TestLinkClaimChangeRepends: changing link_claim sends the row back for approval. Who
// may get in does not change, but where they land does — it adds a button that reaches
// existing accounts, which is exactly the change an approver has to see.
func TestLinkClaimChangeRepends(t *testing.T) {
	active := store.TenantIdP{
		Kind: auth.TenantIdPKindOIDC, Status: "active", ClientID: "c", Issuer: entraIssuer,
		Trust: auth.TrustIssuer, AllowedDomains: "sub.co.jp",
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

// --- values come only from the token --------------------------------------------

// TestLinkClaimValueComesFromTheToken: a row (and env) may name only the claim NAME, and
// the value is always read from the token. Reversed, a tenant could write somebody else's
// oid and forge rule 1.5.
func TestLinkClaimValueComesFromTheToken(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{
		idTokenClaims: map[string]any{
			"sub": "pairwise-A", "email": "yamada@acme.co.jp", "email_verified": true,
			"oid": "oid-1",
		},
		userinfoClaims: map[string]any{"sub": "pairwise-A", "email": "yamada@acme.co.jp", "email_verified": true},
	})
	p := stubProvider("entra", idp, auth.TrustEmailVerified)
	p.LinkClaim = "oid"
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
	// An IdP that emits no such claim leaves both empty — the precondition for empty
	// never matching empty.
	p2 := stubProvider("okta", idp, auth.TrustEmailVerified)
	p2.LinkClaim = "employee_id"
	pr2, err := p2.Exchange(t.Context(), "code", "https://af.example.com/oauth2/callback")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if pr2.RealmClaim != "" || pr2.RealmSubject != "" {
		t.Fatalf("出ていないクレームで値が入った: %q / %q", pr2.RealmClaim, pr2.RealmSubject)
	}
	// Name no claim and nothing is read.
	p3 := stubProvider("plain", idp, auth.TrustEmailVerified)
	pr3, err := p3.Exchange(t.Context(), "code", "https://af.example.com/oauth2/callback")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if pr3.RealmClaim != "" || pr3.RealmSubject != "" {
		t.Fatalf("link_claim 未設定で値が入った: %q / %q", pr3.RealmClaim, pr3.RealmSubject)
	}
}
