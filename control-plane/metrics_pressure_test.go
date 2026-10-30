package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// localDockerStats stands in for the host cgroup read of a docker deployment: mem_used
// present, so workspaceStats takes the local path.
func localDockerStats(t *testing.T) {
	t.Helper()
	old := hostStatsHook
	hostStatsHook = func(context.Context, string) (map[string]any, string) {
		return map[string]any{"running": true, "mem_used": uint64(1 << 30), "cpu_pct": 3.0}, "run-1"
	}
	memPressures = &pressureCache{m: map[string]*pressureEntry{}}
	t.Cleanup(func() {
		hostStatsHook = old
		memPressures = &pressureCache{m: map[string]*pressureEntry{}}
	})
}

func pressureRT(t *testing.T, h http.HandlerFunc) (*manager, agentStatsRuntime) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "running"})
	return mgr, agentStatsRuntime{stubRuntime: stubRuntime{endpoint: srv.URL}, state: "running"}
}

func statsOnce(m *manager, rt agentStatsRuntime) map[string]any {
	return workspaceStats(context.Background(), m, rt, func() string { return "running" })
}

func waitPressure(t *testing.T, m *manager, rt agentStatsRuntime, want bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		out := statsOnce(m, rt)
		if _, ok := out["mem_pressure"]; ok == want {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("mem_pressure present=%v never became %v: %v", !want, want, out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const agentPressureBody = `{"mem_used":999,"cpu_pct":77,"mem_pressure":{"available":true,"some_avg10":1.6,"agent_latency_ms":0.2}}`

// The bug: on the local path the Agent was never asked, so mem_pressure never arrived. Only
// that axis is merged; every other gauge stays from the local read.
func TestLocalStatsMergeOnlyMemPressureFromTheAgent(t *testing.T) {
	localDockerStats(t)
	m, rt := pressureRT(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(agentPressureBody)) })

	out := waitPressure(t, m, rt, true)
	mp := out["mem_pressure"].(*memPressure)
	if !mp.Available || mp.SomeAvg10 == nil || *mp.SomeAvg10 != 1.6 {
		t.Errorf("mem_pressure = %+v", mp)
	}
	if out["mem_used"] != uint64(1<<30) || out["cpu_pct"] != 3.0 {
		t.Errorf("local gauges were overwritten by the Agent's: %v", out)
	}
	if _, ok := out["boot_install_failed"]; ok {
		t.Errorf("a non-pressure Agent key leaked into the local answer: %v", out)
	}
}

// An unreachable or old Agent leaves the axis absent, never a zero, and the first tick must
// not wait for the Agent at all.
func TestLocalStatsNeverBlockOnAgentAndNeverInventPressure(t *testing.T) {
	localDockerStats(t)
	release := make(chan struct{})
	m, rt := pressureRT(t, func(w http.ResponseWriter, r *http.Request) { <-release })
	// Registered after the server's Close so it runs first: Close waits for this handler.
	t.Cleanup(func() { close(release) })

	start := time.Now()
	out := statsOnce(m, rt)
	if d := time.Since(start); d > time.Second {
		t.Errorf("tick took %v behind a hung Agent", d)
	}
	if _, ok := out["mem_pressure"]; ok {
		t.Errorf("mem_pressure invented: %v", out)
	}
	if out["mem_used"] == nil {
		t.Errorf("local gauges lost: %v", out)
	}
}

func TestLocalStatsOldAgentStaysAbsentAndIsBackedOff(t *testing.T) {
	localDockerStats(t)
	var hits atomic.Int32
	m, rt := pressureRT(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(404) })

	for i := 0; i < 20; i++ {
		if _, ok := statsOnce(m, rt)["mem_pressure"]; ok {
			t.Fatal("mem_pressure present for a 404 Agent")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("Agent asked %d times in a burst of ticks, want 1 (backoff)", n)
	}
}

func TestLocalStatsPressureIsDroppedWhenTheAgentGoesQuiet(t *testing.T) {
	localDockerStats(t)
	m, rt := pressureRT(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(agentPressureBody)) })
	waitPressure(t, m, rt, true)

	memPressures.mu.Lock()
	memPressures.m[rt.Name()].okAt = time.Now().Add(-2 * pressureMaxAge)
	memPressures.m[rt.Name()].tryAt = time.Now() // no refetch in this tick
	memPressures.mu.Unlock()
	if _, ok := statsOnce(m, rt)["mem_pressure"]; ok {
		t.Error("a sample older than pressureMaxAge was still shown")
	}
}

// Idle workspace: two samples that differ only by jitter must marshal equal after rounding,
// or the events stream would emit a frame every tick.
func TestLocalStatsPressureDoesNotBreakPushSuppression(t *testing.T) {
	localDockerStats(t)
	m, rt := pressureRT(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(agentPressureBody)) })
	waitPressure(t, m, rt, true)
	enc := func() string {
		b, err := json.Marshal(roundStats(statsOnce(m, rt)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	first := enc()
	jit := 1.7
	lat := 0.4
	memPressures.mu.Lock()
	e := memPressures.m[rt.Name()]
	e.mp = &memPressure{Available: true, SomeAvg10: &jit, AgentLatencyMs: &lat, SampledAtMs: 12345}
	e.tryAt = time.Now()
	memPressures.mu.Unlock()
	if second := enc(); first != second {
		t.Errorf("jitter changed the pushed payload:\n%s\n%s", first, second)
	}
}

// A sample must not outlive the Agent's answers: success followed by a failure shows nothing.
func TestLocalStatsPressureVanishesOnTheFirstFailureAfterASuccess(t *testing.T) {
	localDockerStats(t)
	var fail atomic.Bool
	m, rt := pressureRT(t, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(agentPressureBody))
	})
	waitPressure(t, m, rt, true)
	fail.Store(true)
	memPressures.mu.Lock()
	memPressures.m[rt.Name()].tryAt = time.Now().Add(-time.Hour) // due for a refetch
	memPressures.mu.Unlock()
	waitPressure(t, m, rt, false)
}

// A stopped workspace forgets its sample, and a response that was in flight from the previous
// run is rejected instead of landing on the new one.
func TestLocalStatsPressureIsInvalidatedByStopAndRejectsOrphanedResponses(t *testing.T) {
	c := &pressureCache{m: map[string]*pressureEntry{}}
	now := time.Now()
	_, oldGen, fetch := c.lookup("w", "r", now)
	if !fetch {
		t.Fatal("first lookup must claim a fetch")
	}
	c.invalidate("w")
	_, newGen, _ := c.lookup("w", "r", now)
	if newGen == oldGen {
		t.Fatal("a recreated entry reused the generation")
	}
	c.finish("w", oldGen, &memPressure{Available: true}, now)
	if mp, _, _ := c.lookup("w", "r", now); mp != nil {
		t.Error("a response from the previous run was accepted")
	}
	c.finish("w", newGen, &memPressure{Available: true}, now)
	if mp, _, _ := c.lookup("w", "r", now); mp == nil {
		t.Error("the current run's response was rejected")
	}
	c.invalidate("w")
	if mp, _, _ := c.lookup("w", "r", now); mp != nil {
		t.Error("sample survived invalidate")
	}
}

func TestPressureCacheCollectsIdleEntriesButNotInflightOnes(t *testing.T) {
	c := &pressureCache{m: map[string]*pressureEntry{}}
	t0 := time.Now()
	c.lookup("idle", "r", t0)
	_, g, _ := c.lookup("busy", "r", t0) // claims a fetch, stays in flight
	c.finish("idle", c.m["idle"].gen, &memPressure{Available: true}, t0)
	later := t0.Add(pressureIdleAfter + 2*pressureSweepEvery)
	c.lookup("other", "r", later)
	if _, ok := c.m["idle"]; ok {
		t.Error("idle entry was not collected")
	}
	if _, ok := c.m["busy"]; !ok {
		t.Error("an entry with a fetch in flight was collected")
	}
	c.finish("busy", g, nil, later) // must not panic or resurrect anything
}

// The container was replaced (or restarted in place) between two ticks, so the tick never saw
// it stopped: the run token alone must drop the old sample and orphan the old fetch.
func TestPressureCacheRunChangeDropsSampleAndOrphansFetch(t *testing.T) {
	c := &pressureCache{m: map[string]*pressureEntry{}}
	now := time.Now()
	_, g1, _ := c.lookup("w", "run-1", now)
	c.finish("w", g1, &memPressure{Available: true}, now)
	if mp, _, _ := c.lookup("w", "run-1", now); mp == nil {
		t.Fatal("same run must keep its sample")
	}
	mp, g2, fetch := c.lookup("w", "run-2", now)
	if mp != nil || !fetch || g2 == g1 {
		t.Fatalf("new run: mp=%v fetch=%v gen %d->%d", mp, fetch, g1, g2)
	}
	c.finish("w", g1, &memPressure{Available: true}, now) // the old run's fetch lands late
	if mp, _, _ := c.lookup("w", "run-2", now); mp != nil {
		t.Error("old run's response contaminated the new run")
	}
}
