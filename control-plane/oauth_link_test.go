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
	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// docs/log/61 §61.16 + ADR0043 decision 37 — attaching a second sign-in method with
// the owner's consent. What is pinned here:
//   - a link only ever starts from the signed-in owner (/oauth2/ is excluded from
//     authGate, so the handler is the gate itself: drop that check and identity rows
//     become writable without authentication)
//   - the method's own gate (org, domain) always runs — a link is not a way around it
//   - refuse when the claimed address is not the owner's (decision 37: a different
//     email is never joined)
//   - refuse when the far-side IdP account already belongs to somebody (never move it,
//     never merge)
//   - the identity row is untouched, so roles do not move (decision 31)
//   - the link round trip never issues or replaces a session

// --- store layer -------------------------------------------------------------

// After a link, signing in with that method must land on the same identity by rule 1,
// and the identity row (email, user_key, role) must not move because of the link.
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
	// Pressing the button twice by mistake changes nothing (idempotent).
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
	// Actually signing in with the linked method resolves to the same person by rule 1.
	// The point is that it passes with emailJoin=false (a tenant-defined method), so
	// rule 2' is not what carried it.
	got, isNew, err := st.LinkIdentity(ctx, linkOf("t:sub:github", "gh-1", email, false))
	if err != nil || isNew || got.ID != me.ID {
		t.Fatalf("login with the linked method: %+v isNew=%v err=%v", got, isNew, err)
	}
	lp, err := st.ListLinkedProviders(ctx, me.ID)
	if err != nil || len(lp) != 2 {
		t.Fatalf("linked = %+v (%v), want 2", lp, err)
	}
}

// Never move an account, never merge two. All three routes here (the pair is already
// someone else's, rule 1.5 lands on someone else, the address is someone else's) are
// irreversible once taken, so refusing is the only right answer.
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

	// 1. The pair itself already belongs to somebody else.
	if err := st.AttachProvider(ctx, me.ID, store.IdentityLink{
		Provider: "github", Subject: "gh-9", Realm: auth.GithubWebBase, Email: "yamada@acme.co.jp",
	}); !errors.Is(err, store.ErrLinkTaken) {
		t.Fatalf("err = %v, want errLinkTaken", err)
	}
	// 2. Rule 1.5 — a different button (the tenant's GitHub) but the same GitHub
	//    account. Signing in with it lands on `other`, so linking it here would let
	//    two people claim one account.
	if err := st.AttachProvider(ctx, me.ID, store.IdentityLink{
		Provider: "t:sub:github", Subject: "gh-9", Realm: auth.GithubWebBase, Email: "yamada@acme.co.jp",
	}); !errors.Is(err, store.ErrLinkTaken) {
		t.Fatalf("rule 1.5 err = %v, want errLinkTaken", err)
	}
	// 3. The address is somebody else's — a layer that still holds behind the
	//    caller's same-address rule.
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

// linkTestConfig is the minimal setup for "someone signed in with Google adds a second
// method (entra)".
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

// This is the only gate. /oauth2/ is an authGate-excluded prefix, so dropping this
// check turns the route into an endpoint that writes identity_provider with no session.
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

// The happy path: after the round trip the pair is recorded, and the session is neither
// issued nor replaced.
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
	// realm must be stamped with what the adapter reported — the precondition for
	// rule 1.5 to work at all.
	for _, r := range lp {
		if r.Provider == "entra" && r.Realm != idp.URL {
			t.Fatalf("realm = %q, want %q", r.Realm, idp.URL)
		}
	}
	// And from now on that method reaches the same workspaces.
	got, isNew, err := st.LinkIdentity(t.Context(), linkOf("entra", "e-1", email, true))
	if err != nil || isNew || got.ID != me.ID {
		t.Fatalf("sign-in with the linked method: %+v isNew=%v err=%v", got, isNew, err)
	}
}

// Decision 37: only a method claiming the same address may be added. Joining a
// different address runs into §61.5 — being able to sign in to both is not proof that
// they are the same person.
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

// A link is not a way around the gate: someone who cannot sign in with that method
// cannot link it either.
func TestLinkRunsTheMethodsOwnGate(t *testing.T) {
	const email = "yamada@acme.co.jp"
	idp := newStubIdP(t, &stubIdP{
		idTokenClaims:  map[string]any{"sub": "e-1", "email": email, "email_verified": true},
		userinfoClaims: map[string]any{"sub": "e-1", "email": email, "email_verified": true},
	})
	cfg, st := linkTestConfig(t, idp)
	// An allow-list on this provider alone, for another domain — so this person is out.
	cfg.providers[0].(*auth.OIDCProvider).AllowDomains = envx.DomainSet("sub.co.jp")
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

// A signed state only says "the CP wrote this". If the browser became somebody else on
// the second leg (signed in again in another tab), the method would be attached to that
// other person's account.
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
	// Come back holding somebody else's session.
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

// A tenant-defined method is only offered to people on that tenant's roster, for the
// same reason as decision 32-4: a subsidiary's list is not shown to the whole
// deployment. The rule runs at the start of the flow too, not just when filtering the
// list (decision 14: what is displayed is not a gate).
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

// The account screen's list: what is linked, and what may be added next. A method
// already held is not offered again.
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
	// entra is already linked and so drops out of the candidates; google is in env but
	// is not treated as unlinked either.
	for _, c := range got.Linkable {
		if c.Provider == "entra" {
			t.Fatalf("an already-linked method must not be offered again: %+v", got.Linkable)
		}
	}
}

// --- detaching (docs/log/61 §61.16.4) ----------------------------------------------

// detachReq issues one DELETE /api/me/login-methods. provider / subject go in the query
// rather than the path, because a tenant provider id contains ":".
func detachReq(t *testing.T, api accountAPI, me store.Identity, cur loginRef, provider, subject string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{"provider": {provider}, "subject": {subject}}
	r := httptest.NewRequest(http.MethodDelete, "/api/me/login-methods?"+q.Encode(), nil)
	r = r.WithContext(withLoginRef(r.Context(), cur))
	w := httptest.NewRecorder()
	api.detachLoginMethod(w, r, me)
	return w
}

// The three guards break in three different ways if any one of them goes:
//   - removing the last method leaves an account nobody can ever enter again, with no
//     recovery path
//   - removing the method in use cuts the ground from under the current session
//   - somebody else's row: drop identity_id from the WHERE clause and guessing a pair
//     is enough to delete another person's method
func TestDetachRefusesTheLastMethodTheCurrentOneAndSomebodyElses(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{})
	cfg, st := linkTestConfig(t, idp)
	me, _ := seedSignedIn(t, cfg, st, "yamada@acme.co.jp")
	api := newAccountAPI(cfg)
	cur := loginRef{auth.GoogleProviderID, "g-1"}

	// 1. Only one method exists. That overlaps with the current-method guard, so
	//    pretend the session came in through another method and observe the count
	//    guard on its own — the two hold independently.
	if w := detachReq(t, api, me, loginRef{"entra", "e-1"}, auth.GoogleProviderID, "g-1"); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "last_login_method") {
		t.Fatalf("最後の 1 つは外せてはいけない: %d %s", w.Code, w.Body.String())
	}
	// The SQL layer counts too: another tab can slip in between the API's check and
	// the DELETE.
	if err := st.DetachProvider(t.Context(), me.ID, auth.GoogleProviderID, "g-1"); !errors.Is(err, store.ErrLastLoginMethod) {
		t.Fatalf("store 層の残数ガードが効いていない: %v", err)
	}

	if err := st.AttachProvider(t.Context(), me.ID, store.IdentityLink{
		Provider: "entra", Subject: "e-1", Realm: idp.URL, Email: "yamada@acme.co.jp",
	}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// 2. Even with two, the method the session is using cannot be removed.
	if w := detachReq(t, api, me, cur, auth.GoogleProviderID, "g-1"); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "current_login_method") {
		t.Fatalf("現セッションの方式は外せてはいけない: %d %s", w.Code, w.Body.String())
	}
	// 3. Somebody else's row: knowing the pair still does not reach it.
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

	// And the legitimate one does come off. The identity row does not move — a detach
	// is no more a login than an attach is.
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
	// It lands in the ledger, so when that door opened and closed stays readable later.
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

// The server answers "may this be removed" as part of the list. The UI only mirrors that
// answer and decides nothing (decision 14), and the detach API checks the same rule
// again for itself.
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

	// With only one, it is also the current session's method, so both guards bite.
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
