// Package pushhub runs one ticker per key (a workspace) for all of its subscribers (#3271).
//
// Each /api/events connection used to tick on its own, so N windows on one workspace rebuilt the
// same payloads N times per period on unaligned clocks. A Hub runs one goroutine per key, alive
// only while the key has a subscriber. Each tick it builds one snapshot and hands it to every
// subscriber; whatever differs per subscriber stays with the subscriber.
//
// The build runs on the hub's context, never a subscriber's, so one subscriber going away cannot
// fail the others. A subscriber's mailbox holds one snapshot: a slow subscriber has the older one
// replaced rather than queued, and the hub never waits on it.
//
// In-process only: two CP processes (a blue/green switch) each run their own hub, which is the
// old cost and nothing worse.
package pushhub

import (
	"context"
	"sync"
	"time"
)

// Hub[S, T]: S is what a build needs from a subscriber (the latest joiner's value is used), T is
// the snapshot. The zero value is usable.
type Hub[S, T any] struct {
	mu   sync.Mutex
	keys map[string]*keyHub[S, T]
}

// Sub is one subscriber's mailbox.
type Sub[T any] struct {
	C   <-chan T // capacity 1; the latest snapshot wins
	ch  chan T
	got bool // guarded by Hub.mu
}

type keyHub[S, T any] struct {
	key   string
	tick  time.Duration
	build func(ctx context.Context, src S) T
	kick  chan struct{} // a joiner without a snapshot wants one now
	// Done is closed when the goroutine has exited.
	done   chan struct{}
	cancel context.CancelFunc

	// guarded by Hub.mu
	subs    map[*Sub[T]]struct{}
	src     S
	snap    T
	snapAt  time.Time
	hasSnap bool
}

// Join registers a subscriber under key, starting the ticker if it is the first. build is used
// only by the goroutine that Join starts. leave must be called exactly once; the last one stops
// the goroutine.
func (h *Hub[S, T]) Join(key string, src S, tick time.Duration, build func(ctx context.Context, src S) T) (sub *Sub[T], leave func()) {
	ch := make(chan T, 1)
	sub = &Sub[T]{C: ch, ch: ch}
	h.mu.Lock()
	k := h.keys[key]
	if k == nil {
		ctx, cancel := context.WithCancel(context.Background())
		k = &keyHub[S, T]{key: key, tick: tick, build: build, kick: make(chan struct{}, 1),
			done: make(chan struct{}), cancel: cancel, subs: map[*Sub[T]]struct{}{}}
		if h.keys == nil {
			h.keys = map[string]*keyHub[S, T]{}
		}
		h.keys[key] = k
		go h.run(ctx, k)
	}
	k.subs[sub] = struct{}{}
	k.src = src
	// A snapshot younger than half a tick is as good as a fresh one for a subscriber that just
	// arrived; an older one makes the hub build now. Either way the first frame is complete.
	if k.hasSnap && time.Since(k.snapAt) < k.tick/2 {
		sub.ch <- k.snap
		sub.got = true
	} else {
		select {
		case k.kick <- struct{}{}:
		default:
		}
	}
	h.mu.Unlock()

	return sub, func() {
		h.mu.Lock()
		delete(k.subs, sub)
		last := len(k.subs) == 0
		if last && h.keys[key] == k {
			delete(h.keys, key)
		}
		h.mu.Unlock()
		if last {
			k.cancel()
		}
	}
}

// Active reports how many keys have a running ticker.
func (h *Hub[S, T]) Active() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.keys)
}

// Done returns a channel closed once key's goroutine has exited, or nil if key has no hub.
func (h *Hub[S, T]) Done(key string) <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if k := h.keys[key]; k != nil {
		return k.done
	}
	return nil
}

func (h *Hub[S, T]) run(ctx context.Context, k *keyHub[S, T]) {
	defer close(k.done)
	t := time.NewTicker(k.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.step(ctx, k, true)
		case <-k.kick:
			h.step(ctx, k, false)
		}
	}
}

// step builds a snapshot and fans it out. A kick that finds a fresh snapshot (another joiner's
// kick built it) only serves the subscribers that have none, so joiners never cost a rebuild
// within half a tick.
func (h *Hub[S, T]) step(ctx context.Context, k *keyHub[S, T], ticked bool) {
	h.mu.Lock()
	src, snap, fresh := k.src, k.snap, k.hasSnap && time.Since(k.snapAt) < k.tick/2
	h.mu.Unlock()
	if !ticked && fresh {
		h.deliver(k, snap, false)
		return
	}
	snap = k.build(ctx, src)
	if ctx.Err() != nil {
		return
	}
	h.mu.Lock()
	k.snap, k.snapAt, k.hasSnap = snap, time.Now(), true
	h.mu.Unlock()
	h.deliver(k, snap, true)
}

func (h *Hub[S, T]) deliver(k *keyHub[S, T], snap T, all bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range k.subs {
		if !all && s.got {
			continue
		}
		s.got = true
		select { // drop the stale one: never block on, or queue behind, a slow subscriber
		case <-s.ch:
		default:
		}
		select {
		case s.ch <- snap:
		default:
		}
	}
}
