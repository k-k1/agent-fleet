package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Stage three of the cleanup (docs/log/61 §61.18): remove from the roster → destroy the
// workspace → delete the row. This last stage must refuse unless the first two are done.

// cleanupFixture builds tenant sales with one administrator and one subject. The subject
// gets a workspace row, working data that must be deleted (user_limit) and history that
// must survive (occupancy, cost, audit).
func cleanupFixture(t *testing.T) (*store.SQL, *manager, store.Tenant, string) {
	t.Helper()
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	mgr.rtFactory = &destroyingFactory{}
	tn, err := st.CreateTenant(ctx, "sales", "営業部")
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	admin, _ := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin")
	if _, err := st.EnsureMembership(ctx, admin.ID, tn.ID, "tenant_admin"); err != nil {
		t.Fatalf("admin membership: %v", err)
	}
	victim, _ := st.UpsertIdentity(ctx, "leaver@acme.co.jp", "leaver-acme-co-jp", "")
	mem, err := st.EnsureMembership(ctx, victim.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	if err := st.CreateWorkspace(ctx, store.Workspace{
		ID: "W-1", TenantID: tn.ID, MembershipID: mem.ID,
		ContainerName: "af-ws-sales-leaver", DataDir: "/srv/data/sales/leaver",
		AgentPort: "7731", AgentToken: "tok", State: "stopped", CreatedAt: store.NowTS(),
	}); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := st.PutUserLimit(ctx, mem.ID, store.UserQuota{MaxSessions: 3}); err != nil {
		t.Fatalf("user limit: %v", err)
	}
	if err := st.AddUsage(ctx, mem.ID, tn.ID, "2026-07-01", 3600); err != nil {
		t.Fatalf("usage: %v", err)
	}
	if err := st.PutCloudCost(ctx, []string{"2026-07-01"}, []store.CloudCostRow{
		{Day: "2026-07-01", MembershipID: mem.ID, TenantID: tn.ID, Service: "Amazon EC2",
			Unblended: 4200, Currency: "USD"},
	}); err != nil {
		t.Fatalf("cloud cost: %v", err)
	}
	if err := st.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), TenantID: tn.ID, ActorKind: "user", ActorID: admin.ID,
		Action: "membership.remove", Target: "leaver-acme-co-jp", At: store.NowTS(),
	}); err != nil {
		t.Fatalf("audit: %v", err)
	}
	return st, mgr, tn, mem.ID
}

func callDeleteMembership(mgr *manager, slug, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodDelete, "/api/admin/tenants/"+slug+"/members/"+key, nil)
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	r.SetPathValue("slug", slug)
	r.SetPathValue("key", key)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).deleteMembership(w, r)
	return w
}

// An active member's row cannot be deleted — the same line as ADR 0045 decision 13-2, for
// the same reason: no irreversible operation one click away in the admin screen.
func TestDeleteMembershipRefusesAnActiveMember(t *testing.T) {
	st, mgr, tn, memID := cleanupFixture(t)
	w := callDeleteMembership(mgr, "sales", "leaver-acme-co-jp")
	if w.Code != http.StatusConflict {
		t.Fatalf("delete of an active member = %d %s, want 409", w.Code, w.Body.String())
	}
	if _, ok, _ := st.GetMembershipByID(context.Background(), memID); !ok {
		t.Error("the membership was deleted despite the refusal")
	}
	_ = tn
}

// Refused while the workspace row is still there. Deleting it leaves the home, EBS and
// EFS billed with nothing in the DB pointing at them — exactly the hole destroyWorkspace
// closes.
func TestDeleteMembershipRefusesWhileTheWorkspaceRowIsThere(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, memID := cleanupFixture(t)
	if err := st.SetMembershipStatus(ctx, memID, "inactive"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	w := callDeleteMembership(mgr, "sales", "leaver-acme-co-jp")
	if w.Code != http.StatusConflict {
		t.Fatalf("delete with a workspace row = %d %s, want 409", w.Code, w.Body.String())
	}
	if _, ok, _ := st.GetWorkspaceByMembership(ctx, memID); !ok {
		t.Error("the workspace row disappeared during a refusal")
	}
}

// What is deleted and what is kept once the delete is allowed: the whole of the
// operation.
func TestDeleteMembershipRemovesTheWorkAndKeepsTheHistory(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, memID := cleanupFixture(t)
	if err := st.SetMembershipStatus(ctx, memID, "inactive"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if err := st.DeleteWorkspace(ctx, "W-1"); err != nil {
		t.Fatalf("destroy: %v", err)
	}

	w := callDeleteMembership(mgr, "sales", "leaver-acme-co-jp")
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, ok, _ := st.GetMembershipByID(ctx, memID); ok {
		t.Error("the membership row survived")
	}
	if _, ok, err := st.GetUserLimit(ctx, memID); err != nil || ok {
		t.Errorf("the per-membership quota survived (ok=%v err=%v)", ok, err)
	}

	// Survivor 1: occupancy.
	usage, err := st.ListUsage(ctx, tn.ID, "2026-07-01", "2026-07-01")
	if err != nil || len(usage) == 0 {
		t.Errorf("occupancy history was deleted: %+v %v", usage, err)
	}
	// Survivor 2: cost. A past month's billed total must never change after the fact.
	cost, err := st.ListCloudCost(ctx, "", memID, "2026-07-01", "2026-07-01")
	if err != nil || len(cost) == 0 || cost[0].Unblended != 4200 {
		t.Errorf("cost history changed after the fact: %+v %v", cost, err)
	}
	// Survivor 3: audit. The record of a removal must not be erasable by the cleanup of
	// that same removal.
	rows, err := st.ListAuditByTenant(ctx, tn.ID, 20)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	var removed, deleted bool
	for _, row := range rows {
		if row.Action == "membership.remove" {
			removed = true
		}
		if row.Action == "membership.delete" && row.Target == "leaver-acme-co-jp" {
			deleted = true
		}
	}
	if !removed {
		t.Error("the earlier membership.remove entry was deleted with the row")
	}
	if !deleted {
		t.Errorf("the deletion itself is not in the audit log: %+v", rows)
	}
}

// A reserved membership (the seed and the probe) cannot be deleted: the next bake just
// recreates it, and deleting one mid-bake leaves it orphaned while still holding a slot.
func TestDeleteMembershipRefusesAReservedMembership(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	tn, err := st.CreateTenant(ctx, goldenTenantSlug, goldenTenantName)
	if err != nil {
		t.Fatalf("golden tenant: %v", err)
	}
	admin, _ := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin")
	if _, err := st.EnsureMembership(ctx, admin.ID, tn.ID, "tenant_admin"); err != nil {
		t.Fatalf("admin membership: %v", err)
	}
	seed, _ := st.UpsertIdentity(ctx, "", runtime.GoldenSeedKey, "")
	seedMem, err := st.EnsureMembership(ctx, seed.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	if err := st.SetMembershipStatus(ctx, seedMem.ID, "inactive"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	w := callDeleteMembership(mgr, goldenTenantSlug, runtime.GoldenSeedKey)
	if w.Code != http.StatusConflict {
		t.Fatalf("delete of a reserved membership = %d %s, want 409", w.Code, w.Body.String())
	}
	if _, ok, _ := st.GetMembershipByID(ctx, seedMem.ID); ok {
		return // GetMembershipByID says ok=false for an inactive row; checked below instead.
	}
	members, err := st.ListRemovedMembersByTenant(ctx, tn.ID)
	if err != nil || len(members) != 1 {
		t.Errorf("the reserved membership was deleted anyway: %+v %v", members, err)
	}
}
