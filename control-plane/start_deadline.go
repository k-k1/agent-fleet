package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// startDeadline is the upper bound on `starting` that the Runtime port promises
// (runtime.go, State). Nothing else would end it: while a workspace is `starting` the
// caller does not re-Start it and the reaper does not idle-stop it, and the ECS adapters
// report `starting` for as long as desired is 1 and no task has rolled out. A task ECS
// refuses to place does that forever (docs/log/70 §70.14.6), and on ecs-ec2 it keeps a
// slot running and counts against the tenant's max_workspaces the whole time.
//
// The clock is the later of two instants, never an adapter's own timestamp:
//   - the first sweep of THIS process that found the workspace `starting`, so a CP restart
//     or a second replica only ever lengthens the wait;
//   - the workspace's last_active_at, which ensureWorkspaceStartedRTLocked stamps right
//     after every Start, so a launch the user re-issued after a stop gets a fresh window.
//
// The PRIMARY deployment's CreatedAt looks like the natural clock and is wrong: an
// ecs-ec2 restart on an unchanged task definition scales the old deployment back up
// without creating a new one, so a launch seconds old would read as days overdue.
//
// Overdue is not yet wedged. `starting` also covers a rollout whose new task already
// runs while an old one drains, so a workspace with a running task is never stopped here —
// that would take a working session down to end a wait that is not blocking anybody. The
// adapter's own count decides where there is one (runtime.TaskCounter); elsewhere an Agent
// that answers does.
type startDeadline struct {
	mgr   *manager
	after time.Duration // <= 0 disables the deadline

	mu       sync.Mutex
	seen     map[string]time.Time // workspace ID -> first sweep that found it starting
	inflight map[string]bool      // workspace IDs with a stop running (dispatch)
	wg       sync.WaitGroup       // the dispatched stops; tests wait on it
}

// startDeadlineWorkers caps the stops running at once. They run off the sampler's walk,
// which must keep its 5-minute rhythm however many launches are overdue; a workspace
// that finds no free worker is picked up by the next sample.
const startDeadlineWorkers = 2

// startDeadlineFenceWait bounds the wait for the fences. The deadline runs on the usage
// sampler's walk, and waiting behind a recreate that holds them for minutes would drop that
// walk's samples for every workspace after this one. A busy workspace is simply retried on
// the next sweep.
var startDeadlineFenceWait = 10 * time.Second // a var so a test can shorten it

func newStartDeadline(mgr *manager, after time.Duration) *startDeadline {
	return &startDeadline{mgr: mgr, after: after, seen: map[string]time.Time{}, inflight: map[string]bool{}}
}

// dispatch runs stop for an overdue workspace on its own goroutine, one per workspace and
// at most startDeadlineWorkers in all, so the walk that found it never waits on the fences
// or the probes.
func (d *startDeadline) dispatch(ctx context.Context, rt runtime.Runtime, ws store.Workspace) {
	d.mu.Lock()
	if d.inflight[ws.ID] || len(d.inflight) >= startDeadlineWorkers {
		d.mu.Unlock()
		return
	}
	d.inflight[ws.ID] = true
	d.mu.Unlock()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer func() {
			d.mu.Lock()
			delete(d.inflight, ws.ID)
			d.mu.Unlock()
		}()
		d.stop(ctx, rt, ws)
	}()
}

// taskRunning reports whether rt has a workspace task up. When the adapter cannot tell,
// the answer is yes: a deadline that skips one sample costs five minutes, a wrong Stop
// costs somebody's session.
func (d *startDeadline) taskRunning(ctx context.Context, rt runtime.Runtime) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if tc, ok := rt.(runtime.TaskCounter); ok {
		n, err := tc.RunningTasks(ctx)
		return err != nil || n > 0
	}
	_, err := d.mgr.agentSessionsEnv(ctx, rt)
	return err == nil
}

// limitFor is the deadline for rt's launches: never inside the adapter's own background
// launch budget (runtime.LaunchBudgeter), whatever the operator configured.
func (d *startDeadline) limitFor(rt runtime.Runtime) time.Duration {
	if b, ok := rt.(runtime.LaunchBudgeter); ok {
		return max(d.after, b.LaunchBudget())
	}
	return d.after
}

// observe records one sweep's view of ws and reports whether its launch has run past the
// deadline. Any state other than `starting` resets the clock.
func (d *startDeadline) observe(ws store.Workspace, rt runtime.Runtime, state string, now time.Time) bool {
	if d == nil || d.after <= 0 {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if state != "starting" {
		delete(d.seen, ws.ID)
		return false
	}
	since, ok := d.seen[ws.ID]
	if !ok {
		since = now
		d.seen[ws.ID] = now
	}
	if ts, err := time.Parse(time.RFC3339, ws.LastActiveAt); err == nil && ts.After(since) {
		since = ts
	}
	return now.Sub(since) >= d.limitFor(rt)
}

// retain drops the clocks of workspaces a complete sweep no longer found, so a workspace
// deleted mid-launch does not keep an entry for the life of the process.
func (d *startDeadline) retain(ids map[string]bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for id := range d.seen {
		if !ids[id] {
			delete(d.seen, id)
		}
	}
}

// stop ends an overdue launch the way an explicit stop would, under the same three
// fences, so it cannot cross a Start, a recreate or an approved shared operation. None of
// them is waited for past startDeadlineFenceWait. The decision is taken again once they
// are held: a Start that won the race meanwhile has stamped last_active_at and is not
// overdue.
func (d *startDeadline) stop(ctx context.Context, rt runtime.Runtime, ws store.Workspace) {
	lock := d.mgr.startLockFor(ws.ID)
	if !lock.TryLock() {
		return // a lifecycle operation is in flight on this CP; the next sweep looks again
	}
	defer lock.Unlock()
	lease, err := acquireWorkspaceLifecycleLease(ctx, d.mgr.store, ws.MembershipID)
	if err != nil {
		log.Printf("start-deadline: lifecycle busy %s: %v", ws.ContainerName, err)
		return
	}
	defer lease.Close()
	fenceCtx, cancelFence := context.WithTimeout(lease.Context(), startDeadlineFenceWait)
	releaseFence, err := d.mgr.acquireWorkspaceOperationFence(fenceCtx, ws.ID, rt)
	cancelFence()
	if err != nil {
		log.Printf("start-deadline: runtime fence %s: %v", ws.ContainerName, err)
		return
	}
	defer releaseFence()
	if err := lease.checkpoint(ctx); err != nil {
		log.Printf("start-deadline: lifecycle lost %s: %v", ws.ContainerName, err)
		return
	}
	fresh, ok, err := d.mgr.store.GetWorkspaceByMembership(lease.Context(), ws.MembershipID)
	if err != nil || !ok {
		log.Printf("start-deadline: refresh workspace %s: found=%v err=%v", ws.ContainerName, ok, err)
		return
	}
	if !d.observe(fresh, rt, rt.State(lease.Context()), time.Now()) {
		return
	}
	if d.taskRunning(lease.Context(), rt) {
		return // a rollout still settling, not a launch that cannot place
	}
	// Read before Stop: on ecs-ec2 this is the ECS sentence naming why the task cannot be
	// placed, and Stop clears it.
	var phase string
	if bp, ok := rt.(interface{ BootPhase() string }); ok {
		phase = bp.BootPhase()
	}
	if err := rt.Stop(lease.Context()); err != nil {
		log.Printf("start-deadline: stop %s: %v", ws.ContainerName, err)
		return
	}
	if err := lease.checkpoint(ctx); err != nil {
		log.Printf("start-deadline: lifecycle lost after stop %s: %v", ws.ContainerName, err)
		return
	}
	if err := d.mgr.store.SetWorkspaceState(ctx, ws.ID, "stopped"); err != nil {
		log.Printf("start-deadline: mark stopped %s: %v", ws.ContainerName, err)
	}
	d.mu.Lock()
	delete(d.seen, ws.ID)
	d.mu.Unlock()
	log.Printf("start-deadline: stopped %s (tenant %s): still starting %s after its launch with no task running (last phase %q)",
		ws.ContainerName, ws.TenantID, d.limitFor(rt), phase)
}
