package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// P7-3 (docs/log/61 §61.17.4 (b) + 決定 41): a SECOND app registration of a directory
// this deployment already has a door to.
//
// The failure being prevented is remote from its cause: the tenant saves a row that
// looks fine, it is approved, and then every colleague who already signs in here is
// refused at login with "this email address is already used by another sign-in
// method" (rule 2'), because a pairwise issuer hands the same person a different
// `sub` per client. Nobody at that point can connect the two events.

// stubIssuer serves just enough discovery to answer "is this issuer pairwise".
// It returns its own URL as the issuer so the endpoints() issuer check passes.
func stubIssuer(t *testing.T, subjectTypes ...string) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/authorize",
			"token_endpoint":                        srv.URL + "/token",
			"subject_types_supported":               subjectTypes,
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	return srv.URL
}

func TestTenantIdPPairwiseSecondRegistrationNeedsLinkClaim(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sub", "子会社")
	if _, err := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("super admin: %v", err)
	}

	pairwise := stubIssuer(t, "pairwise")
	public := stubIssuer(t, "public")

	post := func(api tenantIdPAPI, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/admin/tenants/sub/idp", strings.NewReader(body))
		r.SetPathValue("slug", "sub")
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		w := httptest.NewRecorder()
		api.upsert(w, r)
		return w
	}
	row := func(name, issuer, extra string) string {
		return `{"name":"` + name + `","issuer":"` + issuer + `","client_id":"c","client_secret":"s",` +
			`"trust":"issuer","allowed_domains":"sub.co.jp"` + extra + `}`
	}

	api := newTenantIdPAPI(mgr, nil)

	// ★ The FIRST registration of a pairwise issuer is fine — one door splits nobody.
	// Demanding a claim here would be noise on the common case (every Entra tenant).
	if w := post(api, row("entra", pairwise, "")); w.Code != http.StatusOK {
		t.Fatalf("first registration must be accepted: %d %s", w.Code, w.Body.String())
	}

	// The second one, same issuer, no claim: refused with a reason somebody can act on.
	w := post(api, row("entra2", pairwise, ""))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "tenant_idp_link_claim_required") {
		t.Fatalf("a second registration of a pairwise issuer must be refused: %d %s", w.Code, w.Body.String())
	}
	// …and accepted once it says how the same person is recognised.
	if w := post(api, row("entra2", pairwise, `,"link_claim":"oid"`)); w.Code != http.StatusOK {
		t.Fatalf("link_claim must unblock it: %d %s", w.Code, w.Body.String())
	}

	// ★ The decision comes from DISCOVERY, not from the issuer's hostname. A second
	// registration of a PUBLIC-subject issuer is fine: rule 1.5 joins those on `sub`.
	if w := post(api, row("okta", public, "")); w.Code != http.StatusOK {
		t.Fatalf("first public registration: %d %s", w.Code, w.Body.String())
	}
	if w := post(api, row("okta2", public, "")); w.Code != http.StatusOK {
		t.Fatalf("a public-subject issuer must not require a claim: %d %s", w.Code, w.Body.String())
	}

	// ★ The deployment's OWN provider counts as a door. This is the commonest shape:
	// the tenant registers its own app for the directory the deployment already uses,
	// and the DB rows alone would not see it.
	envAPI := newTenantIdPAPI(mgr, []auth.LoginProvider{&auth.OIDCProvider{ProviderID: "entra-env", Issuer: public}})
	w = post(envAPI, row("okta3", public, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("a public issuer stays fine even when env has it: %d %s", w.Code, w.Body.String())
	}
	envPairwise := newTenantIdPAPI(mgr, []auth.LoginProvider{&auth.OIDCProvider{ProviderID: "entra-env", Issuer: pairwise}})
	st2 := p3Store(t) // a store with no rows, so only the env provider can be the "other door"
	mgr2 := p4Manager(t, st2)
	if _, err := st2.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("super admin: %v", err)
	}
	if _, err := st2.CreateTenant(ctx, "sub", "子会社"); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	envPairwise.mgr = mgr2
	w = post(envPairwise, row("entra", pairwise, ""))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "tenant_idp_link_claim_required") {
		t.Fatalf("an env provider on the same pairwise issuer must trigger it: %d %s", w.Code, w.Body.String())
	}

	// ★ Discovery being unreachable is NOT a refusal. The issuer may simply not be
	// reachable from the CP right now (it is fetched lazily everywhere else for the
	// same reason), and a network blip must not stop somebody saving a form.
	dead := "http://127.0.0.1:1/unreachable"
	seedTenantIdP(t, st, tn.ID, "ghost", "ghost.example", "active")
	if err := st.CreateTenantIdP(ctx, store.TenantIdP{
		ID: store.NewID(), TenantID: tn.ID, Name: "dead1", Issuer: dead, ClientID: "c",
		SecretEnc: "s", Trust: auth.TrustIssuer, AllowedDomains: "sub.co.jp",
		Status: "active", CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if w := post(api, row("dead2", dead, "")); w.Code != http.StatusOK {
		t.Fatalf("an unreachable issuer must not block the save: %d %s", w.Code, w.Body.String())
	}
}

// The ordering rule (docs/log/61 §61.17.4): the old row may not be stopped until the
// people on it have another way in. It is a question, not a veto — suspending is
// also how a compromised IdP is stopped.
func TestSuspendWarnsWhenItIsSomebodysOnlyMethod(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sub", "子会社")
	if _, err := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("super admin: %v", err)
	}
	row := seedTenantIdP(t, st, tn.ID, "entra", "sub.co.jp", "active")
	provID := auth.TenantProviderID("sub", "entra")

	// Somebody whose only proven login is this method.
	only, _ := st.UpsertIdentity(ctx, "only@sub.co.jp", "only-sub-co-jp", "")
	if _, err := st.EnsureMembership(ctx, only.ID, tn.ID, "member"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	if _, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: provID, Subject: "s-only", Email: "only@sub.co.jp", FallbackKey: "only-sub-co-jp",
	}); err != nil {
		t.Fatalf("link: %v", err)
	}

	api := newTenantIdPAPI(mgr, nil)
	suspend := func(q string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/admin/tenants/sub/idp/"+row.ID+"/status"+q,
			strings.NewReader(`{"status":"suspended"}`))
		r.SetPathValue("slug", "sub")
		r.SetPathValue("id", row.ID)
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		w := httptest.NewRecorder()
		api.setStatus(w, r)
		return w
	}

	w := suspend("")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "tenant_idp_last_method_for_members") {
		t.Fatalf("suspending somebody's only method must ask first: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "1 active member") {
		t.Fatalf("the answer must say how many people: %s", w.Body.String())
	}
	// ★ Overridable. Stopping a compromised IdP must stay faster than starting one.
	if w := suspend("?confirm=1"); w.Code != http.StatusOK {
		t.Fatalf("confirm must go through: %d %s", w.Code, w.Body.String())
	}

	// Once that person has a second proven login, the question stops being asked —
	// which is exactly the ordering the rule is there to enforce.
	if err := st.SetTenantIdPStatus(ctx, tn.ID, row.ID, "active", "boss", store.NowTS(), store.NowTS()); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if _, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "google", Subject: "g-only", Email: "only@sub.co.jp",
		FallbackKey: "only-sub-co-jp", EmailJoin: true,
	}); err != nil {
		t.Fatalf("second link: %v", err)
	}
	if w := suspend(""); w.Code != http.StatusOK {
		t.Fatalf("with a second method there is nothing to warn about: %d %s", w.Code, w.Body.String())
	}
}
