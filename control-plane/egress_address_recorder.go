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
// Every replica runs it, like the reaper. The writes are idempotent, so two replicas only
// cost the orchestrator two reads.
type addressRecorder struct {
	mgr      *manager
	interval time.Duration
	now      func() time.Time
}

func newAddressRecorder(mgr *manager, interval time.Duration) *addressRecorder {
	return &addressRecorder{mgr: mgr, interval: interval, now: time.Now}
}

func (ar *addressRecorder) run(ctx context.Context) {
	log.Printf("egress address recorder: interval=%s", ar.interval)
	t := time.NewTicker(ar.interval)
	defer t.Stop()
	for {
		ar.sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (ar *addressRecorder) sweep(ctx context.Context) {
	tenants, err := ar.mgr.store.ListTenants(ctx)
	if err != nil {
		log.Printf("egress address recorder: list tenants: %v", err)
		return
	}
	for _, t := range tenants {
		wss, err := ar.mgr.store.ListWorkspaces(ctx, t.ID)
		if err != nil {
			log.Printf("egress address recorder: list workspaces (%s): %v", t.Slug, err)
			continue
		}
		for _, ws := range wss {
			ar.recordWorkspace(ctx, ws)
		}
	}
}

// recordWorkspace asks one workspace's runtime what it holds. A workspace the database has
// as stopped is still asked while it has an open row, so the row is closed (with the stop
// time ECS reports, where it does) instead of staying open forever.
func (ar *addressRecorder) recordWorkspace(ctx context.Context, ws store.Workspace) {
	if ws.State != "running" {
		open, err := ar.mgr.store.HasOpenWorkspaceAddress(ctx, ws.MembershipID)
		if err != nil || !open {
			return
		}
	}
	rep, ok := ar.mgr.runtimeFor(ws, noSecretKeys).(runtime.AddressReporter)
	if !ok {
		return
	}
	held, err := rep.HeldAddresses(ctx)
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
