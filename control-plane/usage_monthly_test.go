package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func freezeMonths(t *testing.T, day string) {
	t.Helper()
	at, err := time.Parse(usageDayFmt, day)
	if err != nil {
		t.Fatal(err)
	}
	old := monthsNow
	monthsNow = func() time.Time { return at.Add(10 * time.Hour) }
	t.Cleanup(func() { monthsNow = old })
}

func TestMonthSpanCrossesYearAndFindsLeapFebruary(t *testing.T) {
	sp := newMonthSpan(3, time.Date(2027, 1, 15, 3, 0, 0, 0, time.UTC))
	if !reflect.DeepEqual(sp.Keys, []string{"2026-11", "2026-12", "2027-01"}) || sp.From != "2026-11-01" || sp.To != "2027-01-15" || sp.Current != "2027-01" {
		t.Errorf("span = %+v", sp)
	}
	if monthEnd("2028-02") != "2028-02-29" || monthEnd("2027-02") != "2027-02-28" || monthEnd("2026-12") != "2026-12-31" {
		t.Error("month end wrong")
	}
}

// First and last day of a month go to their own month, a month without rows is a real
// zero (present in the list), and only the running month is partial.
func TestMonthlyUsageBoundaries(t *testing.T) {
	sp := newMonthSpan(4, time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC))
	rows := []store.UsageRow{
		{Day: "2026-05-31", RunningSecs: 3600}, // before the window's first month
		{Day: "2026-06-01", RunningSecs: 7200},
		{Day: "2026-06-30", RunningSecs: 3600},
		{Day: "2026-08-01", RunningSecs: 1800},
	}
	got := monthlyUsage(rows, sp)
	// window starts 2026-05 so 05-31 counts in May
	if len(got) != 4 || got[0].Month != "2026-05" || got[0].RunningSecs != 3600 {
		t.Fatalf("got %+v", got)
	}
	if got[1].RunningSecs != 10800 || got[1].RunningHours != 3 {
		t.Errorf("June = %+v", got[1])
	}
	if got[2].Month != "2026-07" || got[2].RunningSecs != 0 || got[2].Partial {
		t.Errorf("empty July = %+v", got[2])
	}
	if *got[2].ChangeSecs != -10800 || *got[2].ChangePct != -100 {
		t.Errorf("July change = %v %v", *got[2].ChangeSecs, *got[2].ChangePct)
	}
	if got[3].ChangePct != nil || !got[3].Partial || got[0].ChangeSecs != nil {
		t.Errorf("Aug (previous is zero => no pct) = %+v, first = %+v", got[3], got[0])
	}
}

func getJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func monthsOf(t *testing.T, got map[string]any) []map[string]any {
	t.Helper()
	raw, ok := got["monthly"].([]any)
	if !ok {
		t.Fatalf("no monthly in %v", got)
	}
	var out []map[string]any
	for _, m := range raw {
		out = append(out, m.(map[string]any))
	}
	return out
}

func TestAdminUsageMonthlyIsTenantScoped(t *testing.T) {
	freezeMonths(t, "2026-08-20")
	st, mgr, tn, mem := cloudCostFixture(t)
	ctx := context.Background()
	other, _ := st.CreateTenant(ctx, "ops", "運用")
	oi, _ := st.UpsertIdentity(ctx, "o@x.co.jp", "o-x-co-jp", "")
	om, _ := st.EnsureMembership(ctx, oi.ID, other.ID, "member")
	_ = st.AddUsage(ctx, mem.ID, tn.ID, "2026-07-31", 3600)
	_ = st.AddUsage(ctx, mem.ID, tn.ID, "2026-08-01", 7200)
	_ = st.AddUsage(ctx, om.ID, other.ID, "2026-08-02", 36000)

	r := httptest.NewRequest(http.MethodGet, "/api/admin/usage?tenant=sales&months=2", nil)
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	w := httptest.NewRecorder()
	newAdminAPI(mgr).usage(w, r)
	m := monthsOf(t, getJSON(t, w))
	if m[0]["running_secs"].(float64) != 3600 || m[1]["running_secs"].(float64) != 7200 {
		t.Errorf("tenant admin saw %v — must be only their own tenant, cut at the UTC month", m)
	}
	if m[1]["change_pct"].(float64) != 100 || m[1]["partial"] != true {
		t.Errorf("Aug = %v", m[1])
	}

	// Without the tenant param a tenant admin is refused, exactly as before.
	r = httptest.NewRequest(http.MethodGet, "/api/admin/usage?months=2", nil)
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	w = httptest.NewRecorder()
	newAdminAPI(mgr).usage(w, r)
	if w.Code == http.StatusOK {
		t.Error("deployment-wide months must stay super_admin only")
	}

	// Positive control: super_admin sees both tenants.
	_, _ = st.UpsertIdentity(ctx, "root@acme.co.jp", "root-acme-co-jp", "super_admin")
	r = httptest.NewRequest(http.MethodGet, "/api/admin/usage?months=2", nil)
	r.Header.Set("X-Forwarded-Email", "root@acme.co.jp")
	w = httptest.NewRecorder()
	newAdminAPI(mgr).usage(w, r)
	if m = monthsOf(t, getJSON(t, w)); m[1]["running_secs"].(float64) != 43200 {
		t.Errorf("super admin Aug = %v", m[1])
	}

	// Bad value is a 400.
	r = httptest.NewRequest(http.MethodGet, "/api/admin/usage?tenant=sales&months=99", nil)
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	w = httptest.NewRecorder()
	newAdminAPI(mgr).usage(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("months=99 status %d", w.Code)
	}
}

func TestMyUsageMonthlyIsOnlyTheCallers(t *testing.T) {
	freezeMonths(t, "2026-08-20")
	st, mgr, tn, mem := cloudCostFixture(t)
	ctx := context.Background()
	oi, _ := st.UpsertIdentity(ctx, "o@x.co.jp", "o-x-co-jp", "")
	om, _ := st.EnsureMembership(ctx, oi.ID, tn.ID, "member")
	_ = st.AddUsage(ctx, mem.ID, tn.ID, "2026-08-01", 3600)
	_ = st.AddUsage(ctx, om.ID, tn.ID, "2026-08-01", 99999)
	r := httptest.NewRequest(http.MethodGet, "/api/usage/me/monthly?months=2", nil)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).myUsageMonthly(w, r, store.Identity{}, store.MembershipView{MembershipID: mem.ID, TenantID: tn.ID})
	got := getJSON(t, w)
	m := monthsOf(t, got)
	if m[1]["running_secs"].(float64) != 3600 {
		t.Errorf("Aug = %v, want only the caller's 3600", m[1])
	}
	if _, ok := got["totals"]; ok {
		t.Error("no deployment totals in a member's own response")
	}
}

func seedMonthlyCost(t *testing.T, st *store.SQL, tn store.Tenant, mem store.Membership) {
	t.Helper()
	ctx := context.Background()
	other, _ := st.CreateTenant(ctx, "ops", "運用")
	oi, _ := st.UpsertIdentity(ctx, "o@x.co.jp", "o-x-co-jp", "")
	om, _ := st.EnsureMembership(ctx, oi.ID, other.ID, "member")
	put := func(day string, row store.CloudCostRow) {
		row.Day, row.Currency = day, "USD"
		if err := st.PutCloudCost(ctx, []string{day}, []store.CloudCostRow{row}); err != nil {
			t.Fatal(err)
		}
	}
	// 2026-08-17 (member 2.0 + shared 9.0) comes from the fixture; add around it.
	put("2026-07-10", store.CloudCostRow{MembershipID: mem.ID, TenantID: tn.ID, Service: "EC2", Unblended: 1_000_000})
	put("2026-07-31", store.CloudCostRow{MembershipID: mem.ID, TenantID: tn.ID, Service: "EC2", Unblended: 500_000})
	put("2026-08-01", store.CloudCostRow{MembershipID: mem.ID, TenantID: tn.ID, Service: "EC2", Unblended: 250_000, Estimated: true})
	put("2026-08-02", store.CloudCostRow{MembershipID: om.ID, TenantID: other.ID, Service: "EC2", Unblended: 7_000_000})
}

func TestCloudCostMonthlyCoverageAndScope(t *testing.T) {
	freezeMonths(t, "2026-08-20")
	st, mgr, tn, mem := cloudCostFixture(t)
	seedMonthlyCost(t, st, tn, mem)

	r := httptest.NewRequest(http.MethodGet, "/api/admin/cloud-cost?tenant=sales&months=3", nil)
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	w := httptest.NewRecorder()
	newAdminAPI(mgr).cloudCost(w, r)
	m := monthsOf(t, getJSON(t, w))
	if len(m) != 3 {
		t.Fatalf("months = %v", m)
	}
	// June predates the first covered day (07-10): unknown, not zero, and no change shown.
	if m[0]["no_coverage"] != true || m[1]["change_micro"] != nil {
		t.Errorf("June/July = %v / %v", m[0], m[1])
	}
	// July starts before coverage => partial; 1.5 only, not the other tenant's money.
	if m[1]["unblended_micro"].(float64) != 1_500_000 || m[1]["partial"] != true || m[1]["estimated"] != false {
		t.Errorf("July = %v", m[1])
	}
	if m[2]["unblended_micro"].(float64) != 2_250_000 || m[2]["estimated"] != true || m[2]["partial"] != true {
		t.Errorf("Aug = %v (tenant admin must not see ops' 7.0 or the shared 9.0)", m[2])
	}
	if m[2]["change_micro"].(float64) != 750_000 || m[2]["change_pct"].(float64) != 50 {
		t.Errorf("Aug change = %v", m[2])
	}
	for _, mm := range m {
		if _, ok := mm["shared_micro"]; ok {
			t.Error("shared_micro leaked to a tenant admin")
		}
	}

	// Positive control: super_admin gets every tenant and the shared bucket.
	_, _ = st.UpsertIdentity(context.Background(), "root@acme.co.jp", "root-acme-co-jp", "super_admin")
	r = httptest.NewRequest(http.MethodGet, "/api/admin/cloud-cost?months=3", nil)
	r.Header.Set("X-Forwarded-Email", "root@acme.co.jp")
	w = httptest.NewRecorder()
	newAdminAPI(mgr).cloudCost(w, r)
	m = monthsOf(t, getJSON(t, w))
	if m[2]["unblended_micro"].(float64) != 9_250_000 || m[2]["shared_micro"].(float64) != 9_000_000 {
		t.Errorf("super admin Aug = %v", m[2])
	}
}

func TestMyCloudCostMonthlyIsOnlyTheCallers(t *testing.T) {
	freezeMonths(t, "2026-08-20")
	st, mgr, tn, mem := cloudCostFixture(t)
	seedMonthlyCost(t, st, tn, mem)
	r := httptest.NewRequest(http.MethodGet, "/api/cost/me?months=2", nil)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).myCloudCost(w, r, store.Identity{}, store.MembershipView{MembershipID: mem.ID, TenantID: tn.ID})
	m := monthsOf(t, getJSON(t, w))
	if m[1]["unblended_micro"].(float64) != 2_250_000 {
		t.Errorf("Aug = %v", m[1])
	}
	if _, ok := m[1]["shared_micro"]; ok {
		t.Error("members never get a shared figure")
	}
}

func TestMonthlyCostWithNoDataAtAll(t *testing.T) {
	sp := newMonthSpan(2, time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC))
	got := monthlyCost(nil, sp, "", false, nil)
	if !got[0].NoCoverage || !got[1].NoCoverage || got[1].ChangeMicro != nil {
		t.Errorf("no store coverage must read as unknown, got %+v", got)
	}
}
