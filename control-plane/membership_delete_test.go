package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 後始末の 3 段目（docs/61 §61.18）のテスト。除名 → Workspace 破棄 → 行の削除、の
// 最後の 1 段で、前の 2 段が済んでいない限り通ってはいけない。

// cleanupFixture: tenant sales に管理者 1 人と対象者 1 人。対象者には workspace 行と、
// 消えるべき作業データ（user_limit）、残るべき履歴（稼働時間・費用・監査）を置く。
func cleanupFixture(t *testing.T) (*sqlStore, *manager, Tenant, string) {
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
	if err := st.CreateWorkspace(ctx, Workspace{
		ID: "W-1", TenantID: tn.ID, MembershipID: mem.ID,
		ContainerName: "af-ws-sales-leaver", DataDir: "/srv/data/sales/leaver",
		AgentPort: "7731", AgentToken: "tok", State: "stopped", CreatedAt: nowTS(),
	}); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := st.PutUserLimit(ctx, mem.ID, UserQuota{MaxSessions: 3}); err != nil {
		t.Fatalf("user limit: %v", err)
	}
	if err := st.AddUsage(ctx, mem.ID, tn.ID, "2026-07-01", 3600); err != nil {
		t.Fatalf("usage: %v", err)
	}
	if err := st.PutCloudCost(ctx, []string{"2026-07-01"}, []CloudCostRow{
		{Day: "2026-07-01", MembershipID: mem.ID, TenantID: tn.ID, Service: "Amazon EC2",
			Unblended: 4200, Currency: "USD"},
	}); err != nil {
		t.Fatalf("cloud cost: %v", err)
	}
	if err := st.InsertAudit(ctx, AuditLog{
		ID: newID(), TenantID: tn.ID, ActorKind: "user", ActorID: admin.ID,
		Action: "membership.remove", Target: "leaver-acme-co-jp", At: nowTS(),
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

// 在席中の人の行は消せない。ADR 0045 決定 13-2 と同じ線で、理由も同じ——管理画面の
// 1 クリック隣に取り消せない操作を置かないため。
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

// workspace 行が残っているうちは消せない。消してしまうと home・EBS・EFS が
// 「DB から誰も指していない」まま課金され続ける——destroyWorkspace が塞いだ穴そのもの。
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

// 通ったときに何が消えて何が残るか——この操作の全部。
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

	// 残るもの 1: 稼働実績。
	usage, err := st.ListUsage(ctx, tn.ID, "2026-07-01", "2026-07-01")
	if err != nil || len(usage) == 0 {
		t.Errorf("occupancy history was deleted: %+v %v", usage, err)
	}
	// 残るもの 2: 費用。過去月の請求合計が後から変わってはいけない。
	cost, err := st.ListCloudCost(ctx, "", memID, "2026-07-01", "2026-07-01")
	if err != nil || len(cost) == 0 || cost[0].Unblended != 4200 {
		t.Errorf("cost history changed after the fact: %+v %v", cost, err)
	}
	// 残るもの 3: 監査。除名した記録を、除名の後始末で消せてはいけない。
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

// 予約メンバーシップ（種と probe）は消させない。次のベイクで作り直されるだけで、
// 焼いている最中ならスロットを掴んだまま宙に浮く。
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
	seed, _ := st.UpsertIdentity(ctx, "", goldenSeedKey, "")
	seedMem, err := st.EnsureMembership(ctx, seed.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	if err := st.SetMembershipStatus(ctx, seedMem.ID, "inactive"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	w := callDeleteMembership(mgr, goldenTenantSlug, goldenSeedKey)
	if w.Code != http.StatusConflict {
		t.Fatalf("delete of a reserved membership = %d %s, want 409", w.Code, w.Body.String())
	}
	if _, ok, _ := st.GetMembershipByID(ctx, seedMem.ID); ok {
		return // inactive なので GetMembershipByID は ok=false。行の存在は下で確かめる。
	}
	members, err := st.ListRemovedMembersByTenant(ctx, tn.ID)
	if err != nil || len(members) != 1 {
		t.Errorf("the reserved membership was deleted anyway: %+v %v", members, err)
	}
}
