package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// テナント削除（docs/log/61 §61.18）。**空のものだけ**消せる——DB の行は、クラウドや
// ディスクに残った資源に対する唯一の手掛かりなので、最初に消えてはいけない。

func callDeleteTenant(mgr *manager, slug string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodDelete, "/api/admin/tenants/"+slug, nil)
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	r.SetPathValue("slug", slug)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).deleteTenant(w, r, Identity{ID: "I-boss", Role: "super_admin"})
	return w
}

func tenantStillThere(t *testing.T, st *sqlStore, slug string) {
	t.Helper()
	if _, ok, err := st.GetTenantBySlug(context.Background(), slug); err != nil || !ok {
		t.Errorf("the tenant was deleted despite the refusal (ok=%v err=%v)", ok, err)
	}
}

// 在席中のメンバーが 1 人でも残っていれば拒否。これは退職処理の道具ではない。
func TestDeleteTenantRefusesWhileAMemberIsOnTheRoster(t *testing.T) {
	st, mgr, _, _ := cleanupFixture(t)
	w := callDeleteTenant(mgr, "sales")
	if w.Code != http.StatusConflict {
		t.Fatalf("delete with members = %d %s, want 409", w.Code, w.Body.String())
	}
	tenantStillThere(t, st, "sales")
}

// workspace 行が残っていれば拒否。テナントを消しても home・EBS・EFS は消えず、
// 消えるのは「それを指していた唯一の行」の方になる。
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

// 内部 git リポジトリが残っていれば拒否。bare とその LFS はディスクに残る。
// ⚠️ しかも順序の罠がある: リポジトリ削除 API は withMembership ゲートなので、
// 最後のメンバーを外した後は誰も消せない。だから拒否のメッセージがその順序を言う。
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
	if err := st.CreateGitRepo(ctx, GitRepo{
		ID: newID(), TenantID: tn.ID, Name: "tools", DefaultBranch: "main",
		CreatedBy: "boss-acme-co-jp", CreatedAt: nowTS(),
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

// 予約テナントと既定テナントは、消しても次の起動やベイクで作り直されるだけ。
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
	for _, slug := range []string{goldenTenantSlug, defaultTenantSlug} {
		if w := callDeleteTenant(mgr, slug); w.Code != http.StatusConflict {
			t.Errorf("delete of %s = %d %s, want 409", slug, w.Code, w.Body.String())
		}
		tenantStillThere(t, st, slug)
	}
}

// 空になっていれば消える。残っていた inactive な membership も一緒に消え、
// 履歴（監査・稼働時間・費用）は残る。
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
	// テナント設定の行も一緒に消えること。
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

	// 残るもの: 稼働実績・費用・監査。テナントが消えても請求の過去は変わらない。
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
		// ⚠️ 監査ビューは tenant_id → slug を ListTenants で引く。消えたテナントの行は
		// テナント欄が空になるので、名前は行の中（Target / Detail）に入っていること。
		if row.Action == "tenant.delete" && row.Target == "sales" {
			logged = true
		}
	}
	if !logged {
		t.Errorf("the deletion is not in the audit log with its slug: %+v", rows)
	}
}

func mustMembers(t *testing.T, st *sqlStore, tenantID string) []MemberInfo {
	t.Helper()
	ms, err := st.ListMembersByTenant(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("members: %v", err)
	}
	return ms
}
