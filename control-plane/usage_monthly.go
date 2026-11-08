package main

import (
	"math"
	"net/http"
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
// SharedMicro is set only on the super_admin deployment-wide view.
type costMonth struct {
	Month       string   `json:"month"`
	Unblended   int64    `json:"unblended_micro"`
	SharedMicro *int64   `json:"shared_micro,omitempty"`
	Estimated   bool     `json:"estimated"`
	Partial     bool     `json:"partial"`
	NoCoverage  bool     `json:"no_coverage"`
	ChangeMicro *int64   `json:"change_micro,omitempty"`
	ChangePct   *float64 `json:"change_pct,omitempty"`
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

// myUsageMonthly (GET /api/usage/me/monthly?months=N) — the caller's own monthly hours.
// Same discipline as /api/cost/me: only the caller's rows, no deployment total.
func (a adminAPI) myUsageMonthly(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	n, aerr := monthsParam(r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if n == 0 {
		n = 6
	}
	sp := newMonthSpan(n, monthsNow())
	rows, err := a.mgr.store.ListUsageMember(r.Context(), mv.MembershipID, sp.From, sp.To)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": sp.From, "to": sp.To, "monthly": monthlyUsage(rows, sp)})
}
