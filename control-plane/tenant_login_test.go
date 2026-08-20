package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// docs/61 §61.9 / §61.10 (P3). What these pin down is the part of per-tenant login
// that is easy to get subtly wrong: the entry gate is a union taken WITHIN the
// email axis (so a membership never buys a way past the GitHub org check), the
// per-tenant provider rule is enforced at tenant resolution rather than by hiding
// buttons, an existing membership — including a deactivated one — outranks
// auto-join, and the super_admin role is revoked at startup rather than at login.

func p3Store(t *testing.T) *sqlStore {
	t.Helper()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func p3Manager(t *testing.T, st *sqlStore) *manager {
	t.Helper()
	return &manager{
		store: st, emailHeader: "X-Forwarded-Email", authMode: "oauth",
		provisionMode: "invite", tenantLogin: newTenantLoginCache(st),
		rts: map[string]cachedRT{},
	}
}

// --- the entry gate ---------------------------------------------------------

// 決定 16: being on a roster is itself permission to reach the login, so an
// invite-run deployment does not have to keep AF_OAUTH_ALLOWED_* as well. This is
// the connection docs/61 §61.9.6 calls "the one that is missing today".
func TestEntryGateAdmitsAnInvitedPersonWithNoDeploymentAllowlist(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	cfg := config{mgr: mgr, allowEmails: emailSet(""), allowDomains: domainSet("")}

	// No allowlist anywhere, so the provider's only remaining term is the database.
	p := &oidcProvider{id: "entra", deployAllowed: cfg.emailAllowed, dbAllowed: cfg.tenantEmailAllowed}

	if ok, _ := p.Allowed(ctx, principal{Email: "yamada@acme.co.jp"}); ok {
		t.Fatal("nobody is listed and nobody is a member — the deployment must stay closed")
	}

	tn, err := st.CreateTenant(ctx, "sales", "営業部")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	ident, err := st.UpsertIdentity(ctx, "yamada@acme.co.jp", sanitizeUser("yamada@acme.co.jp"), "")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	mgr.tenantLogin.invalidate()

	if ok, _ := p.Allowed(ctx, principal{Email: "yamada@acme.co.jp"}); !ok {
		t.Fatal("an invited person must reach the login without also being in the env allowlist")
	}
	// Case-insensitively, too: the IdP asserts whatever casing it likes.
	if ok, _ := p.Allowed(ctx, principal{Email: "Yamada@Acme.co.jp"}); !ok {
		t.Fatal("the membership term must be case-insensitive")
	}
	if ok, _ := p.Allowed(ctx, principal{Email: "stranger@acme.co.jp"}); ok {
		t.Fatal("a colleague who was never invited must still be refused")
	}
}

// auto_join_domains is the other database-derived term: a small deployment can run
// with no invitations and no env allowlist at all (§61.9.5).
func TestEntryGateAdmitsAnAutoJoinDomain(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	cfg := config{mgr: mgr, allowEmails: emailSet(""), allowDomains: domainSet("")}
	p := &oidcProvider{id: "entra", deployAllowed: cfg.emailAllowed, dbAllowed: cfg.tenantEmailAllowed}

	tn, _ := st.CreateTenant(ctx, "acme", "Acme")
	if err := st.SetTenantLogin(ctx, tn.ID, "", "acme.co.jp", "", ""); err != nil {
		t.Fatalf("set login rules: %v", err)
	}
	mgr.tenantLogin.invalidate()

	if ok, _ := p.Allowed(ctx, principal{Email: "anyone@acme.co.jp"}); !ok {
		t.Fatal("an auto-join domain must open the entry gate")
	}
	if ok, _ := p.Allowed(ctx, principal{Email: "anyone@other.example"}); ok {
		t.Fatal("a different domain must not")
	}
}

// ★ The regression this design is most exposed to. The union is taken strictly
// inside the email axis: a provider-specific list still REPLACES the
// deployment-wide one, so narrowing one IdP on purpose is not silently widened
// back to the deployment list by the P3 change.
func TestProviderOwnAllowlistStillReplacesTheDeploymentWideOne(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	cfg := config{mgr: mgr, allowEmails: emailSet(""), allowDomains: domainSet("acme.co.jp")}

	narrowed := &oidcProvider{
		id: "entra_sub", allowDomains: domainSet("sub.acme.co.jp"),
		deployAllowed: cfg.emailAllowed, dbAllowed: cfg.tenantEmailAllowed,
	}
	if ok, _ := narrowed.Allowed(ctx, principal{Email: "someone@acme.co.jp"}); ok {
		t.Fatal("a provider narrowed to sub.acme.co.jp must not inherit the deployment-wide acme.co.jp")
	}
	if ok, _ := narrowed.Allowed(ctx, principal{Email: "someone@sub.acme.co.jp"}); !ok {
		t.Fatal("its own domain must pass")
	}

	// ...and the database term is still OR'd on top of the narrowed list, so an
	// invited contractor from another domain is not locked out (§61.9.5).
	tn, _ := st.CreateTenant(ctx, "sub", "子会社")
	ident, _ := st.UpsertIdentity(ctx, "contractor@partner.example", "contractor-partner-example", "")
	if _, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	mgr.tenantLogin.invalidate()
	if ok, _ := narrowed.Allowed(ctx, principal{Email: "contractor@partner.example"}); !ok {
		t.Fatal("an invited person must pass even when the provider carries its own narrower list")
	}
}

// ★ 決定 2 must survive the union: the GitHub org check is a DIFFERENT axis, so
// holding a membership satisfies the email gate and nothing else. If this ever
// fails, anyone invited to any tenant can sign in through GitHub from outside the
// company's orgs.
func TestMembershipDoesNotBypassTheGitHubOrgGate(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	cfg := config{mgr: mgr, allowEmails: emailSet(""), allowDomains: domainSet("")}

	tn, _ := st.CreateTenant(ctx, "sales", "営業部")
	ident, _ := st.UpsertIdentity(ctx, "outsider@acme.co.jp", "outsider-acme-co-jp", "")
	if _, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member"); err != nil {
		t.Fatalf("membership: %v", err)
	}

	gh := &githubProvider{
		id: githubProviderID, allowedOrgs: []string{"acme"},
		allowDomains: domainSet("acme.co.jp"),
		dbAllowed:    cfg.tenantEmailAllowed,
		ttl:          githubDefaultTTL, grace: githubDefaultGrace,
		cache: map[string]*githubMembership{},
	}
	// The email gate passes (twice over: domain and membership) — but with nothing
	// cached about this subject there is no org answer, so the honest result is
	// "sign in again", never "allowed".
	ok, err := gh.Allowed(ctx, principal{Provider: githubProviderID, Subject: "42", Email: "outsider@acme.co.jp"})
	if ok || err != errNeedsReauth {
		t.Fatalf("Allowed = (%v, %v), want (false, errNeedsReauth) — a membership must not answer the org question", ok, err)
	}
	// And an org answer of "no" stays "no" for someone who holds a membership.
	gh.remember("42", "tok", false)
	if ok, err := gh.Allowed(ctx, principal{Provider: githubProviderID, Subject: "42", Email: "outsider@acme.co.jp"}); ok || err != nil {
		t.Fatalf("Allowed = (%v, %v), want (false, nil) for a non-member of the org", ok, err)
	}
}

// --- the tenant gate --------------------------------------------------------

// 決定 14: the login page's button filter is cosmetic. What actually keeps someone
// out of an "Entra only" tenant is this check at resolution time — otherwise the
// generic /login plus a swapped X-AF-Tenant is all it takes.
func TestAllowedProvidersIsEnforcedAtTenantResolution(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)

	tn, _ := st.CreateTenant(ctx, "sales", "営業部")
	if err := st.SetTenantLogin(ctx, tn.ID, "entra", "", "", ""); err != nil {
		t.Fatalf("set login rules: %v", err)
	}
	mv := MembershipView{MembershipID: "m1", TenantID: tn.ID, TenantSlug: "sales"}

	entra := withLoginRef(ctx, loginRef{provider: "entra", subject: "s1"})
	if aerr := mgr.checkTenantProvider(entra, mv); aerr != nil {
		t.Fatalf("the accepted provider was refused: %v", aerr)
	}
	github := withLoginRef(ctx, loginRef{provider: githubProviderID, subject: "42"})
	aerr := mgr.checkTenantProvider(github, mv)
	if aerr == nil || aerr.code != "provider_required" {
		t.Fatalf("aerr = %+v, want provider_required", aerr)
	}
	if !strings.Contains(aerr.message, "entra") {
		t.Fatalf("the message must name a provider the tenant accepts: %q", aerr.message)
	}
	// A tenant with no rule accepts whatever the deployment enabled (unchanged
	// behaviour for every existing single-IdP deployment).
	open, _ := st.CreateTenant(ctx, "open", "Open")
	if aerr := mgr.checkTenantProvider(github, MembershipView{TenantID: open.ID, TenantSlug: "open"}); aerr != nil {
		t.Fatalf("an unrestricted tenant must accept any provider: %v", aerr)
	}
	// AUTH=proxy / AUTH=dev name no provider at all; requiring one would lock those
	// deployments out of every restricted tenant.
	if aerr := mgr.checkTenantProvider(context.Background(), mv); aerr != nil {
		t.Fatalf("a request with no IdP behind it must not be refused: %v", aerr)
	}
}

// --- attribution order (§61.9.8) --------------------------------------------

func TestAutoJoinCreatesAMembershipButNeverOutranksAnExistingOne(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st) // AF_PROVISION=invite

	sales, _ := st.CreateTenant(ctx, "sales", "営業部")
	if err := st.SetTenantLogin(ctx, sales.ID, "", "acme.co.jp", "", ""); err != nil {
		t.Fatalf("set login rules: %v", err)
	}

	// ② no membership, domain matches → joined.
	ident, _ := st.UpsertIdentity(ctx, "yamada@acme.co.jp", "yamada-acme-co-jp", "")
	ms, aerr := mgr.membershipsFor(ctx, ident)
	if aerr != nil || len(ms) != 1 || ms[0].TenantSlug != "sales" {
		t.Fatalf("auto-join: ms=%+v aerr=%v", ms, aerr)
	}

	// ① an existing membership wins: give the same person a second tenant and check
	// that a later resolution does not re-run the auto-join logic at all.
	dev, _ := st.CreateTenant(ctx, "dev", "開発部")
	if _, err := st.EnsureMembership(ctx, ident.ID, dev.ID, "member"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	ms, aerr = mgr.membershipsFor(ctx, ident)
	if aerr != nil || len(ms) != 2 {
		t.Fatalf("existing memberships must be returned as they are: ms=%+v aerr=%v", ms, aerr)
	}

	// ★ And a DEACTIVATED membership is an answer too: auto-join must not undo an
	// offboarding the next time the person opens the page.
	other, _ := st.UpsertIdentity(ctx, "tanaka@acme.co.jp", "tanaka-acme-co-jp", "")
	mem, _ := st.EnsureMembership(ctx, other.ID, sales.ID, "member")
	if err := st.SetMembershipStatus(ctx, mem.ID, "inactive"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	mgr.tenantLogin.invalidate()
	ms, aerr = mgr.membershipsFor(ctx, other)
	if aerr == nil || aerr.code != "not_provisioned" {
		t.Fatalf("a removed person must stay removed: ms=%+v aerr=%+v", ms, aerr)
	}
	// ...and the row is still inactive rather than quietly revived.
	if got, _, _ := st.GetMembership(ctx, other.ID, sales.ID); got.Status != "inactive" {
		t.Fatalf("membership status = %q, want inactive", got.Status)
	}
}

// Two tenants claiming one domain is a configuration error. The admin API refuses
// to create it; if one exists anyway the outcome must at least be deterministic.
func TestAutoJoinConflictResolvesToTheLowestSlug(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)

	b, _ := st.CreateTenant(ctx, "bravo", "B")
	a, _ := st.CreateTenant(ctx, "alpha", "A")
	for _, id := range []string{b.ID, a.ID} {
		if err := st.SetTenantLogin(ctx, id, "", "acme.co.jp", "", ""); err != nil {
			t.Fatalf("set login rules: %v", err)
		}
	}
	ident, _ := st.UpsertIdentity(ctx, "x@acme.co.jp", "x-acme-co-jp", "")
	ms, aerr := mgr.membershipsFor(ctx, ident)
	if aerr != nil || len(ms) != 1 || ms[0].TenantSlug != "alpha" {
		t.Fatalf("ms=%+v aerr=%v, want the lowest slug (alpha)", ms, aerr)
	}
	rows, err := st.ListAuditByTenant(ctx, a.ID, 10)
	if err != nil || len(rows) == 0 || rows[0].Action != "tenant.auto_join_conflict" {
		t.Fatalf("the ambiguous join must be recorded: %+v %v", rows, err)
	}
}

// --- offboarding (§61.10.6) -------------------------------------------------

func TestRemoveMembershipLocksOutButKeepsTheWorkspace(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)

	tn, _ := st.CreateTenant(ctx, "sales", "営業部")
	admin, _ := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin")
	victim, _ := st.UpsertIdentity(ctx, "leaver@acme.co.jp", "leaver-acme-co-jp", "")
	if _, err := st.EnsureMembership(ctx, victim.ID, tn.ID, "member"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	if _, err := st.EnsureMembership(ctx, admin.ID, tn.ID, "tenant_admin"); err != nil {
		t.Fatalf("membership: %v", err)
	}

	adm := newAdminAPI(mgr)
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodDelete, "/api/admin/memberships", strings.NewReader(body))
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		w := httptest.NewRecorder()
		adm.removeMembership(w, r)
		return w
	}

	if w := call(`{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`); w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if got, ok, _ := st.GetMembership(ctx, victim.ID, tn.ID); !ok || got.Status != "inactive" {
		t.Fatalf("membership = %+v, want a surviving inactive row (the home must not be destroyed)", got)
	}
	if ms, _ := st.ListMemberships(ctx, victim.ID); len(ms) != 0 {
		t.Fatalf("a removed person must resolve to no tenant: %+v", ms)
	}
	// The entry gate follows immediately — that is the whole point, since the
	// session cookie itself stays valid for up to AF_SESSION_TTL.
	if ok, err := st.EmailHasActiveMembership(ctx, "leaver@acme.co.jp"); ok || err != nil {
		t.Fatalf("EmailHasActiveMembership = (%v,%v), want false", ok, err)
	}

	// Re-inviting puts them back (EnsureMembership alone would not).
	r := httptest.NewRequest(http.MethodPost, "/api/admin/memberships",
		strings.NewReader(`{"tenant_slug":"sales","email":"leaver@acme.co.jp"}`))
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	w := httptest.NewRecorder()
	adm.addMembership(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("re-invite: %d %s", w.Code, w.Body.String())
	}
	if got, _, _ := st.GetMembership(ctx, victim.ID, tn.ID); got.Status != "active" {
		t.Fatalf("re-invite must reactivate, got %q", got.Status)
	}

	// Removing yourself is refused: there is no undo from inside the product.
	if w := call(`{"tenant_slug":"sales","user_key":"boss-acme-co-jp"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("self-removal: %d %s", w.Code, w.Body.String())
	}
}

// ★ A removed tenant_admin must stop being an admin. Their session cookie stays
// valid for up to AF_SESSION_TTL and they may still clear the entry gate (a
// deployment-wide allowed domain, another tenant's membership), so if the admin
// gate did not check the membership status they could simply put themselves back
// on the roster — docs/61 §61.10.7 の穴 2.
func TestRemovedTenantAdminLosesAdminRights(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)

	tn, _ := st.CreateTenant(ctx, "sales", "営業部")
	admin, _ := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "")
	mem, _ := st.EnsureMembership(ctx, admin.ID, tn.ID, "tenant_admin")

	if !mgr.tenantAdminFor(ctx, admin, tn.ID) {
		t.Fatal("an active tenant_admin must administer their tenant")
	}
	if err := st.SetMembershipStatus(ctx, mem.ID, "inactive"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if mgr.tenantAdminFor(ctx, admin, tn.ID) {
		t.Fatal("a removed tenant_admin must lose admin rights immediately")
	}
	// ...and the roster still surfaces them, so the operator can finish the job
	// (stop the workspace, wipe the home) after access is already revoked.
	if active, err := st.ListMembersByTenant(ctx, tn.ID); err != nil || len(active) != 0 {
		t.Fatalf("active members = %+v %v, want none", active, err)
	}
	gone, err := st.ListRemovedMembersByTenant(ctx, tn.ID)
	if err != nil || len(gone) != 1 || gone[0].UserKey != "boss-acme-co-jp" {
		t.Fatalf("removed members = %+v %v", gone, err)
	}
}

// allowed_domains bounds the INVITE and nothing else (§61.9.5).
func TestAllowedDomainsGuardsInvitesOnly(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sales", "営業部")
	if err := st.SetTenantLogin(ctx, tn.ID, "", "", "acme.co.jp", ""); err != nil {
		t.Fatalf("set login rules: %v", err)
	}
	if _, err := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	adm := newAdminAPI(mgr)
	invite := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/admin/memberships", strings.NewReader(body))
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		w := httptest.NewRecorder()
		adm.addMembership(w, r)
		return w
	}
	if w := invite(`{"tenant_slug":"sales","email":"ok@acme.co.jp"}`); w.Code != http.StatusOK {
		t.Fatalf("in-domain invite: %d %s", w.Code, w.Body.String())
	}
	if w := invite(`{"tenant_slug":"sales","email":"nope@other.example"}`); w.Code != http.StatusForbidden {
		t.Fatalf("out-of-domain invite: %d %s", w.Code, w.Body.String())
	}
	// A guarded tenant cannot be invited into by bare user_key: there would be no
	// address to check.
	if w := invite(`{"tenant_slug":"sales","user_key":"someone-unknown"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("keyless invite into a guarded tenant: %d %s", w.Code, w.Body.String())
	}
	// ...but somebody already invited keeps working regardless of their domain:
	// the guard is not a per-request constraint.
	other, _ := st.UpsertIdentity(ctx, "contractor@partner.example", "contractor-partner-example", "")
	if _, err := st.EnsureMembership(ctx, other.ID, tn.ID, "member"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	if ms, aerr := mgr.membershipsFor(ctx, other); aerr != nil || len(ms) != 1 {
		t.Fatalf("an out-of-domain member must keep working: ms=%+v aerr=%v", ms, aerr)
	}
}

// --- bootstrap (§61.10.2 / 決定 23) -----------------------------------------

func TestSuperAdminWithNoMembershipStillGetsTheTenantList(t *testing.T) {
	st := p3Store(t)
	mgr := p3Manager(t, st) // invite mode: the documented "can't bootstrap" trap
	mgr.superAdmins = emailSet("boss@acme.co.jp")

	tn := newTenantAPI(mgr)
	get := func(email string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/tenants", nil)
		r.Header.Set("X-Forwarded-Email", email)
		w := httptest.NewRecorder()
		tn.withIdentity(tn.list)(w, r)
		return w
	}
	w := get("boss@acme.co.jp")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"super_admin":true`) {
		t.Fatalf("super_admin bootstrap: %d %s", w.Code, w.Body.String())
	}
	// Everyone else still gets the honest refusal.
	if w := get("nobody@acme.co.jp"); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin without a membership: %d %s", w.Code, w.Body.String())
	}
}

// --- super_admin revocation (決定 24) ---------------------------------------

func TestDemoteSuperAdminsAtStartup(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	if _, err := st.UpsertIdentity(ctx, "old@acme.co.jp", "old-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := st.UpsertIdentity(ctx, "new@acme.co.jp", "new-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// An identity with no address at all: it cannot be named in SUPER_ADMIN_EMAILS,
	// so demoting it would be unrecoverable by the documented procedure.
	if _, err := st.UpsertIdentity(ctx, "", "dev", "super_admin"); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	demoted, err := st.DemoteSuperAdmins(ctx, []string{"New@Acme.co.jp"})
	if err != nil {
		t.Fatalf("demote: %v", err)
	}
	if len(demoted) != 1 || demoted[0] != "old@acme.co.jp" {
		t.Fatalf("demoted = %v, want just the address that left the env", demoted)
	}
	if got, _, _ := st.GetIdentityByUserKey(ctx, "old-acme-co-jp"); got.Role != "user" {
		t.Fatalf("old role = %q, want user", got.Role)
	}
	if got, _, _ := st.GetIdentityByUserKey(ctx, "new-acme-co-jp"); got.Role != "super_admin" {
		t.Fatalf("new role = %q, want super_admin (matching is case-insensitive)", got.Role)
	}
	if got, _, _ := st.GetIdentityByUserKey(ctx, "dev"); got.Role != "super_admin" {
		t.Fatalf("email-less role = %q, want it left alone", got.Role)
	}
	// Idempotent: a second boot changes nothing.
	if again, err := st.DemoteSuperAdmins(ctx, []string{"new@acme.co.jp"}); err != nil || len(again) != 0 {
		t.Fatalf("second pass = %v %v, want no further changes", again, err)
	}
}

// --- login page (§61.9.3) ---------------------------------------------------

func TestPerTenantLoginPage(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sales", "営業部")
	if err := st.SetTenantLogin(ctx, tn.ID, "entra", "", "", ""); err != nil {
		t.Fatalf("set login rules: %v", err)
	}

	cfg := config{
		publicBaseURL: "https://af.example.com",
		cookieSecret:  []byte("0123456789abcdef0123456789abcdef"),
		mgr:           mgr,
	}
	cfg.setProviders([]loginProvider{
		&oidcProvider{id: "entra", labelJA: "Microsoft でサインイン"},
		&githubProvider{id: githubProviderID, labelJA: "GitHub でサインイン"},
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", cfg.handleLogin)
	mux.HandleFunc("GET /login/{slug}", cfg.handleLogin)

	body := func(path string) string {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, w.Code)
		}
		return w.Body.String()
	}

	// The generic page is unchanged: every enabled provider.
	all := body("/login")
	if !strings.Contains(all, "provider=entra") || !strings.Contains(all, "provider=github") {
		t.Fatalf("/login must still show every provider:\n%s", all)
	}
	// The tenant page shows only what the tenant accepts, carries the tenant, and
	// names the department.
	sales := body("/login/sales")
	if !strings.Contains(sales, "provider=entra") || strings.Contains(sales, "provider=github") {
		t.Fatalf("/login/sales must show only entra:\n%s", sales)
	}
	if !strings.Contains(sales, "tenant=sales") {
		t.Fatalf("/login/sales must carry the tenant into the authorize link:\n%s", sales)
	}
	if !strings.Contains(sales, "営業部") {
		t.Fatalf("/login/sales must name the tenant:\n%s", sales)
	}
	// ★ An unknown slug is the GENERIC page, not a 404 — otherwise the status code
	// tells an unauthenticated visitor which department slugs exist.
	unknown := body("/login/no-such-department")
	if !strings.Contains(unknown, "provider=entra") || !strings.Contains(unknown, "provider=github") {
		t.Fatalf("an unknown slug must render the generic page:\n%s", unknown)
	}
	if strings.Contains(unknown, "tenant=no-such-department") {
		t.Fatalf("an unknown slug must not be carried forward:\n%s", unknown)
	}
}

// The tenant hint survives the round trip as a QUERY on the post-login
// destination — a hint for the Console's picker, never an authorization input.
func TestWithTenantHint(t *testing.T) {
	for _, tc := range []struct{ next, tenant, want string }{
		{"/", "sales", "/?tenant=sales"},
		{"/", "", "/"},
		{"/s/foo?x=1", "sales", "/s/foo?tenant=sales&x=1"},
		{"/s/foo?tenant=dev", "sales", "/s/foo?tenant=dev"}, // a deep link wins
	} {
		if got := withTenantHint(tc.next, tc.tenant); got != tc.want {
			t.Fatalf("withTenantHint(%q,%q) = %q, want %q", tc.next, tc.tenant, got, tc.want)
		}
	}
}

// --- the admin write path ---------------------------------------------------

func TestSetTenantLoginRejectsDuplicateAutoJoinDomains(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	mgr.knownProviderIDs = map[string]bool{"entra": true}
	if _, err := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	a, _ := st.CreateTenant(ctx, "alpha", "A")
	if err := st.SetTenantLogin(ctx, a.ID, "", "acme.co.jp", "", ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := st.CreateTenant(ctx, "bravo", "B"); err != nil {
		t.Fatalf("create: %v", err)
	}

	adm := newAdminAPI(mgr)
	put := func(slug, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/admin/tenants/"+slug+"/login", strings.NewReader(body))
		r.SetPathValue("slug", slug)
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		w := httptest.NewRecorder()
		adm.withSuperAdmin(adm.setTenantLogin)(w, r)
		return w
	}
	if w := put("bravo", `{"auto_join_domains":"@acme.co.jp"}`); w.Code != http.StatusConflict {
		t.Fatalf("duplicate auto-join domain: %d %s", w.Code, w.Body.String())
	}
	// A provider the deployment does not have would produce a page with no buttons.
	if w := put("bravo", `{"allowed_providers":"okta"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown provider: %d %s", w.Code, w.Body.String())
	}
	// The happy path normalizes as it stores (lowercase, no leading @).
	if w := put("bravo", `{"allowed_providers":"Entra","auto_join_domains":"@B.example, b.example","allowed_domains":"@B.example"}`); w.Code != http.StatusOK {
		t.Fatalf("valid rules: %d %s", w.Code, w.Body.String())
	}
	got, ok, err := st.GetTenantBySlug(ctx, "bravo")
	if err != nil || !ok {
		t.Fatalf("reload: %v ok=%v", err, ok)
	}
	if got.AllowedProviders != "entra" || got.AutoJoinDomains != "b.example" || got.AllowedDomains != "b.example" {
		t.Fatalf("stored rules = %+v, want normalized and deduped", got)
	}
}

// GET /api/admin/providers answers the question the free-text allowed_providers
// field asks and never answered: which ids may be written there. The test pins the
// two properties the endpoint exists for — it names every enabled provider with a
// label, and it leaks no credential — plus the gate.
//
// ★ P7 (docs/61 §61.17.9 ①) widened the gate: the deployment's methods ARE the
// default tenant's methods, so every tenant's sign-in method panel lists them and
// its administrator has to be able to read them. What did NOT widen is the ISSUER,
// which names the operator's own directory and is absent from /login — so the
// column is dropped for a tenant_admin. Editing the rule this list feeds is still
// super_admin-only (決定 19), and that gate lives on the PUT, not here.
func TestAdminProvidersListsEnabledProvidersWithoutSecrets(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	if _, err := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("super admin: %v", err)
	}
	tn, _ := st.CreateTenant(ctx, "sub", "子会社")
	lead, _ := st.UpsertIdentity(ctx, "lead@sub.co.jp", "lead-sub-co-jp", "")
	if _, err := st.EnsureMembership(ctx, lead.ID, tn.ID, "tenant_admin"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	// A plain member of the same tenant: the widened gate must not become "anyone
	// who is signed in".
	staff, _ := st.UpsertIdentity(ctx, "staff@sub.co.jp", "staff-sub-co-jp", "")
	if _, err := st.EnsureMembership(ctx, staff.ID, tn.ID, "member"); err != nil {
		t.Fatalf("membership: %v", err)
	}

	api := newLoginProviderAPI(mgr, []loginProvider{
		&oidcProvider{
			id: "entra", labelJA: "Microsoft でサインイン", labelEN: "Sign in with Microsoft",
			issuer:   "https://login.microsoftonline.com/11111111-2222-3333-4444-555555555555/v2.0",
			clientID: "cid-entra", clientSecret: "sekrit-entra",
		},
		// No labels declared: the endpoint must still hand the Console something
		// printable, or every caller re-invents defaultProviderLabel.
		&oidcProvider{id: "okta", issuer: "https://acme.okta.com", clientID: "cid-okta", clientSecret: "sekrit-okta"},
		&githubProvider{
			id: githubProviderID, labelJA: "GitHub でサインイン", labelEN: "Sign in with GitHub",
			clientID: "cid-gh", clientSecret: "sekrit-gh",
		},
	})

	get := func(email string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/admin/providers", nil)
		r.Header.Set("X-Forwarded-Email", email)
		w := httptest.NewRecorder()
		api.withAnyTenantAdmin(api.list)(w, r)
		return w
	}
	type provRow struct {
		ID      string `json:"id"`
		LabelJA string `json:"label_ja"`
		LabelEN string `json:"label_en"`
		Issuer  string `json:"issuer"`
	}
	decode := func(w *httptest.ResponseRecorder) []provRow {
		t.Helper()
		var got struct {
			Providers []provRow `json:"providers"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v (%s)", err, w.Body.String())
		}
		return got.Providers
	}

	// ★ A plain member has no use for this list, so the widened gate still refuses
	// them. "Not secret" is not a reason to open a gate.
	if w := get("staff@sub.co.jp"); w.Code != http.StatusForbidden {
		t.Fatalf("member: %d %s", w.Code, w.Body.String())
	}

	// ★ A tenant_admin READS it (their own panel lists these methods since P7) —
	// but without the issuer, which names the operator's directory.
	wLead := get("lead@sub.co.jp")
	if wLead.Code != http.StatusOK {
		t.Fatalf("tenant_admin: %d %s", wLead.Code, wLead.Body.String())
	}
	lp := decode(wLead)
	if len(lp) != 3 {
		t.Fatalf("tenant_admin providers = %+v, want all three", lp)
	}
	for _, p := range lp {
		if p.Issuer != "" {
			t.Fatalf("tenant_admin sees issuer %q on %s; it names the operator's directory", p.Issuer, p.ID)
		}
		if p.LabelJA == "" || p.LabelEN == "" {
			t.Fatalf("tenant_admin row = %+v, want id and both labels", p)
		}
	}
	// Omitted, not blanked: an empty issuer would render as an empty cell and read
	// like a misconfiguration, so the key must be absent from the JSON entirely.
	if strings.Contains(wLead.Body.String(), "issuer") {
		t.Fatalf("tenant_admin response carries an issuer key:\n%s", wLead.Body.String())
	}

	w := get("boss@acme.co.jp")
	if w.Code != http.StatusOK {
		t.Fatalf("super_admin: %d %s", w.Code, w.Body.String())
	}
	got := struct{ Providers []provRow }{Providers: decode(w)}
	if len(got.Providers) != 3 {
		t.Fatalf("providers = %+v, want all three, in the deployment's order", got.Providers)
	}
	if p := got.Providers[0]; p.ID != "entra" || p.LabelJA != "Microsoft でサインイン" || p.LabelEN != "Sign in with Microsoft" ||
		p.Issuer != "https://login.microsoftonline.com/11111111-2222-3333-4444-555555555555/v2.0" {
		t.Fatalf("entra = %+v, want id, both labels and the issuer", p)
	}
	if p := got.Providers[1]; p.LabelJA != "Okta でサインイン" || p.LabelEN != "Sign in with Okta" {
		t.Fatalf("okta = %+v, want the generated labels rather than empty strings", p)
	}
	if p := got.Providers[2]; p.ID != githubProviderID || p.Issuer != githubWebBase {
		t.Fatalf("github = %+v, want the fixed identity source", p)
	}
	// ★ The response is read by whoever can open the admin modal; a client_id is
	// not a secret but it is not on screen either, and client_secret must never
	// come back out of the process.
	for _, leak := range []string{"sekrit-entra", "sekrit-okta", "sekrit-gh", "cid-entra", "cid-okta", "cid-gh"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Fatalf("response leaks %q:\n%s", leak, w.Body.String())
		}
	}
}
