package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ADR 0114 decision 8 (P2). What these pin down:
//
//   - the admin face is the tenant's own: another tenant's admin and a plain member are refused
//   - only the known agents, strictly decoded, are accepted, and a refusal stores nothing
//   - a save is audited
//   - members of that tenant read the default through GET /api/me/ui-state, others do not, and a
//     member cannot write it there
//   - deleting the tenant takes the row

func memberDefaultsCall(mgr *manager, method, slug, email, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/admin/tenants/"+slug+"/member-defaults", strings.NewReader(body))
	r.SetPathValue("slug", slug)
	r.Header.Set("X-Forwarded-Email", email)
	w := httptest.NewRecorder()
	adm := newAdminAPI(mgr)
	if method == http.MethodGet {
		adm.tenantMemberDefaults(w, r)
	} else {
		adm.setTenantMemberDefaults(w, r)
	}
	return w
}

func memberUIStateCall(mgr *manager, method, tenant, email, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/me/ui-state", strings.NewReader(body))
	r.Header.Set("X-Forwarded-Email", email)
	r.Header.Set("X-AF-Tenant", tenant)
	w := httptest.NewRecorder()
	api := newMemberUIStateAPI(mgr)
	if method == http.MethodGet {
		api.withMembership(api.get)(w, r)
	} else {
		api.withMembership(api.patch)(w, r)
	}
	return w
}

func TestTenantMemberDefaultsAdminIsTheTenants(t *testing.T) {
	st := p3Store(t)
	mgr := p3Manager(t, st)
	sub := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	seedGitOAuthTenant(t, st, "other", "admin@other.co.jp")
	addBranchRulesMember(t, st, sub.ID, "user@sub.co.jp", "member")

	if w := memberDefaultsCall(mgr, http.MethodGet, "sub", "admin@sub.co.jp", ""); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"defaults":{"agent":""}`) {
		t.Fatalf("empty get: %d %s", w.Code, w.Body.String())
	}
	if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", `{"agent":"codex"}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if w := memberDefaultsCall(mgr, http.MethodGet, "sub", "admin@sub.co.jp", ""); !strings.Contains(w.Body.String(), `"agent":"codex"`) ||
		!strings.Contains(w.Body.String(), `"updated_at"`) {
		t.Fatalf("get after save: %s", w.Body.String())
	}

	for _, who := range []string{"admin@other.co.jp", "user@sub.co.jp"} {
		if w := memberDefaultsCall(mgr, http.MethodGet, "sub", who, ""); w.Code != http.StatusForbidden {
			t.Errorf("%s reads sub: %d", who, w.Code)
		}
		if w := memberDefaultsCall(mgr, http.MethodPut, "sub", who, `{"agent":"claude"}`); w.Code != http.StatusForbidden {
			t.Errorf("%s writes sub: %d", who, w.Code)
		}
	}

	for _, bad := range []string{
		`{"agent":"shell"}`,
		`{"agent":"lcpp"}`,
		`{"agent":"Claude"}`,
		`{"agent":"claude","role":"dev"}`,
		`{"agent":"claude"} {}`,
		`{"agent":"claude"}]`,
		`{}}`,
		`null`,
		`not json`,
		`{"agent":"` + strings.Repeat("x", tenantMemberDefaultsMaxBody) + `"}`,
	} {
		if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", bad); w.Code != http.StatusBadRequest && w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("accepted %.60s: %d %s", bad, w.Code, w.Body.String())
		}
	}
	if w := memberDefaultsCall(mgr, http.MethodGet, "sub", "admin@sub.co.jp", ""); !strings.Contains(w.Body.String(), `"agent":"codex"`) {
		t.Errorf("a refused save changed the default: %s", w.Body.String())
	}
	// Clearing is a save like any other.
	if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", `{"agent":""}`); w.Code != http.StatusOK {
		t.Errorf("clear: %d %s", w.Code, w.Body.String())
	}
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='tenant.member_defaults'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("audit rows = %d (%v), want one per accepted save", n, err)
	}
}

func TestTenantMemberDefaultsReachTheTenantsMembers(t *testing.T) {
	st := p3Store(t)
	mgr := p3Manager(t, st)
	sub := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	other := seedGitOAuthTenant(t, st, "other", "admin@other.co.jp")
	addBranchRulesMember(t, st, sub.ID, "user@sub.co.jp", "member")
	addBranchRulesMember(t, st, other.ID, "user@other.co.jp", "member")

	if w := memberDefaultsCall(mgr, http.MethodPut, "sub", "admin@sub.co.jp", `{"agent":"claude"}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if w := memberUIStateCall(mgr, http.MethodGet, "sub", "user@sub.co.jp", ""); w.Code != http.StatusOK ||
		strings.TrimSpace(w.Body.String()) != `{"defaults":{"agent":"claude"}}` {
		t.Errorf("sub member GET = %d %s", w.Code, w.Body.String())
	}
	if w := memberUIStateCall(mgr, http.MethodGet, "other", "user@other.co.jp", ""); strings.TrimSpace(w.Body.String()) != emptyUIState {
		t.Errorf("another tenant's member sees sub's default: %s", w.Body.String())
	}
	// The defaults ride along on a PATCH answer too, and a member cannot write them.
	if w := memberUIStateCall(mgr, http.MethodPatch, "sub", "user@sub.co.jp", `{"onboarding":{"dismissed":true,"track":""}}`); !strings.Contains(w.Body.String(), `"defaults":{"agent":"claude"}`) {
		t.Errorf("PATCH answer = %d %s", w.Code, w.Body.String())
	}
	if w := memberUIStateCall(mgr, http.MethodPatch, "sub", "user@sub.co.jp", `{"defaults":{"agent":"codex"}}`); w.Code != http.StatusBadRequest {
		t.Errorf("member wrote defaults: %d %s", w.Code, w.Body.String())
	}
}

func TestTenantMemberDefaultsGoWithTheTenant(t *testing.T) {
	st := p3Store(t)
	ctx := context.Background()
	a, err := st.CreateTenant(ctx, "sales", "Sales")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutTenantMemberDefaults(ctx, storeTenantMemberDefaults(a.ID, `{"agent":"claude"}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTenant(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.GetTenantMemberDefaults(ctx, a.ID); err != nil || ok {
		t.Errorf("a deleted tenant left its defaults: ok=%v err=%v", ok, err)
	}
}

func storeTenantMemberDefaults(tenantID, doc string) store.TenantMemberDefaults {
	return store.TenantMemberDefaults{TenantID: tenantID, Defaults: doc, UpdatedAt: store.NowTS()}
}
