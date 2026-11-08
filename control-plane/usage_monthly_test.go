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
	// July is short only because coverage starts on 07-10, so August is not compared with it.
	if m[2]["change_micro"] != nil || m[2]["change_pct"] != nil {
		t.Errorf("Aug must not be compared with a coverage-truncated July: %v", m[2])
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

func TestMonthlyCostChangeComparesWithAFullPreviousMonth(t *testing.T) {
	sp := newMonthSpan(2, time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC))
	rows := []store.CloudCostRow{{Day: "2026-07-05", Unblended: 1_000_000}, {Day: "2026-08-02", Unblended: 1_500_000}}
	got := monthlyCost(rows, sp, "2026-07-01", false, nil)
	if got[1].ChangeMicro == nil || *got[1].ChangeMicro != 500_000 || *got[1].ChangePct != 50 {
		t.Errorf("a fully covered previous month compares even against the running month: %+v", got[1])
	}
}

func TestMonthsParam(t *testing.T) {
	for q, want := range map[string]struct {
		n  int
		ok bool
	}{"": {0, true}, "1": {1, true}, "24": {24, true}, "0": {}, "-1": {}, "25": {}, "abc": {}, "1.5": {}, "+3": {}} {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		if q != "" {
			r.URL.RawQuery = "months=" + q
		}
		n, aerr := monthsParam(r)
		if (aerr == nil) != want.ok || (want.ok && n != want.n) {
			t.Errorf("months=%q => %d, %v", q, n, aerr)
		}
	}
}

func TestMonthsOneHasNoChangeAndMyUsageDefaultsToSix(t *testing.T) {
	freezeMonths(t, "2026-08-20")
	sp := newMonthSpan(1, monthsNow())
	if got := monthlyUsage(nil, sp); len(got) != 1 || got[0].ChangeSecs != nil || got[0].ChangePct != nil {
		t.Errorf("single month: %+v", got)
	}
	_, mgr, tn, mem := cloudCostFixture(t)
	w := httptest.NewRecorder()
	newAdminAPI(mgr).myUsageMonthly(w, httptest.NewRequest(http.MethodGet, "/api/usage/me/monthly", nil),
		store.Identity{}, store.MembershipView{MembershipID: mem.ID, TenantID: tn.ID})
	if m := monthsOf(t, getJSON(t, w)); len(m) != 6 || m[0]["month"] != "2026-03" {
		t.Errorf("default window = %v", m)
	}
}

// --- per-member monthly rows (issue #3609) ---------------------------------------------

func adminGet(h func(http.ResponseWriter, *http.Request), url, email string) map[string]any {
	r := httptest.NewRequest(http.MethodGet, url, nil)
	r.Header.Set("X-Forwarded-Email", email)
	w := httptest.NewRecorder()
	h(w, r)
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	got["_status"] = float64(w.Code)
	return got
}

func membersOf(t *testing.T, got map[string]any) []map[string]any {
	t.Helper()
	raw, ok := got["monthly_members"].([]any)
	if !ok {
		t.Fatalf("no monthly_members in %v", got)
	}
	var out []map[string]any
	for _, m := range raw {
		out = append(out, m.(map[string]any))
	}
	return out
}

func memberMonths(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, x := range m["months"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func TestAdminUsageMonthlyMembersScopeOrderAndLimit(t *testing.T) {
	freezeMonths(t, "2026-08-20")
	st, mgr, tn, mem := cloudCostFixture(t)
	ctx := context.Background()
	other, _ := st.CreateTenant(ctx, "ops", "運用")
	oi, _ := st.UpsertIdentity(ctx, "o@x.co.jp", "o-x-co-jp", "")
	om, _ := st.EnsureMembership(ctx, oi.ID, other.ID, "member")
	// An idle member of sales (no rows) and a removed member whose membership is gone.
	idle, _ := st.UpsertIdentity(ctx, "idle@acme.co.jp", "idle-acme-co-jp", "")
	_, _ = st.EnsureMembership(ctx, idle.ID, tn.ID, "member")
	_ = st.AddUsage(ctx, mem.ID, tn.ID, "2026-07-31", 3600)
	_ = st.AddUsage(ctx, mem.ID, tn.ID, "2026-08-01", 7200)
	_ = st.AddUsage(ctx, "gone-membership", tn.ID, "2026-08-02", 1800)
	_ = st.AddUsage(ctx, om.ID, other.ID, "2026-08-02", 36000)
	api := newAdminAPI(mgr)

	got := adminGet(api.usage, "/api/admin/usage?tenant=sales&months=2", "boss@acme.co.jp")
	ms := membersOf(t, got)
	if len(ms) != 2 || got["monthly_members_total"].(float64) != 2 {
		t.Fatalf("tenant admin members = %v (idle member must be absent, ops member must not appear)", ms)
	}
	if ms[0]["user_key"] != "w-acme-co-jp" || ms[0]["total_secs"].(float64) != 10800 || ms[1]["user_key"] != "" {
		t.Errorf("order/labels = %v", ms)
	}
	mo := memberMonths(ms[0])
	if mo[0]["running_secs"].(float64) != 3600 || mo[1]["running_secs"].(float64) != 7200 ||
		mo[1]["change_pct"].(float64) != 100 || mo[1]["partial"] != true {
		t.Errorf("w months = %v", mo)
	}
	// The removed member keeps their hours: first month has no change, second has no pct.
	if gm := memberMonths(ms[1]); gm[1]["running_secs"].(float64) != 1800 || gm[1]["change_pct"] != nil {
		t.Errorf("removed member months = %v", gm)
	}

	// Positive control: super_admin sees the other tenant's member too, biggest first.
	_, _ = st.UpsertIdentity(ctx, "root@acme.co.jp", "root-acme-co-jp", "super_admin")
	got = adminGet(api.usage, "/api/admin/usage?months=2", "root@acme.co.jp")
	ms = membersOf(t, got)
	if len(ms) != 3 || ms[0]["user_key"] != "o-x-co-jp" || ms[0]["tenant"] != "ops" {
		t.Errorf("super members = %v", ms)
	}
	// Cap keeps the biggest and still reports the full count.
	got = adminGet(api.usage, "/api/admin/usage?months=2&member_limit=1", "root@acme.co.jp")
	if ms = membersOf(t, got); len(ms) != 1 || got["monthly_members_total"].(float64) != 3 {
		t.Errorf("limited = %v / %v", ms, got["monthly_members_total"])
	}
	if got = adminGet(api.usage, "/api/admin/usage?months=2&member_limit=0", "root@acme.co.jp"); got["_status"].(float64) != 400 {
		t.Errorf("member_limit=0 status %v", got["_status"])
	}
	// No months => no per-member block.
	if got = adminGet(api.usage, "/api/admin/usage", "root@acme.co.jp"); got["monthly_members"] != nil {
		t.Error("monthly_members without months")
	}
}

func TestCloudCostMonthlyMembersCoverageAndScope(t *testing.T) {
	freezeMonths(t, "2026-08-20")
	st, mgr, tn, mem := cloudCostFixture(t)
	seedMonthlyCost(t, st, tn, mem)
	api := newAdminAPI(mgr)

	got := adminGet(api.cloudCost, "/api/admin/cloud-cost?tenant=sales&months=3", "boss@acme.co.jp")
	ms := membersOf(t, got)
	if len(ms) != 1 || ms[0]["user_key"] != "w-acme-co-jp" || ms[0]["tenant"] != "sales" {
		t.Fatalf("tenant admin members = %v (ops' member and the shared bucket must not appear)", ms)
	}
	mo := memberMonths(ms[0])
	if mo[0]["no_coverage"] != true || mo[1]["unblended_micro"].(float64) != 1_500_000 ||
		mo[2]["unblended_micro"].(float64) != 2_250_000 {
		t.Errorf("months = %v", mo)
	}
	if mo[2]["change_micro"] != nil || mo[1]["change_micro"] != nil {
		t.Errorf("no change against a coverage-truncated month: %v", mo)
	}
	if ms[0]["total_unblended_micro"].(float64) != 3_750_000 {
		t.Errorf("total = %v", ms[0]["total_unblended_micro"])
	}

	_, _ = st.UpsertIdentity(context.Background(), "root@acme.co.jp", "root-acme-co-jp", "super_admin")
	got = adminGet(api.cloudCost, "/api/admin/cloud-cost?months=3", "root@acme.co.jp")
	ms = membersOf(t, got)
	if len(ms) != 2 || ms[0]["user_key"] != "o-x-co-jp" || got["monthly_members_total"].(float64) != 2 {
		t.Errorf("super members = %v (shared bucket is not a member)", ms)
	}
	got = adminGet(api.cloudCost, "/api/admin/cloud-cost?months=3&member_limit=1", "root@acme.co.jp")
	if len(membersOf(t, got)) != 1 || got["monthly_members_total"].(float64) != 2 {
		t.Errorf("limited = %v", got)
	}
}

func TestMonthlyCostByMemberComparesWithFullPreviousMonth(t *testing.T) {
	sp := newMonthSpan(2, time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC))
	rows := []store.CloudCostRow{
		{Day: "2026-07-05", MembershipID: "a", Unblended: 1_000_000},
		{Day: "2026-08-02", MembershipID: "a", Unblended: 1_500_000},
		{Day: "2026-08-03", MembershipID: "", Unblended: 9_000_000},
	}
	got, total := monthlyCostByMember(rows, sp, "2026-07-01", func(r store.CloudCostRow) bool { return r.MembershipID == "" }, nil, 10)
	if total != 1 || got[0].Months[1].ChangePct == nil || *got[0].Months[1].ChangePct != 50 {
		t.Errorf("got %+v", got)
	}
}

func memberUsageReq(slug, key, email, q string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/admin/tenants/"+slug+"/members/"+key+"/usage-monthly"+q, nil)
	r.SetPathValue("slug", slug)
	r.SetPathValue("key", key)
	if email != "" {
		r.Header.Set("X-Forwarded-Email", email)
	}
	return r
}

func TestMemberUsageMonthlyForTheDetailView(t *testing.T) {
	freezeMonths(t, "2026-08-20")
	st, mgr, tn, mem := cloudCostFixture(t)
	ctx := context.Background()
	oi, _ := st.UpsertIdentity(ctx, "o@x.co.jp", "o-x-co-jp", "")
	other, _ := st.CreateTenant(ctx, "ops", "運用")
	om, _ := st.EnsureMembership(ctx, oi.ID, other.ID, "member")
	_ = st.AddUsage(ctx, mem.ID, tn.ID, "2026-07-31", 3600)
	_ = st.AddUsage(ctx, mem.ID, tn.ID, "2026-08-01", 7200)
	_ = st.AddUsage(ctx, om.ID, other.ID, "2026-08-01", 99999)
	api := newAdminAPI(mgr)

	w := httptest.NewRecorder()
	api.memberUsageMonthly(w, memberUsageReq("sales", "w-acme-co-jp", "boss@acme.co.jp", "?months=2"))
	m := monthsOf(t, getJSON(t, w))
	if m[0]["running_secs"].(float64) != 3600 || m[1]["running_secs"].(float64) != 7200 || m[1]["change_pct"].(float64) != 100 {
		t.Errorf("months = %v", m)
	}
	// An ordinary member cannot read a colleague's hours.
	w = httptest.NewRecorder()
	api.memberUsageMonthly(w, memberUsageReq("sales", "w-acme-co-jp", "w@acme.co.jp", ""))
	if w.Code != http.StatusForbidden {
		t.Errorf("member status %d, want 403", w.Code)
	}
	// Another tenant's member is a 404 under this tenant's slug, even for its admin.
	w = httptest.NewRecorder()
	api.memberUsageMonthly(w, memberUsageReq("sales", "o-x-co-jp", "boss@acme.co.jp", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant status %d, want 404", w.Code)
	}
	// Positive control: super_admin reads the other tenant's member under its own slug.
	_, _ = st.UpsertIdentity(ctx, "root@acme.co.jp", "root-acme-co-jp", "super_admin")
	w = httptest.NewRecorder()
	api.memberUsageMonthly(w, memberUsageReq("ops", "o-x-co-jp", "root@acme.co.jp", "?months=2"))
	if m = monthsOf(t, getJSON(t, w)); m[1]["running_secs"].(float64) != 99999 {
		t.Errorf("super months = %v", m)
	}
}
