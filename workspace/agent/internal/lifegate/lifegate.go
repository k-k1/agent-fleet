// Package lifegate is the per-session lifecycle gate of ADR 0115 decision 11.1: what lets a
// move close a session's input so that exactly one session can act on the working copy.
//
// Every path that can start a turn or a process on a session holds the gate shared, as a
// Lease, from its checks through the point where its input is committed (typed, accepted into
// a driver's queue, handed to the runtime). A move takes it exclusively, so it waits for the
// inputs already admitted and none is admitted while it holds the gate. Once the session's
// meta says moving or movedTo, Enter refuses with a *MovedError naming the target.
//
// The meta lock (sessionx's sessionLockMu) cannot do this: it is one mutex for every session,
// so it can never be held across a send or a CP call.
//
// One lease per input operation. The gate prefers a waiting writer — otherwise a steady trickle
// of inputs could keep a move waiting forever — and that makes a second shared take on the same
// goroutine a deadlock: it queues behind the writer, which waits for the first. So the public
// entry takes the lease once and passes it inward: a function that needs the gate held takes a
// *Lease parameter and never calls Enter, and a lease carried on a context (WithLease) is
// borrowed by Enter instead of taken again, for inner steps that are reached through an HTTP
// handler.
package lifegate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// gate is one session's reader/writer gate. It is hand-rolled rather than a sync.RWMutex
// because the exclusive side has to give up on a context, and tests need to see that a writer
// is waiting.
type gate struct {
	mu      sync.Mutex
	readers int
	writer  bool
	waiting int // writers queued: while any is, no new reader is admitted
	// changed is closed and replaced on every transition, waking whoever waits on it.
	changed chan struct{}
	// closed mirrors the meta's moving / movedTo for the life of this process. The meta is
	// the durable record; this copy is what Enter trusts while the process lives, because a
	// meta read that lands in the middle of an unrelated rewrite of the file fails to parse.
	closed *MovedError
	// reopen holds the callbacks of refused queue pumps, run once by Reopen.
	reopen []func()
}

var (
	gatesMu sync.Mutex
	gates   = map[string]*gate{}
)

func gateOf(name string) *gate {
	gatesMu.Lock()
	defer gatesMu.Unlock()
	g := gates[name]
	if g == nil {
		g = &gate{changed: make(chan struct{})}
		gates[name] = g
	}
	return g
}

func (g *gate) signalLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}

// MovedError is the refusal of a session whose input a move has closed.
type MovedError struct {
	Session string
	Target  string
	// Done: the target exists (movedTo). false: the move is still running (moving).
	Done bool
}

func (e *MovedError) Error() string {
	if e.Done {
		return fmt.Sprintf("session %s was moved to %s and accepts no input; send to %s instead", e.Session, e.Target, e.Target)
	}
	return fmt.Sprintf("session %s is being moved to %s and accepts no input; send to %s once it exists", e.Session, e.Target, e.Target)
}

// HeldError is the refusal of a session whose input something other than a move keeps closed
// until it is settled (an unfinished route switch). It is not a *MovedError: there is no target
// to send to.
type HeldError struct {
	Session string
	Reason  string
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("session %s accepts no input: %s", e.Session, e.Reason)
}

var heldCheck func(name string) string

// SetHeldCheck installs the check Enter and Closed run after the move state: a non-empty answer
// is the reason the session's input stays closed. The answer must rest on durable state, so a
// restart does not open what was closed. Installed once at start-up.
func SetHeldCheck(fn func(name string) string) { heldCheck = fn }

// ErrNotHeld is what a step that requires a lease answers when it was handed none, or one
// that is released or names another session. It is a programming error made visible: without
// it a nil lease would silently run the step ungated.
var ErrNotHeld = errors.New("lifegate: the session's lease is not held")

// Lease is a shared hold on one session's gate. A borrowed lease (Enter found one on the
// context) releases nothing: its owner does.
type Lease struct {
	name     string
	g        *gate
	borrowed bool
	mu       sync.Mutex
	released bool
}

// Name is the session the lease holds.
func (l *Lease) Name() string { return l.name }

// Holds reports whether l is a live lease on name.
func (l *Lease) Holds(name string) bool {
	if l == nil || l.name != name {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.released
}

// Check is Holds as an error, for a step that requires the lease.
func (l *Lease) Check(name string) error {
	if !l.Holds(name) {
		return ErrNotHeld
	}
	return nil
}

// Release gives the hold back. Idempotent, so a path can release at its commit point and still
// defer a release for its early returns.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return
	}
	l.released = true
	l.mu.Unlock()
	if l.borrowed {
		return
	}
	g := l.g
	g.mu.Lock()
	g.readers--
	if g.readers == 0 {
		g.signalLocked()
	}
	g.mu.Unlock()
}

type leaseKey struct{}

// WithLease carries l on ctx, so an inner step reached through an HTTP handler borrows it
// instead of taking a second one.
func WithLease(ctx context.Context, l *Lease) context.Context {
	return context.WithValue(ctx, leaseKey{}, l)
}

// Enter takes name's gate shared, waiting while a move holds it or waits for it, and refuses
// with a *MovedError when the session's input is closed. A live lease on name carried by ctx
// is borrowed instead. A session with no meta is admitted: whether it exists is the caller's
// question.
func Enter(ctx context.Context, name string) (*Lease, error) {
	if outer, ok := ctx.Value(leaseKey{}).(*Lease); ok && outer.Holds(name) {
		return &Lease{name: name, g: outer.g, borrowed: true}, nil
	}
	g := gateOf(name)
	g.mu.Lock()
	for g.writer || g.waiting > 0 {
		ch := g.changed
		g.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		g.mu.Lock()
	}
	g.readers++
	closed := g.closed
	g.mu.Unlock()
	l := &Lease{name: name, g: g}
	if closed == nil {
		var err error
		if closed, err = closedByMeta(name); err != nil {
			l.Release()
			return nil, err
		}
	}
	if closed != nil {
		l.Release()
		e := *closed
		return nil, &e
	}
	if heldCheck != nil {
		if why := heldCheck(name); why != "" {
			l.Release()
			return nil, &HeldError{Session: name, Reason: why}
		}
	}
	return l, nil
}

// ErrMetaUnreadable refuses a session whose meta exists but cannot be read or parsed. After a
// restart the meta is the only record of moving / movedTo, so a lease issued on a meta that
// could not be read could start a moved source. The caller may try again later.
var ErrMetaUnreadable = errors.New("lifegate: the session's meta cannot be read; refusing until it can")

// closedByMeta reads the durable side of the closed state. Only a meta that does not exist
// admits the input as "no session". One that does not parse is usually being rewritten
// (session.WriteMeta truncates first), so it is read again for a while; if it still cannot be
// read, the answer is ErrMetaUnreadable, never "open".
func closedByMeta(name string) (*MovedError, error) {
	var lastErr error
	for i := 0; i < 20; i++ {
		if i > 0 {
			time.Sleep(5 * time.Millisecond)
		}
		b, err := os.ReadFile(session.MetaPath(name))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err == nil {
			var m session.Meta
			if err = json.Unmarshal(b, &m); err == nil {
				m.Name = name
				return movedErrorOf(m), nil
			}
		}
		lastErr = err
	}
	return nil, fmt.Errorf("%w: %v", ErrMetaUnreadable, lastErr)
}

func movedErrorOf(m session.Meta) *MovedError {
	switch {
	case m.MovedTo != "":
		return &MovedError{Session: m.Name, Target: m.MovedTo, Done: true}
	case m.Moving != "":
		return &MovedError{Session: m.Name, Target: m.Moving}
	}
	return nil
}

// Closed reports whether name's input is closed now, without taking the gate; an unreadable
// meta counts as closed. For a caller that must not wait, such as a refused pump checking
// whether it missed the reopen.
func Closed(name string) bool {
	g := gateOf(name)
	g.mu.Lock()
	closed := g.closed
	g.mu.Unlock()
	if closed != nil {
		return true
	}
	byMeta, err := closedByMeta(name)
	if byMeta != nil || err != nil {
		return true
	}
	return heldCheck != nil && heldCheck(name) != ""
}

// Exclusive is a move's hold on one session's gate: no lease is held while it is, and none is
// admitted until it is released.
type Exclusive struct {
	name string
	g    *gate
	once sync.Once
}

// Lock takes name's gate exclusively: it waits for every lease to be released, and admits no
// new one from the moment it starts waiting. It gives up when ctx ends. It must never be called
// while holding a lease on the same session, which would wait for itself.
func Lock(ctx context.Context, name string) (*Exclusive, error) {
	g := gateOf(name)
	g.mu.Lock()
	g.waiting++
	for g.writer || g.readers > 0 {
		ch := g.changed
		g.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			g.mu.Lock()
			g.waiting--
			g.signalLocked() // the readers it held back may go
			g.mu.Unlock()
			return nil, ctx.Err()
		}
		g.mu.Lock()
	}
	g.waiting--
	g.writer = true
	g.mu.Unlock()
	return &Exclusive{name: name, g: g}, nil
}

// SetClosed records in memory that the session's input is closed (moving, or moved when done)
// or, with target "", open again. Only the holder of the exclusive gate changes it, so a lease
// either sees the change or was admitted before it.
func (x *Exclusive) SetClosed(target string, done bool) {
	setClosed(x.g, x.name, target, done)
}

// Unlock releases the exclusive hold. Idempotent.
func (x *Exclusive) Unlock() {
	x.once.Do(func() {
		x.g.mu.Lock()
		x.g.writer = false
		x.g.signalLocked()
		x.g.mu.Unlock()
	})
}

// SetClosed is Exclusive.SetClosed for the transitions that need no exclusive hold: moving to
// movedTo (input is closed either way), and opening again on a rollback or the user's reopen
// (nothing waits for an open gate to stay closed).
func SetClosed(name, target string, done bool) {
	setClosed(gateOf(name), name, target, done)
}

func setClosed(g *gate, name, target string, done bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if target == "" {
		g.closed = nil
		return
	}
	g.closed = &MovedError{Session: name, Target: target, Done: done}
}

// WhenReopened registers fn to run once when the session's input opens again (Reopen). A queue
// pump refused by a move leaves its input queued and stops; this is how it starts again if the
// move rolls back.
func WhenReopened(name string, fn func()) {
	g := gateOf(name)
	g.mu.Lock()
	g.reopen = append(g.reopen, fn)
	g.mu.Unlock()
}

// Reopen opens the session's input in memory and runs the callbacks WhenReopened registered.
// The caller has already cleared the meta.
func Reopen(name string) {
	g := gateOf(name)
	g.mu.Lock()
	g.closed = nil
	fns := g.reopen
	g.reopen = nil
	g.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// DropReopen forgets the callbacks without running them: the move committed, and the source's
// queue goes with its stop.
func DropReopen(name string) {
	g := gateOf(name)
	g.mu.Lock()
	g.reopen = nil
	g.mu.Unlock()
}

// WritersWaiting is how many exclusive acquires are queued on name's gate. For tests that have
// to know a move is waiting before they go on.
func WritersWaiting(name string) int {
	g := gateOf(name)
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.waiting
}

// ResetForTest forgets every gate, as an Agent restart does.
func ResetForTest() {
	gatesMu.Lock()
	gates = map[string]*gate{}
	gatesMu.Unlock()
}
