package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Showback (docs/log/roadmap.md P3-9, operational maturity). The infra cost worth attributing in
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

// usageDiskTimeout bounds ONE workspace's host-side du inside a sweep, for the same reason as
// usageSessionTimeout: du over a huge or slow home would otherwise hold the whole sequential
// sweep, and every later member's usage, mirror and the heartbeat with it. Longer than the
// Agent read because walking a tree is slower than answering a statfs. A variable so a test can
// shrink it.
var usageDiskTimeout = 30 * time.Second

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
	// deadline rides this walk because it needs exactly what the walk already has — every
	// tenant's workspaces and each one's State() — and a second ticker repeating it is the
	// cost usage.go avoids for the hourly bucket too. nil = no deadline.
	deadline *startDeadline
	// backgroundDef is the deployment default of background_hold_max, so the heatmap reads a
	// lapsed background row as the reaper does (0 = unbounded).
	backgroundDef time.Duration
	// diskBudget caps one sweep's disk reads; 0 = half the interval. A field so a test can
	// shrink it.
	diskBudget time.Duration
	// diskNext is where the next sweep starts reading disks (see recordDisks).
	diskNext int
}

func newUsageSampler(mgr *manager, interval time.Duration) *usageSampler {
	return &usageSampler{mgr: mgr, interval: interval, backgroundDef: defaultBackgroundHoldMax}
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
	// The disk gauge is cosmetic and its reads are slow (du up to usageDiskTimeout, a statfs
	// call up to usageSessionTimeout, per workspace), while everything else here is accounting.
	// Collected in the walk, read once the usage rows, deadline dispatch, heartbeat and prune
	// are done, so a slow home delays none of them.
	var disks []diskJob
	defer func() { u.recordDisks(ctx, disks) }()
	found := map[string]bool{}
	var overdue []overdueStart
	for _, t := range tenants {
		wss, err := u.mgr.store.ListWorkspaces(ctx, t.ID)
		if err != nil {
			log.Printf("showback: list workspaces (%s): %v", t.Slug, err)
			complete = false
			continue
		}
		for _, ws := range wss {
			found[ws.ID] = true
			rt := u.mgr.runtimeFor(ws, noSecretKeys)
			state := rt.State(ctx)
			if u.deadline.observe(ws, rt, state, time.Now()) {
				overdue = append(overdue, overdueStart{rt, ws})
				continue
			}
			if state != "running" {
				continue
			}
			if err := u.mgr.store.AddUsage(ctx, ws.MembershipID, ws.TenantID, day, secs); err != nil {
				log.Printf("showback: add usage (%s): %v", ws.ContainerName, err)
			}
			c := u.counters(ctx, ws, rt, secs, backgroundHoldMax(parseLimits(t.Limits), u.backgroundDef))
			if err := u.mgr.store.AddUsageHour(ctx, ws.MembershipID, ws.TenantID, hour, c); err != nil {
				log.Printf("showback: add hourly usage (%s): %v", ws.ContainerName, err)
			}
			// The disk read is deferred to after the accounting (see recordDisks). MeasuredSecs == 0
			// means the Agent did not answer the sessions read a moment ago; asking it again for
			// the disk would only spend a second timeout on the same wedge.
			disks = append(disks, diskJob{ws, rt, c.MeasuredSecs > 0})
		}
	}
	u.deadline.dispatch(ctx, overdue)
	if !complete {
		return
	}
	u.deadline.retain(found)
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
func (u *usageSampler) counters(ctx context.Context, ws store.Workspace, rt runtime.Runtime, secs int, bgMax time.Duration) store.UsageHourCounters {
	c := store.UsageHourCounters{Samples: 1, RunningSecs: secs}
	sctx, cancel := context.WithTimeout(ctx, usageSessionTimeout)
	defer cancel()
	readStart := time.Now()
	env, err := u.mgr.agentSessionsEnv(sctx, rt)
	if err != nil {
		return c
	}
	// Keep the session mirror fresh for every running workspace, from the envelope this
	// sample already paid for: the member roster reads sessions and their age from the
	// mirror, and the list handlers only refresh it for a workspace someone opens. Only a
	// successful read gets here, so an unreachable Agent leaves the mirror as it was.
	if err := u.mgr.mirrorSessions(ctx, ws.ID, readStart, env.Sessions); err != nil {
		log.Printf("showback: mirror sessions (%s): %v", ws.ContainerName, err)
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
		if sessionActivity(s, bgMax) == activityMachineBusy {
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

// diskJob is one running workspace whose disk is read after the accounting.
type diskJob struct {
	ws      store.Workspace
	rt      runtime.Runtime
	agentUp bool
}

// recordDisks reads the collected workspaces' disks within an overall budget (half the sampling
// interval unless diskBudget says otherwise). Workspaces left when it runs out are skipped, not
// stored as anything: their last reading and its age stay. The next sweep starts where this one
// stopped, so a slow head of the list cannot starve the tail of readings for good.
func (u *usageSampler) recordDisks(ctx context.Context, jobs []diskJob) {
	if len(jobs) == 0 {
		return
	}
	budget := u.diskBudget
	if budget <= 0 {
		budget = u.interval / 2
	}
	bctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	start := u.diskNext % len(jobs)
	for i := 0; i < len(jobs); i++ {
		if bctx.Err() != nil {
			return
		}
		j := jobs[(start+i)%len(jobs)]
		u.recordDisk(bctx, j.ws, j.rt, j.agentUp)
		u.diskNext = start + i + 1
	}
}

// recordDisk stores the home-disk reading the roster gauges from, for one running workspace.
// What a reading means depends on the runtime (WorkspaceSizing.DiskMeaning):
//   - home (ecs-ec2, kubernetes): the Agent's statfs of the member's own volume, used and total.
//   - quota (local, native): the host-side size of the home tree against the member's disk
//     quota; total is 0 when no quota is set, so the figure is "used" alone.
//   - work (ecs Fargate): nothing is recorded. The working disk is wiped on stop and statfs
//     of it says nothing about the member's data; the roster shows the configured size.
//
// A reading that cannot be taken is skipped, never stored as zero, so the last real one (and
// its age) stays on the card.
func (u *usageSampler) recordDisk(ctx context.Context, ws store.Workspace, rt runtime.Runtime, agentUp bool) {
	var used, total uint64
	taken := time.Now()
	// One Agent call serves the install outcome (every runtime) and the home statfs. The outcome
	// is recorded whether or not a disk reading follows: a disk that cannot be read must neither
	// drop a new failure nor keep an old one, and a clean answer withdraws it.
	var st map[string]any
	if agentUp {
		sctx, cancel := context.WithTimeout(ctx, usageSessionTimeout)
		s, err := u.mgr.agentStats(sctx, rt)
		cancel()
		if err == nil {
			st = s
			u.recordBootFailed(ctx, ws, s, taken)
		}
	}
	switch u.mgr.workspaceSizing().DiskMeaning {
	case runtime.DiskMeaningHome:
		if st == nil {
			return
		}
		du, ok1 := st["disk_used"].(uint64)
		dt, ok2 := st["disk_total"].(uint64)
		if !ok1 || !ok2 || dt == 0 {
			return
		}
		used, total = du, dt
	case runtime.DiskMeaningQuota:
		dctx, cancel := context.WithTimeout(ctx, usageDiskTimeout)
		defer cancel()
		du, ok := dirDiskUsage(dctx, u.mgr.rootedDataDir(ws))
		if !ok {
			return
		}
		used = du
		// "No limit set" (ok=false) and "could not read the limit" (err) are different: storing
		// total=0 for the second would erase the last good denominator and make a full disk look
		// like one with no quota. On an error the whole reading is dropped.
		ul, hasLimit, err := u.mgr.store.GetUserLimit(ctx, ws.MembershipID)
		if err != nil {
			log.Printf("showback: disk quota (%s): %v", ws.ContainerName, err)
			return
		}
		if hasLimit && ul.DiskGB > 0 {
			total = uint64(ul.DiskGB) * gib
		}
	default:
		return
	}
	if err := u.mgr.store.PutWorkspaceDisk(ctx, store.WorkspaceDisk{
		WorkspaceID: ws.ID, Used: used, Total: total, ObservedAt: store.DiskObservedAt(taken),
	}); err != nil {
		log.Printf("showback: record disk (%s): %v", ws.ContainerName, err)
	}
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
	// engine_hourly (ADR 0071) is the same kind of row on the same retention, and the
	// engines write it from their own controllers. It is pruned from here rather than from
	// each controller so the deployment has one hourly-bucket janitor rather than one per
	// engine racing on the same clock — and so an engine table that shrinks does not strand
	// the rows of an engine nobody runs any more.
	if err := u.mgr.store.PruneEngineHourly(ctx, cutoff); err != nil {
		log.Printf("showback: prune hourly engine occupancy: %v", err)
	}
	// Who the engine work was for (ADR 0079 open question 7): the same retention, from the same
	// janitor, for the same reason. The hourly buckets take the hour cutoff; the undelivered
	// rows are timestamped RFC3339, so they take the same instant spelled the way they store it
	// — cutting them on the hour string would delete nothing, because "2026-06-13T04" sorts
	// before every full timestamp in that hour.
	if err := u.mgr.store.PruneEngineMembershipHourly(ctx, cutoff); err != nil {
		log.Printf("showback: prune hourly engine attribution: %v", err)
	}
	tsCutoff := now.AddDate(0, 0, -usageHourlyRetentionDays).UTC().Format(time.RFC3339)
	if err := u.mgr.store.PruneEngineUsageUndelivered(ctx, tsCutoff); err != nil {
		log.Printf("showback: prune undelivered engine usage: %v", err)
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

// recordBootFailed stores which CLIs failed to install on this start, from an answered stats
// call. An Agent that reports none withdraws an earlier failure.
func (u *usageSampler) recordBootFailed(ctx context.Context, ws store.Workspace, st map[string]any, at time.Time) {
	bf, _ := st["boot_install_failed"].(map[string]string)
	failed := ""
	if len(bf) > 0 {
		b, err := json.Marshal(bf)
		if err != nil {
			return
		}
		failed = string(b)
	}
	if err := u.mgr.store.PutWorkspaceBootFailed(ctx, ws.ID, failed, store.DiskObservedAt(at)); err != nil {
		log.Printf("showback: record boot install (%s): %v", ws.ContainerName, err)
	}
}
