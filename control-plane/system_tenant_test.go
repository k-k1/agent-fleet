package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// 予約テナントは管理画面のテナント一覧に出さない（docs/log/61 §61.18）。焼き直しのたびに
// 使い回される入れ物であって、人のテナントではない。
func TestListTenantsHidesTheSystemTenant(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	if _, err := st.CreateTenant(ctx, "sales", "営業部"); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	if _, err := st.CreateTenant(ctx, goldenTenantSlug, goldenTenantName); err != nil {
		t.Fatalf("golden tenant: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/admin/tenants", nil)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).listTenants(w, r, store.Identity{ID: "I-1", Role: "super_admin"})
	if w.Code != http.StatusOK {
		t.Fatalf("listTenants = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), goldenTenantSlug) {
		t.Errorf("the system tenant is on the admin roster: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "sales") {
		t.Errorf("an ordinary tenant went missing with it: %s", w.Body.String())
	}
}

// ★ 落とすのは API 層だけ。store.ListTenants を絞ると、監査ビューの tenant_id → slug
// 解決（audit.go）と費用ポーラーの membership → tenant 解決（cloudcost.go）が同時に
// 壊れる — どちらも「テナントの分からない行」を作りはじめ、症状は管理画面ではなく
// 監査と請求に出る。
func TestStoreListTenantsStillReturnsTheSystemTenant(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	if _, err := st.CreateTenant(ctx, goldenTenantSlug, goldenTenantName); err != nil {
		t.Fatalf("golden tenant: %v", err)
	}
	ts, err := st.ListTenants(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, tn := range ts {
		if tn.Slug == goldenTenantSlug {
			return
		}
	}
	t.Fatalf("the store hid the system tenant too; audit and cost resolution depend on it: %+v", ts)
}

// systemMembershipIDs は active も inactive も拾う。予約メンバーシップは焼き直しの合間に
// inactive になっていることがあり、そこで漏らすと「たまたま今アクティブでない golden の
// 費用だけが人の列に出る」という、いちばん気づきにくい形になる。
func TestSystemMembershipIDsIncludeTheDeactivatedOnes(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	tn, err := st.CreateTenant(ctx, goldenTenantSlug, goldenTenantName)
	if err != nil {
		t.Fatalf("golden tenant: %v", err)
	}
	seed, _ := st.UpsertIdentity(ctx, "", goldenSeedKey, "")
	probe, _ := st.UpsertIdentity(ctx, "", goldenProbeKey, "")
	seedMem, err := st.EnsureMembership(ctx, seed.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	probeMem, err := st.EnsureMembership(ctx, probe.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("probe membership: %v", err)
	}
	if err := st.SetMembershipStatus(ctx, probeMem.ID, "inactive"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	got, err := mgr.systemMembershipIDs(ctx)
	if err != nil {
		t.Fatalf("systemMembershipIDs: %v", err)
	}
	if !got[seedMem.ID] || !got[probeMem.ID] {
		t.Errorf("system memberships = %v, want both the seed and the deactivated probe", got)
	}
}

// 予約テナントが無いデプロイ（ecs-ec2 以外はこれが常態）では空集合。
func TestSystemMembershipIDsAreEmptyWithoutTheReservedTenant(t *testing.T) {
	st := p3Store(t)
	mgr := p3Manager(t, st)
	got, err := mgr.systemMembershipIDs(context.Background())
	if err != nil {
		t.Fatalf("systemMembershipIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("system memberships = %v, want none", got)
	}
}

// 稼働時間の面からも予約テナントは消す。テナント一覧から消しておきながらここに
// 「af-golden / af-golden-seed」の行が残ると、隠した意味が無くなる。
func TestUsageDropsTheSystemTenantsRows(t *testing.T) {
	rows := []store.UsageRow{
		{TenantSlug: "sales", UserKey: "yamada", Day: "2026-08-21", RunningSecs: 60},
		{TenantSlug: goldenTenantSlug, UserKey: goldenSeedKey, Day: "2026-08-21", RunningSecs: 1200},
	}
	got := withoutSystemTenants(rows)
	if len(got) != 1 || got[0].TenantSlug != "sales" {
		t.Errorf("usage rows = %+v, want only the ordinary tenant's", got)
	}
	if rows[1].TenantSlug != goldenTenantSlug {
		t.Error("the caller's slice was rewritten in place; the ledger must stay as it is")
	}
}

// 予約メンバーシップの費用は SHARED へ畳む。⚠️ 畳んだ結果 (day, service) が衝突したら
// **足す**こと — PutCloudCost は置き換えなので、足さずに 2 行返すと後の行が前の行の
// 金額を消す。
func TestFoldSystemMembershipsSumsIntoTheSharedBucket(t *testing.T) {
	rows := []store.CloudCostRow{
		{Day: "2026-08-21", MembershipID: "", TenantID: "", Service: "Amazon EC2", Unblended: 100, Amortized: 100},
		{Day: "2026-08-21", MembershipID: "M-seed", TenantID: "T-golden", Service: "Amazon EC2", Unblended: 25, Amortized: 25, Estimated: true},
		{Day: "2026-08-21", MembershipID: "M-real", TenantID: "T-sales", Service: "Amazon EC2", Unblended: 7, Amortized: 7},
	}
	got := foldSystemMemberships(rows, map[string]bool{"M-seed": true})
	if len(got) != 2 {
		t.Fatalf("rows = %+v, want the shared row and the real member's row", got)
	}
	var shared, real store.CloudCostRow
	for _, r := range got {
		if r.MembershipID == "" {
			shared = r
		} else {
			real = r
		}
	}
	if shared.Unblended != 125 || shared.Amortized != 125 {
		t.Errorf("shared = %d/%d micro, want 125/125 (the seed's money was overwritten, not added)",
			shared.Unblended, shared.Amortized)
	}
	if !shared.Estimated {
		t.Error("one half was still estimated, so the sum is too")
	}
	if shared.TenantID != "" {
		t.Errorf("shared row carries tenant %q; the shared bucket has no tenant", shared.TenantID)
	}
	if real.MembershipID != "M-real" || real.Unblended != 7 {
		t.Errorf("a real member's row was disturbed: %+v", real)
	}
}

// 予約テナントの無いデプロイでは 1 行も動かさない。
func TestFoldSystemMembershipsIsANoOpWithoutReservedMemberships(t *testing.T) {
	rows := []store.CloudCostRow{
		{Day: "2026-08-21", MembershipID: "M-real", Service: "Amazon EC2", Unblended: 7},
	}
	got := foldSystemMemberships(rows, nil)
	if len(got) != 1 || got[0].MembershipID != "M-real" {
		t.Errorf("rows = %+v, want them untouched", got)
	}
}

// 費用画面の読み側でも畳む（取り込みの窓より古い行が残っているため）。
func TestCloudCostFoldsAnOldSystemRowIntoShared(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	tn, err := st.CreateTenant(ctx, goldenTenantSlug, goldenTenantName)
	if err != nil {
		t.Fatalf("golden tenant: %v", err)
	}
	seed, _ := st.UpsertIdentity(ctx, "", goldenSeedKey, "")
	seedMem, err := st.EnsureMembership(ctx, seed.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	day := "2026-07-01"
	if err := st.PutCloudCost(ctx, []string{day}, []store.CloudCostRow{
		{Day: day, MembershipID: "", Service: "Amazon Route 53", Unblended: 50, Currency: "USD"},
		{Day: day, MembershipID: seedMem.ID, TenantID: tn.ID, Service: "Amazon EC2", Unblended: 20, Currency: "USD"},
	}); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/admin/cloud-cost?from="+day+"&to="+day, nil)
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	if _, err := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("admin identity: %v", err)
	}
	w := httptest.NewRecorder()
	newAdminAPI(mgr).cloudCost(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("cloudCost = %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Members         []map[string]any `json:"members"`
		AttributedMicro int64            `json:"attributed_micro"`
		SharedMicro     int64            `json:"shared_micro"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Members) != 0 {
		t.Errorf("the golden seed is listed as a member: %+v", resp.Members)
	}
	if resp.AttributedMicro != 0 {
		t.Errorf("attributed = %d, want 0 — none of this is anybody's", resp.AttributedMicro)
	}
	if resp.SharedMicro != 70 {
		t.Errorf("shared = %d, want 70 (50 untagged + the seed's 20)", resp.SharedMicro)
	}
}
