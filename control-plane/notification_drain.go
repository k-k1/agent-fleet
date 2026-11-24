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
	store     store.Store
	runtimeOf func(store.Workspace) runtime.Runtime
	interval  time.Duration
	// Zero means the package defaults; tests shorten them.
	drainTimeout, queryTimeout time.Duration
	// nextPrune is when the next prune may run: an hour after a success, a minute after a
	// failure, so one lock wait at start-up does not skip the first prune for an hour.
	nextPrune time.Time
}

func (d *notificationDrainer) drainTO() time.Duration {
	if d.drainTimeout > 0 {
		return d.drainTimeout
	}
	return notificationDrainTimeout
}

func (d *notificationDrainer) queryTO() time.Duration {
	if d.queryTimeout > 0 {
		return d.queryTimeout
	}
	return notificationDrainQueryTimeout
}

// startNotificationDrain starts the timer only when the Web Push sender exists. A sender that
// failed to start (bad VAPID key) leaves push nil and the notification hook unset, and
// draining then would ack events nobody can ever push.
func startNotificationDrain(ctx context.Context, mgr *manager, iv time.Duration) bool {
	if mgr.push == nil {
		return false
	}
	go newNotificationDrainer(mgr, iv).run(ctx)
	return true
}

func newNotificationDrainer(mgr *manager, iv time.Duration) *notificationDrainer {
	return &notificationDrainer{store: mgr.store, interval: iv,
		runtimeOf: func(ws store.Workspace) runtime.Runtime { return mgr.runtimeFor(ws, noSecretKeys) }}
}

// Bounds one database read of the sweep, so a single stalled query cannot hold up the
// workspaces behind it for the rest of the tick.
const notificationDrainQueryTimeout = 10 * time.Second

// How often the sweep prunes expired notification rows. A Console prunes on every list; with
// none open the drain is the only writer, so it must prune too.
const notificationPruneEvery = time.Hour

const notificationPruneRetry = time.Minute

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
	qctx, qcancel := context.WithTimeout(ctx, d.queryTO())
	tenants, err := d.store.ListTenants(qctx)
	qcancel()
	if err != nil {
		return
	}
	if !time.Now().Before(d.nextPrune) {
		pctx, pcancel := context.WithTimeout(ctx, d.queryTO())
		if err := d.store.SweepNotifications(pctx, notificationCutoff()); err != nil {
			log.Printf("notification-drain: prune: %v", err)
			d.nextPrune = time.Now().Add(notificationPruneRetry)
		} else {
			d.nextPrune = time.Now().Add(notificationPruneEvery)
		}
		pcancel()
	}
	// A member with no subscribed device has nobody to push to; leaving the outbox alone keeps
	// the load off every other workspace. One read answers it for the whole sweep; when it
	// fails the sweep is skipped like a failed tenant list, and the next tick asks again.
	sctx, scancel := context.WithTimeout(ctx, d.queryTO())
	subbed, err := d.store.ListPushSubscribedMemberships(sctx)
	scancel()
	if err != nil || len(subbed) == 0 {
		return
	}
	subscribed := make(map[string]bool, len(subbed))
	for _, m := range subbed {
		subscribed[m] = true
	}
	sem := make(chan struct{}, notificationDrainParallel)
	var wg sync.WaitGroup
	for _, tn := range tenants {
		wctx, wcancel := context.WithTimeout(ctx, d.queryTO())
		wss, err := d.store.ListWorkspaces(wctx, tn.ID)
		wcancel()
		if err != nil {
			continue
		}
		for _, ws := range wss {
			if !subscribed[ws.MembershipID] {
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
				rt := d.runtimeOf(ws)
				dctx, cancel := context.WithTimeout(ctx, d.drainTO())
				defer cancel()
				d.drainWorkspace(dctx, rt, ws.MembershipID)
			}(ws)
		}
	}
	wg.Wait()
}

// drainWorkspace drains one workspace's outbox, only while it is running: asking a stopped
// workspace would start nothing but a failed connection, and the outbox was already drained
// before the stop.
func (d *notificationDrainer) drainWorkspace(ctx context.Context, rt runtime.Runtime, membershipID string) string {
	if rt.State(ctx) != "running" {
		return "stopped"
	}
	return drainAgentOutbox(ctx, d.store, rt, membershipID)
}
