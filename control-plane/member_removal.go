package main

// What removing a member does to things that outlive a single request (issue #1087).
// Every route re-checks the membership per request, so the residue is what is already
// running or already connected: the member's workspace, and long-lived connections
// (WebSocket / SSE / streamed previews) opened while they were still a member.

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// memberConnRegistry holds a cancel function for every in-flight request the CP serves on
// behalf of a membership: the caller of a withResolved route, and both the viewer and the
// owner of a host-mode preview. Cancelling a membership ends those requests — a reverse
// proxied WebSocket or SSE closes with its request context — so a removal reaches
// connections that were authorised before it. The zero value is ready to use.
type memberConnRegistry struct {
	mu    sync.Mutex
	seq   uint64
	conns map[string]map[uint64]context.CancelFunc
}

// track derives a cancellable context from parent and files it under every non-empty
// membership id given. The returned func must be called when the request ends.
func (r *memberConnRegistry) track(parent context.Context, membershipIDs ...string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	r.mu.Lock()
	if r.conns == nil {
		r.conns = map[string]map[uint64]context.CancelFunc{}
	}
	r.seq++
	id := r.seq
	var filed []string
	for _, mid := range membershipIDs {
		if mid == "" {
			continue
		}
		if r.conns[mid] == nil {
			r.conns[mid] = map[uint64]context.CancelFunc{}
		}
		r.conns[mid][id] = cancel
		filed = append(filed, mid)
	}
	r.mu.Unlock()
	return ctx, func() {
		r.mu.Lock()
		for _, mid := range filed {
			delete(r.conns[mid], id)
			if len(r.conns[mid]) == 0 {
				delete(r.conns, mid)
			}
		}
		r.mu.Unlock()
		cancel()
	}
}

// cancel ends every tracked request of a membership and reports how many there were.
func (r *memberConnRegistry) cancel(membershipID string) int {
	r.mu.Lock()
	fns := r.conns[membershipID]
	delete(r.conns, membershipID)
	r.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
	return len(fns)
}

func (r *memberConnRegistry) memberships() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.conns))
	for mid := range r.conns {
		out = append(out, mid)
	}
	return out
}

// errMembershipActive is stopWorkspaceOfRemovedMember's answer when the membership is
// active again by the time the stop holds the workspace's locks.
var errMembershipActive = errors.New("membership is active again; workspace left running")

// stopWorkspaceOfRemovedMember is the removal's stop: the administrator's stop, with the
// membership re-read once the start lock, the lifecycle lease and the runtime fence are
// held. A stop queued behind a start (the member was removed while their workspace was
// booting) would otherwise stop the workspace of somebody re-invited in the meantime.
//
// The re-read is the linearisation point. Starts take the same lock, so between the read
// and Stop nothing can start this workspace; a re-invite landing in that window orders
// after the stop, and the person starts it again like any stopped workspace. A removal,
// restore and second removal need no generation counter: whichever stop gets the lock
// acts on the status it reads then, which is the current one.
func (m *manager) stopWorkspaceOfRemovedMember(ctx context.Context, membershipID string) error {
	return m.stopWorkspaceByMembershipIf(ctx, membershipID, func(ctx context.Context) error {
		if _, active, err := m.store.GetMembershipByID(ctx, membershipID); err != nil {
			return err
		} else if active {
			return errMembershipActive
		}
		return nil
	})
}

// removedMemberSweepInterval is how often every CP replica reconciles removed members.
const removedMemberSweepInterval = time.Minute

// runRemovedMemberSweep reconciles at start-up and then every interval. The removal request
// stops the workspace and closes connections itself, but only on the replica that served
// it and only while that process lives: a CP restart mid-stop, a stop that outlasts its
// budget, or a connection held by another replica would otherwise be left behind.
func (m *manager) runRemovedMemberSweep(ctx context.Context, interval time.Duration) {
	m.sweepRemovedMembers(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sweepRemovedMembers(ctx)
		}
	}
}

// sweepRemovedMembers closes this replica's connections of memberships that are no longer
// active, and stops any workspace whose membership is inactive and whose row is not
// already stopped. A removed member cannot start their workspace, so once stopped a row
// stays stopped and costs the sweep one comparison.
func (m *manager) sweepRemovedMembers(ctx context.Context) {
	for _, mid := range m.memberConns.memberships() {
		if _, active, err := m.store.GetMembershipByID(ctx, mid); err == nil && !active {
			if n := m.memberConns.cancel(mid); n > 0 {
				log.Printf("removed-member sweep: closed %d connection(s) of inactive membership %s", n, mid)
			}
		}
	}
	tenants, err := m.store.ListTenants(ctx)
	if err != nil {
		log.Printf("removed-member sweep: list tenants: %v", err)
		return
	}
	for _, t := range tenants {
		wss, err := m.store.ListWorkspaces(ctx, t.ID)
		if err != nil {
			log.Printf("removed-member sweep: list workspaces (%s): %v", t.Slug, err)
			continue
		}
		for _, ws := range wss {
			if ws.State == "stopped" || ws.MembershipID == "" {
				continue
			}
			if _, active, err := m.store.GetMembershipByID(ctx, ws.MembershipID); err != nil || active {
				continue
			}
			err := m.stopWorkspaceOfRemovedMember(ctx, ws.MembershipID)
			if errors.Is(err, errMembershipActive) || errors.Is(err, store.ErrSessionShareOwnerBusy) {
				continue // restored, or another lifecycle operation holds it: next pass
			}
			if err != nil {
				// Logged, not audited: the next pass tries again, and an audit row a minute
				// for a stop that keeps failing would bury the log it is meant to be read in.
				log.Printf("removed-member sweep: stop %s of inactive membership %s failed: %v", ws.ContainerName, ws.MembershipID, err)
				continue
			}
			outcome, status := "stopped", 200
			log.Printf("removed-member sweep: stopped %s of inactive membership %s", ws.ContainerName, ws.MembershipID)
			_ = m.store.InsertAudit(ctx, store.AuditLog{
				ID: store.NewID(), TenantID: ws.TenantID, ActorKind: "system",
				Action: "membership.remove.stop_workspace", Target: ws.ContainerName,
				Detail: "workspace stop " + outcome + " (removed-member sweep)", HTTPStatus: status, At: store.NowTS(),
			})
		}
	}
}
