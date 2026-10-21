package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestMCPOAuthConnectedOwnershipAndWire(t *testing.T) {
	e := newMCPOAuthEnv(t)
	registerPATRoutes(e.mux, config{mgr: e.mgr})
	client := e.registerClient(loopbackCB)
	tk := e.connect(client, loopbackCB, "read")
	rec := e.do("GET", "/api/mcp-oauth/grants", "", nil)
	var out mcpConnectedList
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 || !out.Enabled || len(out.Grants) != 1 {
		t.Fatalf("list: %d %s %v", rec.Code, rec.Body, err)
	}
	g := out.Grants[0]
	if g.ClientName != "Test Client" || g.RedirectHost != "127.0.0.1:53682" || g.Tenant != e.tenA.Slug || g.Scope != "read" || g.CreatedAt == 0 || g.LastUsedAt == 0 {
		t.Fatalf("display fields: %+v", g)
	}
	for _, secret := range []string{"code_hash", "identity_id", "resource", "afmcp_", devIdPHash(tk.Code)} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("wire leaked %q", secret)
		}
	}
	other, err := e.mgr.store.UpsertIdentity(context.Background(), "", "other-user", "")
	if err != nil {
		t.Fatal(err)
	}
	apps := mcpConnectedAPI{memberAuth{e.mgr}}
	revoke := func(id string, who store.Identity) *httptest.ResponseRecorder {
		r := httptest.NewRequest("DELETE", "/api/mcp-oauth/grants/"+id, nil)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		apps.revoke(w, r, who)
		return w
	}
	foreign, unknown := revoke(g.ID, other), revoke("unknown", other)
	if foreign.Code != 404 || foreign.Code != unknown.Code || foreign.Body.String() != unknown.Body.String() {
		t.Fatalf("IDOR: %d %s / %d %s", foreign.Code, foreign.Body, unknown.Code, unknown.Body)
	}
	if e.mcpCall(tk.Access, "tools/list").Code != 200 {
		t.Fatal("foreign revoke ended owner's grant")
	}
	if w := revoke(g.ID, e.ident); w.Code != 204 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if e.mcpCall(tk.Access, "tools/list").Code != 401 {
		t.Fatal("revoked access still accepted")
	}
	if w := e.refresh(client, tk.Refresh); jsonField(t, w, "error") != "invalid_grant" {
		t.Fatalf("revoked refresh: %s", w.Body)
	}
	e.mgr.mcpOAuthServed.Store(false)
	w := e.do("GET", "/api/mcp-oauth/grants", "", nil)
	if !strings.Contains(w.Body.String(), `"enabled":false`) || !strings.Contains(w.Body.String(), `"grants":[]`) {
		t.Fatalf("deployment flag: %s", w.Body)
	}
}

func TestMCPOAuthTenantGateEveryPath(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	client := e.registerClient(loopbackCB)
	tk := e.connect(client, loopbackCB, "read")
	_, challenge := pkcePair()
	q := e.authQuery(client, loopbackCB, challenge)
	ticket := e.ticketFrom(e.authorize(q))
	pat, hash := newPATToken()
	if err := e.mgr.store.CreatePAT(ctx, store.PAT{ID: store.NewID(), IdentityID: e.ident.ID, MembershipID: e.membershipID(), Scope: "read", CreatedAt: store.NowTS()}, hash); err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.store.SetTenantMCPOAuth(ctx, e.tenA.ID, false, e.ident.ID, e.now().Unix()); err != nil {
		t.Fatal(err)
	}
	if w := e.authorize(q); w.Code != 403 || w.Header().Get("Location") != "" {
		t.Fatalf("authorize: %d %s", w.Code, w.Body)
	}
	if w := e.consent(ticket, url.Values{"membership": {e.membershipID()}, "scope": {"read"}}); w.Code == 302 || w.Code == 200 {
		t.Fatalf("stale consent: %d %s", w.Code, w.Body)
	}
	if w := e.refresh(client, tk.Refresh); jsonField(t, w, "error") != "invalid_grant" {
		t.Fatalf("refresh: %d %s", w.Code, w.Body)
	}
	if w := e.mcpCall(tk.Access, "tools/list"); w.Code != 401 {
		t.Fatalf("mcp: %d %s", w.Code, w.Body)
	}
	if w := e.mcpCall(pat, "tools/list"); w.Code != 200 {
		t.Fatalf("PAT affected: %d %s", w.Code, w.Body)
	}
	if err := e.mgr.store.SetTenantMCPOAuth(ctx, e.tenA.ID, true, e.ident.ID, e.now().Unix()); err != nil {
		t.Fatal(err)
	}
	if w := e.mcpCall(tk.Access, "tools/list"); w.Code != 401 {
		t.Fatal("reenable restored old access")
	}
	e.connect(client, loopbackCB, "read")
}

type tokenInsertHold struct {
	store.Store
	once sync.Once
	hook func()
}

func (s *tokenInsertHold) InsertMCPOAuthToken(ctx context.Context, tok store.MCPOAuthToken) error {
	if tok.Kind == store.MCPOAuthAccessKind {
		s.once.Do(s.hook)
	}
	return s.Store.InsertMCPOAuthToken(ctx, tok)
}

func TestMCPOAuthTenantDisableDuringRefresh(t *testing.T) {
	e := newMCPOAuthEnv(t)
	client := e.registerClient(loopbackCB)
	tk := e.connect(client, loopbackCB, "read")
	original := e.mgr.store
	e.mgr.store = &tokenInsertHold{Store: original, hook: func() {
		if err := original.SetTenantMCPOAuth(context.Background(), e.tenA.ID, false, e.ident.ID, e.now().Unix()); err != nil {
			t.Fatal(err)
		}
		if err := original.SetTenantMCPOAuth(context.Background(), e.tenA.ID, true, e.ident.ID, e.now().Unix()); err != nil {
			t.Fatal(err)
		}
	}}
	w := e.refresh(client, tk.Refresh)
	if w.Code != 400 || jsonField(t, w, "error") != "invalid_grant" {
		t.Fatalf("in-flight refresh: %d %s", w.Code, w.Body)
	}
}

func TestMCPOAuthTenantDisableDuringCodeExchange(t *testing.T) {
	e := newMCPOAuthEnv(t)
	client := e.registerClient(loopbackCB)
	verifier, challenge := pkcePair()
	ticket := e.ticketFrom(e.authorize(e.authQuery(client, loopbackCB, challenge)))
	code := locationQuery(t, e.consent(ticket, url.Values{"membership": {e.membershipID()}, "scope": {"read"}})).Get("code")
	original := e.mgr.store
	e.mgr.store = &grantHold{Store: original, hook: func() {
		for _, enabled := range []bool{false, true} {
			if err := original.SetTenantMCPOAuth(context.Background(), e.tenA.ID, enabled, e.ident.ID, e.now().Unix()); err != nil {
				t.Fatal(err)
			}
		}
	}}
	w := e.post(mcpOAuthToken, url.Values{"grant_type": {"authorization_code"}, "client_id": {client}, "code": {code}, "redirect_uri": {loopbackCB}, "code_verifier": {verifier}, "resource": {testResource}})
	if w.Code != 400 || jsonField(t, w, "error") != "invalid_grant" {
		t.Fatalf("in-flight exchange: %d %s", w.Code, w.Body)
	}
	rows, err := original.ListAuditByTenant(context.Background(), e.tenA.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundRefusal := false
	for _, row := range rows {
		if row.Action == "mcp.oauth.grant" && row.Detail == "refused reason=tenant-disabled" {
			foundRefusal = true
		}
	}
	if !foundRefusal {
		t.Fatal("disabled grant creation lacks refusal audit")
	}
}

func TestMCPOAuthTenantAdminScope(t *testing.T) {
	st, mgr, tn, _, _ := networkFixture(t)
	other, err := st.CreateTenant(context.Background(), "other", "Other")
	if err != nil {
		t.Fatal(err)
	}
	call := func(slug, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/api/admin/tenants/"+slug+"/mcp-oauth", strings.NewReader(body))
		r.SetPathValue("slug", slug)
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		w := httptest.NewRecorder()
		newAdminAPI(mgr).setTenantMCPOAuth(w, r)
		return w
	}
	if w := call(tn.Slug, `{"enabled":false}`); w.Code != http.StatusOK {
		t.Fatalf("own tenant: %d %s", w.Code, w.Body)
	}
	if w := call(other.Slug, `{"enabled":false}`); w.Code != http.StatusForbidden {
		t.Fatalf("foreign tenant: %d %s", w.Code, w.Body)
	}
	if enabled, err := st.GetTenantMCPOAuth(context.Background(), other.ID); err != nil || !enabled {
		t.Fatalf("other tenant changed: %v %v", enabled, err)
	}
}

func TestMCPOAuthConsentOmitsOnlyDisabledTenants(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	disabledMembership := e.membershipID()
	other, err := e.mgr.store.CreateTenant(ctx, "other", "Other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.store.EnsureMembership(ctx, e.ident.ID, other.ID, "member"); err != nil {
		t.Fatal(err)
	}
	client := e.registerClient(loopbackCB)
	_, challenge := pkcePair()
	if err := e.mgr.store.SetTenantMCPOAuth(ctx, e.tenA.ID, false, e.ident.ID, e.now().Unix()); err != nil {
		t.Fatal(err)
	}
	w := e.authorize(e.authQuery(client, loopbackCB, challenge))
	if w.Code != 200 || !strings.Contains(w.Body.String(), ">Other</b>") || strings.Contains(w.Body.String(), `value="`+disabledMembership+`"`) {
		t.Fatalf("tenant choices: %d %s", w.Code, w.Body)
	}
}

func TestMCPOAuthTenantGateMemberAndSuperAdmin(t *testing.T) {
	st, mgr, tn, _, member := networkFixture(t)
	ctx := context.Background()
	if _, err := st.UpsertIdentity(ctx, "super@example.com", "super-example-com", "super_admin"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		email string
		want  int
	}{{member.Email, 403}, {"super@example.com", 200}} {
		r := httptest.NewRequest("PUT", "/api/admin/tenants/"+tn.Slug+"/mcp-oauth", strings.NewReader(`{"enabled":false}`))
		r.SetPathValue("slug", tn.Slug)
		r.Header.Set("X-Forwarded-Email", tc.email)
		w := httptest.NewRecorder()
		newAdminAPI(mgr).setTenantMCPOAuth(w, r)
		if w.Code != tc.want {
			t.Fatalf("role status: %d want %d: %s", w.Code, tc.want, w.Body)
		}
	}
	if enabled, err := st.GetTenantMCPOAuth(ctx, tn.ID); err != nil || enabled {
		t.Fatalf("super admin save: %v %v", enabled, err)
	}
}

type ownedRevokeHold struct {
	store.Store
	hook func()
}

func (s ownedRevokeHold) RevokeMCPOAuthGrantOf(ctx context.Context, id, identity, reason string, now int64) (bool, error) {
	s.hook()
	return s.Store.RevokeMCPOAuthGrantOf(ctx, id, identity, reason, now)
}

func TestMCPOAuthConnectedRevokeRaceReturnsNotFound(t *testing.T) {
	e := newMCPOAuthEnv(t)
	registerPATRoutes(e.mux, config{mgr: e.mgr})
	client := e.registerClient(loopbackCB)
	e.connect(client, loopbackCB, "read")
	original := e.mgr.store
	grants, err := original.ListMCPOAuthGrantsByIdentity(context.Background(), e.ident.ID, e.now().Unix())
	if err != nil || len(grants) != 1 {
		t.Fatal("missing grant", err)
	}
	g := grants[0]
	e.mgr.store = ownedRevokeHold{Store: original, hook: func() {
		if _, err := original.RevokeMCPOAuthGrantOf(context.Background(), g.ID, e.ident.ID, "revoked by member", e.now().Unix()); err != nil {
			t.Fatal(err)
		}
	}}
	w := e.do("DELETE", "/api/mcp-oauth/grants/"+g.ID, "", nil)
	if w.Code != 404 {
		t.Fatalf("raced revoke: %d %s", w.Code, w.Body)
	}
}
