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
type startDeadline struct {
	mgr   *manager
	after time.Duration // <= 0 disables the deadline

	mu   sync.Mutex
	seen map[string]time.Time // workspace ID -> first sweep that found it starting
}

func newStartDeadline(mgr *manager, after time.Duration) *startDeadline {
	return &startDeadline{mgr: mgr, after: after, seen: map[string]time.Time{}}
}

// observe records one sweep's view of ws and reports whether its launch has run past the
// deadline. Any state other than `starting` resets the clock.
func (d *startDeadline) observe(ws store.Workspace, state string, now time.Time) bool {
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
	return now.Sub(since) >= d.after
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
// fences, so it cannot cross a Start, a recreate or an approved shared operation. The
// decision is taken again once they are held: a Start that won the race while this waited
// has stamped last_active_at and is not overdue.
func (d *startDeadline) stop(ctx context.Context, rt runtime.Runtime, ws store.Workspace) {
	lock := d.mgr.startLockFor(ws.ID)
	lock.Lock()
	defer lock.Unlock()
	lease, err := acquireWorkspaceLifecycleLease(ctx, d.mgr.store, ws.MembershipID)
	if err != nil {
		log.Printf("start-deadline: lifecycle busy %s: %v", ws.ContainerName, err)
		return
	}
	defer lease.Close()
	releaseFence, err := d.mgr.acquireWorkspaceOperationFence(lease.Context(), ws.ID, rt)
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
	if !d.observe(fresh, rt.State(lease.Context()), time.Now()) {
		return
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
	log.Printf("start-deadline: stopped %s (tenant %s): still starting %s after its launch (last phase %q)",
		ws.ContainerName, ws.TenantID, d.after, phase)
}
