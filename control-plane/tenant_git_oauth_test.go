package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// docs/log/71 + ADR0052. What these pin down is the part of "the tenant owns its git OAuth
// app" that is easy to lose in a later edit, and each one has a consequence:
//
//   - the secret is write-only and survives a save that omits it
//   - a Bitbucket row cannot be created without a secret (it would fail at the token
//     exchange, looking configured the whole time)
//   - a GitHub row never stores one (the device flow has no use for it)
//   - one tenant's administrator cannot read or write another tenant's app
//   - env is not a fallback: with no row, the OAuth start says not_configured

func gitOAuthEnv(t *testing.T) (*store.SQL, *manager, tenantGitOAuthAPI) {
	t.Helper()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	return st, mgr, newTenantGitOAuthAPI(mgr)
}

// gitOAuthCall drives one admin request. The path values are set by hand because the
// handlers are called directly rather than through the mux.
func gitOAuthCall(api tenantGitOAuthAPI, method, slug, provider, email, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/admin/tenants/"+slug+"/git-oauth/"+provider, strings.NewReader(body))
	r.SetPathValue("slug", slug)
	r.SetPathValue("provider", provider)
	r.Header.Set("X-Forwarded-Email", email)
	w := httptest.NewRecorder()
	switch method {
	case http.MethodGet:
		api.list(w, r)
	case http.MethodDelete:
		api.remove(w, r)
	default:
		api.save(w, r)
	}
	return w
}

// seedGitOAuthTenant creates a tenant with one tenant_admin.
func seedGitOAuthTenant(t *testing.T, st *store.SQL, slug, adminEmail string) store.Tenant {
	t.Helper()
	ctx := context.Background()
	tn, err := st.CreateTenant(ctx, slug, slug)
	if err != nil {
		t.Fatalf("tenant %s: %v", slug, err)
	}
	admin, err := st.UpsertIdentity(ctx, adminEmail, sanitizeUser(adminEmail), "")
	if err != nil {
		t.Fatalf("identity %s: %v", adminEmail, err)
	}
	if _, err := st.EnsureMembership(ctx, admin.ID, tn.ID, "tenant_admin"); err != nil {
		t.Fatalf("membership %s: %v", adminEmail, err)
	}
	return tn
}

// ★ The editor never sees the stored secret, so it cannot retype it. A save that omits
// the field therefore has to KEEP it — otherwise renaming the client_id silently blanks
// the credential and the next connect fails at Bitbucket with invalid_client.
func TestGitOAuthSecretIsWriteOnlyAndSurvivesAnOmittedSave(t *testing.T) {
	ctx := context.Background()
	st, mgr, api := gitOAuthEnv(t)
	tn := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")

	w := gitOAuthCall(api, http.MethodPut, "sub", "bitbucket", "admin@sub.co.jp",
		`{"client_id":"key-1","client_secret":"s3cret"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "s3cret") {
		t.Fatalf("the response must never carry the secret: %s", w.Body.String())
	}

	// Edit the client_id only.
	w = gitOAuthCall(api, http.MethodPut, "sub", "bitbucket", "admin@sub.co.jp", `{"client_id":"key-2"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	id, secret, ok, err := mgr.gitOAuthApp(ctx, tn.ID, gitOAuthBitbucket)
	if err != nil || !ok {
		t.Fatalf("resolve = (%v,%v)", ok, err)
	}
	if id != "key-2" || secret != "s3cret" {
		t.Fatalf("after the edit: client_id=%q secret kept=%v", id, secret == "s3cret")
	}

	// The listing reports that a secret exists without disclosing it.
	w = gitOAuthCall(api, http.MethodGet, "sub", "", "admin@sub.co.jp", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "s3cret") {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var listed struct {
		Providers []gitOAuthBody `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed.Providers) != len(gitOAuthProviders) {
		t.Fatalf("every known provider must come back as a card: %+v", listed.Providers)
	}
	for _, p := range listed.Providers {
		if p.Provider == gitOAuthBitbucket && (!p.HasSecret || !p.NeedsSecret || p.ClientID != "key-2") {
			t.Fatalf("bitbucket card: %+v", p)
		}
		if p.Provider == gitOAuthGitHub && (p.HasSecret || p.NeedsSecret || p.ClientID != "") {
			t.Fatalf("an unregistered github card must be empty and secretless: %+v", p)
		}
	}
}

// ★ A first save with no secret would store a row that LOOKS configured — the member
// gets the OAuth button, presses it, and the failure surfaces at Bitbucket as
// invalid_client. Refuse it where the administrator can still act.
func TestGitOAuthBitbucketRefusesAFirstSaveWithNoSecret(t *testing.T) {
	st, _, api := gitOAuthEnv(t)
	seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	w := gitOAuthCall(api, http.MethodPut, "sub", "bitbucket", "admin@sub.co.jp", `{"client_id":"key-1"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "secret_required") {
		t.Fatalf("want secret_required, got %d %s", w.Code, w.Body.String())
	}
}

// GitHub's device grant authenticates with the client_id alone. Storing a secret anyway
// would leave a credential in the database that nothing reads and nobody rotates.
func TestGitOAuthGithubNeverStoresASecret(t *testing.T) {
	ctx := context.Background()
	st, mgr, api := gitOAuthEnv(t)
	tn := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	w := gitOAuthCall(api, http.MethodPut, "sub", "github", "admin@sub.co.jp",
		`{"client_id":"Iv1.app","client_secret":"should-be-dropped"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	row, ok, err := st.GetTenantGitOAuth(ctx, tn.ID, gitOAuthGitHub)
	if err != nil || !ok {
		t.Fatalf("row = (%v,%v)", ok, err)
	}
	if row.SecretEnc != "" || row.KeyRef != "" {
		t.Fatalf("github row kept a secret: %+v", row)
	}
	if id, _, ok, _ := mgr.gitOAuthApp(ctx, tn.ID, gitOAuthGitHub); !ok || id != "Iv1.app" {
		t.Fatalf("resolve = (%q,%v)", id, ok)
	}
}

// ★ The whole point of the row being per tenant is that it is per tenant. A
// tenant_admin of one company must not read another's client_id, let alone overwrite
// the app their members are sent to.
func TestGitOAuthIsScopedToTheTenant(t *testing.T) {
	ctx := context.Background()
	st, mgr, api := gitOAuthEnv(t)
	a := seedGitOAuthTenant(t, st, "alpha", "admin@alpha.example")
	seedGitOAuthTenant(t, st, "beta", "admin@beta.example")

	if w := gitOAuthCall(api, http.MethodPut, "alpha", "github", "admin@alpha.example",
		`{"client_id":"alpha-app"}`); w.Code != http.StatusOK {
		t.Fatalf("alpha save: %d %s", w.Code, w.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		w := gitOAuthCall(api, method, "alpha", "github", "admin@beta.example", `{"client_id":"stolen"}`)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s by another tenant's admin: want 403 got %d %s", method, w.Code, w.Body.String())
		}
	}
	if id, _, _, _ := mgr.gitOAuthApp(ctx, a.ID, gitOAuthGitHub); id != "alpha-app" {
		t.Fatalf("alpha's app was reachable from beta: %q", id)
	}
	// Beta has none of its own — an unconfigured tenant is not "the other tenant's".
	if mgr.gitOAuthConfigured(ctx, "", gitOAuthGitHub) {
		t.Fatal("an empty tenant id must never resolve to an app")
	}
}

// Deleting takes the OAuth option away from new connections.
func TestGitOAuthDeleteRemovesTheApp(t *testing.T) {
	ctx := context.Background()
	st, mgr, api := gitOAuthEnv(t)
	tn := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	if w := gitOAuthCall(api, http.MethodPut, "sub", "github", "admin@sub.co.jp",
		`{"client_id":"Iv1.app"}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if !mgr.gitOAuthConfigured(ctx, tn.ID, gitOAuthGitHub) {
		t.Fatal("precondition: configured")
	}
	if w := gitOAuthCall(api, http.MethodDelete, "sub", "github", "admin@sub.co.jp", ""); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if mgr.gitOAuthConfigured(ctx, tn.ID, gitOAuthGitHub) {
		t.Fatal("still configured after delete")
	}
}

// An unknown provider slug is refused rather than stored — a row nothing ever reads is
// worse than an error, because it looks like the setting was accepted.
func TestGitOAuthRefusesAnUnknownProvider(t *testing.T) {
	st, _, api := gitOAuthEnv(t)
	seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	w := gitOAuthCall(api, http.MethodPut, "sub", "gitlab", "admin@sub.co.jp", `{"client_id":"x"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "bad_provider") {
		t.Fatalf("want bad_provider, got %d %s", w.Code, w.Body.String())
	}
}

// ★ env is NOT a fallback (docs/log/71 決定 2). With BITBUCKET_OAUTH_KEY/SECRET set in the
// process and no row for the tenant, the start leg must still answer not_configured —
// otherwise "which app am I being sent to" has two answers and the one that wins
// depends on which tenant you are in.
func TestGitOAuthStartDoesNotFallBackToEnv(t *testing.T) {
	ctx := context.Background()
	st, mgr, _ := gitOAuthEnv(t)
	t.Setenv("BITBUCKET_OAUTH_KEY", "deployment-key")
	t.Setenv("BITBUCKET_OAUTH_SECRET", "deployment-secret")
	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "deployment-client-id")

	tn := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	member, _ := st.UpsertIdentity(ctx, "user@sub.co.jp", "user-sub-co-jp", "")
	if _, err := st.EnsureMembership(ctx, member.ID, tn.ID, "member"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	mgr.publicBaseURL = "https://af.example"
	cfg := config{mgr: mgr, publicBaseURL: mgr.publicBaseURL}

	for _, tc := range []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
		body string
	}{
		{"bitbucket", cfg.handleBitbucketOAuthStart, ""},
		{"github", cfg.handleGithubDeviceStart, "{}"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/connections/git/"+tc.name+"/oauth/start", strings.NewReader(tc.body))
		r.Header.Set("X-Forwarded-Email", "user@sub.co.jp")
		r.Header.Set("X-AF-Tenant", "sub")
		w := httptest.NewRecorder()
		tc.call(w, r)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not_configured") {
			t.Fatalf("%s start: want not_configured, got %d %s", tc.name, w.Code, w.Body.String())
		}
	}
}

// ★ "The app is registered but PUBLIC_BASE_URL is missing" must NOT be reported as
// not_configured. The two have different owners: the first is the tenant administrator's
// to fix, the second the operator's — and a tenant administrator told "not configured"
// goes and re-enters a setting that was already correct.
func TestBitbucketStartSeparatesAMissingPublicBaseURLFromAMissingApp(t *testing.T) {
	ctx := context.Background()
	st, mgr, api := gitOAuthEnv(t)
	tn := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	member, _ := st.UpsertIdentity(ctx, "user@sub.co.jp", "user-sub-co-jp", "")
	if _, err := st.EnsureMembership(ctx, member.ID, tn.ID, "member"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	if w := gitOAuthCall(api, http.MethodPut, "sub", "bitbucket", "admin@sub.co.jp",
		`{"client_id":"key-1","client_secret":"s3cret"}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	start := func() *httptest.ResponseRecorder {
		cfg := config{mgr: mgr, publicBaseURL: mgr.publicBaseURL}
		r := httptest.NewRequest(http.MethodGet, "/api/connections/git/bitbucket/oauth/start", nil)
		r.Header.Set("X-Forwarded-Email", "user@sub.co.jp")
		r.Header.Set("X-AF-Tenant", "sub")
		w := httptest.NewRecorder()
		cfg.handleBitbucketOAuthStart(w, r)
		return w
	}

	mgr.publicBaseURL = "" // the operator has not set one
	w := start()
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no_public_base_url") {
		t.Fatalf("want no_public_base_url, got %d %s", w.Code, w.Body.String())
	}

	mgr.publicBaseURL = "https://af.example"
	w = start()
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	// The authorize URL carries the tenant's key and the CP-owned callback.
	body := w.Body.String()
	for _, want := range []string{"client_id=key-1", "af.example%2Fapi%2Foauth%2Fbitbucket%2Fcallback"} {
		if !strings.Contains(body, want) {
			t.Fatalf("authorize_url missing %q: %s", want, body)
		}
	}
}

// The member-facing availability endpoint answers for the caller's own tenant, and it
// is what the git tab draws its buttons from.
func TestGitOAuthAvailabilityIsPerTenant(t *testing.T) {
	ctx := context.Background()
	st, _, api := gitOAuthEnv(t)
	tn := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	member, _ := st.UpsertIdentity(ctx, "user@sub.co.jp", "user-sub-co-jp", "")
	if _, err := st.EnsureMembership(ctx, member.ID, tn.ID, "member"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	ask := func() map[string]struct {
		Configured bool `json:"configured"`
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/git-oauth", nil)
		r.Header.Set("X-Forwarded-Email", "user@sub.co.jp")
		r.Header.Set("X-AF-Tenant", "sub")
		w := httptest.NewRecorder()
		api.withMembership(api.availability)(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("availability: %d %s", w.Code, w.Body.String())
		}
		var out map[string]struct {
			Configured bool `json:"configured"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}
	if got := ask(); got[gitOAuthGitHub].Configured || got[gitOAuthBitbucket].Configured {
		t.Fatalf("nothing is registered yet: %+v", got)
	}
	if w := gitOAuthCall(api, http.MethodPut, "sub", "github", "admin@sub.co.jp",
		`{"client_id":"Iv1.app"}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if got := ask(); !got[gitOAuthGitHub].Configured || got[gitOAuthBitbucket].Configured {
		t.Fatalf("only github is registered: %+v", got)
	}
}

// ★ AUTH=dev is the single fixed unauthenticated user, and SUPER_ADMIN_EMAILS matches on
// an address it does not have — so a native / WSL deployment had NO administrator and no
// way into the tenant settings screen (docs/log/71 §71.6). That was survivable while every
// deployment setting lived in env; it is not, now that the OAuth apps are tenant rows.
func TestDevAuthUserIsSuperAdmin(t *testing.T) {
	m := &manager{authMode: "dev", superAdmins: map[string]bool{}}
	if got := m.roleHintFor(""); got != "super_admin" {
		t.Fatalf("dev role hint = %q", got)
	}
	// Every other mode is unchanged: only the listed addresses are upgraded.
	m2 := &manager{authMode: "oauth", superAdmins: emailSet("boss@acme.co.jp")}
	if got := m2.roleHintFor("member@acme.co.jp"); got != "" {
		t.Fatalf("a plain member must get no role hint, got %q", got)
	}
	if got := m2.roleHintFor("boss@acme.co.jp"); got != "super_admin" {
		t.Fatalf("a listed operator must be upgraded, got %q", got)
	}
}
