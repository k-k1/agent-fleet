package main

import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// addressRecorder keeps workspace_address current: which workspace held which address and
// when (ADR 0122, addendum 2026-10-24). Egress events are attributed against it at read
// time (egress_attribution.go).
//
// It polls the orchestrator rather than recording at Start, because the address changes
// without the CP: the ECS service scheduler replaces a failed task with a new ENI, and a
// StatefulSet pod comes back with a new IP, and the CP calls Start for neither.
//
// One Control Plane runs it at a time (addressRecorderLease). Two writers would apply their
// reads in either order, and an older read landing last closes rows a newer one opened; it
// would also double the orchestrator reads, which share ECS's per-account throttle with
// every other task call the CP makes.
type addressRecorder struct {
	mgr      *manager
	interval time.Duration
	holder   string
	now      func() time.Time

	// lastPolled orders a sweep: the workspaces asked longest ago go first, so a sweep cut
	// off by its deadline resumes where it stopped instead of starting over at the same
	// head every time. In memory: a CP that takes the lease over starts from no order,
	// which is only one unfair sweep.
	lastPolled map[string]time.Time
	// unresolvedSince is when a ref the runtime could not account for was first seen so.
	unresolvedSince map[string]time.Time
}

const addressRecorderLease = "egress-address-recorder"

// addressRecentlyActive is how long after its last activity a stopped workspace is still
// asked. A workspace started and stopped between two sweeps never had an open row, and ECS
// still describes its stopped task for at least an hour, so its interval can be recovered.
const addressRecentlyActive = time.Hour

// addressUnresolvedGrace is how long a ref the orchestrator cannot account for stays open.
// ECS answers MISSING for a few seconds while its index catches up with a new task, and
// for good once a stopped task has aged out (an hour at least): past the grace the row is
// closed at now, later than the task really stopped, which shows as ambiguous if its
// address was given out again, never as the wrong workspace.
const addressUnresolvedGrace = 15 * time.Minute

func newAddressRecorder(mgr *manager, interval time.Duration) *addressRecorder {
	return &addressRecorder{mgr: mgr, interval: interval, holder: store.NewID(), now: time.Now}
}

// addressReleaseHook is Config.OnAddressesReleasing: what a docker or Kubernetes workspace
// holds just before a Stop takes it away, so one started and stopped between two sweeps is
// still on record. Rows only: the next sweep closes them once the holder is gone.
func addressReleaseHook(m *manager) runtime.AddressReleaseHook {
	return func(ctx context.Context, membershipID, workspaceID string, held []runtime.HeldAddress) {
		if m.store == nil {
			return
		}
		if err := m.store.UpsertWorkspaceAddresses(ctx, membershipID, toWorkspaceAddresses(membershipID, workspaceID, held)); err != nil {
			log.Printf("egress address recorder: record released addresses of %s: %v", workspaceID, err)
		}
	}
}

func toWorkspaceAddresses(membershipID, workspaceID string, held []runtime.HeldAddress) []store.WorkspaceAddress {
	out := make([]store.WorkspaceAddress, 0, len(held))
	for _, h := range held {
		a := store.WorkspaceAddress{
			MembershipID: membershipID, WorkspaceID: workspaceID, Address: h.Address, Ref: h.Ref,
			From: h.From.UnixMilli(),
		}
		if !h.Until.IsZero() {
			a.Until = h.Until.UnixMilli()
		}
		out = append(out, a)
	}
	return out
}

func (ar *addressRecorder) run(ctx context.Context) {
	log.Printf("egress address recorder: interval=%s", ar.interval)
	t := time.NewTicker(ar.interval)
	defer t.Stop()
	for {
		ar.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// tick sweeps if this CP holds the lease. The sweep is cut off well before the lease can
// expire, counted from when the request that took it was sent, so a slow sweep never
// overlaps the next holder's.
func (ar *addressRecorder) tick(ctx context.Context) {
	ttl := 3 * ar.interval
	sent := time.Now()
	ok, err := ar.mgr.store.AcquireCPLease(ctx, addressRecorderLease, ar.holder, ttl)
	if err != nil {
		log.Printf("egress address recorder: lease: %v", err)
		return
	}
	if !ok {
		return
	}
	sctx, cancel := context.WithDeadline(ctx, sent.Add(ttl*2/3))
	defer cancel()
	ar.sweep(sctx)
}

// sweep reads the workspace list from the database first and closes orphans from it, then
// asks the runtimes, oldest-asked first, for as long as the deadline allows.
func (ar *addressRecorder) sweep(ctx context.Context) {
	if ar.lastPolled == nil {
		ar.lastPolled = map[string]time.Time{}
	}
	if ar.unresolvedSince == nil {
		ar.unresolvedSince = map[string]time.Time{}
	}
	tenants, err := ar.mgr.store.ListTenants(ctx)
	if err != nil {
		log.Printf("egress address recorder: list tenants: %v", err)
		return
	}
	var all []store.Workspace
	live := map[string]bool{}
	for _, t := range tenants {
		wss, err := ar.mgr.store.ListWorkspaces(ctx, t.ID)
		if err != nil {
			// Without the full list an orphan cannot be told from a workspace not read.
			log.Printf("egress address recorder: list workspaces (%s): %v", t.Slug, err)
			return
		}
		for _, ws := range wss {
			live[ws.MembershipID] = true
		}
		all = append(all, wss...)
	}
	ar.closeOrphans(ctx, live)
	sort.SliceStable(all, func(i, j int) bool {
		return ar.lastPolled[all[i].MembershipID].Before(ar.lastPolled[all[j].MembershipID])
	})
	for _, ws := range all {
		if ctx.Err() != nil {
			return
		}
		ar.recordWorkspace(ctx, ws)
	}
	for m := range ar.lastPolled {
		if !live[m] {
			delete(ar.lastPolled, m)
		}
	}
}

// recordWorkspace asks one workspace's runtime what it holds. A workspace the database has
// as stopped is still asked while it has an open row, so the row is closed (with the stop
// time ECS reports, where it does), and while it was active recently, so a task that came
// and went between two sweeps is still recorded.
func (ar *addressRecorder) recordWorkspace(ctx context.Context, ws store.Workspace) {
	open, err := ar.mgr.store.OpenWorkspaceAddressRefs(ctx, ws.MembershipID)
	if err != nil {
		log.Printf("egress address recorder: open rows of %s: %v", ws.ContainerName, err)
		return
	}
	if ws.State != "running" && len(open) == 0 && !ar.recentlyActive(ws) {
		return
	}
	rep, ok := ar.mgr.runtimeFor(ws, noSecretKeys).(runtime.AddressReporter)
	if !ok {
		return
	}
	ar.lastPolled[ws.MembershipID] = ar.now()
	report, err := rep.HeldAddresses(ctx, open)
	if err != nil {
		// Unknown is not gone: closing the open rows here would end intervals that are
		// still running and leave the next connections unattributed.
		log.Printf("egress address recorder: %s: %v", ws.ContainerName, err)
		return
	}
	var keep []string
	now := ar.now()
	for _, ref := range report.Unresolved {
		since, ok := ar.unresolvedSince[ref]
		if !ok {
			since = now
			ar.unresolvedSince[ref] = now
		}
		if now.Sub(since) < addressUnresolvedGrace {
			keep = append(keep, ref)
		} else {
			delete(ar.unresolvedSince, ref)
		}
	}
	for _, h := range report.Held {
		delete(ar.unresolvedSince, h.Ref)
	}
	if err := ar.mgr.store.ObserveWorkspaceAddresses(ctx, ws.MembershipID,
		toWorkspaceAddresses(ws.MembershipID, ws.ID, report.Held), keep, now.UnixMilli()); err != nil {
		log.Printf("egress address recorder: record %s: %v", ws.ContainerName, err)
	}
}

func (ar *addressRecorder) recentlyActive(ws store.Workspace) bool {
	at, err := time.Parse(time.RFC3339, ws.LastActiveAt)
	return err == nil && ar.now().Sub(at) < addressRecentlyActive
}

// closeOrphans ends the open rows of memberships that no longer have a workspace: one
// destroyed between two sweeps is never listed again, and its row would otherwise stay open
// and claim the address for good. They close at now, later than the address was released;
// an overlap with its next holder shows as ambiguous rather than as the wrong workspace.
func (ar *addressRecorder) closeOrphans(ctx context.Context, live map[string]bool) {
	ms, err := ar.mgr.store.OpenAddressMemberships(ctx)
	if err != nil {
		log.Printf("egress address recorder: open memberships: %v", err)
		return
	}
	for _, m := range ms {
		if live[m] {
			continue
		}
		if err := ar.mgr.store.ObserveWorkspaceAddresses(ctx, m, nil, nil, ar.now().UnixMilli()); err != nil {
			log.Printf("egress address recorder: close orphan %s: %v", m, err)
		}
	}
}
