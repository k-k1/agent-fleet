package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitDiskIdle blocks until no background du is in flight.
func waitDiskIdle(t *testing.T) {
	t.Helper()
	diskBG.wg.Wait()
}

// resetDiskBG gives a test its own refresher, du cache and runDU, restored afterwards.
func resetDiskBG(t *testing.T, du func(context.Context, string) ([]byte, error)) {
	t.Helper()
	prevBG, prevDU, prevTO := diskBG, runDU, diskRefreshTimeout
	diskBG = newDiskRefresher()
	diskUsages.m = map[string]diskSample{}
	runDU = du
	t.Cleanup(func() {
		diskBG.cancel()
		diskBG.wg.Wait()
		diskBG, runDU, diskRefreshTimeout = prevBG, prevDU, prevTO
		diskUsages.m = map[string]diskSample{}
	})
}

// A slow du must not stall the request, concurrent pollers share one du, and the empty
// cache answers "unknown", never 0.
func TestDirDiskUsageNoWaitIsSingleFlightAndNeverBlocks(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	resetDiskBG(t, func(ctx context.Context, _ string) ([]byte, error) {
		calls.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []byte("777\thome"), nil
	})

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, ok := dirDiskUsageNoWait("/d"); ok {
				t.Error("ok with an empty cache, want unknown")
			}
		}()
	}
	wg.Wait()
	if time.Since(start) > time.Second {
		t.Fatalf("callers waited %v on a blocked du", time.Since(start))
	}
	close(release)
	waitDiskIdle(t)
	if n := calls.Load(); n != 1 {
		t.Errorf("du ran %d times for 8 concurrent callers, want 1", n)
	}
	v, at, ok := dirDiskUsageNoWait("/d")
	if !ok || v != 777 || time.Since(at) > time.Minute {
		t.Errorf("after refresh = %d %v %v, want 777 with a fresh timestamp", v, at, ok)
	}
}

// An expired sample is still served at once (with its old timestamp) while a refresh runs.
func TestDirDiskUsageNoWaitServesStaleWhileRefreshing(t *testing.T) {
	release := make(chan struct{})
	resetDiskBG(t, func(ctx context.Context, _ string) ([]byte, error) {
		<-release
		return []byte("2\tx"), nil
	})
	old := time.Now().Add(-10 * time.Minute)
	diskUsages.put("/d/home", 1, old)

	v, at, ok := dirDiskUsageNoWait("/d")
	if !ok || v != 1 || !at.Equal(old) {
		t.Errorf("stale read = %d %v %v, want the old sample 1 @ %v", v, at, ok, old)
	}
	close(release)
	waitDiskIdle(t)
	if v, _, _ := dirDiskUsageNoWait("/d"); v != 2 {
		t.Errorf("after refresh = %d, want 2", v)
	}
}

// A hung du is killed at the timeout and frees the single-flight slot; shutdown cancels it too.
func TestDiskRefreshTimeoutAndShutdownKillDU(t *testing.T) {
	var calls atomic.Int32
	resetDiskBG(t, func(ctx context.Context, _ string) ([]byte, error) {
		calls.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	diskRefreshTimeout = 50 * time.Millisecond
	dirDiskUsageNoWait("/d")
	waitDiskIdle(t) // returns only because the timeout cancelled du
	dirDiskUsageNoWait("/d")
	waitDiskIdle(t)
	if calls.Load() != 2 {
		t.Errorf("du calls = %d, want 2 (slot freed after the timeout)", calls.Load())
	}

	diskRefreshTimeout = time.Hour
	dirDiskUsageNoWait("/e")
	done := make(chan struct{})
	go func() { stopDiskRefresh(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stopDiskRefresh did not cancel the running du")
	}
	before := calls.Load()
	dirDiskUsageNoWait("/f")
	if calls.Load() != before {
		t.Error("a du started after shutdown")
	}
}

// The member detail's stats request returns while du is still running, reports the figure
// as pending, and later states how old the sample is.
func TestMemberStatsDoesNotWaitOnDU(t *testing.T) {
	srv := statsAgent(t, 200, `{"mem_used":100,"mem_max":200,"cpu_pct":3}`)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "running"})
	mgr.dataRoot = t.TempDir()
	release := make(chan struct{})
	resetDiskBG(t, func(ctx context.Context, _ string) ([]byte, error) {
		select {
		case <-release:
			return []byte("4096\tx"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	start := time.Now()
	out := memberStatsJSON(t, mgr)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("stats waited %v on du", time.Since(start))
	}
	if out["disk_pending"] != true || out["disk_used"] != nil {
		t.Errorf("disk_pending=%v disk_used=%v, want pending and no figure (0 reads as an empty home)", out["disk_pending"], out["disk_used"])
	}
	close(release)
	waitDiskIdle(t)
	out = memberStatsJSON(t, mgr)
	if out["disk_used"] != float64(4096) || out["disk_observed_at"] == nil || out["disk_pending"] != nil {
		t.Errorf("after refresh: %v", out)
	}
}
