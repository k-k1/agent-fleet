package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Showback (docs/roadmap.md P3-9, operational maturity). The infra cost worth attributing in
// the BYO model is *workspace occupancy* — Claude usage is each user's own
// subscription and not counted; what costs the operator RAM/CPU (or Fargate hours
// on AWS) is how long each workspace runs. A background sampler credits every
// running workspace one interval of seconds per sweep into a per-(membership, day)
// bucket, and the admin API serves it back per tenant/member as JSON (dashboard)
// or CSV (spreadsheet / optional chargeback). No external billing.

const usageDayFmt = "2006-01-02"

// usageHourFmt is the hourly bucket key (docs/log/83). UTC, like the day bucket —
// the client shifts it to local time for display, because a 24-row heatmap drawn in
// UTC tells a reader in Tokyo that they work at four in the morning.
const usageHourFmt = "2006-01-02T15"

// usageSessionTimeout bounds ONE workspace's session read inside a sweep. The shared
// agent client allows two minutes, which is right for a user-facing call and wrong
// here: a handful of wedged agents would run the sampler past its own tick, and a
// missed tick loses occupancy that cannot be recovered. A read that does not answer
// quickly is recorded as "running, sessions unmeasured" instead.
const usageSessionTimeout = 10 * time.Second

// usageHourlyRetentionDays caps how far back the hourly buckets are kept. They are 24x
// the rows of usage_daily and answer a question nobody asks about last spring; the daily
// ledger, which does get asked, is never pruned.
const usageHourlyRetentionDays = 92

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
//
// The same pass fills the hourly bucket behind the uptime heatmap (docs/log/83). It
// rides here rather than on a timer of its own on purpose: this sweep already resolves
// every tenant's workspaces and asks each runtime for its state, and a second resident
// ticker doing the same walk is how this host has run out of memory before (docs/log/26).
func (u *usageSampler) sample(ctx context.Context) {
	secs := int(u.interval.Seconds())
	if secs <= 0 {
		return
	}
	now := time.Now().UTC()
	day, hour := now.Format(usageDayFmt), now.Format(usageHourFmt)
	tenants, err := u.mgr.store.ListTenants(ctx)
	if err != nil {
		log.Printf("showback: list tenants: %v", err)
		return
	}
	// A sweep that could not enumerate everything must not claim the hour was
	// observed. The heartbeat is what makes an empty cell mean "stopped" rather than
	// "unknown", so writing it after a partial walk would paint grey over workspaces
	// this pass never reached — a confident answer produced by a failure.
	complete := true
	for _, t := range tenants {
		wss, err := u.mgr.store.ListWorkspaces(ctx, t.ID)
		if err != nil {
			log.Printf("showback: list workspaces (%s): %v", t.Slug, err)
			complete = false
			continue
		}
		for _, ws := range wss {
			rt := u.mgr.runtimeFor(ws, "")
			if rt.State(ctx) != "running" {
				continue
			}
			if err := u.mgr.store.AddUsage(ctx, ws.MembershipID, ws.TenantID, day, secs); err != nil {
				log.Printf("showback: add usage (%s): %v", ws.ContainerName, err)
			}
			if err := u.mgr.store.AddUsageHour(ctx, ws.MembershipID, ws.TenantID, hour,
				u.counters(ctx, rt, secs)); err != nil {
				log.Printf("showback: add hourly usage (%s): %v", ws.ContainerName, err)
			}
		}
	}
	if !complete {
		return
	}
	if err := u.mgr.store.AddUsageHour(ctx, "", "", hour, store.UsageHourCounters{Samples: 1}); err != nil {
		log.Printf("showback: heartbeat: %v", err)
	}
	u.prune(ctx, now)
}

// counters turns one running workspace into this sample's contribution.
//
// An unreachable Agent leaves MeasuredSecs at 0 rather than recording zero sessions.
// A workspace mid-start, or one whose Agent is wedged, is exactly the case where the
// count is unknown, and "0 sessions" would draw a cold cell over a busy hour — the same
// 0-vs-unmeasured confusion the usage ledger keeps re-teaching.
func (u *usageSampler) counters(ctx context.Context, rt runtime.Runtime, secs int) store.UsageHourCounters {
	c := store.UsageHourCounters{Samples: 1, RunningSecs: secs}
	sctx, cancel := context.WithTimeout(ctx, usageSessionTimeout)
	defer cancel()
	env, err := u.mgr.agentSessionsEnv(sctx, rt)
	if err != nil {
		return c
	}
	alive, busy := 0, 0
	for _, s := range env.Sessions {
		if !s.Alive {
			continue
		}
		alive++
		// sessionActivity (session_activity.go) is the ONE definition of "running". The
		// point of this metric is that the set the reaper reads as "do not stop this
		// workspace" and the dark cells of the heatmap mean the same thing; copying the
		// predicate here leaves one of the two stale the next time a state is added. A
		// keepAwake pin counting as machineBusy is intended — a pin left dark all weekend
		// is exactly the waste this screen exists to show.
		if sessionActivity(s) == activityMachineBusy {
			busy++
		}
	}
	c.MeasuredSecs = secs
	c.SessionSecs = alive * secs
	c.BusySecs = busy * secs
	c.MaxSessions = alive
	c.MaxBusy = busy
	return c
}

// prune drops hourly buckets past the retention window, once an hour rather than on
// every sweep (the delete is cheap, but twelve identical no-op deletes an hour are
// twelve write locks nobody needs).
func (u *usageSampler) prune(ctx context.Context, now time.Time) {
	// Floor of one minute: a plain interval.Minutes() makes the window 0 minutes wide
	// on a 30-second interval, the condition is then always true, and the delete runs on
	// every sweep.
	window := int(u.interval.Minutes())
	if window < 1 {
		window = 1
	}
	if now.Minute() >= window {
		return
	}
	cutoff := now.AddDate(0, 0, -usageHourlyRetentionDays).Format(usageHourFmt)
	if err := u.mgr.store.PruneUsageHourly(ctx, cutoff); err != nil {
		log.Printf("showback: prune hourly usage: %v", err)
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
func aggregateUsage(rows []store.UsageRow) []usageTotal {
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
func withoutSystemTenants(rows []store.UsageRow) []store.UsageRow {
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
	// A reserved tenant's occupancy is not a person's (system_tenant.go). Hiding those
	// tenants from the tenant list while af-golden / af-golden-seed rows stay here defeats
	// the point. The sampler keeps recording them — the ledger stays raw — so only the
	// presentation drops them.
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

func writeUsageCSV(w http.ResponseWriter, rows []store.UsageRow) {
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
