package main

// The status panel's material (ADR 0071): the hourly occupancy sampler and the "when does this
// stop by itself" prediction.
//
// What is pinned here is one property above all others: THE PANEL MUST NOT INVENT AN ANSWER.
// The engine is a $1.26/hour GPU that is asleep most of the time, and every number on the screen
// is one an operator may act on. Three ways of inventing one are tested against directly —
// filling an unobserved hour with the state that happens to be current, counting down to a stop
// that will not happen, and reconstructing a denominator the sampler never measured.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// fakeEngineHours records what the controller sampled, keyed by hour.
type fakeEngineHours struct {
	rows  map[string]store.EngineHourCounters
	calls int
	err   error
}

func newFakeEngineHours() *fakeEngineHours {
	return &fakeEngineHours{rows: map[string]store.EngineHourCounters{}}
}

func (f *fakeEngineHours) AddEngineHour(_ context.Context, _, hour string, d store.EngineHourCounters) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	c := f.rows[hour]
	c.Samples += d.Samples
	c.ObservedSecs += d.ObservedSecs
	c.RunningSecs += d.RunningSecs
	c.StartingSecs += d.StartingSecs
	c.DrainingSecs += d.DrainingSecs
	f.rows[hour] = c
	return nil
}

// The three billing states have to stay apart. Folding starting and draining into running would
// claim the engine served requests during its cold start (165-197 s measured) and during the
// several minutes Managed Instances keeps the instance after the task is gone (427-477 s
// measured); dropping them would hide money that was spent.
func TestEngineUptimeSampleKeepsTheBillingStatesApart(t *testing.T) {
	for _, tc := range []struct {
		state                     string
		run, start, drain, observ int
	}{
		{"running", 30, 0, 0, 30},
		{"starting", 0, 30, 0, 30},
		{"draining", 0, 0, 30, 30},
		{"stopped", 0, 0, 0, 30},
		// A service that is INACTIVE or missing was still LOOKED AT. Recording the observation
		// with no time in any state is what makes that hour grey ("watched, nothing there")
		// rather than blank ("never watched").
		{"none", 0, 0, 0, 30},
	} {
		got := engineUptimeSample(tc.state, 30)
		if got.RunningSecs != tc.run || got.StartingSecs != tc.start || got.DrainingSecs != tc.drain {
			t.Errorf("%s: running=%d starting=%d draining=%d, want %d/%d/%d",
				tc.state, got.RunningSecs, got.StartingSecs, got.DrainingSecs, tc.run, tc.start, tc.drain)
		}
		if got.ObservedSecs != tc.observ || got.Samples != 1 {
			t.Errorf("%s: observed=%d samples=%d, want %d/1 — every tick is an observation",
				tc.state, got.ObservedSecs, got.Samples, tc.observ)
		}
	}
}

// The one thing that would make the heatmap lie: a control plane that was down for an hour comes
// back with a perfectly valid current state and a large elapsed time, and attributing that gap to
// what it happens to see now fills the outage with confident colour.
//
// The rule is that a tick claims AT MOST one interval and the first tick of a process claims
// nothing at all. The cost is one lost tick per CP start; the alternative is a panel that invents
// history.
func TestEngineUptimeNeverClaimsTimeItDidNotWatch(t *testing.T) {
	rec := newFakeEngineHours()
	c := &engineController{
		eng:    &engineECS{key: "llm"},
		cfg:    engineControlCfg{interval: 30 * time.Second},
		uptime: rec,
	}
	base := time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)

	// First tick of the process: nothing to vouch for yet.
	c.recordUptime(t.Context(), base, "running")
	if rec.calls != 0 {
		t.Fatalf("the first tick recorded %d row(s); it has no previous observation to measure from", rec.calls)
	}

	// A normal tick claims exactly the elapsed time.
	c.recordUptime(t.Context(), base.Add(30*time.Second), "running")
	if got := rec.rows["2026-09-08T04"].ObservedSecs; got != 30 {
		t.Errorf("observed=%d, want 30", got)
	}

	// A busy tick (engineControlBusyInterval) claims only the 5 seconds it covers. Reconstructing
	// this later as samples x nominal interval is what would push a busy hour past 100%.
	c.recordUptime(t.Context(), base.Add(35*time.Second), "starting")
	if got := rec.rows["2026-09-08T04"]; got.StartingSecs != 5 || got.ObservedSecs != 35 {
		t.Errorf("after a 5 s tick: starting=%d observed=%d, want 5/35", got.StartingSecs, got.ObservedSecs)
	}

	// The outage: an hour of silence, then a tick. It may claim one interval, never the gap.
	c.recordUptime(t.Context(), base.Add(time.Hour+35*time.Second), "running")
	gap := rec.rows["2026-09-08T05"]
	if gap.ObservedSecs != 30 {
		t.Errorf("after an hour off the air the tick claimed %d s; it may claim one interval (30), "+
			"or a control-plane outage renders as observed history", gap.ObservedSecs)
	}
	// And the hour nobody watched must have NO row at all — the absence is what the UI draws
	// as blank, and a zero-filled row would draw as "the GPU was idle".
	if _, ok := rec.rows["2026-09-08T04"]; !ok {
		t.Fatal("the watched hour lost its row")
	}
	if c, ok := rec.rows["2026-09-08T05"]; !ok || c.Samples != 1 {
		t.Fatalf("the hour the tick landed in should hold exactly that tick: %+v", c)
	}
}

// The countdown must not promise a stop that will not happen. Each refusal below is a case where
// a plausible-looking time would be read as a schedule.
func TestEngineStopETAOnlyWhenItWillActuallyStop(t *testing.T) {
	now := time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)
	last := now.Add(-5 * time.Minute)
	cfg := engineControlCfg{idle: 30 * time.Minute, deadline: 15 * time.Minute}

	if eta := engineStopETA(engineModeOnDemand, true, last, cfg); !eta.Equal(last.Add(30 * time.Minute)) {
		t.Errorf("ondemand and up: eta=%v, want %v", eta, last.Add(30*time.Minute))
	}
	// Pinned on: the controller never stops it, so a countdown is a promise of a saving that
	// will not arrive.
	if eta := engineStopETA(engineModeOn, true, last, cfg); !eta.IsZero() {
		t.Errorf("mode on: eta=%v, want none — an engine pinned on does not stop by itself", eta)
	}
	if eta := engineStopETA(engineModeOff, true, last, cfg); !eta.IsZero() {
		t.Errorf("mode off: eta=%v, want none", eta)
	}
	// Nothing is running, so there is nothing to stop. "Would stop at" for a box that does not
	// exist reads as a schedule for one that does.
	if eta := engineStopETA(engineModeOnDemand, false, last, cfg); !eta.IsZero() {
		t.Errorf("stopped: eta=%v, want none", eta)
	}
	// No demand mark: decideEngineAction judges nothing on that pass either, so there is
	// genuinely no answer yet.
	if eta := engineStopETA(engineModeOnDemand, true, time.Time{}, cfg); !eta.IsZero() {
		t.Errorf("no demand mark: eta=%v, want none", eta)
	}
	// idle 0 is configured to mean "never stop".
	if eta := engineStopETA(engineModeOnDemand, true, last, engineControlCfg{idle: 0}); !eta.IsZero() {
		t.Errorf("idle disabled: eta=%v, want none", eta)
	}
}

// The countdown and the controller must reach the same moment. They are separate code paths
// reading the same tuning, and a panel counting down to a time nothing happens at is worse than
// one that shows nothing — the clamp to the start deadline is the part that is easy to forget.
func TestEngineStopETAAgreesWithTheController(t *testing.T) {
	// An idle window SHORTER than the start deadline: the controller clamps it up, because a
	// window shorter than a cold start stops the service while it is still starting.
	cfg := engineControlCfg{idle: 5 * time.Minute, deadline: 15 * time.Minute}
	last := time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)
	eta := engineStopETA(engineModeOnDemand, true, last, cfg)

	snap := engineSnapshot{
		state: "running", desired: 1, mode: engineModeOnDemand, lastDemand: last, windowUnits: 0,
	}
	// One second before the predicted moment the controller must still be leaving it alone.
	if action, reason := decideEngineAction(eta.Add(-time.Second), snap, cfg); action != engineActionNone {
		t.Errorf("a second before the panel's ETA the controller already acts: %s/%s", action, reason)
	}
	// At it, it stops. If this fails the panel is counting down to the wrong second.
	if action, reason := decideEngineAction(eta, snap, cfg); action != engineActionStop || reason != engineReasonIdle {
		t.Errorf("at the panel's ETA the controller does %s/%s, want stop/idle", action, reason)
	}
}

// The rolling demand count is in-process memory and nothing else. A CP replaced two minutes ago
// answers "0 requests in the last 5 minutes" while somebody is mid-conversation with the engine,
// and the only defence is reporting how much of the window it can actually speak for.
func TestEngineDemandSaysHowMuchOfTheWindowItHasCounted(t *testing.T) {
	now := time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)
	d := newEngineDemand(nil, "k", 5*time.Minute)
	d.since = now
	d.now = func() time.Time { return now }

	if got := d.countedFor(); got != 0 {
		t.Errorf("at start: countedFor=%v, want 0 — a fresh process has counted nothing", got)
	}
	now = now.Add(2 * time.Minute)
	if got := d.countedFor(); got != 2*time.Minute {
		t.Errorf("two minutes in: countedFor=%v, want 2m", got)
	}
	// Never more than the window: past that, "0 in the window" is a real zero.
	now = now.Add(time.Hour)
	if got := d.countedFor(); got != 5*time.Minute {
		t.Errorf("well past the window: countedFor=%v, want the whole window (5m)", got)
	}
}

// The store side: accumulation, per-engine scoping and the retention sweep.
//
// Scoping is the one that would go unnoticed. Two engines share one table, and a query that
// returned both would draw the llm engine's night shift onto the image engine's heatmap — which
// reads as a GPU somebody forgot to stop.
func TestEngineHourlyStoreAccumulatesPerEngine(t *testing.T) {
	ctx := context.Background()
	st := testSettingsStore(t)
	add := func(key, hour string, c store.EngineHourCounters) {
		t.Helper()
		if err := st.AddEngineHour(ctx, key, hour, c); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	add("llm", "2026-09-08T04", store.EngineHourCounters{Samples: 1, ObservedSecs: 30, RunningSecs: 30})
	add("llm", "2026-09-08T04", store.EngineHourCounters{Samples: 1, ObservedSecs: 30, StartingSecs: 30})
	add("image", "2026-09-08T04", store.EngineHourCounters{Samples: 1, ObservedSecs: 30, DrainingSecs: 30})
	add("llm", "2026-05-01T04", store.EngineHourCounters{Samples: 1, ObservedSecs: 30, RunningSecs: 30})

	rows, err := st.ListEngineHourly(ctx, "llm", "2026-09-01T00", "2026-09-30T23")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want the image engine's row excluded", rows)
	}
	got := rows[0].EngineHourCounters
	if got.Samples != 2 || got.ObservedSecs != 60 || got.RunningSecs != 30 || got.StartingSecs != 30 {
		t.Errorf("counters = %+v, want two ticks summed into 60 s observed (30 running, 30 starting)", got)
	}
	if got.DrainingSecs != 0 {
		t.Errorf("the image engine's draining time landed on llm: %+v", got)
	}

	if err := st.PruneEngineHourly(ctx, "2026-08-01T00"); err != nil {
		t.Fatalf("prune: %v", err)
	}
	all, _ := st.ListEngineHourly(ctx, "", "2026-01-01T00", "2026-12-31T23")
	if len(all) != 2 {
		t.Fatalf("after the prune: %+v, want only the two September rows (both engines)", all)
	}
}

// The whole path, from the tick that observes to the JSON the panel reads. A sampler that writes
// somewhere nothing reads, or an endpoint reading a table nothing fills, both look green in a
// unit test of either half.
func TestEngineHourlyRoundTripsFromTheControllerToTheAPI(t *testing.T) {
	st := testSettingsStore(t)
	f := &fakeTTSECS{svc: &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}}
	e := newTestImageEngine(t, "http://127.0.0.1:1", f)
	e.settings = st
	now := time.Now().UTC().Truncate(time.Hour).Add(4 * time.Minute)
	c := newEngineController(e.ecs, engineSettingsFor("image"), nil, e.demand, st, nil,
		engineControlCfg{interval: 30 * time.Second})
	c.uptime = st
	c.now = func() time.Time { return now }
	e.ctrl = c

	// Two ticks: the first has nothing to measure from, the second records 30 seconds of running.
	_ = c.tick(t.Context())
	now = now.Add(30 * time.Second)
	_ = c.tick(t.Context())

	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/admin/engines/image/hourly", nil)
	r.SetPathValue("key", "image")
	a.uptime(rec, r, store.Identity{ID: "u1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("hourly = %d: %s", rec.Code, rec.Body.String())
	}
	var out engineHourlyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Hours) != 1 {
		t.Fatalf("hours = %+v, want the one hour the controller watched", out.Hours)
	}
	if out.Hours[0].RunningSecs != 30 || out.Hours[0].ObservedSecs != 30 {
		t.Errorf("hour = %+v, want 30 s observed and 30 s running", out.Hours[0])
	}

	// An engine the deployment does not run is a 404, not an empty history: "no records" and
	// "no such engine" are answers to different questions and only one of them is reassuring.
	rec = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/api/admin/engines/nosuch/hourly", nil)
	r.SetPathValue("key", "nosuch")
	a.uptime(rec, r, store.Identity{ID: "u1"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown engine = %d, want 404", rec.Code)
	}
}

// buildEngineHourly is the wire, and the property it must keep is what it does NOT do: an hour
// with no sample gets no row, so the client can tell "not watched" from "watched and idle".
func TestBuildEngineHourlyLeavesUnwatchedHoursOut(t *testing.T) {
	rows := []store.EngineHourRow{
		{EngineKey: "llm", Hour: "2026-09-08T04",
			EngineHourCounters: store.EngineHourCounters{Samples: 120, ObservedSecs: 3600, RunningSecs: 1800}},
		// 05 is missing on purpose: the CP was down.
		{EngineKey: "llm", Hour: "2026-09-08T06",
			EngineHourCounters: store.EngineHourCounters{Samples: 120, ObservedSecs: 3600}},
	}
	out := buildEngineHourly("llm", rows, "2026-09-08", "2026-09-08",
		engineControlCfg{interval: 30 * time.Second})

	if len(out.Hours) != 2 {
		t.Fatalf("hours=%d, want 2 — the gap must NOT be padded with a zero row (that renders as "+
			"'the engine was idle' over a control-plane outage)", len(out.Hours))
	}
	if out.Hours[1].Hour != "2026-09-08T06" || out.Hours[1].RunningSecs != 0 {
		t.Errorf("second hour = %+v, want the observed-but-idle 06 bucket", out.Hours[1])
	}
	if out.IntervalSecs != 30 || out.Engine != "llm" {
		t.Errorf("envelope = %+v", out)
	}
}
