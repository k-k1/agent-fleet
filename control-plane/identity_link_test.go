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

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// docs/log/61 P1 — deciding that two logins are the same person. What is pinned here:
//   - the first login on an existing Google deployment writes the (google, sub) row against
//     the current identity without moving user_key (zero migration)
//   - an email change at the IdP adds no identity; only the displayed email changes
//   - the same email from a different IdP is the same identity (same workspace / home /
//     secrets)
//   - an email that does not match becomes a new identity, and the user is told so
//   - AUTH=proxy / AUTH=dev change in no way (no IdP subject in those modes)

func newLinkStore(t *testing.T) *store.SQL {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func countRows(t *testing.T, st *store.SQL, table string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// linkOf is one perfectly ordinary login with no realm. Tests that exercise realm
// (rule 1.5, docs/log/61 §61.15) build an IdentityLink directly; the default is empty here
// because rows without a realm behaving exactly as before is the migration requirement.
func linkOf(provider, subject, email string, emailJoin bool) store.IdentityLink {
	return store.IdentityLink{
		Provider: provider, Subject: subject, Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: emailJoin,
	}
}

// TestLinkIdentityKeepsUserKeyAcrossEmailChange covers the migration face of acceptance
// criterion 6: someone on a deployment that has run Google-only must not become a different
// person at the first login after the upgrade, and a surname change at the IdP (a new
// email) must not move user_key, which is the home directory name.
func TestLinkIdentityKeepsUserKeyAcrossEmailChange(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	const email = "yamada@acme.co.jp"

	// A row that predates P1: an identity built from the email.
	seed, err := st.UpsertIdentity(ctx, email, sanitizeUser(email), "")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, isNew, err := st.LinkIdentity(ctx, linkOf(auth.GoogleProviderID, "g-sub-1", email, true))
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	if isNew {
		t.Fatal("the first login on an existing deployment is treated as a new account")
	}
	if got.ID != seed.ID || got.UserKey != seed.UserKey {
		t.Fatalf("identity moved: %+v want id=%s key=%s", got, seed.ID, seed.UserKey)
	}
	if n := countRows(t, st, "identity_provider"); n != 1 {
		t.Fatalf("identity_provider rows = %d, want 1", n)
	}

	// Re-login with the same (provider, subject) must not add an identity.
	again, isNew, err := st.LinkIdentity(ctx, linkOf(auth.GoogleProviderID, "g-sub-1", email, true))
	if err != nil || isNew || again.ID != seed.ID {
		t.Fatalf("re-login: id=%s isNew=%v err=%v", again.ID, isNew, err)
	}
	if n := countRows(t, st, "identity"); n != 1 {
		t.Fatalf("identity rows after re-login = %d, want 1", n)
	}

	// The email changed at the IdP (surname change, domain merge). Still the same person:
	// user_key stays put and only the displayed email is new.
	const renamed = "yamada-hanako@acme.co.jp"
	moved, isNew, err := st.LinkIdentity(ctx, linkOf(auth.GoogleProviderID, "g-sub-1", renamed, true))
	if err != nil {
		t.Fatalf("after rename: %v", err)
	}
	switch {
	case isNew:
		t.Fatal("an email change created a new account")
	case moved.ID != seed.ID:
		t.Fatalf("an email change moved the identity: %s -> %s", seed.ID, moved.ID)
	case moved.UserKey != seed.UserKey:
		t.Fatalf("user_key moved: %q -> %q (it is the home directory name, so staying put is the requirement)", seed.UserKey, moved.UserKey)
	case moved.Email != renamed:
		t.Fatalf("the displayed email was not updated: %q", moved.Email)
	}
	if n := countRows(t, st, "identity"); n != 1 {
		t.Fatalf("identity rows after rename = %d, want 1", n)
	}
}

// TestLinkIdentityJoinsSameEmailFromAnotherProvider — entering from another IdP with the
// same email is the same person: which button was pressed must not change the workspace
// (§61.5, second line).
func TestLinkIdentityJoinsSameEmailFromAnotherProvider(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	const email = "yamada@acme.co.jp"

	first, _, err := st.LinkIdentity(ctx, linkOf(auth.GoogleProviderID, "g-1", email, true))
	if err != nil {
		t.Fatalf("google: %v", err)
	}
	second, isNew, err := st.LinkIdentity(ctx, linkOf("entra", "e-1", email, true))
	if err != nil {
		t.Fatalf("entra: %v", err)
	}
	if isNew || second.ID != first.ID || second.UserKey != first.UserKey {
		t.Fatalf("a different IdP with the same email became a different person: %+v want %+v (isNew=%v)", second, first, isNew)
	}
	if n := countRows(t, st, "identity"); n != 1 {
		t.Fatalf("identity rows = %d, want 1", n)
	}
	if n := countRows(t, st, "identity_provider"); n != 2 {
		t.Fatalf("identity_provider rows = %d, want 2", n)
	}
}

// TestLinkIdentityNewAccountWhenEmailIsUnknown — an email that does not match becomes a new
// identity. isNew is the only basis for the notice shown right after login, so it is pinned
// here (acceptance criterion 3), together with the fact that claiming a row an invitation
// created earlier is not "new".
func TestLinkIdentityNewAccountWhenEmailIsUnknown(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	const known = "yamada@acme.co.jp"
	first, _, err := st.LinkIdentity(ctx, linkOf(auth.GoogleProviderID, "g-1", known, true))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	const other = "tanaka@acme.co.jp"
	got, isNew, err := st.LinkIdentity(ctx, linkOf("entra", "e-2", other, true))
	if err != nil {
		t.Fatalf("new person: %v", err)
	}
	if !isNew {
		t.Fatal("an unknown email is not reported as a new account")
	}
	if got.ID == first.ID {
		t.Fatal("a different email joined an existing identity (by design there is no join)")
	}
	if got.UserKey != sanitizeUser(other) {
		t.Fatalf("user_key = %q, want %q", got.UserKey, sanitizeUser(other))
	}
	if n := countRows(t, st, "identity"); n != 2 {
		t.Fatalf("identity rows = %d, want 2", n)
	}

	// A row an admin created by user_key while the email was still unknown (tenants.go),
	// then claimed by its owner at first login, is not a new account.
	const invited = "suzuki@acme.co.jp"
	inv, err := st.UpsertIdentity(ctx, "", sanitizeUser(invited), "")
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	claimed, isNew, err := st.LinkIdentity(ctx, linkOf(auth.GoogleProviderID, "g-3", invited, true))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if isNew || claimed.ID != inv.ID {
		t.Fatalf("claiming an invited row: id=%s want %s isNew=%v", claimed.ID, inv.ID, isNew)
	}
}

// TestIdentityResolutionUnchangedWithoutAnIdPSubject — AUTH=proxy and AUTH=dev have neither
// provider nor subject. P1 does nothing there and keeps the current contract of resolving
// by email alone; failing closed here would break existing proxy deployments and dev.
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
		t.Fatalf("%d identity_provider rows were written in a mode that has no subject", n)
	}
}

// TestAuthGateCarriesTheIdPSubjectIntoIdentityResolution — authGate must carry prov/sub
// downstream; drop them and an email change moves the home. The judgement is made on what
// comes back after a rename, because as long as the email stays the same this path can be
// broken and the test still passes.
func TestAuthGateCarriesTheIdPSubjectIntoIdentityResolution(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	const email = "yamada@acme.co.jp"
	seed, err := st.UpsertIdentity(ctx, email, sanitizeUser(email), "")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	idp := newStubIdP(t, &stubIdP{})
	cfg := oauthTestConfig(t, stubProvider(auth.GoogleProviderID, idp, auth.TrustEmailVerified))
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
			Prov: auth.GoogleProviderID, Sub: "g-sub-1",
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
	// Same subject, different email. Falling back to the email-derived key moves the home.
	const renamed = "yamada-hanako@acme.co.jp"
	if got := call(renamed); got != seed.UserKey {
		t.Fatalf("user_key after the rename = %q, want %q (falling back to %q means prov/sub never arrived)",
			got, seed.UserKey, sanitizeUser(renamed))
	}
	if n := countRows(t, st, "identity"); n != 1 {
		t.Fatalf("identity rows = %d, want 1", n)
	}
}

// TestNewAccountNoticeIsShownOnceOnMultiIdPDeployments — a new account is surfaced to its
// owner (acceptance criterion 3: never silently create a second workspace). On a deployment
// with a single IdP, "new" only ever means a new colleague, so the existing experience is
// left as it was.
func TestNewAccountNoticeIsShownOnceOnMultiIdPDeployments(t *testing.T) {
	st := newLinkStore(t)
	newcomer := func(t *testing.T, email string) *stubIdP {
		return newStubIdP(t, &stubIdP{
			idTokenClaims:  map[string]any{"sub": "sub-" + email, "email": email, "email_verified": true},
			userinfoClaims: map[string]any{"sub": "sub-" + email, "email": email, "email_verified": true},
		})
	}

	idp := newcomer(t, "tanaka@acme.co.jp")
	cfg := oauthTestConfig(t, stubProvider(auth.GoogleProviderID, idp, auth.TrustEmailVerified),
		stubProvider("okta", idp, auth.TrustEmailVerified))
	cfg.mgr.store = st

	st1, au := startLogin(t, cfg, "?provider=okta&next=%2Fsessions")
	w := callback(t, cfg, st1, "code", au.Query().Get("state"))
	if w.Code != http.StatusOK {
		t.Fatalf("no notice is shown for a new account: %d -> %s", w.Code, w.Header().Get("Location"))
	}
	page := w.Body.String()
	if !strings.Contains(page, "新しいワークスペース") || !strings.Contains(page, "tanaka@acme.co.jp") {
		t.Fatalf("notice body:\n%s", page)
	}
	if sessionCookieOf(t, w) == nil {
		t.Fatal("the session must not be dropped in order to show the notice")
	}

	// The second time the (provider, subject) is the same, so it is not new: pass through.
	st2, au := startLogin(t, cfg, "?provider=okta&next=%2Fsessions")
	w = callback(t, cfg, st2, "code", au.Query().Get("state"))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/sessions" {
		t.Fatalf("re-login: %d -> %q", w.Code, w.Header().Get("Location"))
	}

	// A deployment with a single IdP passes through even for someone new.
	single := oauthTestConfig(t, stubProvider(auth.GoogleProviderID, newcomer(t, "sato@acme.co.jp"), auth.TrustEmailVerified))
	single.mgr.store = st
	st3, au := startLogin(t, single, "?next=%2Fsessions")
	w = callback(t, single, st3, "code", au.Query().Get("state"))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/sessions" {
		t.Fatalf("single-IdP deployment: %d -> %q", w.Code, w.Header().Get("Location"))
	}
}
