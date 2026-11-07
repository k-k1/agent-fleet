package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Issue #3450: PAT issuance and the admin gates apply the same tenant sign-in-method and
// network rules as normal request resolution. Each refusal is paired with the case that must
// still pass, because a gate that refuses everybody satisfies every refusal assertion.

const (
	officeNet = "203.0.113.0/24"
	inOffice  = "203.0.113.7"
	elsewhere = "198.51.100.9"
)

type accessFixture struct {
	st           *store.SQL
	mgr          *manager
	acme, beta   store.Tenant
	boss, member store.Identity
	root         store.Identity
	// tadmin signed in through beta's own IdP ("t:beta:corp") and is tenant_admin of both
	// tenants, so only the pin of a tenant-defined session to its tenant can refuse acme.
	tadmin store.Identity
}

// acme restricts sign-in to "entra" and to officeNet; beta has no rules. boss administers both,
// member belongs to acme only. root is a super_admin.
func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	mgr.authMode = "proxy"
	mgr.superAdmins = map[string]bool{"root@example.com": true}
	acme, _ := st.CreateTenant(ctx, "acme", "Acme")
	beta, _ := st.CreateTenant(ctx, "beta", "Beta")
	if err := st.SetTenantLogin(ctx, acme.ID, "entra", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTenantAllowedCIDRs(ctx, acme.ID, officeNet); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTenantLimits(ctx, acme.ID, `{"allow_engine_ingest":true}`); err != nil {
		t.Fatal(err)
	}
	mk := func(email, tid, role string) store.Identity {
		id, err := st.UpsertIdentity(ctx, email, sanitizeUser(email), mgr.roleHintFor(email))
		if err != nil {
			t.Fatal(err)
		}
		if tid != "" {
			if _, err := st.EnsureMembership(ctx, id.ID, tid, role); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	f := &accessFixture{st: st, mgr: mgr, acme: acme, beta: beta}
	f.boss = mk("boss@example.com", acme.ID, "tenant_admin")
	if _, err := st.EnsureMembership(ctx, f.boss.ID, beta.ID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}
	f.member = mk("member@example.com", acme.ID, "member")
	f.root = mk("root@example.com", "", "")
	if _, err := st.EnsureMembership(ctx, f.root.ID, beta.ID, "member"); err != nil {
		t.Fatal(err)
	}
	ti, _, err := st.LinkIdentity(ctx, store.IdentityLink{Provider: "t:beta:corp", Subject: "s-tadmin@beta.example",
		Email: "tadmin@beta.example", FallbackKey: sanitizeUser("tadmin@beta.example")})
	if err != nil {
		t.Fatal(err)
	}
	f.tadmin = ti
	for _, tn := range []store.Tenant{acme, beta} {
		if _, err := st.EnsureMembership(ctx, ti.ID, tn.ID, "tenant_admin"); err != nil {
			t.Fatal(err)
		}
	}
	mgr.tenantLogin.invalidate()
	return f
}

// req builds a request as email, arriving from ip with a session minted by provider.
func (f *accessFixture) req(method, target, body, email, tenant, provider, ip string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("X-Forwarded-Email", email)
	if tenant != "" {
		r.Header.Set("X-AF-Tenant", tenant)
	}
	ctx := r.Context()
	if provider != "" {
		ctx = withLoginRef(ctx, loginRef{provider: provider, subject: "s-" + email})
	}
	if ip != "" {
		ctx = context.WithValue(ctx, clientIPKey{}, clientIPInfo{IP: netip.MustParseAddr(ip), OK: true})
	}
	return r.WithContext(ctx)
}

func accessErrCode(w *httptest.ResponseRecorder) string {
	return accessJSONField(w.Body.String(), "code")
}

func accessJSONField(body, key string) string {
	i := strings.Index(body, `"`+key+`":"`)
	if i < 0 {
		return ""
	}
	rest := body[i+len(key)+4:]
	return rest[:strings.Index(rest, `"`)]
}

func TestPATIssuanceAppliesTheTenantRules(t *testing.T) {
	f := newAccessFixture(t)
	api := newPATAPI(f.mgr)
	mint := func(r *http.Request, who store.Identity) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		api.create(w, r, who)
		return w
	}
	body := `{"name":"t","tenant":"acme"}`

	// Still allowed: right sign-in method from the office.
	if w := mint(f.req("POST", "/api/pat", body, "member@example.com", "", "entra", inOffice), f.member); w.Code != 200 {
		t.Fatalf("allowed issuance: %d %s", w.Code, w.Body)
	}
	// Refused: network outside the tenant's allowed_cidrs.
	if w := mint(f.req("POST", "/api/pat", body, "member@example.com", "", "entra", elsewhere), f.member); w.Code != 403 || accessErrCode(w) != "ip_not_allowed" {
		t.Errorf("outside the network: %d %s", w.Code, w.Body)
	}
	// Refused: a sign-in method the tenant does not accept.
	if w := mint(f.req("POST", "/api/pat", body, "member@example.com", "", "google", inOffice), f.member); w.Code != 403 || accessErrCode(w) != "provider_required" {
		t.Errorf("wrong provider: %d %s", w.Code, w.Body)
	}
	// Refused: a session from beta's own IdP, for acme — the pin to the defining tenant is what
	// refuses it, and it holds from inside the office network too.
	if w := mint(f.req("POST", "/api/pat", `{"name":"t","tenant":"acme"}`, "tadmin@beta.example", "", "t:beta:corp", inOffice), f.tadmin); w.Code != 403 || accessErrCode(w) != "provider_required" {
		t.Errorf("tenant-defined session for another tenant: %d %s", w.Code, w.Body)
	}
	// Still allowed: that session for its own tenant.
	if w := mint(f.req("POST", "/api/pat", `{"name":"t","tenant":"beta"}`, "tadmin@beta.example", "", "t:beta:corp", elsewhere), f.tadmin); w.Code != 200 {
		t.Errorf("tenant-defined session for its own tenant: %d %s", w.Code, w.Body)
	}
	// A tenant without rules is unaffected from anywhere.
	if w := mint(f.req("POST", "/api/pat", `{"name":"t","tenant":"beta"}`, "boss@example.com", "", "entra", elsewhere), f.boss); w.Code != 200 {
		t.Errorf("rule-free tenant: %d %s", w.Code, w.Body)
	}
	// super_admin keeps the network exemption.
	if w := mint(f.req("POST", "/api/pat", `{"name":"t","tenant":"beta"}`, "root@example.com", "", "", elsewhere), f.root); w.Code == 403 {
		t.Errorf("super_admin refused: %d %s", w.Code, w.Body)
	}
}

func TestPATListAndRevokeStillWorkOutsideTheNetwork(t *testing.T) {
	f := newAccessFixture(t)
	api := newPATAPI(f.mgr)
	w := httptest.NewRecorder()
	api.create(w, f.req("POST", "/api/pat", `{"name":"t","tenant":"acme"}`, "member@example.com", "", "entra", inOffice), f.member)
	if w.Code != 200 {
		t.Fatalf("issue: %d %s", w.Code, w.Body)
	}
	id := accessJSONField(w.Body.String(), "id")

	out := f.req("GET", "/api/pat", "", "member@example.com", "", "google", elsewhere)
	lw := httptest.NewRecorder()
	api.list(lw, out, f.member)
	if lw.Code != 200 || !strings.Contains(lw.Body.String(), id) {
		t.Fatalf("list from outside: %d %s", lw.Code, lw.Body)
	}
	rr := f.req("DELETE", "/api/pat/"+id, "", "member@example.com", "", "google", elsewhere)
	rr.SetPathValue("id", id)
	rw := httptest.NewRecorder()
	api.revoke(rw, rr, f.member)
	if rw.Code != 200 {
		t.Fatalf("revoke from outside: %d %s", rw.Code, rw.Body)
	}
	pats, _ := f.st.ListPATsByIdentity(context.Background(), f.member.ID)
	if len(pats) != 1 || pats[0].RevokedAt == "" {
		t.Fatalf("token not revoked: %+v", pats)
	}
}

func TestPATLifetimeIsCapped(t *testing.T) {
	f := newAccessFixture(t)
	f.mgr.patMaxDays = 30
	api := newPATAPI(f.mgr)
	mint := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		api.create(w, f.req("POST", "/api/pat", body, "boss@example.com", "beta", "entra", inOffice), f.boss)
		return w
	}
	if w := mint(`{"name":"t","ttl_days":-1}`); w.Code != 400 || accessErrCode(w) != "invalid_ttl" {
		t.Errorf("never-expires must be refused: %d %s", w.Code, w.Body)
	}
	if w := mint(`{"name":"t","ttl_days":31}`); w.Code != 400 || accessErrCode(w) != "invalid_ttl" {
		t.Errorf("above the cap: %d %s", w.Code, w.Body)
	}
	for _, body := range []string{`{"name":"t","ttl_days":30}`, `{"name":"t"}`} {
		w := mint(body)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
		exp, err := time.Parse(time.RFC3339, accessJSONField(w.Body.String(), "expires_at"))
		if err != nil || time.Until(exp) > 31*24*time.Hour || time.Until(exp) < 29*24*time.Hour {
			t.Errorf("%s: expires_at %q (%v)", body, accessJSONField(w.Body.String(), "expires_at"), err)
		}
	}
}

func TestTenantAdminGateAppliesTheTenantRules(t *testing.T) {
	f := newAccessFixture(t)
	a := memberAuth{f.mgr}
	gate := func(r *http.Request, slug string) (int, string) {
		w := httptest.NewRecorder()
		if _, _, ok := a.tenantAdminFor(w, r, slug); ok {
			return 200, ""
		}
		return w.Code, accessErrCode(w)
	}
	// Positive control: an admin of acme with the right method from the office.
	if c, _ := gate(f.req("GET", "/x", "", "boss@example.com", "", "entra", inOffice), "acme"); c != 200 {
		t.Fatalf("allowed admin refused: %d", c)
	}
	if c, k := gate(f.req("GET", "/x", "", "boss@example.com", "", "entra", elsewhere), "acme"); c != 403 || k != "ip_not_allowed" {
		t.Errorf("admin outside the network: %d %s", c, k)
	}
	if c, k := gate(f.req("GET", "/x", "", "boss@example.com", "", "google", inOffice), "acme"); c != 403 || k != "provider_required" {
		t.Errorf("admin with the wrong method: %d %s", c, k)
	}
	// A tenant-defined IdP session administers its own tenant (beta) and no other.
	if c, _ := gate(f.req("GET", "/x", "", "tadmin@beta.example", "", "t:beta:corp", elsewhere), "beta"); c != 200 {
		t.Errorf("tenant-defined session on its own tenant: %d", c)
	}
	if c, k := gate(f.req("GET", "/x", "", "tadmin@beta.example", "", "t:beta:corp", inOffice), "acme"); c != 403 || k != "provider_required" {
		t.Errorf("tenant-defined session on another tenant: %d %s", c, k)
	}
	// A rule-free tenant stays reachable from anywhere; a plain member is still not an admin.
	if c, _ := gate(f.req("GET", "/x", "", "boss@example.com", "", "google", elsewhere), "beta"); c != 200 {
		t.Errorf("rule-free tenant: %d", c)
	}
	if c, _ := gate(f.req("GET", "/x", "", "member@example.com", "", "entra", inOffice), "acme"); c != 403 {
		t.Errorf("plain member: %d", c)
	}
	// super_admin is never locked out of a tenant's pages.
	if c, _ := gate(f.req("GET", "/x", "", "root@example.com", "", "google", elsewhere), "acme"); c != 200 {
		t.Errorf("super_admin: %d", c)
	}
}

func TestAnyTenantAdminGateNeedsOneAdminTenantThatAdmitsTheRequest(t *testing.T) {
	f := newAccessFixture(t)
	a := memberAuth{f.mgr}
	gate := func(r *http.Request) (int, string) {
		w := httptest.NewRecorder()
		if _, ok := a.anyTenantAdminFor(w, r); ok {
			return 200, ""
		}
		return w.Code, accessErrCode(w)
	}
	// boss administers acme (restricted) and beta (open): from elsewhere beta carries them.
	if c, _ := gate(f.req("GET", "/x", "", "boss@example.com", "", "google", elsewhere)); c != 200 {
		t.Errorf("admin of an open tenant: %d", c)
	}
	// Only admin of acme: refused from elsewhere, allowed from the office.
	if _, err := f.st.EnsureMembership(context.Background(), f.member.ID, f.acme.ID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetMembershipRole(context.Background(), accessMembership(t, f, f.member, f.acme).ID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}
	if c, k := gate(f.req("GET", "/x", "", "member@example.com", "", "entra", elsewhere)); c != 403 || k != "ip_not_allowed" {
		t.Errorf("acme-only admin outside the network: %d %s", c, k)
	}
	if c, _ := gate(f.req("GET", "/x", "", "member@example.com", "", "entra", inOffice)); c != 200 {
		t.Errorf("acme-only admin in the office: %d", c)
	}
	if c, _ := gate(f.req("GET", "/x", "", "root@example.com", "", "google", elsewhere)); c != 200 {
		t.Errorf("super_admin: %d", c)
	}
}

func accessMembership(t *testing.T, f *accessFixture, id store.Identity, tn store.Tenant) store.Membership {
	t.Helper()
	m, ok, err := f.st.GetMembership(context.Background(), id.ID, tn.ID)
	if err != nil || !ok {
		t.Fatalf("membership: %v %v", ok, err)
	}
	return m
}

func TestIngestGateIsBoundToTheSelectedTenant(t *testing.T) {
	f := newAccessFixture(t)
	reg, _ := newAdminTestRegistry(t, &engineTestECS{}, testSettingsStore(t))
	a := engineAdminAPI{memberAuth{f.mgr}, reg, f.st}
	gate := func(r *http.Request) (int, string) {
		w := httptest.NewRecorder()
		g, ok := a.ingestAdminFor(w, r)
		if ok {
			return 200, g.tenantID
		}
		return w.Code, accessErrCode(w)
	}
	// boss: tenant_admin of acme (granted, restricted) and beta (admin, no grant).
	if c, tid := gate(f.req("POST", "/i", "", "boss@example.com", "acme", "entra", inOffice)); c != 200 || tid != f.acme.ID {
		t.Fatalf("granted tenant from the office: %d %q", c, tid)
	}
	if c, k := gate(f.req("POST", "/i", "", "boss@example.com", "acme", "entra", elsewhere)); c != 403 || k != "ip_not_allowed" {
		t.Errorf("outside the network: %d %s", c, k)
	}
	if c, k := gate(f.req("POST", "/i", "", "boss@example.com", "acme", "google", inOffice)); c != 403 || k != "provider_required" {
		t.Errorf("wrong method: %d %s", c, k)
	}
	// Selecting beta must not borrow acme's grant.
	if c, _ := gate(f.req("POST", "/i", "", "boss@example.com", "beta", "entra", inOffice)); c != 403 {
		t.Errorf("grant borrowed from another tenant: %d", c)
	}
	// Two memberships and no selection is ambiguous, as everywhere else.
	if c, k := gate(f.req("POST", "/i", "", "boss@example.com", "", "entra", inOffice)); c != 409 || k != "tenant_selection_required" {
		t.Errorf("no selection: %d %s", c, k)
	}
	// The operator is unaffected and still carries no tenant.
	if c, tid := gate(f.req("POST", "/i", "", "root@example.com", "acme", "google", elsewhere)); c != 200 || tid != "" {
		t.Errorf("super_admin: %d %q", c, tid)
	}
}

// --- offboarding -------------------------------------------------------------------

func TestDeactivatingAMembershipRevokesItsPATsAndGrants(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	client := e.registerClient(loopbackCB)
	tk := e.connect(client, loopbackCB, "read")
	mid := e.membershipID()
	patTok, patHash := newPATToken()
	if err := e.mgr.store.CreatePAT(ctx, store.PAT{ID: "p1", IdentityID: e.ident.ID, MembershipID: mid, Scope: "read", Name: "t", CreatedAt: store.NowTS()}, patHash); err != nil {
		t.Fatal(err)
	}
	// Positive control: both work before the membership goes.
	for name, tok := range map[string]string{"oauth": tk.Access, "pat": patTok} {
		if rec := e.mcpCall(tok, "tools/list"); rec.Code != 200 {
			t.Fatalf("%s before: %d", name, rec.Code)
		}
	}
	if err := e.mgr.store.SetMembershipStatus(ctx, mid, "inactive"); err != nil {
		t.Fatal(err)
	}
	// Re-inviting must not bring the old credentials back.
	if err := e.mgr.store.SetMembershipStatus(ctx, mid, "active"); err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"oauth": tk.Access, "pat": patTok} {
		if rec := e.mcpCall(tok, "tools/list"); rec.Code != 401 {
			t.Errorf("%s after deactivate+reactivate: %d, want 401", name, rec.Code)
		}
	}
	pats, _ := e.mgr.store.ListPATsByIdentity(ctx, e.ident.ID)
	if len(pats) != 1 || pats[0].RevokedAt == "" {
		t.Errorf("PAT row not revoked: %+v", pats)
	}
}

func TestAllowlistRemovalRevokesBearers(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	client := e.registerClient(loopbackCB)
	tk := e.connect(client, loopbackCB, "read")
	patTok, patHash := newPATToken()
	if err := e.mgr.store.CreatePAT(ctx, store.PAT{ID: "p1", IdentityID: e.ident.ID, MembershipID: e.membershipID(), Scope: "read", Name: "t", CreatedAt: store.NowTS()}, patHash); err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.store.AttachProvider(ctx, e.ident.ID, store.IdentityLink{Provider: "entra", Subject: "s1", Email: "dev@example.com"}); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"dev@example.com": true}
	cfg := config{mgr: e.mgr, allowEmails: envx.EmailSet(""), allowDomains: envx.DomainSet("")}
	cfg.setProviders([]auth.LoginProvider{&auth.OIDCProvider{ProviderID: "entra",
		DeployAllowed: func(email string) bool { return allowed[email] }}})
	e.mgr.bearerEntryAllowed = cfg.identityStillAllowed

	for name, tok := range map[string]string{"oauth": tk.Access, "pat": patTok} {
		if rec := e.mcpCall(tok, "tools/list"); rec.Code != 200 {
			t.Fatalf("%s while allowed: %d", name, rec.Code)
		}
	}
	delete(allowed, "dev@example.com") // removed from the allowlist
	for name, tok := range map[string]string{"oauth": tk.Access, "pat": patTok} {
		if rec := e.mcpCall(tok, "tools/list"); rec.Code != 401 {
			t.Errorf("%s after removal: %d, want 401", name, rec.Code)
		}
	}
	// The removal is a revoke, not a pause: adding the address back does not revive them.
	allowed["dev@example.com"] = true
	for name, tok := range map[string]string{"oauth": tk.Access, "pat": patTok} {
		if rec := e.mcpCall(tok, "tools/list"); rec.Code != 401 {
			t.Errorf("%s after re-adding: %d, want 401", name, rec.Code)
		}
	}
}

func TestLegacyNonExpiringPATIsCappedFromCreation(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	mk := func(id string, age time.Duration) string {
		tok, h := newPATToken()
		created := time.Now().Add(-age).UTC().Format(time.RFC3339)
		if err := e.mgr.store.CreatePAT(ctx, store.PAT{ID: id, IdentityID: e.ident.ID, MembershipID: e.membershipID(), Scope: "read", Name: id, CreatedAt: created}, h); err != nil {
			t.Fatal(err)
		}
		return tok
	}
	young, old := mk("young", 24*time.Hour), mk("old", 400*24*time.Hour)
	if rec := e.mcpCall(young, "tools/list"); rec.Code != 200 {
		t.Errorf("recent non-expiring token: %d, want 200", rec.Code)
	}
	if rec := e.mcpCall(old, "tools/list"); rec.Code != 401 {
		t.Errorf("non-expiring token older than the cap: %d, want 401", rec.Code)
	}
}

// --- review round 1 ----------------------------------------------------------------

func TestListTenantsOmitsTenantsThatRefuseTheRequest(t *testing.T) {
	f := newAccessFixture(t)
	adm := newAdminAPI(f.mgr)
	slugs := func(r *http.Request, who store.Identity) string {
		w := httptest.NewRecorder()
		adm.listTenants(w, r, who)
		if w.Code != 200 {
			t.Fatalf("list: %d %s", w.Code, w.Body)
		}
		return w.Body.String()
	}
	// Positive control: from the office with the right method both rows come back.
	in := slugs(f.req("GET", "/t", "", "boss@example.com", "", "entra", inOffice), f.boss)
	if !strings.Contains(in, `"slug":"acme"`) || !strings.Contains(in, `"slug":"beta"`) {
		t.Fatalf("allowed request lost a row: %s", in)
	}
	out := slugs(f.req("GET", "/t", "", "boss@example.com", "", "entra", elsewhere), f.boss)
	if strings.Contains(out, `"slug":"acme"`) || !strings.Contains(out, `"slug":"beta"`) {
		t.Errorf("refused tenant listed (or open tenant dropped): %s", out)
	}
	if got := slugs(f.req("GET", "/t", "", "root@example.com", "", "google", elsewhere), f.root); !strings.Contains(got, `"slug":"acme"`) {
		t.Errorf("super_admin lost a row: %s", got)
	}
}

func TestIdentityStillAllowedOnlyJudgesWhatItCanVerify(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	if err := e.mgr.store.AttachProvider(ctx, e.ident.ID, store.IdentityLink{Provider: "entra", Subject: "s1", Email: "dev@example.com"}); err != nil {
		t.Fatal(err)
	}
	mk := func(file string) config {
		c := config{mgr: e.mgr, allowEmails: envx.EmailSet(""), allowDomains: envx.DomainSet(""), allowEmailsFile: file}
		c.setProviders([]auth.LoginProvider{&auth.OIDCProvider{ProviderID: "entra", DeployAllowed: c.emailAllowed}})
		return c
	}
	// Positive control: a definitive denial (nobody lists the address, the roster has no row for it).
	if mk("").identityStillAllowed(ctx, e.ident) {
		t.Fatal("a definitive denial must answer false")
	}
	// An allowlist file that cannot be read is "could not tell", not "no".
	if !mk(filepath.Join(t.TempDir(), "missing.txt")).identityStillAllowed(ctx, e.ident) {
		t.Error("an unreadable allowlist file revoked credentials")
	}
	// A provider that offers no verdict is not judged.
	c := mk("")
	c.setProviders([]auth.LoginProvider{noVerdictProvider{id: "entra"}})
	if !c.identityStillAllowed(ctx, e.ident) {
		t.Error("a provider with no verdict revoked credentials")
	}
}

type noVerdictProvider struct{ id string }

func (p noVerdictProvider) ID() string          { return p.id }
func (p noVerdictProvider) Label(string) string { return p.id }
func (p noVerdictProvider) AuthorizeURL(context.Context, string, string) (string, error) {
	return "", nil
}
func (p noVerdictProvider) Exchange(context.Context, string, string) (auth.Principal, error) {
	return auth.Principal{}, nil
}
func (p noVerdictProvider) Allowed(context.Context, auth.Principal) (bool, error) { return false, nil }

func TestGitHubEntryVerdictKeepsUnknownApartFromDenied(t *testing.T) {
	p := &auth.GitHubProvider{ProviderID: "github", DeployAllowed: func(e string) bool { return e == "ok@example.com" }, DeployHasList: true,
		TTL: time.Minute, Grace: time.Minute}
	ctx := context.Background()
	if v := p.EntryVerdict(ctx, auth.Principal{Email: "no@example.com", Subject: "1"}); v != auth.EntryDenied {
		t.Errorf("email gate = %v, want denied", v)
	}
	// Nothing cached (restart / other replica): the org gate cannot be judged.
	if v := p.EntryVerdict(ctx, auth.Principal{Email: "ok@example.com", Subject: "1"}); v != auth.EntryUnknown {
		t.Errorf("no snapshot = %v, want unknown", v)
	}
	// A fresh snapshot is GitHub's own answer, either way.
	p.Remember("2", "tok", true)
	if v := p.EntryVerdict(ctx, auth.Principal{Email: "ok@example.com", Subject: "2"}); v != auth.EntryAllowed {
		t.Errorf("fresh member = %v, want allowed", v)
	}
	p.Remember("3", "tok", false)
	if v := p.EntryVerdict(ctx, auth.Principal{Email: "ok@example.com", Subject: "3"}); v != auth.EntryDenied {
		t.Errorf("fresh non-member = %v, want denied", v)
	}
}

func TestBadPATRequestBodiesAreRefused(t *testing.T) {
	f := newAccessFixture(t)
	api := newPATAPI(f.mgr)
	mint := func(body string) int {
		w := httptest.NewRecorder()
		api.create(w, f.req("POST", "/api/pat", body, "boss@example.com", "beta", "entra", inOffice), f.boss)
		return w.Code
	}
	for _, body := range []string{`{"ttl_days":99999999999999999999}`, `{"ttl_days":"30"}`, `{"name":`} {
		if c := mint(body); c != 400 {
			t.Errorf("%s: %d, want 400", body, c)
		}
	}
	if c := mint(``); c != 200 {
		t.Errorf("empty body: %d, want 200", c)
	}
}

func TestParsePATMaxDays(t *testing.T) {
	for raw, want := range map[string]int{"": 0, "30": 30, " 365 ": 365, "3650": 3650} {
		if got, err := parsePATMaxDays(raw); err != nil || got != want {
			t.Errorf("%q = %d, %v; want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"0", "-1", "abc", "3651", "99999999999999999999", "1.5"} {
		if _, err := parsePATMaxDays(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}

func TestPATWithAnUnreadableExpiryIsNotHonored(t *testing.T) {
	e := newMCPOAuthEnv(t)
	tok, h := newPATToken()
	if err := e.mgr.store.CreatePAT(context.Background(), store.PAT{ID: "bad", IdentityID: e.ident.ID, MembershipID: e.membershipID(),
		Scope: "read", Name: "bad", CreatedAt: store.NowTS(), ExpiresAt: "not-a-date"}, h); err != nil {
		t.Fatal(err)
	}
	if rec := e.mcpCall(tok, "tools/list"); rec.Code != 401 {
		t.Errorf("unparsable expiry: %d, want 401", rec.Code)
	}
}

func TestCredentialsCannotBeIssuedAgainstAnInactiveMembership(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	mid := e.membershipID()
	if err := e.mgr.store.SetMembershipStatus(ctx, mid, "inactive"); err != nil {
		t.Fatal(err)
	}
	_, h := newPATToken()
	err := e.mgr.store.CreatePAT(ctx, store.PAT{ID: "late", IdentityID: e.ident.ID, MembershipID: mid, Scope: "read", Name: "late", CreatedAt: store.NowTS()}, h)
	if !errors.Is(err, store.ErrMembershipInactive) {
		t.Errorf("PAT insert on an inactive membership: %v", err)
	}
	err = e.mgr.store.CreateMCPOAuthGrant(ctx, store.MCPOAuthGrant{ID: "g-late", ClientID: "c", IdentityID: e.ident.ID, MembershipID: mid,
		TenantID: e.tenA.ID, Scope: "read", Resource: "r", CreatedAt: time.Now().Unix(), IdleExpiresAt: time.Now().Unix() + 100, AbsExpiresAt: time.Now().Unix() + 100})
	if !errors.Is(err, store.ErrMembershipInactive) {
		t.Errorf("grant insert on an inactive membership: %v", err)
	}
	// Positive control: once active again, issuing works.
	if err := e.mgr.store.SetMembershipStatus(ctx, mid, "active"); err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.store.CreatePAT(ctx, store.PAT{ID: "ok", IdentityID: e.ident.ID, MembershipID: mid, Scope: "read", Name: "ok", CreatedAt: store.NowTS()}, h); err != nil {
		t.Errorf("PAT insert on an active membership: %v", err)
	}
}

func TestBearerSetRevokeSparesCredentialsMintedAfterTheSnapshot(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	mk := func(id string) {
		_, h := newPATToken()
		// Everything is created in the same second on purpose: the set, not the clock, decides.
		if err := e.mgr.store.CreatePAT(ctx, store.PAT{ID: id, IdentityID: e.ident.ID, MembershipID: e.membershipID(), Scope: "read", Name: id,
			CreatedAt: store.NowTS(), ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, h); err != nil {
			t.Fatal(err)
		}
	}
	mk("before")
	set, err := e.mgr.store.LiveBearersOfIdentity(ctx, e.ident.ID)
	if err != nil {
		t.Fatal(err)
	}
	mk("after")
	if err := e.mgr.store.RevokeBearerSet(ctx, e.ident.ID, "test", set); err != nil {
		t.Fatal(err)
	}
	pats, _ := e.mgr.store.ListPATsByIdentity(ctx, e.ident.ID)
	if len(pats) != 2 {
		t.Fatalf("pats: %+v", pats)
	}
	for _, p := range pats {
		if (p.ID == "before") != (p.RevokedAt != "") {
			t.Errorf("%s: revoked_at=%q", p.ID, p.RevokedAt)
		}
	}
}

// flakyRoster fails the roster lookup after `okCalls` successful ones.
type flakyRoster struct {
	*store.SQL
	okCalls int
}

func (f *flakyRoster) EmailHasActiveMembership(ctx context.Context, email string) (bool, error) {
	if f.okCalls <= 0 {
		return false, errors.New("transient")
	}
	f.okCalls--
	return f.SQL.EmailHasActiveMembership(ctx, email)
}

func TestIdentityStillAllowedKeepsCredentialsWhenTheJudgmentsOwnReadFails(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	if err := e.mgr.store.AttachProvider(ctx, e.ident.ID, store.IdentityLink{Provider: "entra", Subject: "s1", Email: "gone@example.com"}); err != nil {
		t.Fatal(err)
	}
	st := e.mgr.store.(*store.SQL)
	mk := func(okCalls int) config {
		e.mgr.tenantLogin = newTenantLoginCache(&flakyRoster{SQL: st, okCalls: okCalls})
		c := config{mgr: e.mgr, allowEmails: envx.EmailSet(""), allowDomains: envx.DomainSet("")}
		c.setProviders([]auth.LoginProvider{&auth.OIDCProvider{ProviderID: "entra", DBAllowed: c.tenantEmailAllowed}})
		return c
	}
	// Positive control: the roster answers "no" for real, so this is a definitive denial.
	if mk(100).identityStillAllowed(ctx, e.ident) {
		t.Fatal("a definitive denial must answer false")
	}
	// The verdict's own roster read fails: no final "no".
	if !mk(0).identityStillAllowed(ctx, e.ident) {
		t.Error("a failed roster read inside the judgment revoked credentials")
	}
}

func TestPendingCodesDieWithTheMembership(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	client := e.registerClient(loopbackCB)
	verifier, challenge := pkcePair()
	ticket := e.ticketFrom(e.authorize(e.authQuery(client, loopbackCB, challenge)))
	mid := e.membershipID()
	lq := locationQuery(t, e.consent(ticket, url.Values{"membership": {mid}, "scope": {"read"}}))
	code := lq.Get("code")
	if code == "" {
		t.Fatalf("no code: %v", lq)
	}
	if err := e.mgr.store.SetMembershipStatus(ctx, mid, "inactive"); err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.store.SetMembershipStatus(ctx, mid, "active"); err != nil {
		t.Fatal(err)
	}
	rec := e.post(mcpOAuthToken, url.Values{"grant_type": {"authorization_code"}, "client_id": {client},
		"code": {code}, "redirect_uri": {loopbackCB}, "code_verifier": {verifier}, "resource": {testResource}})
	if rec.Code == http.StatusOK {
		t.Fatalf("a code approved before the deactivation was exchanged after the re-invite: %s", rec.Body)
	}
	// Positive control: a fresh authorization in the new active period still works.
	if tk := e.connect(client, loopbackCB, "read"); tk.Access == "" {
		t.Error("fresh connect after re-invite failed")
	}
}

func TestInFlightExchangeCannotCompleteAfterReinvite(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	if _, err := e.mgr.store.CreateMCPOAuthClient(ctx, store.MCPOAuthClient{ClientID: "c", RedirectURIs: "[]", CreatedAt: time.Now().Unix()}, 10); err != nil {
		t.Fatal(err)
	}
	mid := e.membershipID()
	g := store.MCPOAuthGrant{ID: "g-inflight", ClientID: "c", IdentityID: e.ident.ID, MembershipID: mid, TenantID: e.tenA.ID,
		Scope: "read", Resource: "r", CodeHash: "gone-code", CreatedAt: time.Now().Unix(),
		IdleExpiresAt: time.Now().Unix() + 100, AbsExpiresAt: time.Now().Unix() + 100}
	// The code row was deleted by a deactivation that finished before this exchange got to the insert.
	if err := e.mgr.store.CreateMCPOAuthGrantFromCode(ctx, g); !errors.Is(err, store.ErrMembershipInactive) {
		t.Errorf("exchange without its code row: %v", err)
	}
}

// flakyRules fails the tenant-rules refresh while `fail` is set.
type flakyRules struct {
	*store.SQL
	fail bool
}

func (f *flakyRules) ListTenantLoginRules(ctx context.Context) ([]store.TenantLoginRules, error) {
	if f.fail {
		return nil, errors.New("transient")
	}
	return f.SQL.ListTenantLoginRules(ctx)
}

func TestIdentityStillAllowedKeepsCredentialsWhenTheRulesRefreshFails(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	if err := e.mgr.store.AttachProvider(ctx, e.ident.ID, store.IdentityLink{Provider: "entra", Subject: "s1", Email: "gone@example.com"}); err != nil {
		t.Fatal(err)
	}
	fr := &flakyRules{SQL: e.mgr.store.(*store.SQL)}
	cache := newTenantLoginCache(fr)
	e.mgr.tenantLogin = cache
	c := config{mgr: e.mgr, allowEmails: envx.EmailSet(""), allowDomains: envx.DomainSet("")}
	c.setProviders([]auth.LoginProvider{&auth.OIDCProvider{ProviderID: "entra", DBAllowed: c.tenantEmailAllowed}})
	// Positive control with a healthy read: a definitive denial.
	if c.identityStillAllowed(ctx, e.ident) {
		t.Fatal("a definitive denial must answer false")
	}
	// The snapshot is non-nil but expired, and its refresh fails: the stale rules cannot make a final "no".
	cache.mu.Lock()
	cache.snapExp = time.Time{}
	cache.mu.Unlock()
	fr.fail = true
	if !c.identityStillAllowed(ctx, e.ident) {
		t.Error("a failed rules refresh behind a stale snapshot revoked credentials")
	}
}

func TestCodeAndConsentRowsAreIssuedUnderTheMembershipLock(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	if _, err := e.mgr.store.CreateMCPOAuthClient(ctx, store.MCPOAuthClient{ClientID: "c", RedirectURIs: "[]", CreatedAt: time.Now().Unix()}, 10); err != nil {
		t.Fatal(err)
	}
	mid := e.membershipID()
	row := func(hash, grant string) store.MCPOAuthToken {
		return store.MCPOAuthToken{Hash: hash, Kind: store.MCPOAuthCode, GrantID: grant, ClientID: "c", IdentityID: e.ident.ID,
			MembershipID: mid, ExpiresAt: time.Now().Unix() + 100}
	}
	if err := e.mgr.store.InsertMCPOAuthToken(ctx, row("c-active", "")); err != nil {
		t.Fatalf("code insert while active: %v", err)
	}
	if err := e.mgr.store.SetMembershipStatus(ctx, mid, "inactive"); err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.store.InsertMCPOAuthToken(ctx, row("c-late", "")); !errors.Is(err, store.ErrMembershipInactive) {
		t.Errorf("code insert after deactivation: %v", err)
	}
	if err := e.mgr.store.SetMembershipStatus(ctx, mid, "active"); err != nil {
		t.Fatal(err)
	}
	// The late row never landed, so it cannot be redeemed after the re-invite.
	if _, state, _ := e.mgr.store.ConsumeMCPOAuthToken(ctx, "c-late", store.MCPOAuthCode, time.Now().Unix()); state != store.MCPOAuthSpendUnknown {
		t.Errorf("late code after re-invite: %v, want unknown", state)
	}
}
