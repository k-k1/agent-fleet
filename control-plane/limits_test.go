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

// --- Tenant quotas against the slot pool (docs/log/64 §64.35 / ADR 0045 decision 25) ---
//
// Without validation, an allocation beyond the pool ceiling saves silently. The excess then
// surfaces only in the forms furthest from the settings screen: a launch that fails while
// apparently within quota, or a workspace taking another tenant's slot.

// poolFactory stands in for a runtime that has a slot ceiling. The pool-less counterpart is
// needed too, so a deployment without a pool can be shown to have no such check at all
// rather than one that passes vacuously.
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

// Re-baking the golden needs two slots at the same time, for the seed and the probe
// (bakeReservedSlots). Hand every slot out and no bake can ever run again; the symptom is
// "a new member's first launch is slow", noticed weeks later, so capacity is measured with
// those two subtracted.
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

// 0 means unlimited, not zero workspaces. A single such tenant is enough for the sum to stop
// bounding anything, which is a different problem from being over and has to be reported in
// a different way.
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

// A suspended tenant runs nothing. Counting it would shrink the running tenants' share for
// the sake of a stopped one.
func TestPoolBudgetSkipsSuspendedTenants(t *testing.T) {
	ctx := context.Background()
	st, mgr := budgetFixture(t, 10, map[string]int{"acme": 6, "gone": 6})
	tn, _, _ := st.GetTenantBySlug(ctx, "gone")
	// There is no API to suspend a tenant yet, so the row is written directly. The column
	// exists and ListTenants does not filter on status, so this branch is "nothing reaches
	// it yet", not "it does not exist".
	if _, err := st.DB().ExecContext(ctx, `UPDATE tenant SET status='suspended' WHERE id=?`, tn.ID); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	b, _, _ := mgr.poolBudget(ctx, "", 0)
	if b.Allocated != 6 || b.Over {
		t.Fatalf("budget = %+v, want only the active tenant counted", b)
	}
}

// A runtime without a pool has nothing to compare against. ok=false means "the question does
// not apply", not "everything fits" — answering OK vacuously would claim a guarantee that a
// Fargate deployment does not have.
func TestPoolBudgetIsAbsentWithoutAPool(t *testing.T) {
	st := p3Store(t)
	mgr := p3Manager(t, st)
	mgr.rtFactory = &poollessFactory{}
	if _, ok, err := mgr.poolBudget(context.Background(), "", 0); ok || err != nil {
		t.Fatalf("poolBudget on a poolless runtime = ok:%v err:%v, want ok:false", ok, err)
	}
}

// The value has to be previewable before it is saved, or the warning describes the number it
// replaced instead of the one just typed.
func TestPoolBudgetCanPreviewAnUnsavedValue(t *testing.T) {
	ctx := context.Background()
	st, mgr := budgetFixture(t, 10, map[string]int{"acme": 4, "beta": 4})
	tn, _, _ := st.GetTenantBySlug(ctx, "acme")
	b, _, _ := mgr.poolBudget(ctx, tn.ID, 50)
	if !b.Over || b.Allocated != 54 {
		t.Fatalf("budget = %+v, want the unsaved 50 counted (54 > 8)", b)
	}
}

// --- Validation of PUT /api/admin/tenants/{slug}/limits ---

func putLimits(mgr *manager, slug string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, "/api/admin/tenants/"+slug+"/limits", strings.NewReader(body))
	r.SetPathValue("slug", slug)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).setTenantLimits(w, r, store.Identity{ID: "I-boss", Role: "super_admin"})
	return w
}

// 0 = unlimited, so a negative number is not a small quota but one nobody can satisfy:
// max_workspaces=-1 makes `running >= limit` true before anyone has started anything, and
// the tenant can never open a workspace again. A typo in a number field must not be
// discovered through a member's failed launch.
func TestSetTenantLimitsRejectsNegativeQuotas(t *testing.T) {
	_, mgr := budgetFixture(t, 30, map[string]int{"acme": 4})
	w := putLimits(mgr, "acme", `{"max_workspaces":-1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT with max_workspaces=-1 = %d %s, want 400", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "max_workspaces") {
		t.Errorf("the error must name the field: %s", w.Body.String())
	}
	// And nothing was written.
	tn, _, _ := mgr.store.GetTenantBySlug(context.Background(), "acme")
	if got := parseLimits(tn.Limits).MaxWorkspaces; got != 4 {
		t.Errorf("max_workspaces = %d, want the old 4 — a rejected PUT must not write", got)
	}
}

// An over-allocation is not rejected. It is not an invariant this endpoint can hold
// (Ec2MaxSlots is CP env, and lowering it makes no API call), over-subscribing tenants whose
// peaks never coincide is legitimate operation, and above all a deployment that is already
// over would lose the ability to edit every other field on this screen.
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
		Budget *runtime.PoolBudget `json:"pool_budget"`
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

// Nothing is attached when it fits: an "all good" on every save stops being read by the time
// there is something to read.
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
