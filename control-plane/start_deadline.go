package main

import (
	"context"
	"encoding/json"
	"log"
	"sort"
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
	tried    map[string]time.Time // workspace ID -> when a stop last got as far as deciding
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

// startDeadlineStopBudget bounds one whole stop — lease, fences, the task count, Stop — so
// a hung AWS call cannot hold a worker for good and switch the deadline off for everybody
// queued behind it.
var startDeadlineStopBudget = 2 * time.Minute

func newStartDeadline(mgr *manager, after time.Duration) *startDeadline {
	return &startDeadline{mgr: mgr, after: after, seen: map[string]time.Time{},
		inflight: map[string]bool{}, tried: map[string]time.Time{}}
}

// overdueStart is one workspace a walk found past its deadline.
type overdueStart struct {
	rt runtime.Runtime
	ws store.Workspace
}

// dispatch runs stop for the overdue workspaces one walk found, each on its own goroutine,
// one per workspace and at most startDeadlineWorkers in all, so the walk never waits on the
// fences or the probes. The free workers go to the workspaces whose last decided attempt
// is oldest, never-tried first: the walk's order is fixed, and handing them out in that
// order would give the same first few every worker on every sample. An attempt that could
// not get past the fences is not a decided one, so that workspace stays at the front.
func (d *startDeadline) dispatch(ctx context.Context, found []overdueStart) {
	if d == nil || len(found) == 0 {
		return
	}
	d.mu.Lock()
	var queue []overdueStart
	for _, o := range found {
		if !d.inflight[o.ws.ID] {
			queue = append(queue, o)
		}
	}
	sort.SliceStable(queue, func(i, j int) bool {
		return d.tried[queue[i].ws.ID].Before(d.tried[queue[j].ws.ID])
	})
	queue = queue[:min(len(queue), max(startDeadlineWorkers-len(d.inflight), 0))]
	for _, o := range queue {
		d.inflight[o.ws.ID] = true
	}
	d.mu.Unlock()
	for _, o := range queue {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			stopCtx, cancel := context.WithTimeout(ctx, startDeadlineStopBudget)
			decided := d.stop(stopCtx, o.rt, o.ws)
			cancel()
			d.mu.Lock()
			delete(d.inflight, o.ws.ID)
			if decided {
				d.tried[o.ws.ID] = time.Now()
			}
			d.mu.Unlock()
		}()
	}
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
		delete(d.tried, ws.ID)
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
	for id := range d.tried {
		if !ids[id] {
			delete(d.tried, id)
		}
	}
}

// stop ends an overdue launch the way an explicit stop would, under the same three
// fences, so it cannot cross a Start, a recreate or an approved shared operation. It
// reports whether it held them and took the decision (see dispatch). None of
// them is waited for past startDeadlineFenceWait. The decision is taken again once they
// are held: a Start that won the race meanwhile has stamped last_active_at and is not
// overdue.
func (d *startDeadline) stop(ctx context.Context, rt runtime.Runtime, ws store.Workspace) (decided bool) {
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
	// From here on the attempt counts as decided whatever it concludes: the fences were
	// ours, so another workspace should get the next free worker.
	decided = true
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
	// Told and recorded before the checkpoint: the Stop has already cleared the phase, and a
	// lost lease below would otherwise lose the one record of why, with no later sweep to
	// write it.
	d.notify(ctx, fresh, d.limitFor(rt), phase)
	d.record(ctx, fresh, d.limitFor(rt), phase)
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
	return
}

// record keeps the reason on the workspace for its tenant admins (#1384): the
// notification reaches the member alone, so without it the admin views read plain
// "stopped". The next start deletes it (store.SetWorkspaceState).
func (d *startDeadline) record(ctx context.Context, ws store.Workspace, limit time.Duration, phase string) {
	a := store.WorkspaceAutoStop{Kind: "start-deadline", Phase: phase,
		LimitMinutes: limitMinutes(limit), StoppedAt: store.NowTS()}
	if err := d.mgr.store.SetWorkspaceAutoStop(ctx, ws.ID, a); err != nil {
		log.Printf("start-deadline: record %s: %v", ws.ContainerName, err)
	}
}

func limitMinutes(limit time.Duration) int { return int(limit.Round(time.Minute) / time.Minute) }

// notify tells the member that their launch was stopped and why. Without it they see only
// starting -> stopped, and on ecs-ec2 the Stop has just cleared the phase that named the
// reason, so the notification is the one place it survives until the next attempt. Only this
// path writes it: a stop the member or the reaper asked for adds nothing.
//
// ws must be the row read under the lease: its last_active_at is the launch's own stamp, so
// the event ID names the launch and a second stop of the same launch (another replica whose
// sweep raced a lost lease) collapses into the first notification instead of adding one.
func (d *startDeadline) notify(ctx context.Context, ws store.Workspace, limit time.Duration, phase string) {
	now := store.NowTS()
	launch := ws.LastActiveAt
	if launch == "" {
		launch = now
	}
	payload, _ := json.Marshal(map[string]any{"phase": phase, "limitMinutes": limitMinutes(limit)})
	n := store.Notification{EventID: "start-deadline:" + ws.ID + ":" + launch, MembershipID: ws.MembershipID,
		Kind: "start-deadline", TargetType: "workspace", Payload: string(payload), CreatedAt: now}
	if err := d.mgr.store.InsertNotification(ctx, n); err != nil {
		log.Printf("start-deadline: notify %s: %v", ws.ContainerName, err)
	}
}
