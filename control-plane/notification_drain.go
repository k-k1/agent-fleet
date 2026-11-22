package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Bounds one workspace's drain. A slow or wedged Agent must not hold up the sweep for the
// workspaces behind it.
const notificationDrainTimeout = 10 * time.Second

// Workspaces drained at once within a sweep: enough that one slow Agent does not stall the
// rest, few enough not to spike a small host.
const notificationDrainParallel = 4

// notificationDrainer pulls the Agent notification outbox of every running workspace into the
// store on a timer. Without it an Agent's events reach the CP only when a Console asks or just
// before the workspace is stopped, so a question card raised while no Console is open would not
// reach Web Push until someone looks. The push itself is not sent here: it hangs off
// InsertNotification, so a row this drain creates is announced exactly like one a Console
// drain creates, and a row both created is announced once.
type notificationDrainer struct {
	mgr      *manager
	interval time.Duration
}

func newNotificationDrainer(mgr *manager, iv time.Duration) *notificationDrainer {
	return &notificationDrainer{mgr: mgr, interval: iv}
}

func (d *notificationDrainer) run(ctx context.Context) {
	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.sweep(ctx)
		}
	}
}

func (d *notificationDrainer) sweep(ctx context.Context) {
	tenants, err := d.mgr.store.ListTenants(ctx)
	if err != nil {
		return
	}
	sem := make(chan struct{}, notificationDrainParallel)
	var wg sync.WaitGroup
	for _, tn := range tenants {
		wss, err := d.mgr.store.ListWorkspaces(ctx, tn.ID)
		if err != nil {
			continue
		}
		for _, ws := range wss {
			// A member with no subscribed device has nobody to push to; leaving the outbox
			// alone keeps the load off every other workspace.
			if !d.hasSubscription(ctx, ws.MembershipID) {
				continue
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				wg.Wait()
				return
			}
			wg.Add(1)
			go func(ws store.Workspace) {
				defer wg.Done()
				defer func() { <-sem }()
				defer func() {
					if r := recover(); r != nil {
						log.Printf("notification-drain: %s: panic: %v", ws.ContainerName, r)
					}
				}()
				rt := d.mgr.runtimeFor(ws, noSecretKeys)
				dctx, cancel := context.WithTimeout(ctx, notificationDrainTimeout)
				defer cancel()
				d.drainWorkspace(dctx, rt, ws.MembershipID)
			}(ws)
		}
	}
	wg.Wait()
}

func (d *notificationDrainer) hasSubscription(ctx context.Context, membershipID string) bool {
	subs, err := d.mgr.store.ListPushSubscriptions(ctx, membershipID)
	return err == nil && len(subs) > 0
}

// drainWorkspace drains one workspace's outbox, only while it is running: asking a stopped
// workspace would start nothing but a failed connection, and the outbox was already drained
// before the stop.
func (d *notificationDrainer) drainWorkspace(ctx context.Context, rt runtime.Runtime, membershipID string) string {
	if rt.State(ctx) != "running" {
		return "stopped"
	}
	return drainAgentOutbox(ctx, d.mgr.store, rt, membershipID)
}
