package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// silentStop is the optional action on top of the dead-man check (agent_liveness.go, #3212):
// a workspace whose Agent has stayed flagged silent for a longer ceiling is stopped, under
// the rules of the administrator's stop (stopWorkspaceByMembershipIf): start lock, lifecycle
// lease, runtime operation fence, and a decision taken again once they are held.
//
// It is off unless AF_AGENT_SILENT_STOP_AFTER is set. A wrong Stop costs a member their live
// sessions, so the default is to flag only, as before.
//
// Stopping on a CP-side verdict is only as good as the CP's own network, so three guards sit
// in front of it. (1) A sweep stops anything only if at least one other running workspace
// answered it: when nothing answers, the CP is more likely the one that is cut off. (2) At
// most silentStopPerSweep stops per sweep, longest-silent first. (3) Under the lease the
// workspace must still be `running` (never `starting`, which covers the Agent's boot and
// agentStartingMarker) and a fresh /healthz probe must fail once more.
//
// Replicas need no coordination of their own: the second one to take the lease finds the
// workspace already stopped, and the notification's event ID names the launch, so even a
// lost lease collapses into one notification.
type silentStop struct {
	mgr   *manager
	after time.Duration // flagged-silent time that triggers the stop; <= 0 disables

	mu       sync.Mutex
	inflight map[string]bool
	wg       sync.WaitGroup // the dispatched stops; tests wait on it
}

const (
	// silentStopPerSweep caps the stops one sweep starts.
	silentStopPerSweep = 2
	// silentStopMin is the shortest ceiling honoured: the sweep runs every few minutes, and
	// anything below the flag's own period would stop a workspace the moment it is flagged.
	silentStopMin = 10 * time.Minute
)

// silentStopFenceWait and silentStopBudget play the roles of the start deadline's: the stop
// rides the usage sweep and must neither wait behind a long operation nor hang on a dead call.
var (
	silentStopFenceWait = 10 * time.Second
	silentStopBudget    = 2 * time.Minute
)

// newSilentStop returns nil (off) for a non-positive ceiling or when the dead-man check
// itself is off; a ceiling under silentStopMin is raised to it.
func newSilentStop(mgr *manager, after time.Duration) *silentStop {
	if after <= 0 || agentSilentAfter() <= 0 {
		return nil
	}
	if after < silentStopMin {
		log.Printf("agent silent stop: AF_AGENT_SILENT_STOP_AFTER=%s is below the %s minimum; using the minimum", after, silentStopMin)
		after = silentStopMin
	}
	return &silentStop{mgr: mgr, after: after, inflight: map[string]bool{}}
}

// silentCandidate is a running workspace one sweep found flagged silent.
type silentCandidate struct {
	rt  runtime.Runtime
	ws  store.Workspace
	key string
}

// dispatch stops the candidates that are past the ceiling. answering is how many other
// running workspaces answered this sweep; complete is whether the walk reached everything.
func (d *silentStop) dispatch(ctx context.Context, cands []silentCandidate, answering int, complete bool) {
	if d == nil || len(cands) == 0 {
		return
	}
	now := time.Now()
	var due []silentCandidate
	for _, c := range cands {
		if agentLive.silentFor(c.key, now) >= d.after {
			due = append(due, c)
		}
	}
	if len(due) == 0 {
		return
	}
	if !complete || answering == 0 {
		log.Printf("agent silent stop: %d workspace(s) past the ceiling but not stopped: complete walk=%v, answering workspaces=%d (the Control Plane may be the one cut off)",
			len(due), complete, answering)
		return
	}
	sort.SliceStable(due, func(i, j int) bool {
		return agentLive.silentFor(due[i].key, now) > agentLive.silentFor(due[j].key, now)
	})
	due = due[:min(len(due), silentStopPerSweep)]
	for _, c := range due {
		d.mu.Lock()
		busy := d.inflight[c.ws.ID]
		d.inflight[c.ws.ID] = true
		d.mu.Unlock()
		if busy {
			continue
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			stopCtx, cancel := context.WithTimeout(ctx, silentStopBudget)
			defer cancel()
			d.stop(stopCtx, c)
			d.mu.Lock()
			delete(d.inflight, c.ws.ID)
			d.mu.Unlock()
		}()
	}
}

// stop takes the same fences as an administrator's stop, without waiting for a busy one past
// silentStopFenceWait (the next sweep looks again), then decides again with them held.
func (d *silentStop) stop(ctx context.Context, c silentCandidate) {
	ws, rt := c.ws, c.rt
	lock := d.mgr.startLockFor(ws.ID)
	if !lock.TryLock() {
		return
	}
	defer lock.Unlock()
	lease, err := acquireWorkspaceLifecycleLease(ctx, d.mgr.store, ws.MembershipID)
	if err != nil {
		log.Printf("agent silent stop: lifecycle busy %s: %v", ws.ContainerName, err)
		return
	}
	defer lease.Close()
	fenceCtx, cancelFence := context.WithTimeout(lease.Context(), silentStopFenceWait)
	releaseFence, err := d.mgr.acquireWorkspaceOperationFence(fenceCtx, ws.ID, rt)
	cancelFence()
	if err != nil {
		log.Printf("agent silent stop: runtime fence %s: %v", ws.ContainerName, err)
		return
	}
	defer releaseFence()
	if err := lease.checkpoint(ctx); err != nil {
		log.Printf("agent silent stop: lifecycle lost %s: %v", ws.ContainerName, err)
		return
	}
	fresh, ok, err := d.mgr.store.GetWorkspaceByMembership(lease.Context(), ws.MembershipID)
	if err != nil || !ok {
		log.Printf("agent silent stop: refresh workspace %s: found=%v err=%v", ws.ContainerName, ok, err)
		return
	}
	// A member's own stop or start, or another replica's stop, may have run while the fences
	// were awaited: anything but `running` now is not this outage.
	if rt.State(lease.Context()) != "running" {
		return
	}
	// A launch or activity after the flag was raised means this verdict is about an earlier
	// Agent: a Stop/Start completed on another replica that this one never saw leave `running`.
	// The tracker restarts its clock; the new Agent gets its grace period.
	if t, err := time.Parse(time.RFC3339, fresh.LastActiveAt); err == nil && t.After(agentLive.silentSince(c.key)) {
		agentLive.forget(c.key)
		return
	}
	tok, silentFor, ok := agentLive.silentToken(c.key, time.Now())
	if !ok || silentFor < d.after || !d.probeFails(lease.Context(), rt) {
		return
	}
	// The probe may have waited seconds; an answer to any other call meanwhile (or a recovery
	// and a fresh flag) makes that verdict stale, the same rule probeDone applies.
	if !agentLive.sameVerdict(c.key, tok) {
		return
	}
	if err := rt.Stop(lease.Context()); err != nil {
		log.Printf("agent silent stop: stop %s: %v", ws.ContainerName, err)
		return
	}
	// Told and recorded before the checkpoint, as the start deadline does: a lease lost below
	// must not lose the one record of why.
	d.audit(ctx, fresh, silentFor)
	d.notify(ctx, fresh, silentFor)
	d.record(ctx, fresh, silentFor)
	if err := lease.checkpoint(ctx); err != nil {
		log.Printf("agent silent stop: lifecycle lost after stop %s: %v", ws.ContainerName, err)
		return
	}
	if err := d.mgr.store.SetWorkspaceState(ctx, ws.ID, "stopped"); err != nil {
		log.Printf("agent silent stop: mark stopped %s: %v", ws.ContainerName, err)
	}
	agentLive.forget(c.key)
	log.Printf("agent silent stop: stopped %s (tenant %s): Agent silent for %s with /healthz failing",
		ws.ContainerName, ws.TenantID, silentFor.Round(time.Second))
}

// probeFails is the last word before a Stop: a /healthz that fails right now. An answer also
// reaches agentLive through the transport, so a live Agent clears its own flag.
func (d *silentStop) probeFails(ctx context.Context, rt runtime.Runtime) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*healthzClient.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rt.Endpoint()+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := healthzClient.Do(req)
	if err != nil {
		return true
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// audit writes the system row for this stop; only the replica that performed the Stop gets here.
func (d *silentStop) audit(ctx context.Context, ws store.Workspace, silentFor time.Duration) {
	a := store.AuditLog{ID: store.NewID(), TenantID: ws.TenantID, ActorKind: "system", ActorID: "agent-liveness",
		Action: "workspace.agent_silent_stop", Target: ws.ID,
		Detail: fmt.Sprintf("Agent silent for %s; stopped after the %s ceiling", silentFor.Round(time.Second), d.after),
		At:     store.NowTS()}
	if err := d.mgr.store.InsertAudit(context.WithoutCancel(ctx), a); err != nil {
		log.Printf("agent silent stop: audit %s: %v", ws.ContainerName, err)
	}
}

// notify tells the member whose workspace it was. The event ID names the launch (ws is the
// row read under the lease), so a second stop of the same launch adds no second notification.
func (d *silentStop) notify(ctx context.Context, ws store.Workspace, silentFor time.Duration) {
	now := store.NowTS()
	launch := ws.LastActiveAt
	if launch == "" {
		launch = now
	}
	payload, _ := json.Marshal(map[string]any{"silentMinutes": limitMinutes(silentFor)})
	n := store.Notification{EventID: "agent-silent-stop:" + ws.ID + ":" + launch, MembershipID: ws.MembershipID,
		Kind: "agent-silent-stop", TargetType: "workspace", Payload: string(payload), CreatedAt: now}
	if err := d.mgr.store.InsertNotification(context.WithoutCancel(ctx), n); err != nil {
		log.Printf("agent silent stop: notify %s: %v", ws.ContainerName, err)
	}
}

// record keeps the reason on the workspace for the tenant's admins, like the start deadline.
func (d *silentStop) record(ctx context.Context, ws store.Workspace, silentFor time.Duration) {
	a := store.WorkspaceAutoStop{Kind: "agent-silent", LimitMinutes: limitMinutes(silentFor), StoppedAt: store.NowTS()}
	if err := d.mgr.store.SetWorkspaceAutoStop(context.WithoutCancel(ctx), ws.ID, a); err != nil {
		log.Printf("agent silent stop: record %s: %v", ws.ContainerName, err)
	}
}
