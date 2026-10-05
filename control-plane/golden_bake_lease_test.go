package main

import (
	"context"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
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
	if ok, err := f.store.RenewCPLease(ctx, goldenLeaseName, oldCP.holder, time.Millisecond); err != nil || !ok {
		t.Fatalf("shorten the lease: %v %v", ok, err)
	}
	time.Sleep(20 * time.Millisecond)
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
	if ok, err := f.store.AcquireCPLease(ctx, goldenLeaseName, "another-cp", time.Hour); err != nil || !ok {
		t.Fatalf("lease: %v %v", ok, err)
	}
	stepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go f.baker.keepLease(stepCtx, cancel, 30*time.Millisecond, time.Now().Add(time.Hour))
	select {
	case <-stepCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the step went on after another CP took the lease")
	}
}

// blockingRenewStore holds every renewal until its context ends, or answers it late.
type blockingRenewStore struct {
	store.Store
	late time.Duration // > 0: answer ok after this long instead of blocking
}

func (s blockingRenewStore) RenewCPLease(ctx context.Context, _, _ string, _ time.Duration) (bool, error) {
	if s.late > 0 {
		time.Sleep(s.late)
		return true, nil
	}
	<-ctx.Done()
	return false, ctx.Err()
}

// A renewal stuck on the database does not keep the step alive past the lease's expiry:
// the expiry is watched on its own, and another CP may hold the lease by then.
func TestGoldenBakeStepStopsAtExpiryWhileARenewalBlocks(t *testing.T) {
	f := newGoldenFixture(t, nil)
	f.baker.mgr.store = blockingRenewStore{Store: f.store}
	stepCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.baker.keepLease(stepCtx, cancel, 60*time.Millisecond, time.Now().Add(60*time.Millisecond))
	select {
	case <-stepCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the step outlived its lease while a renewal was blocked")
	}
}

// A renewal that succeeds late extends the lease from when it was sent, not from when its
// answer came back: the database set the expiry at the earlier moment.
func TestGoldenBakeLateRenewalDoesNotOverstateTheLease(t *testing.T) {
	f := newGoldenFixture(t, nil)
	f.baker.mgr.store = blockingRenewStore{Store: f.store, late: 150 * time.Millisecond}
	stepCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	// ttl 300ms: the renewal goes out at 100ms and answers at 250ms. Counted from its answer
	// it would hold until 550ms, from its sending 400ms.
	go f.baker.keepLease(stepCtx, cancel, 300*time.Millisecond, start.Add(300*time.Millisecond))
	select {
	case <-stepCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("late renewals kept extending the lease from their answers")
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("the step ran %s on a lease that expired by 400ms", took)
	}
}
