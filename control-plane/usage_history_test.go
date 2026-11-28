package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// boxRT is a runtime that names a box (the ecs-ec2 capability) and counts its Stops.
type boxRT struct {
	stubRuntime
	box   runtime.WorkspaceMachine
	stops *int
}

func (b boxRT) MachineProfile() runtime.WorkspaceMachine { return b.box }
func (b boxRT) Stop(context.Context) error               { *b.stops++; return nil }

// minuteAgent plays the Agent's /workspace/usage-windows: it honours `since`, the way the real
// one does, so a double pull is only harmless if the CP's cursor is right.
type minuteAgent struct {
	mu    sync.Mutex
	wins  []map[string]any
	calls int
	block chan struct{} // non-nil: every windows request waits on it
	// barrier > 0: a windows request is answered only once that many have arrived, so two pulls
	// both read the same cursor before either folds.
	barrier int
	srv     *httptest.Server
}

func newMinuteAgent(t *testing.T) *minuteAgent {
	t.Helper()
	a := &minuteAgent{}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions":
			_, _ = w.Write([]byte(`{"sessions":[]}`))
		case "/workspace/stats":
			_, _ = w.Write([]byte(`{}`))
		case "/workspace/usage-windows":
			a.mu.Lock()
			a.calls++
			block, barrier := a.block, a.barrier
			a.mu.Unlock()
			for deadline := time.Now().Add(3 * time.Second); barrier > 0 && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
				a.mu.Lock()
				n := a.calls
				a.mu.Unlock()
				if n >= barrier {
					break
				}
			}
			if block != nil {
				select {
				case <-block:
				case <-r.Context().Done():
					return
				}
			}
			var since int64
			fmt.Sscan(r.URL.Query().Get("since"), &since)
			a.mu.Lock()
			out := []map[string]any{}
			cursor := since
			for _, w := range a.wins {
				if t := int64(w["t"].(int64)); t > since {
					out = append(out, w)
					if t > cursor {
						cursor = t
					}
				}
			}
			a.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"windows": out, "cursor": cursor})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *minuteAgent) add(t int64, lid string, extra map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w := map[string]any{"t": t, "lid": lid}
	for k, v := range extra {
		w[k] = v
	}
	a.wins = append(a.wins, w)
}

// usageDaysOf sums a member's rows by instance type, so a test straddling UTC midnight reads the
// same as one that does not.
func usageDaysOf(t *testing.T, st *store.SQL, membershipID string) map[string]store.UsageDay {
	t.Helper()
	rows, err := st.ListUsageDays(context.Background(), "", membershipID, "2000-01-01", "2100-01-01")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.UsageDay{}
	for _, r := range rows {
		k := r.InstanceType
		if r.Override != "" {
			k += "/" + r.Override
		}
		s := out[k]
		s.RunningSecs += r.RunningSecs
		s.CPUMeasuredMin += r.CPUMeasuredMin
		s.WSMeasuredMin += r.WSMeasuredMin
		s.PSIMeasuredMin += r.PSIMeasuredMin
		s.VCPU = r.VCPU
		s.BurstMin = r.BurstMin
		out[k] = s
	}
	return out
}

func historyFixture(t *testing.T, state string) (*store.SQL, *manager, store.Workspace, *minuteAgent, boxRT) {
	t.Helper()
	st, mgr, _, _, ws := usageHourFixture(t)
	ag := newMinuteAgent(t)
	rt := boxRT{
		stubRuntime: stubRuntime{endpoint: ag.srv.URL, state: state},
		box:         runtime.WorkspaceMachine{InstanceType: "t3.large", VCPU: 2, BurstBaselinePct: 40, ClassID: "burst"},
		stops:       new(int),
	}
	mgr.rtFactory = stubFactory{rt: rt}
	return st, mgr, ws, ag, rt
}

// A minute is counted once however many pulls see it (tick, tick, Stop), the Stop's pull keeps a
// launch shorter than a tick, and after a resize the old launch's late minutes stay on the old box.
func TestUsageHistoryCountsEachMinuteOnceAndOnItsOwnBox(t *testing.T) {
	ctx := context.Background()
	st, mgr, ws, ag, rt := historyFixture(t, "running")
	now := time.Now().Unix()

	idA, env := mgr.beginLaunch(ctx, ws, rt, "")
	if idA == "" || env != "AF_LAUNCH_ID="+idA {
		t.Fatalf("launch id / env = %q / %q", idA, env)
	}
	for i := int64(0); i < 5; i++ {
		ag.add(now-600+i*60, idA, map[string]any{"cpu_pct": 80.0, "ws": 1 << 30, "some10_max": 1.0, "full60": 0.1})
	}
	sampler := newUsageSampler(mgr, 5*time.Minute)
	sampler.sample(ctx)
	sampler.sample(ctx) // no new minutes: nothing may be added
	if got := usageDaysOf(t, st, ws.MembershipID)["t3.large"]; got.CPUMeasuredMin != 5 || got.PSIMeasuredMin != 5 {
		t.Fatalf("after two ticks: %+v, want 5 minutes counted once", got)
	}

	// Three more minutes land between ticks and the workspace is stopped: the Stop pulls them.
	for i := int64(0); i < 3; i++ {
		ag.add(now-300+i*60, idA, map[string]any{"cpu_pct": 100.0, "ws": 2 << 30})
	}
	if err := mgr.stopRecorded(ctx, ws, rt, true); err != nil {
		t.Fatal(err)
	}
	if *rt.stops != 1 {
		t.Fatalf("stops = %d", *rt.stops)
	}
	got := usageDaysOf(t, st, ws.MembershipID)["t3.large"]
	// The three late minutes measured CPU and working set but no pressure: those counters stay at 5.
	if got.CPUMeasuredMin != 8 || got.WSMeasuredMin != 8 || got.PSIMeasuredMin != 5 {
		t.Fatalf("after the stop: %+v, want 8 cpu, 8 working-set and 5 pressure minutes", got)
	}
	if _, open, _ := st.OpenLaunch(ctx, ws.MembershipID); open {
		t.Fatal("launch still open after the Stop")
	}
	if got.RunningSecs < 0 || got.VCPU != 2 || got.BurstMin == nil {
		t.Fatalf("declared rung lost: %+v", got)
	}

	// Next start on another box. A's last minutes were still behind the cursor and are fetched now.
	rtB := rt
	rtB.box = runtime.WorkspaceMachine{InstanceType: "m7i.xlarge", VCPU: 4}
	idB, _ := mgr.beginLaunch(ctx, ws, rtB, "")
	ag.add(now-120, idA, map[string]any{"cpu_pct": 10.0})
	ag.add(now-60, idB, map[string]any{"cpu_pct": 20.0, "ws": 1 << 30})
	if err := mgr.pullUsageHistory(ctx, ws.MembershipID, rtB); err != nil {
		t.Fatal(err)
	}
	days := usageDaysOf(t, st, ws.MembershipID)
	if days["t3.large"].CPUMeasuredMin != 9 || days["m7i.xlarge"].CPUMeasuredMin != 1 {
		t.Fatalf("old minute moved boxes: t3.large %+v, m7i.xlarge %+v", days["t3.large"], days["m7i.xlarge"])
	}
	// The new rung declared no baseline: the column is NULL, not a zero.
	if days["m7i.xlarge"].BurstMin != nil {
		t.Fatalf("undeclared baseline read as %d minutes", *days["m7i.xlarge"].BurstMin)
	}
}

// A Stop never waits on an Agent that is slow or gone: the pull is bounded, the Stop is issued
// and the launch is closed either way.
func TestStopDoesNotWaitOnASlowOrAbsentAgent(t *testing.T) {
	ctx := context.Background()
	old := usageStopPullTimeout
	usageStopPullTimeout = 150 * time.Millisecond
	t.Cleanup(func() { usageStopPullTimeout = old })

	for name, setup := range map[string]func(ag *minuteAgent, rt *boxRT){
		"slow": func(ag *minuteAgent, rt *boxRT) { ag.block = make(chan struct{}) },
		"gone": func(ag *minuteAgent, rt *boxRT) { rt.stubRuntime.endpoint = "http://127.0.0.1:1" },
	} {
		t.Run(name, func(t *testing.T) {
			st, mgr, ws, ag, rt := historyFixture(t, "running")
			setup(ag, &rt)
			t.Cleanup(func() {
				if ag.block != nil {
					close(ag.block)
				}
			})
			mgr.beginLaunch(ctx, ws, rt, "")
			begin := time.Now()
			if err := mgr.stopRecorded(ctx, ws, rt, true); err != nil {
				t.Fatal(err)
			}
			if d := time.Since(begin); d > 2*time.Second {
				t.Fatalf("Stop took %v waiting for the Agent", d)
			}
			if *rt.stops != 1 {
				t.Fatalf("stops = %d, the Stop was not issued", *rt.stops)
			}
			if _, open, _ := st.OpenLaunch(ctx, ws.MembershipID); open {
				t.Fatal("launch left open")
			}
		})
	}
}

// A workspace that died without a CP Stop is closed where it was last seen, so the gap until
// its next start is not counted as running; a launch begun after the state was read is left alone.
func TestSamplerClosesAStoppedLaunchButNotANewerOne(t *testing.T) {
	ctx := context.Background()
	st, mgr, ws, _, rt := historyFixture(t, "stopped")
	id, _ := mgr.beginLaunch(ctx, ws, rt, "")
	l, _, _ := st.GetLaunch(ctx, id)
	if err := st.TouchLaunch(ctx, id, l.StartedAt+120); err != nil {
		t.Fatal(err)
	}

	// The state was read BEFORE this launch began (a Start raced the sweep): hands off.
	mgr.tickUsageHistory(ctx, ws, rt, "stopped", time.Unix(l.StartedAt-5, 0))
	if _, open, _ := st.OpenLaunch(ctx, ws.MembershipID); !open {
		t.Fatal("the sweep closed a launch that began after it read the state")
	}
	mgr.tickUsageHistory(ctx, ws, rt, "stopped", time.Unix(l.StartedAt+500, 0))
	got, _, _ := st.GetLaunch(ctx, id)
	if got.StoppedAt != l.StartedAt+120 {
		t.Fatalf("closed at %d, want its last-seen %d", got.StoppedAt, l.StartedAt+120)
	}
	if secs := usageDaysOf(t, st, ws.MembershipID)["t3.large"].RunningSecs; secs != 120 {
		t.Fatalf("running secs = %d, want 120 (the dead time must not count)", secs)
	}
	// "starting" leaves an open launch alone.
	id2, _ := mgr.beginLaunch(ctx, ws, rt, "")
	mgr.tickUsageHistory(ctx, ws, rt, "starting", time.Now().Add(time.Hour))
	if l2, _, _ := st.GetLaunch(ctx, id2); l2.StoppedAt != 0 {
		t.Fatal("a starting workspace's launch was closed")
	}
}

// A running workspace with no launch record (CP restart mid-start, or one that predates the
// ledger) gets running time, flagged as adopted, and no measured minutes.
func TestSamplerAdoptsARunningWorkspaceWithNoLaunch(t *testing.T) {
	ctx := context.Background()
	st, mgr, ws, _, _ := historyFixture(t, "running")
	newUsageSampler(mgr, 5*time.Minute).sample(ctx)
	l, open, _ := st.OpenLaunch(ctx, ws.MembershipID)
	if !open || l.Override != "adopted" || l.InstanceType != "t3.large" {
		t.Fatalf("adopted launch = %+v open=%v", l, open)
	}
	// Its seconds are folded like any launch's, onto a row of its own.
	if err := st.TouchLaunch(ctx, l.ID, l.StartedAt+90); err != nil {
		t.Fatal(err)
	}
	mgr.foldRunning(ctx, ws.MembershipID)
	if d := usageDaysOf(t, st, ws.MembershipID)["t3.large/adopted"]; d.RunningSecs != 90 || d.CPUMeasuredMin != 0 {
		t.Fatalf("adopted row = %+v", d)
	}
}

// A runtime with no box to name keeps no history and is not handed a launch id.
func TestNoLaunchRecordedWhereTheRuntimeNamesNoBox(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, _, ws := usageHourFixture(t)
	rt := stubRuntime{}
	mgr.rtFactory = stubFactory{rt: rt}
	if id, env := mgr.beginLaunch(ctx, ws, rt, ""); id != "" || env != "" {
		t.Fatalf("launch for a boxless runtime: %q %q", id, env)
	}
	newUsageSampler(mgr, 5*time.Minute).sample(ctx)
	if _, open, _ := st.OpenLaunch(ctx, ws.MembershipID); open {
		t.Fatal("sampler opened a launch for a boxless runtime")
	}
	if err := mgr.stopRecorded(ctx, ws, rt, true); err != nil {
		t.Fatal(err)
	}
}

func TestPullIgnoresAnAgentThatAnswersBehindTheCursor(t *testing.T) {
	ctx := context.Background()
	st, mgr, ws, ag, rt := historyFixture(t, "running")
	id, _ := mgr.beginLaunch(ctx, ws, rt, "")
	now := time.Now().Unix()
	ag.add(now-60, id, map[string]any{"cpu_pct": 5.0})
	if err := mgr.pullUsageHistory(ctx, ws.MembershipID, rt); err != nil {
		t.Fatal(err)
	}
	// An Agent whose clock went back answers with a lower cursor and the same minute again.
	ag.mu.Lock()
	ag.wins = append(ag.wins, map[string]any{"t": now - 600, "lid": id, "cpu_pct": 99.0})
	ag.mu.Unlock()
	_ = mgr.pullUsageHistory(ctx, ws.MembershipID, rt)
	if got := usageDaysOf(t, st, ws.MembershipID)["t3.large"].CPUMeasuredMin; got != 1 {
		t.Fatalf("cpu minutes = %d", got)
	}
}

// --- API scope and null-vs-zero ---

func decodeUsageHistory(t *testing.T, h func(http.ResponseWriter, *http.Request), url, email string) (int, []map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, url, nil)
	r.Header.Set("X-Forwarded-Email", email)
	w := httptest.NewRecorder()
	h(w, r)
	var body struct {
		Days []map[string]any `json:"days"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body.Days
}

func TestUsageHistoryAPIScopeAndUnmeasuredIsNull(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, wmem := cloudCostFixture(t)
	other, _ := st.CreateTenant(ctx, "ops", "運用")
	oi, _ := st.UpsertIdentity(ctx, "o@x.co.jp", "o-x-co-jp", "")
	om, _ := st.EnsureMembership(ctx, oi.ID, other.ID, "member")
	// Pin the launch to a fixed hour-of-day inside one UTC day (yesterday 12:00-13:00Z): a launch that
	// starts at now-1h straddles UTC midnight for the first hour of every day and then folds into
	// two day rows, so the test would depend on the wall-clock time of day.
	// Truncate(24h) aligns on UTC midnights (the zero time is a UTC midnight), whatever the local zone.
	start := time.Now().UTC().Truncate(24 * time.Hour).Add(-12 * time.Hour)
	end := start.Add(time.Hour)
	day := start.Format("2006-01-02")
	seed := func(m store.Membership, tenant string, vcpu int, withWindow bool) {
		l := store.Launch{ID: "L-" + m.ID, WorkspaceID: "w-" + m.ID, MembershipID: m.ID, TenantID: tenant,
			StartedAt: start.Unix(), InstanceType: "m7i.large", VCPU: vcpu}
		if err := st.BeginLaunch(ctx, l); err != nil {
			t.Fatal(err)
		}
		_ = st.TouchLaunch(ctx, l.ID, end.Unix())
		_ = st.FoldRunning(ctx, m.ID)
		if withWindow {
			ws := uint64(3 << 30)
			c := 150.0
			_ = st.FoldWindows(ctx, m.ID, 0, end.Unix(), []store.UsageWindow{{T: end.Unix() - 60, LID: l.ID, CPUPct: &c, WorkingSet: &ws}})
		}
	}
	seed(wmem, tn.ID, 2, true)
	seed(om, other.ID, 0, false)
	api := newAdminAPI(mgr)

	// A tenant administrator reads their tenant, with the member named, and nothing else.
	code, days := decodeUsageHistory(t, api.adminUsageHistory, "/api/admin/usage/history?tenant=sales", "boss@acme.co.jp")
	if code != 200 || len(days) != 1 || days[0]["user_key"] != "w-acme-co-jp" || days[0]["day"] != day {
		t.Fatalf("tenant admin: %d %v", code, days)
	}
	row := days[0]
	if row["cpu_box_p95"] == nil || row["working_set_p95"] == nil || row["running_minutes"].(float64) < 50 {
		t.Fatalf("measured figures missing: %v", row)
	}
	// Cross-tenant: another tenant, and the deployment-wide view, are refused.
	if code, _ := decodeUsageHistory(t, api.adminUsageHistory, "/api/admin/usage/history?tenant=ops", "boss@acme.co.jp"); code != 403 {
		t.Fatalf("other tenant: %d", code)
	}
	if code, _ := decodeUsageHistory(t, api.adminUsageHistory, "/api/admin/usage/history", "boss@acme.co.jp"); code != 403 {
		t.Fatalf("deployment-wide view for a tenant admin: %d", code)
	}
	// A plain member cannot reach the admin view.
	if code, _ := decodeUsageHistory(t, api.adminUsageHistory, "/api/admin/usage/history?tenant=sales", "w@acme.co.jp"); code != 403 {
		t.Fatalf("member on the admin view: %d", code)
	}

	// The member sees their own rows only, unlabelled.
	r := httptest.NewRequest(http.MethodGet, "/api/usage/me/history", nil)
	w := httptest.NewRecorder()
	api.myUsageHistory(w, r, store.Identity{}, store.MembershipView{MembershipID: om.ID, TenantID: other.ID, TenantSlug: "ops"})
	var mine struct{ Days []map[string]any }
	_ = json.Unmarshal(w.Body.Bytes(), &mine)
	if len(mine.Days) != 1 {
		t.Fatalf("member rows = %v", mine.Days)
	}
	if _, has := mine.Days[0]["user_key"]; has {
		t.Fatalf("own view carries a label: %v", mine.Days[0])
	}
	// ops' row has no window and no declared vCPU: every unmeasured figure is an explicit null,
	// the measured-minute counters are 0, and running time is still there.
	for _, k := range []string{"cpu_p50", "cpu_p95", "cpu_box_p50", "cpu_box_p95", "working_set_p95", "working_set_max",
		"psi_full_avg60_max", "psi_warn_minutes", "refault_p95", "above_baseline_minutes", "vcpu", "burst_baseline_pct"} {
		v, has := mine.Days[0][k]
		if !has || v != nil {
			t.Errorf("%s = %v (present=%v), want an explicit null", k, v, has)
		}
	}
	m := mine.Days[0]["measured_minutes"].(map[string]any)
	keys := make([]string, 0)
	for k, v := range m {
		keys = append(keys, k)
		if v.(float64) != 0 {
			t.Errorf("measured_minutes.%s = %v", k, v)
		}
	}
	sort.Strings(keys)
	if len(keys) != 4 {
		t.Errorf("measured minute keys = %v", keys)
	}
	if mine.Days[0]["running_minutes"].(float64) < 50 {
		t.Errorf("running minutes = %v", mine.Days[0]["running_minutes"])
	}
}

// A tick's pull and a Stop's pull that both start from the same cursor: one folds, the other drops
// what it fetched, and no minute is counted twice.
func TestConcurrentPullsCountEachMinuteOnce(t *testing.T) {
	ctx := context.Background()
	st, mgr, ws, ag, rt := historyFixture(t, "running")
	id, _ := mgr.beginLaunch(ctx, ws, rt, "")
	now := time.Now().Unix()
	for i := int64(0); i < 3; i++ {
		ag.add(now-300+i*60, id, map[string]any{"cpu_pct": 10.0})
	}
	ag.mu.Lock()
	ag.barrier = 2
	ag.mu.Unlock()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := mgr.pullUsageHistory(ctx, ws.MembershipID, rt); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := usageDaysOf(t, st, ws.MembershipID)["t3.large"].CPUMeasuredMin; got != 3 {
		t.Fatalf("cpu minutes = %d, want 3 (each minute once)", got)
	}
}

// An Agent whose clock ran ahead: the cursor stops before its future windows, so they are fetched
// again, not lost, once the clock is right.
func TestPullHoldsTheCursorBeforeWindowsFromTheFuture(t *testing.T) {
	ctx := context.Background()
	st, mgr, ws, ag, rt := historyFixture(t, "running")
	id, _ := mgr.beginLaunch(ctx, ws, rt, "")
	now := time.Now().Unix()
	ag.add(now-120, id, map[string]any{"cpu_pct": 10.0})
	ag.add(now-60, id, map[string]any{"cpu_pct": 10.0})
	ag.add(now+3600, id, map[string]any{"cpu_pct": 10.0})
	for i := 0; i < 2; i++ {
		if err := mgr.pullUsageHistory(ctx, ws.MembershipID, rt); err != nil {
			t.Fatal(err)
		}
	}
	if c, _ := st.UsagePullCursor(ctx, ws.MembershipID); c != now-60 {
		t.Fatalf("cursor = %d, want %d (just before the future window)", c, now-60)
	}
	if got := usageDaysOf(t, st, ws.MembershipID)["t3.large"].CPUMeasuredMin; got != 2 {
		t.Fatalf("cpu minutes = %d", got)
	}
}

// Where the Agent is the reason for the stop, it is not asked.
func TestStopWithoutPullDoesNotCallTheAgent(t *testing.T) {
	ctx := context.Background()
	_, mgr, ws, ag, rt := historyFixture(t, "running")
	mgr.beginLaunch(ctx, ws, rt, "")
	if err := mgr.stopRecorded(ctx, ws, rt, false); err != nil {
		t.Fatal(err)
	}
	ag.mu.Lock()
	defer ag.mu.Unlock()
	if ag.calls != 0 || *rt.stops != 1 {
		t.Fatalf("agent calls %d stops %d", ag.calls, *rt.stops)
	}
}

func TestUsageHistoryRefusesAnInvertedRange(t *testing.T) {
	_, mgr, _, _ := cloudCostFixture(t)
	code, _ := decodeUsageHistory(t, newAdminAPI(mgr).adminUsageHistory,
		"/api/admin/usage/history?tenant=sales&from=2026-12-01&to=2026-01-01", "boss@acme.co.jp")
	if code != 400 {
		t.Fatalf("inverted range = %d", code)
	}
}

// A stop the sampler inferred wrongly (ecs-ec2 answers "none" when one read of the home volume
// fails) is taken back when the same container is seen running again: the launch keeps its
// box, its override and its hours, instead of the rest of its run going to an adopted launch
// the boost budget does not count.
func TestSamplerReopensALaunchItClosedOnATransientState(t *testing.T) {
	ctx := context.Background()
	st, mgr, ws, _, rt := historyFixture(t, "running")
	id, _ := mgr.beginLaunch(ctx, ws, rt, "boost")
	l, _, _ := st.GetLaunch(ctx, id)
	if err := st.TouchLaunch(ctx, id, l.StartedAt+600); err != nil {
		t.Fatal(err)
	}
	mgr.tickUsageHistory(ctx, ws, rt, "none", time.Unix(l.StartedAt+900, 0))
	if _, open, _ := st.OpenLaunch(ctx, ws.MembershipID); open {
		t.Fatal("the launch is still open after a none")
	}
	mgr.tickUsageHistory(ctx, ws, rt, "running", time.Unix(l.StartedAt+1200, 0))
	got, open, _ := st.OpenLaunch(ctx, ws.MembershipID)
	if !open || got.ID != id || got.Override != "boost" {
		t.Fatalf("after running again: %+v open=%v, want %s reopened", got, open, id)
	}
	// Seen running on: the hours across the false stop count, all as boosted.
	if err := st.TouchLaunch(ctx, id, l.StartedAt+1500); err != nil {
		t.Fatal(err)
	}
	if secs, _ := st.OverrideRunningSecs(ctx, ws.MembershipID, "boost", l.StartedAt, l.StartedAt+1e6); secs != 1500 {
		t.Fatalf("boosted secs = %d, want 1500", secs)
	}

	// A launch the CP itself stopped (closed at the stop, after its last sighting) is not taken
	// back: a workspace running after that is a new launch.
	if _, _, err := st.EndOpenLaunch(ctx, ws.MembershipID, l.StartedAt+1600); err != nil {
		t.Fatal(err)
	}
	mgr.tickUsageHistory(ctx, ws, rt, "running", time.Unix(l.StartedAt+1700, 0))
	if again, _, _ := st.OpenLaunch(ctx, ws.MembershipID); again.ID == id || again.Override != "adopted" {
		t.Fatalf("a CP stop was taken back: %+v", again)
	}
	if _, ok, _ := st.ReopenLaunch(ctx, ws.MembershipID, time.Now().Unix()+1e6, 0); ok {
		t.Fatal("reopened while another launch is open")
	}
}

// A stop the sampler inferred longer ago than launchReopenWindow stays a stop.
func TestSamplerDoesNotReopenAnOldStop(t *testing.T) {
	ctx := context.Background()
	st, mgr, ws, _, rt := historyFixture(t, "running")
	id, _ := mgr.beginLaunch(ctx, ws, rt, "boost")
	l, _, _ := st.GetLaunch(ctx, id)
	mgr.tickUsageHistory(ctx, ws, rt, "none", time.Unix(l.StartedAt+60, 0))
	mgr.tickUsageHistory(ctx, ws, rt, "running", time.Unix(l.StartedAt, 0).Add(launchReopenWindow+time.Minute))
	if got, _, _ := st.OpenLaunch(ctx, ws.MembershipID); got.ID == id || got.Override != "adopted" {
		t.Fatalf("an old stop was taken back: %+v", got)
	}
}

// The sampler reading "stopped" in the moment between a recorded launch and its Start closes
// the launch at its start. The Start then comes up: the launch is reopened, not adopted.
func TestSamplerReopensALaunchItClosedBeforeItsStart(t *testing.T) {
	ctx := context.Background()
	st, mgr, ws, _, rt := historyFixture(t, "running")
	id, _ := mgr.beginLaunch(ctx, ws, rt, "boost")
	l, _, _ := st.GetLaunch(ctx, id)
	mgr.tickUsageHistory(ctx, ws, rt, "stopped", time.Unix(l.StartedAt+1, 0))
	if got, _, _ := st.GetLaunch(ctx, id); got.StoppedAt != l.StartedAt {
		t.Fatalf("closed at %d, want its start %d", got.StoppedAt, l.StartedAt)
	}
	mgr.tickUsageHistory(ctx, ws, rt, "running", time.Unix(l.StartedAt+600, 0))
	if got, open, _ := st.OpenLaunch(ctx, ws.MembershipID); !open || got.ID != id || got.Override != "boost" {
		t.Fatalf("%+v open=%v, want %s reopened", got, open, id)
	}
}

// The start deadline stops a launch that never came up: it ran for no time, so the minutes it
// waited count neither as running time nor against a boost budget.
func TestStartDeadlineStopCountsNoTimeForALaunchNeverSeenRunning(t *testing.T) {
	ctx := context.Background()
	st, mgr, ws, _, rt := historyFixture(t, "starting")
	// Recorded half an hour ago, when the Start was asked for, and never seen running since.
	id := "unplaced"
	if err := st.BeginLaunch(ctx, store.Launch{ID: id, WorkspaceID: ws.ID, MembershipID: ws.MembershipID,
		TenantID: ws.TenantID, StartedAt: time.Now().Add(-30 * time.Minute).Unix(), Override: "boost"}); err != nil {
		t.Fatal(err)
	}
	l, _, _ := st.GetLaunch(ctx, id)
	if err := mgr.stopAtLastSighting(ctx, ws, rt); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := st.GetLaunch(ctx, id); got.StoppedAt != l.StartedAt {
		t.Fatalf("closed at %d, want its start %d", got.StoppedAt, l.StartedAt)
	}
	if secs, _ := st.OverrideRunningSecs(ctx, ws.MembershipID, "boost", 0, time.Now().Unix()+3600); secs != 0 {
		t.Fatalf("boosted secs = %d, want 0", secs)
	}
	if *rt.stops != 1 {
		t.Fatalf("stops = %d", *rt.stops)
	}
}

// reopenFailingStore fails ReopenLaunch while fail is set.
type reopenFailingStore struct {
	store.Store
	fail *bool
}

func (s reopenFailingStore) ReopenLaunch(ctx context.Context, membershipID string, observedAt, closedSince int64) (store.Launch, bool, error) {
	if *s.fail {
		return store.Launch{}, false, fmt.Errorf("injected")
	}
	return s.Store.ReopenLaunch(ctx, membershipID, observedAt, closedSince)
}

// A reopen that errors ends the tick: adopting instead would leave an adopted launch open, and
// no later tick would try the reopen again.
func TestSamplerDoesNotAdoptWhenTheReopenFails(t *testing.T) {
	ctx := context.Background()
	st, mgr, ws, _, rt := historyFixture(t, "running")
	fail := false
	mgr.store = reopenFailingStore{Store: st, fail: &fail}
	id, _ := mgr.beginLaunch(ctx, ws, rt, "boost")
	l, _, _ := st.GetLaunch(ctx, id)
	mgr.tickUsageHistory(ctx, ws, rt, "none", time.Unix(l.StartedAt+60, 0))
	fail = true
	mgr.tickUsageHistory(ctx, ws, rt, "running", time.Unix(l.StartedAt+300, 0))
	if got, open, _ := st.OpenLaunch(ctx, ws.MembershipID); open {
		t.Fatalf("a failed reopen adopted: %+v", got)
	}
	fail = false
	mgr.tickUsageHistory(ctx, ws, rt, "running", time.Unix(l.StartedAt+600, 0))
	if got, open, _ := st.OpenLaunch(ctx, ws.MembershipID); !open || got.ID != id {
		t.Fatalf("the next tick did not reopen: %+v open=%v", got, open)
	}
}

// A "running" read before a launch that then failed its Start is stale: it does not reopen it.
func TestSamplerDoesNotReopenALaunchAfterItsReading(t *testing.T) {
	ctx := context.Background()
	st, mgr, ws, _, rt := historyFixture(t, "running")
	observed := time.Now().Add(-time.Second)
	id, _ := mgr.beginLaunch(ctx, ws, rt, "boost")
	mgr.endLaunchFailed(ctx, ws.MembershipID, id)
	mgr.tickUsageHistory(ctx, ws, rt, "running", observed)
	if got, _, _ := st.OpenLaunch(ctx, ws.MembershipID); got.ID == id {
		t.Fatalf("a failed launch newer than the reading was reopened: %+v", got)
	}
}

// With a sampling interval longer than the hour, a single bad reading is still taken back on the
// next good one, three intervals after the last.
func TestSamplerReopenWindowFollowsALongInterval(t *testing.T) {
	ctx := context.Background()
	st, mgr, ws, _, rt := historyFixture(t, "running")
	mgr.usageInterval = 40 * time.Minute
	id, _ := mgr.beginLaunch(ctx, ws, rt, "boost")
	l, _, _ := st.GetLaunch(ctx, id)
	mgr.tickUsageHistory(ctx, ws, rt, "none", time.Unix(l.StartedAt+40*60, 0))
	mgr.tickUsageHistory(ctx, ws, rt, "running", time.Unix(l.StartedAt+80*60, 0))
	if got, open, _ := st.OpenLaunch(ctx, ws.MembershipID); !open || got.ID != id {
		t.Fatalf("not reopened after 80 minutes at a 40-minute interval: %+v open=%v", got, open)
	}
}

// taskBoxRT is a boxRT that counts its tasks, the ecs-ec2 capability neverCameUp reads.
type taskBoxRT struct {
	boxRT
	tasks int
}

func (r taskBoxRT) RunningTasks(context.Context) (int, error) { return r.tasks, nil }

// A Stop of a launch still starting (or whose start claim lapsed back to stopped) with no task up
// ends it at its last sighting: the minutes spent waiting for a placement are not running time.
// With a task up, the boot counts.
func TestStopOfALaunchThatNeverCameUp(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		state string
		tasks int
		zero  bool
	}{{"starting", 0, true}, {"starting", 1, false}, {"stopped", 0, true}, {"stopped", 1, false}, {"running", 0, false}} {
		st, mgr, ws, _, rt := historyFixture(t, c.state)
		trt := taskBoxRT{boxRT: rt, tasks: c.tasks}
		if err := st.BeginLaunch(ctx, store.Launch{ID: "L", WorkspaceID: ws.ID, MembershipID: ws.MembershipID,
			TenantID: ws.TenantID, StartedAt: time.Now().Add(-15 * time.Minute).Unix(), Override: "boost"}); err != nil {
			t.Fatal(err)
		}
		if err := mgr.stopRecorded(ctx, ws, trt, false); err != nil {
			t.Fatal(err)
		}
		secs, _ := st.OverrideRunningSecs(ctx, ws.MembershipID, "boost", 0, time.Now().Unix()+60)
		if (secs == 0) != c.zero {
			t.Fatalf("%s with %d tasks: %d boosted secs, want zero=%t", c.state, c.tasks, secs, c.zero)
		}
	}
}

// A "running" read before a launch began says nothing about it: a stale reading that lands after
// the CP stopped the launch it saw and recorded the next one does not touch the new one, so a
// Start of it that then fails is left with no running time.
func TestStaleRunningReadingDoesNotTouchANewerLaunch(t *testing.T) {
	ctx := context.Background()
	st, mgr, ws, _, rt := historyFixture(t, "running")
	observed := time.Now().Add(-30 * time.Second)
	if err := st.BeginLaunch(ctx, store.Launch{ID: "A", WorkspaceID: ws.ID, MembershipID: ws.MembershipID,
		TenantID: ws.TenantID, StartedAt: observed.Add(-time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.EndOpenLaunch(ctx, ws.MembershipID, observed.Add(10*time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	// B: recorded after the reading, its Start still under way when the reading lands.
	id := "B"
	if err := st.BeginLaunch(ctx, store.Launch{ID: id, WorkspaceID: ws.ID, MembershipID: ws.MembershipID,
		TenantID: ws.TenantID, StartedAt: observed.Add(15 * time.Second).Unix(), Override: "boost"}); err != nil {
		t.Fatal(err)
	}
	mgr.tickUsageHistory(ctx, ws, rt, "running", observed)
	mgr.endLaunchFailed(ctx, ws.MembershipID, id)
	if secs, _ := st.OverrideRunningSecs(ctx, ws.MembershipID, "boost", 0, time.Now().Unix()+3600); secs != 0 {
		t.Fatalf("the failed launch has %d boosted secs, want 0", secs)
	}
	if a, _, _ := st.GetLaunch(ctx, "A"); a.StoppedAt != observed.Add(10*time.Second).Unix() {
		t.Fatalf("A = %+v, the stale reading reopened or moved it", a)
	}
}

// slowStateRT answers State only when its context ends, like an AWS read that hangs.
type slowStateRT struct {
	taskBoxRT
	stopCtxErr *error
}

func (r slowStateRT) State(ctx context.Context) string {
	<-ctx.Done()
	return "none"
}

func (r slowStateRT) Stop(ctx context.Context) error {
	*r.stopCtxErr = ctx.Err()
	*r.stops++
	return nil
}

// The reads made before a Stop for the accounting are bounded on their own: one that hangs costs
// at most usageStopProbeTimeout, and the Stop still runs on the caller's live context.
func TestStopIsNotHeldByTheAccountingRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	old := usageStopProbeTimeout
	usageStopProbeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { usageStopProbeTimeout = old })
	st, mgr, ws, _, rt := historyFixture(t, "running")
	var stopErr error
	srt := slowStateRT{taskBoxRT: taskBoxRT{boxRT: rt}, stopCtxErr: &stopErr}
	if err := st.BeginLaunch(ctx, store.Launch{ID: "L", WorkspaceID: ws.ID, MembershipID: ws.MembershipID,
		TenantID: ws.TenantID, StartedAt: time.Now().Add(-10 * time.Minute).Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.stopRecorded(ctx, ws, srt, false); err != nil {
		t.Fatal(err)
	}
	if *rt.stops != 1 || stopErr != nil {
		t.Fatalf("stops=%d, stop context err=%v", *rt.stops, stopErr)
	}
	// A read that could not finish is no evidence: the launch counts to the stop.
	if l, _, _ := st.GetLaunch(ctx, "L"); l.StoppedAt-l.StartedAt < 9*60 {
		t.Fatalf("closed at %d, %ds after its start, want the stop", l.StoppedAt, l.StoppedAt-l.StartedAt)
	}
}

// When the caller's own deadline is nearer than usageStopProbeTimeout, the read gets at most half
// of what is left, and the Stop still runs on a live context.
func TestStopKeepsHalfOfACloseDeadline(t *testing.T) {
	old := usageStopMinRemaining
	usageStopMinRemaining = 0
	t.Cleanup(func() { usageStopMinRemaining = old })
	st, mgr, ws, _, rt := historyFixture(t, "running")
	if err := st.BeginLaunch(context.Background(), store.Launch{ID: "L", WorkspaceID: ws.ID, MembershipID: ws.MembershipID,
		TenantID: ws.TenantID, StartedAt: time.Now().Add(-10 * time.Minute).Unix()}); err != nil {
		t.Fatal(err)
	}
	var stopErr error
	srt := slowStateRT{taskBoxRT: taskBoxRT{boxRT: rt}, stopCtxErr: &stopErr}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	if err := mgr.stopRecorded(ctx, ws, srt, false); err != nil {
		t.Fatal(err)
	}
	if *rt.stops != 1 || stopErr != nil {
		t.Fatalf("stops=%d, stop context err=%v", *rt.stops, stopErr)
	}
	// A shorter deadline still leaves the Stop its half.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel2()
	if err := mgr.stopRecorded(ctx2, ws, srt, false); err != nil {
		t.Fatal(err)
	}
	if *rt.stops != 2 || stopErr != nil {
		t.Fatalf("stops=%d, stop context err=%v", *rt.stops, stopErr)
	}
}

// countingStateRT counts State calls; a nil-deadline-free read would block on slowStateRT.
type countingStateRT struct {
	slowStateRT
	reads *int
}

func (r countingStateRT) State(ctx context.Context) string {
	*r.reads++
	return r.slowStateRT.State(ctx)
}

// Below usageStopMinRemaining the accounting read is skipped altogether, so a Stop with a close
// deadline keeps all of it; the launch then counts to the stop, as for any read that cannot tell.
func TestStopSkipsTheAccountingReadNearTheDeadline(t *testing.T) {
	st, mgr, ws, _, rt := historyFixture(t, "running")
	if err := st.BeginLaunch(context.Background(), store.Launch{ID: "L", WorkspaceID: ws.ID, MembershipID: ws.MembershipID,
		TenantID: ws.TenantID, StartedAt: time.Now().Add(-10 * time.Minute).Unix()}); err != nil {
		t.Fatal(err)
	}
	var stopErr error
	reads := 0
	srt := countingStateRT{slowStateRT{taskBoxRT: taskBoxRT{boxRT: rt}, stopCtxErr: &stopErr}, &reads}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	if err := mgr.stopRecorded(ctx, ws, srt, false); err != nil {
		t.Fatal(err)
	}
	if reads != 0 || *rt.stops != 1 || stopErr != nil {
		t.Fatalf("reads=%d stops=%d stopErr=%v, want no read and a live Stop", reads, *rt.stops, stopErr)
	}
	if l, _, _ := st.GetLaunch(context.Background(), "L"); l.StoppedAt-l.StartedAt < 9*60 {
		t.Fatalf("closed %ds after its start, want the stop", l.StoppedAt-l.StartedAt)
	}
}

// probeRT records the context State is read with and reports a launch that never came up.
type probeRT struct {
	taskBoxRT
	reads    int
	budget   time.Duration
	hasLimit bool
}

func (r *probeRT) State(ctx context.Context) string {
	r.reads++
	if dl, ok := ctx.Deadline(); ok {
		r.hasLimit, r.budget = true, time.Until(dl)
	}
	return "stopped"
}

// With the default margin and enough deadline left, the read still happens and gets at most half
// of what is left; with no deadline it gets the plain probe timeout. Guards against the skip
// swallowing every deadline-carrying context.
func TestNoTaskUpReadsWhenTheDeadlineAllowsIt(t *testing.T) {
	_, _, _, _, rt := historyFixture(t, "stopped")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	p := &probeRT{taskBoxRT: taskBoxRT{boxRT: rt}}
	if !noTaskUp(ctx, p) {
		t.Fatal("noTaskUp = false, want true: stopped with no task up")
	}
	if p.reads != 1 || !p.hasLimit || p.budget > 4*time.Second+50*time.Millisecond {
		t.Fatalf("reads=%d limited=%v budget=%v, want one read bounded to about half of 8s", p.reads, p.hasLimit, p.budget)
	}
	p2 := &probeRT{taskBoxRT: taskBoxRT{boxRT: rt}}
	if !noTaskUp(context.Background(), p2) || p2.reads != 1 || p2.budget > usageStopProbeTimeout {
		t.Fatalf("no deadline: reads=%d budget=%v", p2.reads, p2.budget)
	}
}
