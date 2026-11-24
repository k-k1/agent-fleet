package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
	"github.com/k-k1/agent-fleet/control-plane/internal/usagehistory"
)

// Per-member usage history (ADR 0139 decision 3, issue 4333).
//
// Two sources, kept apart on purpose:
//
//   - Running time comes from the CP's own launch records (store.Launch): a row per container
//     launch with its start, its stop and the box the runtime placed it on. Never from Agent
//     samples, which are missing exactly when the box is busiest.
//   - Measurements come from the Agent's fixed one-minute windows (mempressure.Window), each
//     tagged with the launch id the CP handed the container at Start (AF_LAUNCH_ID). That tag
//     is what keeps a minute on the box it was measured on after a resize.
//
// Days are UTC, like usage_daily and the hourly buckets: one timezone for every ledger, so
// a day row and the showback row beside it cover the same 24 hours. A running interval is split
// at UTC midnight; a measured window belongs to the day it ended in.
//
// What is pulled when:
//   - on the usage sampler's tick, for every running workspace;
//   - just before a stop the CP issues, bounded to usageStopPullTimeout and best effort, so a
//     launch shorter than a tick still leaves its minutes (a stop never waits on a slow Agent);
//   - after the next Start, once the new container answers: the series lives on home and
//     survives the stop, and the launch id keeps those minutes on the old box.
//
// The cursor (the Agent's own unix second) advances in the same transaction that folds, and
// only when it still holds the value the pull started from. A pull that loses that race, or
// whose transaction fails, folds nothing: minutes are counted once or not yet, never twice.

const (
	// usagePullPageLimit and usagePullMaxPages bound one pull: about a day of minutes a page,
	// four pages, which is the whole ~4-day series. A longer backlog is finished by the next pull.
	usagePullPageLimit = 1500
	usagePullMaxPages  = 4
	// usageWindowsMaxBytes bounds the body read from an Agent whose workspace is the member's own.
	usageWindowsMaxBytes = 2 << 20
	// usageAfterStartBudget / usageAfterStartEvery: the pull after a Start retries until the new
	// container's Agent answers, for at most this long (the tick picks up anything missed).
	usageAfterStartBudget = 6 * time.Minute
	usageAfterStartEvery  = 15 * time.Second
)

// launchReopenWindow is how long after a stop the sampler inferred it may still take that stop
// back on seeing the workspace running (tickUsageHistory), or three sampling intervals when
// that is longer. It covers the start deadline (30 minutes by default), the longest a launch
// the sampler closed before its Start can take to be seen running.
const launchReopenWindow = time.Hour

// usageStopPullTimeout bounds the pull made before a Stop: a Stop never waits longer than this
// for the Agent, whatever the Agent client's own timeout is. A variable so a test can shrink it.
var usageStopPullTimeout = 3 * time.Second

// usageHistoryRuntime reports whether a runtime takes part in the usage history: only the ones
// that name a box (ecs-ec2) do, because the history is per box.
func usageHistoryRuntime(rt runtime.Runtime) (machineProfiler, bool) {
	mp, ok := rt.(machineProfiler)
	return mp, ok
}

func newLaunchID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Never fail a Start for the history: a clock-derived id is unique enough for one
		// membership's launches.
		return fmt.Sprintf("t%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// beginLaunch records the launch that rt is about to start, before Start, and returns the env
// that hands its id to the container ("" when this runtime keeps no history). It is recorded
// first so a CP that dies between Start and a record written afterwards still knows the id the
// Agent will stamp on its minutes. endLaunchFailed undoes it when Start fails.
//
// override names what chose rt's box, "" when nothing overrode the member's own setting: the
// caller takes it from the same withResolvedSizeSource answer it sized rt with. A boost
// answers "boost" (ADR 0139 decision 5), which splits the day row and is what the boost
// budget counts.
func (m *manager) beginLaunch(ctx context.Context, ws store.Workspace, rt runtime.Runtime, override string) (id, env string) {
	if _, ok := usageHistoryRuntime(rt); !ok {
		return "", ""
	}
	id = newLaunchID()
	if !m.recordLaunch(ctx, ws, rt, override, id) {
		return "", ""
	}
	return id, usagehistory.LaunchEnv + "=" + id
}

// recordLaunch records launch id on rt's box, before rt is started. The Start path mints the id
// first and builds the runtime it starts with the id in its env, so the record is read off the
// very runtime that starts (a runtime built earlier for the record could name another box).
// false when rt keeps no history or the record could not be written.
func (m *manager) recordLaunch(ctx context.Context, ws store.Workspace, rt runtime.Runtime, override, id string) bool {
	mp, ok := usageHistoryRuntime(rt)
	if !ok {
		return false
	}
	box := mp.MachineProfile()
	l := store.Launch{
		ID: id, WorkspaceID: ws.ID, MembershipID: ws.MembershipID, TenantID: ws.TenantID,
		StartedAt: time.Now().Unix(), InstanceType: box.InstanceType, ClassID: box.ClassID, Override: override,
		VCPU: box.VCPU, BaselinePct: box.BurstBaselinePct,
	}
	if err := m.store.BeginLaunch(ctx, l); err != nil {
		log.Printf("usage history: begin launch (ws=%s): %v", ws.ID, err)
		return false
	}
	// The launch it closed (a stale one) still has seconds to count.
	m.foldRunning(ctx, ws.MembershipID)
	return true
}

// endLaunchFailed closes a launch whose Start did not succeed: it ran for no time.
func (m *manager) endLaunchFailed(ctx context.Context, membershipID, id string) {
	if id == "" {
		return
	}
	if l, ok, err := m.store.GetLaunch(ctx, id); err == nil && ok && l.StoppedAt == 0 {
		if _, _, err := m.store.EndOpenLaunch(ctx, membershipID, l.StartedAt); err != nil {
			log.Printf("usage history: close failed launch (ws=%s): %v", membershipID, err)
		}
	}
}

func (m *manager) foldRunning(ctx context.Context, membershipID string) {
	if err := m.store.FoldRunning(ctx, membershipID); err != nil {
		log.Printf("usage history: fold running time (%s): %v", membershipID, err)
	}
}

// stopRecorded is rt.Stop with the usage history around it: a bounded pull of the Agent's
// minutes before (pull=false where the Agent is the reason for the stop, or does not exist yet),
// and the launch closed after. The Stop itself is exactly rt.Stop's result.
func (m *manager) stopRecorded(ctx context.Context, ws store.Workspace, rt runtime.Runtime, pull bool) error {
	return m.stopRecordedAt(ctx, ws, rt, pull, false)
}

// stopAtLastSighting is stopRecorded for the start deadline's stop, of a workspace with no task
// up: its launch ends where it was last seen running, not at the stop. One never seen running
// ends at its own start, so a launch that could not be placed adds no running time (and nothing
// to a boost budget) for the minutes it waited.
func (m *manager) stopAtLastSighting(ctx context.Context, ws store.Workspace, rt runtime.Runtime) error {
	return m.stopRecordedAt(ctx, ws, rt, false, true)
}

func (m *manager) stopRecordedAt(ctx context.Context, ws store.Workspace, rt runtime.Runtime, pull, atLastSeen bool) error {
	if _, ok := usageHistoryRuntime(rt); !ok {
		return rt.Stop(ctx)
	}
	if pull {
		pctx, cancel := context.WithTimeout(ctx, usageStopPullTimeout)
		if err := m.pullUsageHistory(pctx, ws.MembershipID, rt); err != nil {
			log.Printf("usage history: pull before stop (ws=%s): %v", ws.ID, err)
		}
		cancel()
	}
	if !atLastSeen {
		atLastSeen = noTaskUp(ctx, rt)
	}
	err := rt.Stop(ctx)
	if err == nil {
		at := time.Now().Unix()
		if atLastSeen {
			at = 0 // EndOpenLaunch never ends a launch before its last sighting
		}
		m.closeLaunch(ctx, ws.MembershipID, at)
	}
	return err
}

// usageStopProbeTimeout bounds the reads noTaskUp makes before a Stop. They are accounting, and
// the Stop that follows still runs on the caller's own context, so a slow read never costs the
// Stop its deadline. A variable so a test can shrink it.
var usageStopProbeTimeout = 5 * time.Second

// noTaskUp reports whether rt is about to be stopped with no task up and not running (a launch
// still waiting for a placement, or one whose start claim lapsed back to stopped), so its launch
// ends at its last sighting rather than at the stop: minutes spent waiting for a placement that
// never came are not running time. A launch with a task up is counted to the stop (its box is
// there and billed), and anything that cannot be told, a read that fails or times out
// included, counts to the stop too.
func noTaskUp(ctx context.Context, rt runtime.Runtime) bool {
	tc, ok := rt.(runtime.TaskCounter)
	if !ok {
		return false
	}
	pctx, cancel := context.WithTimeout(ctx, usageStopProbeTimeout)
	defer cancel()
	if st := rt.State(pctx); st == "running" || pctx.Err() != nil {
		return false
	}
	n, err := tc.RunningTasks(pctx)
	return err == nil && n == 0
}

// closeLaunch ends the membership's open launch at `at` and counts its seconds.
func (m *manager) closeLaunch(ctx context.Context, membershipID string, at int64) {
	if _, _, err := m.store.EndOpenLaunch(ctx, membershipID, at); err != nil {
		log.Printf("usage history: end launch (%s): %v", membershipID, err)
		return
	}
	m.foldRunning(ctx, membershipID)
}

// agentUsageWindows reads one page of the Agent's windows after since. It uses the short stats
// client: this is a gauge-class call, and a wedged Agent must cost a bounded wait.
func (m *manager) agentUsageWindows(ctx context.Context, rt runtime.Runtime, since int64) (usagehistory.Page, error) {
	var page usagehistory.Page
	if rt.Endpoint() == "" {
		return page, fmt.Errorf("agent /workspace/usage-windows: no endpoint")
	}
	q := url.Values{"since": {fmt.Sprint(since)}, "limit": {fmt.Sprint(usagePullPageLimit)}}
	req, _ := http.NewRequestWithContext(ctx, "GET", rt.Endpoint()+"/workspace/usage-windows?"+q.Encode(), nil)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := agentStatsClient.Do(req)
	if err != nil {
		return page, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// An older Agent has no such route: its minutes stay unmeasured, which is what they are.
		return page, fmt.Errorf("agent /workspace/usage-windows: %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, usageWindowsMaxBytes)).Decode(&page); err != nil {
		return usagehistory.Page{}, err
	}
	return page, nil
}

// pullUsageHistory folds what the Agent has measured since the stored cursor. An unreachable
// Agent is an error the caller logs and moves past: nothing was folded, the cursor did not move,
// and the next pull starts from the same place.
func (m *manager) pullUsageHistory(ctx context.Context, membershipID string, rt runtime.Runtime) error {
	if _, ok := usageHistoryRuntime(rt); !ok {
		return nil
	}
	cursor, err := m.store.UsagePullCursor(ctx, membershipID)
	if err != nil {
		return err
	}
	for page := 0; page < usagePullMaxPages; page++ {
		p, err := m.agentUsageWindows(ctx, rt, cursor)
		if err != nil {
			return err
		}
		if p.Cursor <= cursor {
			return nil // nothing new (an Agent that answered a cursor behind ours is not trusted)
		}
		now, to, ws := time.Now(), p.Cursor, p.Windows
		if hold, held, skewed := usagehistory.HoldBeforeFuture(ws, now); skewed {
			// The Agent's clock ran ahead: stop before the first such window and fetch it again later.
			if !held || hold <= cursor {
				return nil
			}
			to = hold
			kept := ws[:0:0]
			for _, w := range ws {
				if w.T <= to {
					kept = append(kept, w)
				}
			}
			ws = kept
		}
		err = m.store.FoldWindows(ctx, membershipID, cursor, to, usagehistory.Sanitize(ws, now))
		if errors.Is(err, store.ErrUsageCursorMoved) {
			return nil // another pull folded these minutes first
		}
		if err != nil {
			return err
		}
		cursor = to
		if !p.More || to != p.Cursor {
			return nil
		}
	}
	return nil
}

// pullUsageAfterStart is the pull at the next Start. The container has only just been asked to
// start, so the Agent is not up: retry on a slow beat until it answers or the budget ends. The
// minutes it fetches are the previous launches', still on home; the launch id keeps them on
// their own box.
func (m *manager) pullUsageAfterStart(membershipID string, rt runtime.Runtime) {
	ctx, cancel := context.WithTimeout(context.Background(), usageAfterStartBudget)
	defer cancel()
	t := time.NewTicker(usageAfterStartEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if m.pullUsageHistory(ctx, membershipID, rt) == nil {
				return
			}
		}
	}
}

// tickUsageHistory is the sampler's per-workspace step for a workspace it saw `state` in at
// observedAt. It keeps the launch record true to what the runtime says and counts running
// seconds; it does not pull (the sweep pulls after its accounting, see pullJobs).
func (m *manager) tickUsageHistory(ctx context.Context, ws store.Workspace, rt runtime.Runtime, state string, observedAt time.Time) bool {
	mp, ok := usageHistoryRuntime(rt)
	if !ok {
		return false
	}
	l, open, err := m.store.OpenLaunch(ctx, ws.MembershipID)
	if err != nil {
		log.Printf("usage history: open launch (ws=%s): %v", ws.ID, err)
		return false
	}
	switch state {
	case "running":
		if !open {
			// A stop this sampler inferred can be wrong: ecs-ec2 answers "none" when it cannot
			// read the home volume, and "stopped" in the moment between a recorded launch and
			// its Start. The same container is then still running, on the box and under the
			// override its launch recorded, so that launch is reopened rather than replaced by
			// an adopted one, which would drop its hours from the boost budget.
			// An error ends this tick without adopting: an adopted launch would stay open and
			// stop the next tick from trying again.
			window := max(launchReopenWindow, 3*m.usageInterval)
			rl, ok, err := m.store.ReopenLaunch(ctx, ws.MembershipID, observedAt.Unix(), observedAt.Add(-window).Unix())
			if err != nil {
				log.Printf("usage history: reopen launch (ws=%s): %v", ws.ID, err)
				return false
			}
			if ok {
				l, open = rl, true
			} else if l, open, err = m.store.OpenLaunch(ctx, ws.MembershipID); err != nil {
				return false // another replica may have reopened it; the next tick sees which
			}
		}
		if !open {
			box := mp.MachineProfile()
			now := observedAt.Unix()
			l = store.Launch{
				ID: newLaunchID(), WorkspaceID: ws.ID, MembershipID: ws.MembershipID, TenantID: ws.TenantID,
				StartedAt: now, InstanceType: box.InstanceType, ClassID: box.ClassID, Override: usagehistory.OverrideAdopted,
				VCPU: box.VCPU, BaselinePct: box.BurstBaselinePct,
			}
			if err := m.store.BeginLaunch(ctx, l); err != nil {
				log.Printf("usage history: adopt running launch (ws=%s): %v", ws.ID, err)
				return false
			}
		}
		// The reading says nothing about a launch begun after it (the CP stopped the one it saw
		// and started another meanwhile): touching that one would give a Start that then fails
		// running time it never had. A launch is touched up to the moment it was seen, no later.
		if l.StartedAt > observedAt.Unix() {
			return true
		}
		if err := m.store.TouchLaunch(ctx, l.ID, observedAt.Unix()); err != nil {
			log.Printf("usage history: touch launch (ws=%s): %v", ws.ID, err)
		}
		m.foldRunning(ctx, ws.MembershipID)
		return true
	case "stopped", "none":
		// A stop the CP did not issue (a crashed task, a host reboot). It ended where it was last
		// seen. A launch begun after this state was read is a new one, and is left alone.
		if open && l.StartedAt < observedAt.Unix() {
			m.closeLaunch(ctx, ws.MembershipID, l.LastSeenAt)
		}
	}
	return false
}

// usagePullJob is one running workspace whose minutes the sweep pulls after its accounting.
type usagePullJob struct {
	ws store.Workspace
	rt runtime.Runtime
}

// pullUsageJobs pulls each job within an overall budget (half the sampling interval), like the
// disk readings: this is accounting that must not delay the next tick, and a job the budget
// leaves out is simply pulled by the next sweep from the same cursor.
func (u *usageSampler) pullUsageJobs(ctx context.Context, jobs []usagePullJob) {
	if len(jobs) == 0 {
		return
	}
	bctx, cancel := context.WithTimeout(ctx, u.interval/2)
	defer cancel()
	for _, j := range jobs {
		if bctx.Err() != nil {
			return
		}
		pctx, pcancel := context.WithTimeout(bctx, usageSessionTimeout)
		if err := u.mgr.pullUsageHistory(pctx, j.ws.MembershipID, j.rt); err != nil {
			log.Printf("usage history: pull (ws=%s): %v", j.ws.ID, err)
		}
		pcancel()
	}
}

// pruneUsageHistory drops what is past retention.
func (u *usageSampler) pruneUsageHistory(ctx context.Context, now time.Time) {
	cut := now.AddDate(0, 0, -usagehistory.RetentionDays)
	if err := u.mgr.store.PruneUsageHistory(ctx, cut.Format(usageDayFmt), cut.Unix()); err != nil {
		log.Printf("usage history: prune: %v", err)
	}
}

// --- HTTP: who reads what ---

// usageHistoryFor serves day rows. tenantID != "" scopes to one tenant, membershipID != "" to
// one member; labelled adds the member's identity to each row. The scope is always one of the
// two, or a super admin's deployment-wide view: a caller never reaches rows outside the
// scope its handler established.
func (a adminAPI) usageHistoryFor(w http.ResponseWriter, r *http.Request, tenantID, membershipID string, labelled bool) {
	from, to, aerr := usageRange(r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if from > to {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "from must not be after to"})
		return
	}
	rows, err := a.mgr.store.ListUsageDays(r.Context(), tenantID, membershipID, from, to)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	days := make([]usagehistory.DayWire, 0, len(rows))
	for _, row := range rows {
		if labelled && isSystemTenantSlug(row.TenantSlug) {
			continue // a reserved tenant's usage is not a person's (system_tenant.go)
		}
		days = append(days, usagehistory.DayOf(row, labelled))
	}
	writeJSON(w, http.StatusOK, usagehistory.Response{From: from, To: to, RetentionDays: usagehistory.RetentionDays, Days: days})
}

// myUsageHistory (GET /api/usage/me/history?from=&to=) — the caller's own history. Scoped by
// membership alone, not by tenant, like the other /me usage views: rows of a destroyed
// workspace are still the caller's.
func (a adminAPI) myUsageHistory(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	a.usageHistoryFor(w, r, "", mv.MembershipID, false)
}

// adminUsageHistory (GET /api/admin/usage/history?tenant=&from=&to=) — a tenant's history for
// its administrator (a super admin may omit tenant for every tenant), the same gate as
// /api/admin/usage.
func (a adminAPI) adminUsageHistory(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := a.tenantScope(w, r)
	if !ok {
		return
	}
	a.usageHistoryFor(w, r, tenantID, "", true)
}

// memberUsageHistory (GET /api/admin/tenants/{slug}/members/{key}/usage-history) — one member,
// on the member detail beside the cost line. Proved by tenantAdminFor + resolveMember, then
// read by membership alone so a destroyed workspace's rows still show (as usage-hourly does).
func (a adminAPI) memberUsageHistory(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.tenantAdminFor(w, r, r.PathValue("slug")); !ok {
		return
	}
	mem, _, _, aerr := a.resolveMember(r, r.PathValue("slug"), r.PathValue("key"))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	a.usageHistoryFor(w, r, "", mem.ID, true)
}
