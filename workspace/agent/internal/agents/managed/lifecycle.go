// Package managed is the package-level scaffold every managed kind repeats: the registry of
// live handles and the lifecycle calls the Agent makes on a kind as a whole (reconcile at boot,
// abort and shutdown on the way down, the per-session aliveness questions). The handle itself
// stays the kind's own.
package managed

import (
	"log"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Handle is what the lifecycle asks of a kind's handle. Each answers under the handle's own
// lock.
type Handle interface {
	// Alive reports a live runtime behind the handle.
	Alive() bool
	// Busy reports a turn running or input waiting (graceful shutdown waits on it; an exclusive
	// driver switch refuses on it).
	Busy() bool
	// HoldsEntry reports an entry taken from the queue and not yet settled: a move asks this,
	// under the exclusive gate, so it never starts while the runtime may be receiving input
	// (ADR 0115 decision 11.2). Entries still queued do not count: the move carries them.
	HoldsEntry() bool
	// Abort interrupts the running turn, if there is one (graceful shutdown's per-pane Ctrl-C).
	Abort()
}

// Downer is a handle with a child to stop at Agent shutdown: Down marks it dead and returns the
// stop, which Shutdown runs once it has released the registry.
type Downer interface {
	Down() (stop func())
}

// Lifecycle is one kind's registry and its kind-wide lifecycle calls. The registry is the kind's
// own mutex and map (tests and the kind's other code reach them directly), so Mu and Handles
// point at them; Handles is a pointer to the variable because a test may swap the map.
type Lifecycle[H Handle] struct {
	Kind string // session.Kind* constant
	// Name prefixes the reconcile log line ("<Name> managed: reconcile …").
	Name    string
	Mu      *sync.Mutex
	Handles *map[string]H
	Ledger  *agents.MsgLedger

	// Resume starts or reattaches m (the kind's Driver.Resume).
	Resume func(m session.Meta) error
	// SkipReconcile leaves m alone at reconcile, after the stopped check. nil: none skipped.
	SkipReconcile func(m session.Meta) bool
	// Claim takes the claim a Managed Resume needs against a concurrent switch to Terminal.
	// ok=false skips m; release ends the claim after Resume. nil: no claim.
	Claim func(name string) (release func(), ok bool)
	// AfterReconcile runs once every meta was handled. nil: nothing.
	AfterReconcile func(reason string)
	// LogReconcile reports a failed reconcile of m. nil: the "<Name> managed: reconcile" line.
	LogReconcile func(m session.Meta, reason string, err error)
}

// Get is the handle registered for name.
func (l *Lifecycle[H]) Get(name string) (H, bool) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	h, ok := (*l.Handles)[name]
	return h, ok
}

// GetOrAdd is the handle registered for name, registering mk()'s first.
func (l *Lifecycle[H]) GetOrAdd(name string, mk func() H) H {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	h, ok := (*l.Handles)[name]
	if !ok {
		h = mk()
		(*l.Handles)[name] = h
	}
	return h
}

// Take removes name's handle from the registry and returns it (DropHandle's first step).
func (l *Lifecycle[H]) Take(name string) (H, bool) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	h, ok := (*l.Handles)[name]
	delete(*l.Handles, name)
	return h, ok
}

// Live snapshots the handles whose runtime is alive.
func (l *Lifecycle[H]) Live() []H {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	var out []H
	for _, h := range *l.Handles {
		if h.Alive() {
			out = append(out, h)
		}
	}
	return out
}

// Alive reports whether the session has a live runtime handle.
func (l *Lifecycle[H]) Alive(name string) bool {
	h, ok := l.Get(name)
	return ok && h.Alive()
}

// Busy reports a turn running or queued for the session.
func (l *Lifecycle[H]) Busy(name string) bool {
	h, ok := l.Get(name)
	return ok && h.Busy()
}

// InFlight reports that the session's handle holds an entry it took from the queue and has not
// settled (Handle.HoldsEntry).
func (l *Lifecycle[H]) InFlight(name string) bool {
	h, ok := l.Get(name)
	return ok && h.HoldsEntry()
}

// RemoveLedger drops the session's ClientMessageID ledger. Only for /stop, which discards the
// slot's identity as well; halt and archive keep it because those sessions can resume.
func (l *Lifecycle[H]) RemoveLedger(name string) { l.Ledger.Remove(name) }

// Abort interrupts every running turn of a live handle.
func (l *Lifecycle[H]) Abort() {
	for _, h := range l.Live() {
		h.Abort()
	}
}

// Shutdown marks every registered handle that has a child dead and stops the children, after
// releasing the registry. The conversation stays with the runtime, and the next boot's
// Reconcile reattaches.
func (l *Lifecycle[H]) Shutdown() {
	l.Mu.Lock()
	var stops []func()
	for _, h := range *l.Handles {
		if d, ok := any(h).(Downer); ok {
			stops = append(stops, d.Down())
		}
	}
	l.Mu.Unlock()
	for _, stop := range stops {
		stop()
	}
}

// Reconcile reattaches the kind's managed sessions after an Agent boot or a runtime death. It
// covers every managed meta of the kind that is not archived and not deliberately stopped. On
// failure the session stays stopped and the member's resume click retries it.
func (l *Lifecycle[H]) Reconcile(reason string) {
	for _, m := range session.ListMetas() {
		if m.Kind != l.Kind || m.DriverKind() != session.DriverManaged || m.Archived {
			continue
		}
		if m.StoppedAt != "" {
			if _, ok := l.Get(m.Name); !ok {
				continue // deliberately stopped — resume only on user action
			}
		}
		if l.SkipReconcile != nil && l.SkipReconcile(m) {
			continue
		}
		release := func() {}
		if l.Claim != nil {
			r, ok := l.Claim(m.Name)
			if !ok {
				continue
			}
			release = r
		}
		if err := l.Resume(m); err != nil {
			if l.LogReconcile != nil {
				l.LogReconcile(m, reason, err)
			} else {
				log.Printf("%s managed: reconcile %s (%s): %v", l.Name, m.Name, reason, err)
			}
		}
		release()
	}
	if l.AfterReconcile != nil {
		l.AfterReconcile(reason)
	}
}
