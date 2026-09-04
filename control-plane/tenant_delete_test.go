package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Tenant deletion (docs/log/61 §61.18): only an empty tenant may go. The DB rows are the
// only handle on what is left behind in the cloud or on disk, so they must never be the
// first thing to disappear.

func callDeleteTenant(mgr *manager, slug string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodDelete, "/api/admin/tenants/"+slug, nil)
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	r.SetPathValue("slug", slug)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).deleteTenant(w, r, store.Identity{ID: "I-boss", Role: "super_admin"})
	return w
}

func tenantStillThere(t *testing.T, st *store.SQL, slug string) {
	t.Helper()
	if _, ok, err := st.GetTenantBySlug(context.Background(), slug); err != nil || !ok {
		t.Errorf("the tenant was deleted despite the refusal (ok=%v err=%v)", ok, err)
	}
}

// Refused while even one member is still on the roster: this is not an offboarding tool.
func TestDeleteTenantRefusesWhileAMemberIsOnTheRoster(t *testing.T) {
	st, mgr, _, _ := cleanupFixture(t)
	w := callDeleteTenant(mgr, "sales")
	if w.Code != http.StatusConflict {
		t.Fatalf("delete with members = %d %s, want 409", w.Code, w.Body.String())
	}
	tenantStillThere(t, st, "sales")
}

// Refused while a workspace row exists. Deleting the tenant does not delete the home, the
// EBS or the EFS — it deletes the only row that pointed at them.
func TestDeleteTenantRefusesWhileAWorkspaceRowIsThere(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, memID := cleanupFixture(t)
	for _, m := range mustMembers(t, st, tn.ID) {
		if err := st.SetMembershipStatus(ctx, m.MembershipID, "inactive"); err != nil {
			t.Fatalf("deactivate: %v", err)
		}
	}
	if _, ok, _ := st.GetWorkspaceByMembership(ctx, memID); !ok {
		t.Fatal("fixture lost its workspace row")
	}
	w := callDeleteTenant(mgr, "sales")
	if w.Code != http.StatusConflict {
		t.Fatalf("delete with a workspace row = %d %s, want 409", w.Code, w.Body.String())
	}
	tenantStillThere(t, st, "sales")
}

// Refused while an internal git repository exists: the bare repo and its LFS stay on disk.
// There is an ordering trap too — the repo-delete API is behind the withMembership gate,
// so once the last member is removed nobody can delete them any more. Hence the refusal
// message spells the order out.
func TestDeleteTenantRefusesWhileAnInternalRepoExists(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, memID := cleanupFixture(t)
	for _, m := range mustMembers(t, st, tn.ID) {
		if err := st.SetMembershipStatus(ctx, m.MembershipID, "inactive"); err != nil {
			t.Fatalf("deactivate: %v", err)
		}
	}
	if err := st.DeleteWorkspace(ctx, "W-1"); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if err := st.CreateGitRepo(ctx, store.GitRepo{
		ID: store.NewID(), TenantID: tn.ID, Name: "tools", DefaultBranch: "main",
		CreatedBy: "boss-acme-co-jp", CreatedAt: store.NowTS(),
	}); err != nil {
		t.Fatalf("repo: %v", err)
	}
	w := callDeleteTenant(mgr, "sales")
	if w.Code != http.StatusConflict {
		t.Fatalf("delete with a repo = %d %s, want 409", w.Code, w.Body.String())
	}
	tenantStillThere(t, st, "sales")
	_ = memID
}

// The reserved and default tenants are recreated by the next boot or bake anyway.
func TestDeleteTenantRefusesTheSystemAndDefaultTenants(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	if _, err := st.CreateTenant(ctx, goldenTenantSlug, goldenTenantName); err != nil {
		t.Fatalf("golden tenant: %v", err)
	}
	if _, err := st.EnsureDefaultTenant(ctx); err != nil {
		t.Fatalf("default tenant: %v", err)
	}
	for _, slug := range []string{goldenTenantSlug, auth.DefaultTenantSlug} {
		if w := callDeleteTenant(mgr, slug); w.Code != http.StatusConflict {
			t.Errorf("delete of %s = %d %s, want 409", slug, w.Code, w.Body.String())
		}
		tenantStillThere(t, st, slug)
	}
}

// An empty tenant is deleted along with any leftover inactive memberships, while the
// history (audit, occupancy, cost) survives.
func TestDeleteTenantRemovesTheEmptyTenantAndKeepsTheHistory(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, memID := cleanupFixture(t)
	for _, m := range mustMembers(t, st, tn.ID) {
		if err := st.SetMembershipStatus(ctx, m.MembershipID, "inactive"); err != nil {
			t.Fatalf("deactivate: %v", err)
		}
	}
	if err := st.DeleteWorkspace(ctx, "W-1"); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	// The tenant's settings rows must go with it.
	if err := st.SetTenantLogin(ctx, tn.ID, "entra", "", "", ""); err != nil {
		t.Fatalf("login rules: %v", err)
	}

	w := callDeleteTenant(mgr, "sales")
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, ok, _ := st.GetTenantBySlug(ctx, "sales"); ok {
		t.Error("the tenant row survived")
	}
	if _, ok, _ := st.GetMembershipByID(ctx, memID); ok {
		t.Error("a membership survived its tenant")
	}
	if left, err := st.ListRemovedMembersByTenant(ctx, tn.ID); err != nil || len(left) != 0 {
		t.Errorf("removed memberships survived: %+v %v", left, err)
	}

	// What survives: occupancy, cost, audit. Deleting a tenant does not rewrite billing
	// history.
	if usage, err := st.ListUsage(ctx, tn.ID, "2026-07-01", "2026-07-01"); err != nil || len(usage) == 0 {
		t.Errorf("occupancy history was deleted with the tenant: %+v %v", usage, err)
	}
	if cost, err := st.ListCloudCost(ctx, tn.ID, "", "2026-07-01", "2026-07-01"); err != nil || len(cost) == 0 {
		t.Errorf("cost history was deleted with the tenant: %+v %v", cost, err)
	}
	rows, err := st.ListAuditByTenant(ctx, tn.ID, 20)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	var logged bool
	for _, row := range rows {
		// The audit view resolves tenant_id → slug through ListTenants, so a deleted
		// tenant's rows show an empty tenant column: the name has to live inside the
		// row itself (Target / Detail).
		if row.Action == "tenant.delete" && row.Target == "sales" {
			logged = true
		}
	}
	if !logged {
		t.Errorf("the deletion is not in the audit log with its slug: %+v", rows)
	}
}

func mustMembers(t *testing.T, st *store.SQL, tenantID string) []store.MemberInfo {
	t.Helper()
	ms, err := st.ListMembersByTenant(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("members: %v", err)
	}
	return ms
}
