package main

// engine_access_test.go — #1215: a tenant_admin restricting a role to granted members. The
// gateway's gates (catalog / token / serve) must honour it per membership, the super_admin's
// tenant denial must win over any grant, and the tenant_admin API must refuse members of
// another tenant and callers who are not a tenant_admin.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestEngineGatewayHonoursTheMemberGrant(t *testing.T) {
	g, mid := engineTenantGateFixture(t)
	ctx := t.Context()
	st := g.mgr.store
	mv, ok, err := st.GetMembershipByID(ctx, mid["allowed"])
	if err != nil || !ok {
		t.Fatalf("membership: %v %v", ok, err)
	}

	// Positive control: the role open to everyone, as before #1215.
	if keys := catalogFor(t, g, mid["allowed"]); len(keys) != 1 {
		t.Fatalf("positive control: catalog = %v, want [llm]", keys)
	}

	if err := st.SetEngineMembersOnly(ctx, mv.TenantID, store.EngineAccessLLM, true); err != nil {
		t.Fatal(err)
	}
	if keys := catalogFor(t, g, mid["allowed"]); len(keys) != 0 {
		t.Errorf("ungranted member's catalog = %v, want none", keys)
	}
	if code, body := issueTokenFor(g, mid["allowed"]); code != http.StatusForbidden || !strings.Contains(body, "engine_forbidden") {
		t.Errorf("ungranted member's token = %d %s, want 403 engine_forbidden", code, body)
	}
	if code, body := serveLLM(g, mid["allowed"]); code != http.StatusForbidden || !strings.Contains(body, "tenant admin") {
		t.Errorf("ungranted member's relay = %d %s, want 403 naming the tenant admin", code, body)
	}

	if err := st.SetEngineGrant(ctx, mv.TenantID, mid["allowed"], store.EngineAccessLLM, true); err != nil {
		t.Fatal(err)
	}
	if keys := catalogFor(t, g, mid["allowed"]); len(keys) != 1 {
		t.Errorf("granted member's catalog = %v, want [llm]", keys)
	}
	if code, body := serveLLM(g, mid["allowed"]); code != http.StatusOK {
		t.Errorf("granted member's relay = %d %s, want 200", code, body)
	}
}

// A grant can only narrow the super_admin's gate: a member ticked in a tenant denied the
// role stays refused, and the message points at the tenant, not the tenant admin.
func TestEngineMemberGrantCannotWidenTheTenantDenial(t *testing.T) {
	g, mid := engineTenantGateFixture(t)
	ctx := t.Context()
	mv, _, _ := g.mgr.store.GetMembershipByID(ctx, mid["denied"])
	if err := g.mgr.store.SetEngineMembersOnly(ctx, mv.TenantID, store.EngineAccessLLM, true); err != nil {
		t.Fatal(err)
	}
	if err := g.mgr.store.SetEngineGrant(ctx, mv.TenantID, mid["denied"], store.EngineAccessLLM, true); err != nil {
		t.Fatal(err)
	}
	code, body := serveLLM(g, mid["denied"])
	if code != http.StatusForbidden || !strings.Contains(body, "this tenant is not allowed") {
		t.Errorf("granted member of a denied tenant = %d %s, want the tenant-level 403", code, body)
	}
	if keys := catalogFor(t, g, mid["denied"]); len(keys) != 0 {
		t.Errorf("catalog = %v, want none", keys)
	}
}

// Gate 4 reads the grant from the per-tenant cache: two members of one tenant must get
// different answers from the same cached entry, and a save must reach it on invalidation.
func TestMemberEngineGateForIsPerMemberFromOneTenantEntry(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, mv, _ := networkFixture(t)
	ms, _ := st.ListMembersByTenant(ctx, tn.ID)
	var other store.MembershipView
	for _, m := range ms {
		if m.MembershipID != mv.MembershipID {
			other = store.MembershipView{MembershipID: m.MembershipID, TenantID: tn.ID}
		}
	}
	invalidateTenantEngineLimits(tn.ID)
	t.Cleanup(func() { invalidateTenantEngineLimits(tn.ID) })

	if !memberEngineGateFor(ctx, mgr, mv).engineRoleAllowed(engineAPIImages) {
		t.Fatal("positive control: an untouched tenant must allow image")
	}
	if err := st.SetEngineMembersOnly(ctx, tn.ID, store.EngineAccessImage, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEngineGrant(ctx, tn.ID, mv.MembershipID, store.EngineAccessImage, true); err != nil {
		t.Fatal(err)
	}
	if !memberEngineGateFor(ctx, mgr, other).engineRoleAllowed(engineAPIImages) {
		t.Fatal("the cache must still hold the old answer before invalidation")
	}
	invalidateTenantEngineLimits(tn.ID)
	if !memberEngineGateFor(ctx, mgr, mv).engineRoleAllowed(engineAPIImages) {
		t.Error("the granted member lost image")
	}
	if memberEngineGateFor(ctx, mgr, other).engineRoleAllowed(engineAPIImages) {
		t.Error("the ungranted member kept image")
	}
	if !memberEngineGateFor(ctx, mgr, other).engineRoleAllowed(engineAPIChat) {
		t.Error("restricting image must leave llm open")
	}
}

func callEngineAccess(mgr *manager, method, path, email, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/admin/tenants/sales/engine-access"+path, strings.NewReader(body))
	r.SetPathValue("slug", "sales")
	r.Header.Set("X-Forwarded-Email", email)
	w := httptest.NewRecorder()
	a := newAdminAPI(mgr)
	switch {
	case method == http.MethodGet:
		a.tenantEngineAccess(w, r)
	case path == "/members":
		a.setMemberEngineAccess(w, r)
	default:
		a.setTenantEngineAccess(w, r)
	}
	return w
}

func TestTenantEngineAccessAPI(t *testing.T) {
	pushed := make(chan string, 8)
	orig := notifyEngineCatalogChangedForTenant
	notifyEngineCatalogChangedForTenant = func(_ context.Context, _ *manager, tenantID, _ string) { pushed <- tenantID }
	t.Cleanup(func() { notifyEngineCatalogChangedForTenant = orig })

	ctx := context.Background()
	st, mgr, tn, mv, _ := networkFixture(t)
	const boss = "boss@acme.co.jp"

	// A plain member may not read or change it.
	if w := callEngineAccess(mgr, http.MethodGet, "", "yamada@acme.co.jp", ""); w.Code != http.StatusForbidden {
		t.Fatalf("member GET = %d %s, want 403", w.Code, w.Body.String())
	}
	if w := callEngineAccess(mgr, http.MethodPut, "", "yamada@acme.co.jp", `{"role":"llm","members_only":true}`); w.Code != http.StatusForbidden {
		t.Fatalf("member PUT = %d %s, want 403", w.Code, w.Body.String())
	}

	if w := callEngineAccess(mgr, http.MethodPut, "", boss, `{"role":"llm","members_only":true}`); w.Code != http.StatusOK {
		t.Fatalf("PUT members_only = %d %s", w.Code, w.Body.String())
	}
	if w := callEngineAccess(mgr, http.MethodPut, "", boss, `{"role":"chat","members_only":true}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown role = %d, want 400", w.Code)
	}
	if w := callEngineAccess(mgr, http.MethodPut, "/members", boss,
		`{"membership_id":"`+mv.MembershipID+`","role":"llm","granted":true}`); w.Code != http.StatusOK {
		t.Fatalf("PUT grant = %d %s", w.Code, w.Body.String())
	}

	// A membership of another tenant is not this tenant_admin's to grant.
	other, _ := st.CreateTenant(ctx, "other", "Other")
	stranger, _ := st.UpsertIdentity(ctx, "x@example.com", "x-example-com", "")
	sm, _ := st.EnsureMembership(ctx, stranger.ID, other.ID, "member")
	if w := callEngineAccess(mgr, http.MethodPut, "/members", boss,
		`{"membership_id":"`+sm.ID+`","role":"llm","granted":true}`); w.Code != http.StatusNotFound {
		t.Errorf("foreign membership = %d %s, want 404", w.Code, w.Body.String())
	}
	if acc, _ := st.GetEngineAccess(ctx, other.ID); len(acc.Grants) != 0 {
		t.Errorf("a grant landed on another tenant: %+v", acc)
	}

	w := callEngineAccess(mgr, http.MethodGet, "", boss, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", w.Code, w.Body.String())
	}
	var got tenantEngineAccessView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Roles) != 2 || got.Roles[0].Role != "llm" || !got.Roles[0].MembersOnly || !got.Roles[0].TenantAllowed || got.Roles[1].MembersOnly {
		t.Errorf("roles = %+v", got.Roles)
	}
	grants := map[string][]string{}
	for _, m := range got.Members {
		grants[m.MembershipID] = m.Grants
	}
	if g := grants[mv.MembershipID]; len(g) != 1 || g[0] != "llm" {
		t.Errorf("grants = %+v", grants)
	}
	if len(got.Members) != 2 {
		t.Errorf("members = %+v, want the two in this tenant", got.Members)
	}

	for i := 0; i < 2; i++ {
		select {
		case id := <-pushed:
			if id != tn.ID {
				t.Errorf("pushed %q, want %q", id, tn.ID)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a save did not push the catalogue change")
		}
	}
	rows, _ := st.ListAuditByTenant(ctx, tn.ID, 0)
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Action] = true
	}
	if !seen["tenant.engine_access"] || !seen["member.engine_access"] {
		t.Errorf("audit actions = %v", seen)
	}
}

// tenantEngineAccessView decodes the GET response on the client's side of the wire.
type tenantEngineAccessView struct {
	Roles []struct {
		Role          string `json:"role"`
		TenantAllowed bool   `json:"tenant_allowed"`
		MembersOnly   bool   `json:"members_only"`
	} `json:"roles"`
	Members []struct {
		MembershipID string   `json:"membership_id"`
		Grants       []string `json:"grants"`
	} `json:"members"`
}
