package main

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Retention of raw egress events (ADR 0122, addendum 2026-10-25). egress_event takes a row
// per connection and egress_daily already keeps the aggregate, so the raw rows are deleted
// once they are older than the retention window. workspace_address rows are deleted with
// them: a closed interval is only read to attribute an event inside it, so it goes once
// every event it could attribute is gone.

// egressEventRetentionDefault is how long raw events are kept when
// AF_EGRESS_EVENT_RETENTION_DAYS is not set.
const egressEventRetentionDefault = 30 * 24 * time.Hour

// egressPurgeSkew is how much further back than the retention window the purge reaches
// before it deletes. Ingest refuses an event older than the window, so a purged row can
// never be stored again by a resend; the margin keeps that true between two CPs whose clocks
// differ by less than this.
const egressPurgeSkew = time.Hour

const (
	egressPurgeInterval = 10 * time.Minute
	// egressPurgeBatch bounds one DELETE, so it holds its locks for milliseconds and the
	// write-ahead log grows in small steps on both SQLite and Postgres.
	egressPurgeBatch = 5000
	// egressPurgePause is the breathing room between two batches, so ingest keeps its turn.
	egressPurgePause = 50 * time.Millisecond
	egressPurgeLease = "egress-event-purge"
)

// parseEgressRetention reads AF_EGRESS_EVENT_RETENTION_DAYS: whole days raw events are kept.
// Empty means the default and 0 means keep forever (no purge, and ingest refuses nothing as
// too old). Anything else that is not a positive number is logged and replaced by the
// default: a typo must not silently switch deletion off or on.
func parseEgressRetention(v string) time.Duration {
	if v == "" {
		return egressEventRetentionDefault
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Printf("egress: ignoring AF_EGRESS_EVENT_RETENTION_DAYS %q: want a number of days, 0 to keep forever", v)
		return egressEventRetentionDefault
	}
	return time.Duration(n) * 24 * time.Hour
}

// egressPurger deletes expired raw events and address intervals. One Control Plane runs it
// at a time (egressPurgeLease, as the address recorder does): two purgers would only
// contend for the same rows.
type egressPurger struct {
	st     store.EgressAttributionStore
	leases interface {
		AcquireCPLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error)
	}
	retention time.Duration
	interval  time.Duration
	batch     int
	pause     time.Duration
	holder    string
	now       func() time.Time
}

func newEgressPurger(mgr *manager, retention time.Duration) *egressPurger {
	return &egressPurger{
		st: mgr.store, leases: mgr.store, retention: retention, interval: egressPurgeInterval,
		batch: egressPurgeBatch, pause: egressPurgePause, holder: store.NewID(), now: time.Now,
	}
}

func (p *egressPurger) run(ctx context.Context) {
	log.Printf("egress purge: keeping raw events %s", p.retention)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		p.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// tick purges if this CP holds the lease, and gives up well before the lease can expire,
// counted from when the request that took it was sent.
func (p *egressPurger) tick(ctx context.Context) {
	ttl := 3 * p.interval
	sent := time.Now()
	ok, err := p.leases.AcquireCPLease(ctx, egressPurgeLease, p.holder, ttl)
	if err != nil {
		log.Printf("egress purge: lease: %v", err)
		return
	}
	if !ok {
		return
	}
	pctx, cancel := context.WithDeadline(ctx, sent.Add(ttl*2/3))
	defer cancel()
	p.purge(pctx)
}

// purge deletes events first, then the address rows: an interval is never gone while an
// event it attributes is still there.
func (p *egressPurger) purge(ctx context.Context) {
	before := p.now().Add(-p.retention - egressPurgeSkew).UnixMilli()
	events := p.drain(ctx, "events", func() (int, error) { return p.st.PurgeEgressEvents(ctx, before, p.batch) })
	addrs := p.drain(ctx, "address rows", func() (int, error) { return p.st.PurgeWorkspaceAddresses(ctx, before, p.batch) })
	if events+addrs > 0 {
		log.Printf("egress purge: deleted %d events and %d address rows older than %s", events, addrs,
			time.UnixMilli(before).UTC().Format(time.RFC3339))
	}
}

// drain runs one bounded delete after another until a batch comes back short.
func (p *egressPurger) drain(ctx context.Context, what string, del func() (int, error)) int {
	total := 0
	for ctx.Err() == nil {
		n, err := del()
		total += n
		if err != nil {
			log.Printf("egress purge: %s: %v", what, err)
			return total
		}
		if n < p.batch {
			return total
		}
		select {
		case <-ctx.Done():
		case <-time.After(p.pause):
		}
	}
	return total
}
