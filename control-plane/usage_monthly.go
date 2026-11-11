package main

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Monthly totals (issue #3383). Months are cut in UTC because the daily ledger rows
// (usageDayFmt) and Cost Explorer days are UTC: a month is exactly the rows whose day
// starts with "YYYY-MM". Each day row is counted once, so a session that spans midnight
// or a month end is already split by the sampler and cannot be counted twice here.
//
// The aggregation is server-side on purpose: a 12-month answer is 12 numbers per
// endpoint instead of a year of per-member daily rows shipped to the browser. It rides
// the existing endpoints as `months=N`, so each keeps its own permission gate unchanged.

const maxMonths = 24

// monthsNow is the clock for month windows; tests replace it.
var monthsNow = func() time.Time { return time.Now().UTC() }

// monthsParam reads `months` (0 = absent). A malformed or out-of-range value is a 400.
func monthsParam(r *http.Request) (int, *apiError) {
	v := r.URL.Query().Get("months")
	if v == "" {
		return 0, nil
	}
	// Digits only: Atoi would also accept a leading "+".
	n, err := strconv.Atoi(v)
	if err != nil || v[0] == '+' || n < 1 || n > maxMonths {
		return 0, &apiError{http.StatusBadRequest, "bad_request", "months must be 1.." + strconv.Itoa(maxMonths)}
	}
	return n, nil
}

// monthSpan is the n calendar months ending with the one containing now.
type monthSpan struct {
	Keys     []string // "YYYY-MM", oldest first
	From, To string   // first day of the oldest month .. today
	Current  string
}

func newMonthSpan(n int, now time.Time) monthSpan {
	now = now.UTC()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	sp := monthSpan{To: now.Format(usageDayFmt), Current: first.Format("2006-01")}
	for i := n - 1; i >= 0; i-- {
		sp.Keys = append(sp.Keys, first.AddDate(0, -i, 0).Format("2006-01"))
	}
	sp.From = sp.Keys[0] + "-01"
	return sp
}

// monthEnd is the last day of a "YYYY-MM" month.
func monthEnd(key string) string {
	t, _ := time.Parse("2006-01", key)
	return t.AddDate(0, 1, -1).Format(usageDayFmt)
}

func pctChange(prev, cur int64) *float64 {
	if prev <= 0 {
		return nil
	}
	v := float64(cur-prev) / float64(prev) * 100
	v = math.Round(v*10) / 10
	return &v
}

// usageMonth is one month of running hours. Change* compare with the previous month in
// the list (absent on the first, and ChangePct also when the previous month is 0).
type usageMonth struct {
	Month        string   `json:"month"`
	RunningSecs  int      `json:"running_secs"`
	RunningHours float64  `json:"running_hours"`
	Partial      bool     `json:"partial"`
	ChangeSecs   *int     `json:"change_secs,omitempty"`
	ChangePct    *float64 `json:"change_pct,omitempty"`
}

func monthlyUsage(rows []store.UsageRow, sp monthSpan) []usageMonth {
	secs := map[string]int{}
	for _, r := range rows {
		if len(r.Day) >= 7 {
			secs[r.Day[:7]] += r.RunningSecs
		}
	}
	out := make([]usageMonth, 0, len(sp.Keys))
	for i, k := range sp.Keys {
		m := usageMonth{Month: k, RunningSecs: secs[k], RunningHours: hoursOf(secs[k]), Partial: k == sp.Current}
		if i > 0 {
			d := m.RunningSecs - out[i-1].RunningSecs
			m.ChangeSecs = &d
			m.ChangePct = pctChange(int64(out[i-1].RunningSecs), int64(m.RunningSecs))
		}
		out = append(out, m)
	}
	return out
}

// costMonth is one month of cloud cost. NoCoverage means the month lies wholly outside
// the days the store has (cost allocation is not retroactive): it is unknown, not zero.
// Partial covers the running month and a month that starts before the first covered day.
// CoverageFrom tells the two apart: it is the first covered day (YYYY-MM-DD) of a finished
// month that is partial because the data starts mid-month, and is absent for the running month
// (running wins when both hold) and for complete months. A separate additive key keeps
// `partial` meaning what it always did for existing readers.
// SharedMicro is set only on the super_admin deployment-wide view.
type costMonth struct {
	Month        string   `json:"month"`
	Unblended    int64    `json:"unblended_micro"`
	SharedMicro  *int64   `json:"shared_micro,omitempty"`
	Estimated    bool     `json:"estimated"`
	Partial      bool     `json:"partial"`
	CoverageFrom string   `json:"coverage_from,omitempty"`
	NoCoverage   bool     `json:"no_coverage"`
	ChangeMicro  *int64   `json:"change_micro,omitempty"`
	ChangePct    *float64 `json:"change_pct,omitempty"`
}

// monthlyCost buckets cost rows by month. withShared fills SharedMicro; isShared says
// which rows are the shared bucket (nil = none), and those never enter Unblended.
func monthlyCost(rows []store.CloudCostRow, sp monthSpan, firstDay string, withShared bool, isShared func(store.CloudCostRow) bool) []costMonth {
	attr := map[string]int64{}
	shared := map[string]int64{}
	est := map[string]bool{}
	for _, r := range rows {
		if len(r.Day) < 7 {
			continue
		}
		k := r.Day[:7]
		if isShared != nil && isShared(r) {
			shared[k] += r.Unblended
		} else {
			attr[k] += r.Unblended
		}
		if r.Estimated {
			est[k] = true
		}
	}
	out := make([]costMonth, 0, len(sp.Keys))
	for i, k := range sp.Keys {
		m := costMonth{Month: k, Unblended: attr[k], Estimated: est[k]}
		covered := firstDay != "" && monthEnd(k) >= firstDay
		m.NoCoverage = !covered
		m.Partial = k == sp.Current || (covered && k+"-01" < firstDay)
		if covered && k != sp.Current && k+"-01" < firstDay {
			m.CoverageFrom = firstDay
		}
		if withShared {
			v := shared[k]
			m.SharedMicro = &v
		}
		// A previous month that is short only because the data starts mid-month is not a
		// fair base; the running month being partial is the ordinary case and still compares.
		prevShort := i > 0 && sp.Keys[i-1]+"-01" < firstDay
		if i > 0 && !out[i-1].NoCoverage && !m.NoCoverage && !prevShort {
			d := m.Unblended - out[i-1].Unblended
			m.ChangeMicro = &d
			m.ChangePct = pctChange(out[i-1].Unblended, m.Unblended)
		}
		out = append(out, m)
	}
	return out
}

// Per-member monthly rows (issue #3609). The rows are computed from the same windowed
// ledger query the overall `monthly` uses, so the month rules (UTC cut, partial current
// month, no_coverage, no change against a coverage-truncated month) are the ones above and
// cannot drift. Only members with at least one ledger row in the window appear; a member
// with none is absent rather than a row of zeros.

const (
	defaultMemberLimit = 50
	maxMemberLimit     = 500
)

// memberLimitParam reads `member_limit` (default 50, 1..500): how many per-member rows the
// answer carries, biggest first. A malformed value is a 400.
func memberLimitParam(r *http.Request) (int, *apiError) {
	v := r.URL.Query().Get("member_limit")
	if v == "" {
		return defaultMemberLimit, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || v[0] == '+' || n < 1 || n > maxMemberLimit {
		return 0, &apiError{http.StatusBadRequest, "bad_request", "member_limit must be 1.." + strconv.Itoa(maxMemberLimit)}
	}
	return n, nil
}

// memberUsageMonths is one member's months of running hours. TotalSecs is the sum over the
// window and is the sort key (largest first).
type memberUsageMonths struct {
	Tenant       string       `json:"tenant"`
	MembershipID string       `json:"membership_id"`
	UserKey      string       `json:"user_key"`
	Email        string       `json:"email"`
	TotalSecs    int          `json:"total_secs"`
	Months       []usageMonth `json:"months"`
}

// memberCostMonths is one member's months of attributed cloud cost.
type memberCostMonths struct {
	Tenant         string      `json:"tenant"`
	MembershipID   string      `json:"membership_id"`
	UserKey        string      `json:"user_key"`
	Email          string      `json:"email"`
	TotalUnblended int64       `json:"total_unblended_micro"`
	Months         []costMonth `json:"months"`
}

// monthlyUsageByMember groups rows by membership and runs each group through monthlyUsage.
// Rows of a removed member keep their membership id (empty labels), so they stay one row.
// It returns the top `limit` and the number of members before truncation.
func monthlyUsageByMember(rows []store.UsageRow, sp monthSpan, limit int) ([]memberUsageMonths, int) {
	groups := map[string][]store.UsageRow{}
	var order []string
	for _, r := range rows {
		k := r.TenantID + "|" + r.MembershipID
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	out := make([]memberUsageMonths, 0, len(order))
	for _, k := range order {
		g := groups[k]
		m := memberUsageMonths{Tenant: g[0].TenantSlug, MembershipID: g[0].MembershipID, UserKey: g[0].UserKey, Email: g[0].Email}
		for _, r := range g {
			m.TotalSecs += r.RunningSecs
		}
		m.Months = monthlyUsage(g, sp)
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TotalSecs != out[j].TotalSecs {
			return out[i].TotalSecs > out[j].TotalSecs
		}
		if out[i].Tenant != out[j].Tenant {
			return out[i].Tenant < out[j].Tenant
		}
		return out[i].UserKey < out[j].UserKey
	})
	total := len(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, total
}

// monthlyCostByMember is monthlyUsageByMember for cost. skip drops the shared and reserved
// buckets (they are not people); firstDay is the store-wide coverage start, so every member
// is judged against the same coverage as the overall monthly figures.
func monthlyCostByMember(rows []store.CloudCostRow, sp monthSpan, firstDay string, skip func(store.CloudCostRow) bool, labels map[string]store.CloudCostTotal, limit int) ([]memberCostMonths, int) {
	groups := map[string][]store.CloudCostRow{}
	var order []string
	for _, r := range rows {
		if skip != nil && skip(r) {
			continue
		}
		if _, ok := groups[r.MembershipID]; !ok {
			order = append(order, r.MembershipID)
		}
		groups[r.MembershipID] = append(groups[r.MembershipID], r)
	}
	out := make([]memberCostMonths, 0, len(order))
	for _, id := range order {
		g := groups[id]
		l := labels[id]
		m := memberCostMonths{Tenant: l.TenantSlug, MembershipID: id, UserKey: l.UserKey, Email: l.Email}
		for _, r := range g {
			m.TotalUnblended += r.Unblended
		}
		m.Months = monthlyCost(g, sp, firstDay, false, nil)
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TotalUnblended != out[j].TotalUnblended {
			return out[i].TotalUnblended > out[j].TotalUnblended
		}
		if out[i].Tenant != out[j].Tenant {
			return out[i].Tenant < out[j].Tenant
		}
		return out[i].UserKey < out[j].UserKey
	})
	total := len(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, total
}

// myUsageMonthly (GET /api/usage/me/monthly?months=N) — the caller's own monthly hours.
// Same discipline as /api/cost/me: only the caller's rows, no deployment total.
func (a adminAPI) myUsageMonthly(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	a.oneMemberUsageMonthly(w, r, mv.MembershipID)
}

// memberUsageMonthly (GET /api/admin/tenants/{slug}/members/{key}/usage-monthly?months=N) —
// one member's monthly hours for the member-detail view, behind the same tenant-admin gate and
// membership resolution as the member's cost and hourly endpoints.
func (a adminAPI) memberUsageMonthly(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.tenantAdminFor(w, r, r.PathValue("slug")); !ok {
		return
	}
	mem, _, _, aerr := a.resolveMember(r, r.PathValue("slug"), r.PathValue("key"))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	a.oneMemberUsageMonthly(w, r, mem.ID)
}

// oneMemberUsageMonthly looks the rows up by membership alone (not tenant), like the cost
// view: rows of a destroyed workspace are still the member's.
func (a adminAPI) oneMemberUsageMonthly(w http.ResponseWriter, r *http.Request, membershipID string) {
	n, aerr := monthsParam(r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if n == 0 {
		n = 6
	}
	sp := newMonthSpan(n, monthsNow())
	rows, err := a.mgr.store.ListUsageMember(r.Context(), membershipID, sp.From, sp.To)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": sp.From, "to": sp.To, "monthly": monthlyUsage(rows, sp)})
}
