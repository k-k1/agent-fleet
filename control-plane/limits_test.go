package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestParseLimitsTerminalHistoryRetention(t *testing.T) {
	got := parseLimits(`{"terminal_history_retention_days":7}`)
	if got.TerminalHistoryRetentionDays != 7 {
		t.Fatalf("retention = %d; want 7", got.TerminalHistoryRetentionDays)
	}
	if zero := parseLimits("").TerminalHistoryRetentionDays; zero != 0 {
		t.Fatalf("default retention = %d; want 0", zero)
	}
}

// --- テナント上限とスロットプールの突き合わせ（docs/log/64 §64.35 / ADR 0045 決定 25）---
//
// 検証が無かったせいで、**プール上限を超える配分が黙って保存できた**。超過は「枠内なのに
// 起動に失敗する」か「他テナントのスロットを奪う」という、設定画面から最も遠い形でしか
// 表に出ない。

// poolFactory はスロット上限を持つランタイムの替え玉。プールを持たない配備で検査が
// **空振りで通る**のではなく**存在しない**ことを確かめたいので、上限を持たない方も要る。
type poolFactory struct{ max int }

func (f *poolFactory) New(runtime.Workspace, string, []string) runtime.Runtime { return nil }
func (f *poolFactory) MaxSlots() int                                           { return f.max }

type poollessFactory struct{}

func (f *poollessFactory) New(runtime.Workspace, string, []string) runtime.Runtime { return nil }

func budgetFixture(t *testing.T, max int, quotas map[string]int) (*store.SQL, *manager) {
	t.Helper()
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	mgr.rtFactory = &poolFactory{max: max}
	for slug, n := range quotas {
		tn, err := st.CreateTenant(ctx, slug, slug)
		if err != nil {
			t.Fatalf("tenant %s: %v", slug, err)
		}
		if err := st.SetTenantLimits(ctx, tn.ID, `{"max_workspaces":`+strconv.Itoa(n)+`}`); err != nil {
			t.Fatalf("limits %s: %v", slug, err)
		}
	}
	return st, mgr
}

// golden の焼き直しは種と探針で 2 枠を同時に要る（bakeReservedSlots）。全部配ってしまうと
// **二度と焼けない**——症状は「新しいメンバーの初回起動が遅い」で、数週間後に気づく類の
// 失敗なので、容量はそこを引いた数で見る。
func TestPoolBudgetLeavesRoomForABake(t *testing.T) {
	_, mgr := budgetFixture(t, 30, map[string]int{"acme": 20, "beta": 8})
	b, ok, err := mgr.poolBudget(context.Background(), "", 0)
	if err != nil || !ok {
		t.Fatalf("poolBudget: ok=%v err=%v", ok, err)
	}
	if b.Capacity != 28 || b.Allocated != 28 {
		t.Fatalf("capacity/allocated = %d/%d, want 28/28", b.Capacity, b.Allocated)
	}
	if b.Over || !b.OK() {
		t.Error("28 allocated against a capacity of 28 is exactly full, not over")
	}

	_, mgr = budgetFixture(t, 30, map[string]int{"acme": 20, "beta": 9})
	b, _, _ = mgr.poolBudget(context.Background(), "", 0)
	if !b.Over {
		t.Errorf("29 allocated against a capacity of 28 must be over: %+v", b)
	}
}

// ⚠️ 0 は「無制限」であって「0 台」ではない。1 テナントでもそれが居れば、**合計はもう
// 上限を縛らない**。「超過」とは別の問題なので、別の言い方で出す必要がある。
func TestPoolBudgetTreatsZeroAsUnlimitedNotZero(t *testing.T) {
	_, mgr := budgetFixture(t, 30, map[string]int{"acme": 5, "beta": 0})
	b, _, _ := mgr.poolBudget(context.Background(), "", 0)
	if b.Allocated != 5 {
		t.Errorf("allocated = %d, want 5 — an unlimited tenant contributes no number", b.Allocated)
	}
	if len(b.Unbounded) != 1 || b.Unbounded[0] != "beta" {
		t.Fatalf("unbounded = %v, want [beta]", b.Unbounded)
	}
	if b.Over {
		t.Error("there is no sum to be over when a tenant has no cap")
	}
	if b.OK() {
		t.Error("a deployment whose pool nothing bounds is not OK")
	}
}

// 停止中のテナントは何も動かさない。数えると、**止まっているテナントのために動いている
// テナントを削る**ことになる。
func TestPoolBudgetSkipsSuspendedTenants(t *testing.T) {
	ctx := context.Background()
	st, mgr := budgetFixture(t, 10, map[string]int{"acme": 6, "gone": 6})
	tn, _, _ := st.GetTenantBySlug(ctx, "gone")
	// テナントを止める API はまだ無いので、行を直接落とす。列は存在し ListTenants は
	// 状態で絞らずに返すので、この分岐は「まだ誰も通らない」であって「無い」ではない。
	if _, err := st.DB().ExecContext(ctx, `UPDATE tenant SET status='suspended' WHERE id=?`, tn.ID); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	b, _, _ := mgr.poolBudget(ctx, "", 0)
	if b.Allocated != 6 || b.Over {
		t.Fatalf("budget = %+v, want only the active tenant counted", b)
	}
}

// プールを持たないランタイムには比べる相手が無い。ok=false は「大丈夫」ではなく
// 「そういう問いが無い」——空振りで OK を返すと、Fargate 配備で存在しない保証を名乗る。
func TestPoolBudgetIsAbsentWithoutAPool(t *testing.T) {
	st := p3Store(t)
	mgr := p3Manager(t, st)
	mgr.rtFactory = &poollessFactory{}
	if _, ok, err := mgr.poolBudget(context.Background(), "", 0); ok || err != nil {
		t.Fatalf("poolBudget on a poolless runtime = ok:%v err:%v, want ok:false", ok, err)
	}
}

// 保存前の値で見られること。そうでないと、いま打った数字ではなく**それが置き換えた方**に
// ついての警告が返る。
func TestPoolBudgetCanPreviewAnUnsavedValue(t *testing.T) {
	ctx := context.Background()
	st, mgr := budgetFixture(t, 10, map[string]int{"acme": 4, "beta": 4})
	tn, _, _ := st.GetTenantBySlug(ctx, "acme")
	b, _, _ := mgr.poolBudget(ctx, tn.ID, 50)
	if !b.Over || b.Allocated != 54 {
		t.Fatalf("budget = %+v, want the unsaved 50 counted (54 > 8)", b)
	}
}

// --- PUT /api/admin/tenants/{slug}/limits の検証 ---

func putLimits(mgr *manager, slug string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, "/api/admin/tenants/"+slug+"/limits", strings.NewReader(body))
	r.SetPathValue("slug", slug)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).setTenantLimits(w, r, store.Identity{ID: "I-boss", Role: "super_admin"})
	return w
}

// 0 = 無制限なので、負の数は「小さい上限」ではなく**誰も満たせない上限**である。
// max_workspaces=-1 は `running >= limit` を誰も起動する前に真にし、そのテナントは
// 二度と Workspace を開けない。数値欄の打ち間違いを、メンバーの起動失敗で気づくもの
// にしてはいけない。
func TestSetTenantLimitsRejectsNegativeQuotas(t *testing.T) {
	_, mgr := budgetFixture(t, 30, map[string]int{"acme": 4})
	w := putLimits(mgr, "acme", `{"max_workspaces":-1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT with max_workspaces=-1 = %d %s, want 400", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "max_workspaces") {
		t.Errorf("the error must name the field: %s", w.Body.String())
	}
	// そして保存されていないこと。
	tn, _, _ := mgr.store.GetTenantBySlug(context.Background(), "acme")
	if got := parseLimits(tn.Limits).MaxWorkspaces; got != 4 {
		t.Errorf("max_workspaces = %d, want the old 4 — a rejected PUT must not write", got)
	}
}

// ⚠️ 超過は**拒否しない**。これはこのエンドポイントが守れる不変条件ではない
// （Ec2MaxSlots は CP の env で、下げるときに API 呼び出しは起きない）し、同時に
// ピークが来ないテナント同士のオーバーサブスクライブは正当な運用でもある。何より、
// 既に超過している配備が**この画面の他の欄すべて**を編集できなくなる。
func TestSetTenantLimitsWarnsButSavesAnOverAllocation(t *testing.T) {
	ctx := context.Background()
	_, mgr := budgetFixture(t, 10, map[string]int{"acme": 4, "beta": 4})
	w := putLimits(mgr, "acme", `{"max_workspaces":50}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s, want 200 — over-subscription is advice, not a gate", w.Code, w.Body.String())
	}
	tn, _, _ := mgr.store.GetTenantBySlug(ctx, "acme")
	if got := parseLimits(tn.Limits).MaxWorkspaces; got != 50 {
		t.Fatalf("max_workspaces = %d, want the requested 50 saved", got)
	}
	var got struct {
		Budget *poolBudget `json:"pool_budget"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Budget == nil || !got.Budget.Over {
		t.Fatalf("the response has to carry the warning, got %s", w.Body.String())
	}
	if got.Budget.Allocated != 54 || got.Budget.Capacity != 8 {
		t.Errorf("budget = %+v, want 54 allocated against a capacity of 8", *got.Budget)
	}
}

// 収まっているときは何も付けない。毎回の保存に「大丈夫です」が付くと、本当に出たときに
// 読まれなくなる。
func TestSetTenantLimitsSaysNothingWhenTheBudgetFits(t *testing.T) {
	_, mgr := budgetFixture(t, 30, map[string]int{"acme": 4, "beta": 4})
	w := putLimits(mgr, "acme", `{"max_workspaces":6}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "pool_budget") {
		t.Errorf("a budget that fits is not news: %s", w.Body.String())
	}
}
