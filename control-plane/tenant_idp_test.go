package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// docs/61 §61.11 / ADR0043 決定 29-33 (P4). What these pin down is the part of
// tenant-defined sign-in methods that is easy to get subtly wrong — and each one is
// a takeover path if it regresses:
//
//   - the id namespace is separate, so a tenant cannot shadow an env provider
//   - a row only works once a super_admin approved it, at the CALLBACK and not
//     merely on the login page
//   - the entry gate never falls back to the deployment allowlist or roster
//   - the deployment role cannot be reached through a tenant's issuer
//   - an address that already belongs to somebody is refused, never joined
//   - such a session opens its own tenant and no other

func p4Manager(t *testing.T, st *sqlStore) *manager {
	t.Helper()
	m := p3Manager(t, st)
	m.tenantIdP = newTenantIdPRegistry(st, m.openTenantSecret)
	return m
}

// seedTenantIdP writes a row directly, for tests that are not about the API.
func seedTenantIdP(t *testing.T, st *sqlStore, tenantID, name, domains, status string) TenantIdP {
	t.Helper()
	row := TenantIdP{
		ID: newID(), TenantID: tenantID, Name: name,
		Issuer:   "https://login.microsoftonline.com/guid-" + name + "/v2.0",
		ClientID: "client-" + name, SecretEnc: "secret-" + name, Trust: trustIssuer,
		AllowedDomains: domains, Status: status, CreatedAt: nowTS(), UpdatedAt: nowTS(),
	}
	if err := st.CreateTenantIdP(context.Background(), row); err != nil {
		t.Fatalf("seed tenant_idp: %v", err)
	}
	return row
}

// --- the id namespace (決定 33) ----------------------------------------------

// ★ Without the split a tenant creates a row named "google" and the deployment's
// Google button is theirs. validProviderID must keep rejecting ":" — relaxing it
// would let an env provider be named INTO the tenant namespace instead.
func TestTenantProviderIDNamespaceIsSeparateFromEnv(t *testing.T) {
	id := tenantProviderID("sub", "entra")
	if id != "t:sub:entra" {
		t.Fatalf("id = %q", id)
	}
	slug, name, ok := parseTenantProviderID(id)
	if !ok || slug != "sub" || name != "entra" {
		t.Fatalf("parse = (%q,%q,%v)", slug, name, ok)
	}
	for _, env := range []string{"google", "entra", "github", ""} {
		if isTenantProviderID(env) {
			t.Fatalf("%q must not read as tenant-defined", env)
		}
	}
	for _, bad := range []string{"t:", "t::x", "t:sub:", "t:sub"} {
		if isTenantProviderID(bad) {
			t.Fatalf("%q must not parse as a tenant provider id", bad)
		}
	}
	if validProviderID("t:sub:entra") {
		t.Fatal("validProviderID must keep rejecting ':' — env ids may not enter the tenant namespace")
	}
}

// --- the entry gate (決定 32-3) -----------------------------------------------

// ★ A subsidiary's issuer admits the subsidiary's people. Not "everyone the
// deployment would have admitted", and not "anyone who holds a membership
// somewhere" — either fallback would turn one approved IdP into a deployment-wide
// door.
func TestTenantProviderGateDoesNotFallBackToTheDeployment(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)

	// A deployment that admits the parent company, and a colleague on some roster.
	cfg := config{mgr: mgr, allowDomains: domainSet("acme.co.jp"), allowEmails: emailSet("")}
	tn, _ := st.CreateTenant(ctx, "sub", "子会社")
	ident, _ := st.UpsertIdentity(ctx, "member@acme.co.jp", "member-acme-co-jp", "")
	if _, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	mgr.tenantLogin.invalidate()
	if ok, _ := (&oidcProvider{deployAllowed: cfg.emailAllowed, dbAllowed: cfg.tenantEmailAllowed}).
		Allowed(ctx, principal{Email: "member@acme.co.jp"}); !ok {
		t.Fatal("precondition: an env provider does admit this person")
	}

	row := seedTenantIdP(t, st, tn.ID, "entra", "sub.co.jp", "active")
	p, err := buildTenantProvider(row, "sub", "s3cret")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if ok, _ := p.Allowed(ctx, principal{Email: "hanako@sub.co.jp"}); !ok {
		t.Fatal("the subsidiary's own domain must be admitted")
	}
	if ok, _ := p.Allowed(ctx, principal{Email: "member@acme.co.jp"}); ok {
		t.Fatal("the deployment allowlist must NOT be a fallback for a tenant-defined provider")
	}
	if ok, _ := p.Allowed(ctx, principal{Email: "stranger@other.example"}); ok {
		t.Fatal("an unlisted domain must be refused")
	}
}

// A row that would admit everybody the issuer asserts is refused at build time, and
// the API refuses it earlier still (see TestTenantIdPSaveTimeValidation). This is
// docs/61 §61.14's first P4 question answered: allowed_domains is required.
func TestBuildTenantProviderRefusesDangerousRows(t *testing.T) {
	base := TenantIdP{
		Name: "entra", Issuer: "https://login.microsoftonline.com/guid/v2.0",
		ClientID: "c", Trust: trustIssuer, AllowedDomains: "sub.co.jp",
	}
	if _, err := buildTenantProvider(base, "sub", "s"); err != nil {
		t.Fatalf("the valid row must build: %v", err)
	}
	bad := map[string]func(*TenantIdP){
		"no domains":    func(r *TenantIdP) { r.AllowedDomains = "" },
		"no trust rule": func(r *TenantIdP) { r.Trust = "" },
		"api trust":     func(r *TenantIdP) { r.Trust = trustAPI },
		"http issuer":   func(r *TenantIdP) { r.Issuer = "http://idp.example/" },
		"multi-tenant issuer": func(r *TenantIdP) {
			r.Issuer = "https://login.microsoftonline.com/common/v2.0"
		},
		"bad name": func(r *TenantIdP) { r.Name = "Entra ID" },
	}
	for label, mutate := range bad {
		row := base
		mutate(&row)
		if _, err := buildTenantProvider(row, "sub", "s"); err == nil {
			t.Fatalf("%s: must be refused", label)
		}
	}
	// A multi-tenant issuer is allowed once the tenant ids are pinned (決定 7).
	row := base
	row.Issuer, row.AllowedTIDs = "https://login.microsoftonline.com/common/v2.0", "guid-a"
	if _, err := buildTenantProvider(row, "sub", "s"); err != nil {
		t.Fatalf("pinned tids must make the multi-tenant issuer acceptable: %v", err)
	}
}

// --- approval (決定 30) -------------------------------------------------------

// ★ The whole feature. A pending row must not resolve to a provider, because that
// is what the CALLBACK checks — hiding the button is presentation, and presentation
// is never the enforcement (決定 14).
func TestOnlyApprovedTenantProvidersResolve(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sub", "子会社")

	row := seedTenantIdP(t, st, tn.ID, "entra", "sub.co.jp", "pending")
	id := tenantProviderID("sub", "entra")
	if mgr.tenantIdP.providerFor(ctx, id) != nil {
		t.Fatal("a pending sign-in method must not resolve to a provider")
	}
	if err := st.SetTenantIdPStatus(ctx, tn.ID, row.ID, "active", "boss", nowTS(), nowTS()); err != nil {
		t.Fatalf("approve: %v", err)
	}
	mgr.tenantIdP.invalidate()
	if mgr.tenantIdP.providerFor(ctx, id) == nil {
		t.Fatal("an approved sign-in method must resolve without a restart")
	}
	if err := st.SetTenantIdPStatus(ctx, tn.ID, row.ID, "suspended", "", "", nowTS()); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	mgr.tenantIdP.invalidate()
	if mgr.tenantIdP.providerFor(ctx, id) != nil {
		t.Fatal("a suspended sign-in method must stop resolving")
	}

	// ★ And the sessions minted with it stop passing the entry gate, which is what
	// makes suspension an offboarding tool rather than a cosmetic switch.
	cfg := config{mgr: mgr}
	if ok, code := cfg.sessionAllowed(ctx, sessionClaims{Email: "h@sub.co.jp", Prov: id}); ok || code != "forbidden" {
		t.Fatalf("sessionAllowed = (%v,%q), want a refusal", ok, code)
	}
}

// The approval flow through the API: a tenant_admin writes the row and can stop it,
// but cannot start it. That single asymmetry is what keeps "tenant_admin" from being
// "super_admin" (決定 30).
func TestTenantAdminCannotApproveItsOwnIdP(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sub", "子会社")
	admin, _ := st.UpsertIdentity(ctx, "admin@sub.co.jp", "admin-sub-co-jp", "")
	if _, err := st.EnsureMembership(ctx, admin.ID, tn.ID, "tenant_admin"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	if _, err := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("super admin: %v", err)
	}
	api := newTenantIdPAPI(mgr)

	call := func(method, path, email, body string, vals map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		for k, v := range vals {
			r.SetPathValue(k, v)
		}
		r.Header.Set("X-Forwarded-Email", email)
		w := httptest.NewRecorder()
		switch {
		case strings.HasSuffix(path, "/status"):
			api.setStatus(w, r)
		case method == http.MethodGet:
			api.list(w, r)
		default:
			api.upsert(w, r)
		}
		return w
	}

	create := `{"name":"entra","issuer":"https://login.microsoftonline.com/guid-sub/v2.0",
	            "client_id":"c","client_secret":"s","trust":"issuer","allowed_domains":"sub.co.jp"}`
	w := call(http.MethodPost, "/api/admin/tenants/sub/idp", "admin@sub.co.jp", create, map[string]string{"slug": "sub"})
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created tenantIdPBody
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Status != "pending" {
		t.Fatalf("a new sign-in method must be born pending, got %q", created.Status)
	}
	if strings.Contains(w.Body.String(), `"s"`) || strings.Contains(w.Body.String(), "client_secret") {
		t.Fatalf("the client secret must never come back out:\n%s", w.Body.String())
	}

	path := "/api/admin/tenants/sub/idp/" + created.ID + "/status"
	ids := map[string]string{"slug": "sub", "id": created.ID}
	if w := call(http.MethodPost, path, "admin@sub.co.jp", `{"status":"active"}`, ids); w.Code != http.StatusForbidden {
		t.Fatalf("a tenant_admin must not be able to activate: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, path, "boss@acme.co.jp", `{"status":"active"}`, ids); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	row, _, _ := st.GetTenantIdP(ctx, tn.ID, created.ID)
	if row.Status != "active" || row.ApprovedBy == "" || row.ApprovedAt == "" {
		t.Fatalf("approval must be recorded on the row: %+v", row)
	}
	// Stopping is open to the tenant's own administrator.
	if w := call(http.MethodPost, path, "admin@sub.co.jp", `{"status":"suspended"}`, ids); w.Code != http.StatusOK {
		t.Fatalf("a tenant_admin must be able to suspend: %d %s", w.Code, w.Body.String())
	}

	// ★ Editing the issuer withdraws the approval: it was given to an issuer, not to
	// a row. The same holds for a WIDENED domain list, which lets the issuer assert
	// addresses the approver never saw.
	if err := st.SetTenantIdPStatus(ctx, tn.ID, created.ID, "active", "boss", nowTS(), nowTS()); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	edit := `{"name":"entra","issuer":"https://login.microsoftonline.com/guid-other/v2.0",
	          "client_id":"c","trust":"issuer","allowed_domains":"sub.co.jp"}`
	if w := call(http.MethodPut, "/api/admin/tenants/sub/idp/"+created.ID, "admin@sub.co.jp", edit, ids); w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	if row, _, _ := st.GetTenantIdP(ctx, tn.ID, created.ID); row.Status != "pending" {
		t.Fatalf("changing the issuer must send the row back to pending, got %q", row.Status)
	}

	if err := st.SetTenantIdPStatus(ctx, tn.ID, created.ID, "active", "boss", nowTS(), nowTS()); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	widen := `{"name":"entra","issuer":"https://login.microsoftonline.com/guid-other/v2.0",
	           "client_id":"c","trust":"issuer","allowed_domains":"sub.co.jp,acme.co.jp"}`
	if w := call(http.MethodPut, "/api/admin/tenants/sub/idp/"+created.ID, "admin@sub.co.jp", widen, ids); w.Code != http.StatusOK {
		t.Fatalf("widen: %d %s", w.Code, w.Body.String())
	}
	if row, _, _ := st.GetTenantIdP(ctx, tn.ID, created.ID); row.Status != "pending" {
		t.Fatalf("widening the domain list must send the row back to pending, got %q", row.Status)
	}
}

// Save-time validation is the DB-side half of the env path's startup checks: it has
// to be a 400, because a running CP cannot be brought down by a form (§61.11.5).
func TestTenantIdPSaveTimeValidation(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sub", "子会社")
	other, _ := st.CreateTenant(ctx, "sibling", "別会社")
	seedTenantIdP(t, st, other.ID, "entra", "sibling.co.jp", "active")
	if _, err := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("super admin: %v", err)
	}
	_ = tn
	api := newTenantIdPAPI(mgr)
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/admin/tenants/sub/idp", strings.NewReader(body))
		r.SetPathValue("slug", "sub")
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		w := httptest.NewRecorder()
		api.upsert(w, r)
		return w
	}
	const good = `"issuer":"https://login.microsoftonline.com/guid-sub/v2.0","client_id":"c","client_secret":"s","trust":"issuer"`

	if w := post(`{"name":"entra",` + good + `,"allowed_domains":""}`); w.Code != http.StatusBadRequest {
		t.Fatalf("an empty domain list must be refused (it would admit nobody): %d %s", w.Code, w.Body.String())
	}
	if w := post(`{"name":"entra","issuer":"https://login.microsoftonline.com/common/v2.0","client_id":"c","client_secret":"s","trust":"issuer","allowed_domains":"sub.co.jp"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("a multi-tenant issuer with no tids must be refused: %d %s", w.Code, w.Body.String())
	}
	if w := post(`{"name":"entra",` + good + `,"trust":"api","allowed_domains":"sub.co.jp"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("trust=api is the GitHub adapter's rule and must be refused: %d %s", w.Code, w.Body.String())
	}
	// ★ One domain, one tenant: otherwise a subsidiary's issuer could assert a
	// sibling's addresses, and identity is keyed by email deployment-wide.
	if w := post(`{"name":"entra",` + good + `,"allowed_domains":"sibling.co.jp"}`); w.Code != http.StatusConflict {
		t.Fatalf("a domain another tenant claims must be refused: %d %s", w.Code, w.Body.String())
	}
	if w := post(`{"name":"entra",` + good + `,"allowed_domains":"@SUB.co.jp"}`); w.Code != http.StatusOK {
		t.Fatalf("the valid row must be accepted: %d %s", w.Code, w.Body.String())
	}
	rows, _ := st.ListTenantIdPs(ctx, tn.ID)
	if len(rows) != 1 || rows[0].AllowedDomains != "sub.co.jp" {
		t.Fatalf("domains must be normalized on save: %+v", rows)
	}
}

// --- what a tenant-defined session may do (決定 31 / 32) ----------------------

// ★ 決定 31, and the reason the whole approval model exists: SUPER_ADMIN_EMAILS is
// matched on the address alone, so without this a subsidiary's administrator asserts
// the operator's address and is upgraded — permanently, since UpsertIdentity never
// downgrades.
func TestTenantProviderCannotReachTheDeploymentRole(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	mgr.superAdmins = map[string]bool{"boss@acme.co.jp": true}

	tenantCtx := withLoginRef(ctx, loginRef{provider: "t:sub:entra", subject: "attacker-1"})
	ident, err := mgr.upsertIdentity(tenantCtx, "boss@acme.co.jp", "boss-acme-co-jp", mgr.roleHintFor("boss@acme.co.jp"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if ident.Role == "super_admin" {
		t.Fatal("a tenant-defined provider must never hand out the deployment role")
	}
	// The operator's own login through an env provider still works as before.
	envCtx := withLoginRef(ctx, loginRef{provider: "entra", subject: "boss-1"})
	ident, err = mgr.upsertIdentity(envCtx, "boss@acme.co.jp", "boss-acme-co-jp", mgr.roleHintFor("boss@acme.co.jp"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if ident.Role != "super_admin" {
		t.Fatalf("an env provider must still apply SUPER_ADMIN_EMAILS, got %q", ident.Role)
	}
}

// ★ 決定 32: rule 2 (join by email) is off for a tenant-defined provider, and
// "off" has to mean REFUSED. Falling through to "create a new identity" would land
// on the same row anyway — user_key is derived from the same address.
func TestTenantProviderRefusesAnAddressThatBelongsToSomebody(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)

	// Somebody who signs in with the deployment's own IdP.
	const victim = "cto@acme.co.jp"
	seed, _, err := st.LinkIdentity(ctx, "entra", "real-1", victim, sanitizeUser(victim), "", true)
	if err != nil {
		t.Fatalf("victim login: %v", err)
	}
	// The subsidiary's issuer asserts that address.
	_, _, err = st.LinkIdentity(ctx, "t:sub:entra", "attacker-1", victim, sanitizeUser(victim), "", false)
	if !errors.Is(err, errIdentityClaimed) {
		t.Fatalf("err = %v, want errIdentityClaimed", err)
	}
	if n := countRows(t, st, "identity"); n != 1 {
		t.Fatalf("identity rows = %d — the refusal must not create a second account", n)
	}
	got, _, _ := st.GetIdentityByID(ctx, seed.ID)
	if got.UserKey != seed.UserKey {
		t.Fatal("the existing identity must be untouched")
	}

	// But the person the tenant INVITED — an identity nobody has signed in as — is
	// claimed, because that is how a subsidiary's first login is meant to work.
	const invited = "hanako@sub.co.jp"
	placeholder, err := st.UpsertIdentity(ctx, invited, sanitizeUser(invited), "")
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	claimed, isNew, err := st.LinkIdentity(ctx, "t:sub:entra", "hanako-1", invited, sanitizeUser(invited), "", false)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.ID != placeholder.ID {
		t.Fatal("an invited person must land on the identity the invite created")
	}
	if isNew {
		t.Fatal("claiming an invite is not a new account")
	}
	// A second login through the same pair is rule 1 and stays stable.
	again, _, err := st.LinkIdentity(ctx, "t:sub:entra", "hanako-1", invited, sanitizeUser(invited), "", false)
	if err != nil || again.ID != placeholder.ID {
		t.Fatalf("re-login: %+v %v", again, err)
	}
}

// ★ 決定 32-3: a tenant that names no allowed_providers accepts every provider, so
// without an explicit pin a subsidiary's own issuer would open every such tenant in
// the deployment.
func TestTenantProviderSessionIsPinnedToItsOwnTenant(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	own, _ := st.CreateTenant(ctx, "sub", "子会社")
	foreign, _ := st.CreateTenant(ctx, "sales", "営業部")

	sess := withLoginRef(ctx, loginRef{provider: "t:sub:entra", subject: "x"})
	if aerr := mgr.checkTenantProvider(sess, MembershipView{TenantID: own.ID, TenantSlug: "sub"}); aerr != nil {
		t.Fatalf("its own tenant must be reachable: %v", aerr)
	}
	aerr := mgr.checkTenantProvider(sess, MembershipView{TenantID: foreign.ID, TenantSlug: "sales"})
	if aerr == nil || aerr.code != "provider_required" {
		t.Fatalf("another tenant must be refused with provider_required, got %+v", aerr)
	}
}

// --- the login page (決定 32-4) -----------------------------------------------

// ★ The generic page must not list the group's subsidiaries: the full set of
// buttons would be a directory readable without signing in.
func TestTenantProviderAppearsOnlyOnItsOwnLoginPage(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sub", "子会社")
	seedTenantIdP(t, st, tn.ID, "entra", "sub.co.jp", "active")
	pending, _ := st.CreateTenant(ctx, "later", "後で")
	seedTenantIdP(t, st, pending.ID, "okta", "later.co.jp", "pending")

	cfg := config{
		publicBaseURL: "https://af.example.com",
		cookieSecret:  []byte("0123456789abcdef0123456789abcdef"),
		mgr:           mgr,
	}
	cfg.setProviders([]loginProvider{&oidcProvider{id: "google", labelJA: "Google でサインイン"}})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", cfg.handleLogin)
	mux.HandleFunc("GET /login/{slug}", cfg.handleLogin)
	body := func(path string) string {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w.Body.String()
	}

	if got := body("/login"); strings.Contains(got, "t%3Asub%3Aentra") || strings.Contains(got, "t:sub:entra") {
		t.Fatalf("the generic page must not reveal a subsidiary's sign-in method:\n%s", got)
	}
	sub := body("/login/sub")
	if !strings.Contains(sub, "t%3Asub%3Aentra") {
		t.Fatalf("/login/sub must show the tenant's own sign-in method:\n%s", sub)
	}
	if got := body("/login/later"); strings.Contains(got, "t%3Alater%3Aokta") {
		t.Fatalf("a pending sign-in method must not be offered:\n%s", got)
	}
	_ = ctx
}
