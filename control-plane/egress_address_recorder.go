package main

import (
	"context"
	"log"
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
}

const addressRecorderLease = "egress-address-recorder"

// addressRecentlyActive is how long after its last activity a stopped workspace is still
// asked. A workspace started and stopped between two sweeps never had an open row, and ECS
// still describes its stopped task for at least an hour, so its interval can be recovered.
const addressRecentlyActive = time.Hour

func newAddressRecorder(mgr *manager, interval time.Duration) *addressRecorder {
	return &addressRecorder{mgr: mgr, interval: interval, holder: store.NewID(), now: time.Now}
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

func (ar *addressRecorder) sweep(ctx context.Context) {
	tenants, err := ar.mgr.store.ListTenants(ctx)
	if err != nil {
		log.Printf("egress address recorder: list tenants: %v", err)
		return
	}
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
			ar.recordWorkspace(ctx, ws)
		}
	}
	ar.closeOrphans(ctx, live)
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
	held, err := rep.HeldAddresses(ctx, open)
	if err != nil {
		// Unknown is not gone: closing the open rows here would end intervals that are
		// still running and leave the next connections unattributed.
		log.Printf("egress address recorder: %s: %v", ws.ContainerName, err)
		return
	}
	seen := make([]store.WorkspaceAddress, 0, len(held))
	for _, h := range held {
		a := store.WorkspaceAddress{
			MembershipID: ws.MembershipID, WorkspaceID: ws.ID, Address: h.Address, Ref: h.Ref,
			From: h.From.UnixMilli(),
		}
		if !h.Until.IsZero() {
			a.Until = h.Until.UnixMilli()
		}
		seen = append(seen, a)
	}
	if err := ar.mgr.store.ObserveWorkspaceAddresses(ctx, ws.MembershipID, seen, ar.now().UnixMilli()); err != nil {
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
		if err := ar.mgr.store.ObserveWorkspaceAddresses(ctx, m, nil, ar.now().UnixMilli()); err != nil {
			log.Printf("egress address recorder: close orphan %s: %v", m, err)
		}
	}
}
