package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

// Showback (docs/roadmap.md P3-9 運用の成熟). The infra cost worth attributing in
// the BYO model is *workspace occupancy* — Claude usage is each user's own
// subscription and not counted; what costs the operator RAM/CPU (or Fargate hours
// on AWS) is how long each workspace runs. A background sampler credits every
// running workspace one interval of seconds per sweep into a per-(membership, day)
// bucket, and the admin API serves it back per tenant/member as JSON (dashboard)
// or CSV (spreadsheet / optional chargeback). No external billing.

const usageDayFmt = "2006-01-02"

// usageSampler periodically credits running-seconds to each running workspace.
// Separate from the reaper (which only visits idle-stop-enabled tenants); showback
// must account for every tenant regardless of idle settings.
type usageSampler struct {
	mgr      *manager
	interval time.Duration
}

func newUsageSampler(mgr *manager, interval time.Duration) *usageSampler {
	return &usageSampler{mgr: mgr, interval: interval}
}

func (u *usageSampler) run(ctx context.Context) {
	log.Printf("showback sampler: interval=%s", u.interval)
	t := time.NewTicker(u.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			u.sample(ctx)
		}
	}
}

// sample credits one interval of seconds to every workspace that is running at
// this instant. This approximates occupancy to within one interval (a workspace
// that starts or stops mid-interval is over/under-counted by at most that much) —
// acceptable for internal showback with no external billing.
func (u *usageSampler) sample(ctx context.Context) {
	secs := int(u.interval.Seconds())
	if secs <= 0 {
		return
	}
	day := time.Now().UTC().Format(usageDayFmt)
	tenants, err := u.mgr.store.ListTenants(ctx)
	if err != nil {
		log.Printf("showback: list tenants: %v", err)
		return
	}
	for _, t := range tenants {
		wss, err := u.mgr.store.ListWorkspaces(ctx, t.ID)
		if err != nil {
			log.Printf("showback: list workspaces (%s): %v", t.Slug, err)
			continue
		}
		for _, ws := range wss {
			if u.mgr.runtimeFor(ws, "").State(ctx) != "running" {
				continue
			}
			if err := u.mgr.store.AddUsage(ctx, ws.MembershipID, ws.TenantID, day, secs); err != nil {
				log.Printf("showback: add usage (%s): %v", ws.ContainerName, err)
			}
		}
	}
}

// --- admin API ---

// usageRange resolves the [from, to] window from query params, defaulting to the
// trailing 30 days (UTC). Both are inclusive YYYY-MM-DD; a malformed value is a
// 400 rather than a silent default so a mistyped range is caught.
func usageRange(r *http.Request) (from, to string, aerr *apiError) {
	now := time.Now().UTC()
	from = now.AddDate(0, 0, -29).Format(usageDayFmt)
	to = now.Format(usageDayFmt)
	if v := r.URL.Query().Get("from"); v != "" {
		if _, err := time.Parse(usageDayFmt, v); err != nil {
			return "", "", &apiError{http.StatusBadRequest, "bad_request", "from must be YYYY-MM-DD"}
		}
		from = v
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if _, err := time.Parse(usageDayFmt, v); err != nil {
			return "", "", &apiError{http.StatusBadRequest, "bad_request", "to must be YYYY-MM-DD"}
		}
		to = v
	}
	return from, to, nil
}

// tenantScope applies the admin gate and returns the tenant filter shared by
// the deployment-wide admin views (usage, all-sessions, audit). With a `tenant`
// query param, a super_admin OR that tenant's tenant_admin may read it (scoped to
// that tenant). Without it, only a super_admin may read the deployment-wide view
// (tenantID="" = every tenant).
func (a adminAPI) tenantScope(w http.ResponseWriter, r *http.Request) (tenantID string, ok bool) {
	if slug := r.URL.Query().Get("tenant"); slug != "" {
		_, t, ok := a.tenantAdminFor(w, r, slug)
		if !ok {
			return "", false
		}
		return t.ID, true
	}
	if _, ok := a.superAdminFor(w, r); !ok {
		return "", false
	}
	return "", true
}

// usageTotal is one member's aggregated occupancy over the window.
type usageTotal struct {
	TenantSlug  string  `json:"tenant"`
	UserKey     string  `json:"user_key"`
	Email       string  `json:"email"`
	RunningSecs int     `json:"running_secs"`
	RunningHrs  float64 `json:"running_hours"`
}

// aggregateUsage sums the per-day rows into one total per member, key order
// preserved from the (tenant, user_key, day)-ordered input.
func aggregateUsage(rows []UsageRow) []usageTotal {
	idx := map[string]int{}
	var out []usageTotal
	for _, r := range rows {
		k := r.TenantID + "|" + r.MembershipID
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, usageTotal{TenantSlug: r.TenantSlug, UserKey: r.UserKey, Email: r.Email})
		}
		out[i].RunningSecs += r.RunningSecs
	}
	for i := range out {
		out[i].RunningHrs = hoursOf(out[i].RunningSecs)
	}
	return out
}

// withoutSystemTenants drops the reserved tenants' occupancy rows from a showback
// answer. Kept next to the aggregation rather than in the store: the ledger records
// every running workspace on purpose (the sampler must not decide what counts), and
// this is only about what an admin screen shows.
func withoutSystemTenants(rows []UsageRow) []UsageRow {
	out := rows[:0:0]
	for _, r := range rows {
		if isSystemTenantSlug(r.TenantSlug) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// hoursOf converts seconds to hours rounded to 2 decimals.
func hoursOf(secs int) float64 {
	h, _ := strconv.ParseFloat(fmt.Sprintf("%.2f", float64(secs)/3600), 64)
	return h
}

// usage (GET /api/admin/usage?from=&to=&tenant=&format=json|csv).
// Returns the per-day rows (for a dashboard chart) and per-member totals (for a
// table); format=csv streams the daily rows as a spreadsheet.
func (a adminAPI) usage(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := a.tenantScope(w, r)
	if !ok {
		return
	}
	from, to, aerr := usageRange(r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	rows, err := a.mgr.store.ListUsage(r.Context(), tenantID, from, to)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// 予約テナントの稼働は人の稼働ではない（system_tenant.go）。テナント一覧から
	// 消しておきながらここに「af-golden / af-golden-seed」の行が残ると、隠した意味が
	// 無くなる。サンプラは記録し続ける（台帳は素のまま）ので、消すのは見せ方だけ。
	rows = withoutSystemTenants(rows)
	if r.URL.Query().Get("format") == "csv" {
		writeUsageCSV(w, rows)
		return
	}
	days := make([]map[string]any, 0, len(rows))
	for _, u := range rows {
		days = append(days, map[string]any{
			"tenant": u.TenantSlug, "user_key": u.UserKey, "email": u.Email,
			"day": u.Day, "running_secs": u.RunningSecs, "running_hours": hoursOf(u.RunningSecs),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": from, "to": to, "days": days, "totals": aggregateUsage(rows),
	})
}

func writeUsageCSV(w http.ResponseWriter, rows []UsageRow) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="showback.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"tenant", "user_key", "email", "day", "running_secs", "running_hours"})
	for _, u := range rows {
		_ = cw.Write([]string{
			u.TenantSlug, u.UserKey, u.Email, u.Day,
			strconv.Itoa(u.RunningSecs), strconv.FormatFloat(hoursOf(u.RunningSecs), 'f', 2, 64),
		})
	}
	cw.Flush()
}
