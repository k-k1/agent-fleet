package main

import (
	"context"
	"testing"
	"time"
)

// countingGoldenPool counts the steps that reached the pool.
type countingGoldenPool struct {
	*fakeGoldenPool
	steps int
}

func (c *countingGoldenPool) BakeArches() []string {
	c.steps++
	return c.fakeGoldenPool.BakeArches()
}

// Two CP tasks overlap during a rolling replacement; only the lease's holder steps the bake,
// and the other takes over once the holder's lease has expired (#1603).
func TestGoldenBakeRunsOnOneCPAtATime(t *testing.T) {
	ctx := context.Background()
	f := newGoldenFixture(t, nil)
	oldPool := &countingGoldenPool{fakeGoldenPool: f.pool}
	newPool := &countingGoldenPool{fakeGoldenPool: f.pool}
	oldCP := newGoldenBaker(f.baker.mgr, oldPool)
	newCP := newGoldenBaker(&manager{store: f.store, rtFactory: f.fac, conns: newConnRegistry(), rts: map[string]cachedRT{}}, newPool)
	every := time.Minute

	oldCP.tick(ctx, every)
	newCP.tick(ctx, every)
	oldCP.tick(ctx, every)
	if oldPool.steps != 2 || newPool.steps != 0 {
		t.Fatalf("steps: old CP %d, new CP %d; want only the holder to step", oldPool.steps, newPool.steps)
	}

	// The old CP is gone: its last renewal expires.
	past := time.Now().Add(-time.Hour)
	if ok, err := f.store.AcquireCPLease(ctx, goldenLeaseName, oldCP.holder, past, past.Add(time.Second)); err != nil || !ok {
		t.Fatalf("age the lease: %v %v", ok, err)
	}
	newCP.tick(ctx, every)
	oldCP.tick(ctx, every)
	if newPool.steps != 1 || oldPool.steps != 2 {
		t.Errorf("after expiry: old CP %d, new CP %d; want the new CP to take over alone", oldPool.steps, newPool.steps)
	}
}

// A step that outlives its lease — another CP took it over — is cancelled rather than left
// driving AWS beside the new holder.
func TestGoldenBakeStepStopsWhenTheLeaseIsLost(t *testing.T) {
	ctx := context.Background()
	f := newGoldenFixture(t, nil)
	b := f.baker
	if ok, err := f.store.AcquireCPLease(ctx, goldenLeaseName, "another-cp", time.Now(), time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("lease: %v %v", ok, err)
	}
	stepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); b.keepLease(stepCtx, cancel, 30*time.Millisecond) }()
	select {
	case <-stepCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the step went on after another CP took the lease")
	}
	<-done
}
