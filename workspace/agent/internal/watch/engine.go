package watch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

// Delivery is a sink's answer for one event.
type Delivery int

const (
	// NotYet: the owner cannot take it now (busy, starting, not ready); ask again later.
	NotYet Delivery = iota
	// Delivered: told, record it.
	Delivered
	// Gone: the owner no longer exists or was archived; its subscriptions end.
	Gone
)

// Sink delivers events to the session that owns a subscription.
type Sink interface {
	// OwnerExists is false for a session that was deleted or archived.
	OwnerExists(owner string) bool
	// Deliver tells the owner about ev. It is called at most once per owner per tick.
	Deliver(sub Subscription, ev Event, now time.Time) Delivery
}

// Store persists the engine's state across Agent restarts.
type Store interface {
	Load() (State, bool)
	Save(State) error
}

// State is what the engine keeps on disk.
type State struct {
	Subs     []Subscription             `json:"subs"`
	Subjects map[SubjectID]SubjectState `json:"subjects"`
}

// SubjectState is the engine's bookkeeping for one subject, shared by every subscription to it.
type SubjectState struct {
	Cursor   json.RawMessage `json:"cursor,omitempty"`
	Events   []Event         `json:"events,omitempty"` // what held at the last good poll
	NextPoll time.Time       `json:"next_poll"`
	LastPoll time.Time       `json:"last_poll,omitempty"`
	Fails    int             `json:"fails,omitempty"`
	LastErr  string          `json:"last_err,omitempty"`
	Settled  bool            `json:"settled,omitempty"`
}

// Engine is the watch engine. All methods are safe for concurrent use.
type Engine struct {
	mu      sync.Mutex
	sources map[string]Source
	sink    Sink
	store   Store
	state   State
	loaded  bool
	now     func() time.Time
	// pollTimeout bounds one Poll.
	pollTimeout time.Duration
}

// New builds an engine over the given sources.
func New(store Store, sink Sink, sources ...Source) *Engine {
	e := &Engine{sources: map[string]Source{}, sink: sink, store: store, now: time.Now, pollTimeout: 30 * time.Second}
	for _, s := range sources {
		e.sources[s.Kind()] = s
	}
	return e
}

// Source returns the adapter of kind.
func (e *Engine) Source(kind string) (Source, bool) {
	s, ok := e.sources[kind]
	return s, ok
}

func (e *Engine) load() {
	if e.loaded {
		return
	}
	e.loaded = true
	if st, ok := e.store.Load(); ok {
		e.state = st
	}
	if e.state.Subjects == nil {
		e.state.Subjects = map[SubjectID]SubjectState{}
	}
	// A restart re-reads every live subject at once (ADR 0133 §5, the start-up poll).
	for id, ss := range e.state.Subjects {
		ss.NextPoll = time.Time{}
		e.state.Subjects[id] = ss
	}
}

func (e *Engine) save() {
	if err := e.store.Save(e.state); err != nil {
		log.Printf("watch: could not save state: %v", err)
	}
}

// Request is one watch call.
type Request struct {
	Owner       string
	Source      string
	Ref         string
	DefaultRepo string
	Types       []string
	Until       string
	Lifetime    time.Duration // 0 = DefaultLifetime
}

// Subscribe resolves the reference, checks the limits and records the subscription. The
// subject is read on the next tick, so a condition that already holds is delivered then.
func (e *Engine) Subscribe(ctx context.Context, r Request) (Subscription, error) {
	src, ok := e.sources[r.Source]
	if !ok {
		return Subscription{}, &ErrRefused{fmt.Sprintf("unknown source %q", r.Source)}
	}
	if err := checkTypes(r.Types, src.Types()); err != nil {
		return Subscription{}, err
	}
	until := r.Until
	if until == "" {
		until = UntilFirst
	}
	if until != UntilFirst && until != UntilDeadline {
		return Subscription{}, &ErrRefused{"until must be first or deadline"}
	}
	life := r.Lifetime
	if life == 0 {
		life = DefaultLifetime
	}
	if life < 0 || life > MaxLifetime {
		return Subscription{}, &ErrRefused{fmt.Sprintf("a watch lives at most %d days", int(MaxLifetime/(24*time.Hour)))}
	}
	// The limits are checked before Resolve spends a request, and again after it.
	if err := e.checkLimits(r.Owner); err != nil {
		return Subscription{}, err
	}
	res, err := src.Resolve(ctx, r.Ref, r.DefaultRepo)
	if err != nil {
		return Subscription{}, err
	}
	if !TokenOK(string(res.Subject)) {
		return Subscription{}, &ErrRefused{"the source returned a subject that cannot be written into a notice"}
	}
	now := e.now()
	sub := Subscription{
		ID: newID(), Owner: r.Owner, Source: r.Source, Subject: res.Subject, Ref: r.Ref, URL: res.URL,
		Types: append([]string(nil), r.Types...), Until: until, Created: now.UTC(), Expires: now.Add(life).UTC(),
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	if err := e.limitsLocked(r.Owner); err != nil {
		return Subscription{}, err
	}
	e.state.Subs = append(e.state.Subs, sub)
	if _, ok := e.state.Subjects[sub.Subject]; !ok {
		e.state.Subjects[sub.Subject] = SubjectState{}
	} else {
		// Match what the subject already showed, so a second watcher hears it without waiting.
		ss := e.state.Subjects[sub.Subject]
		ss.NextPoll = time.Time{}
		e.state.Subjects[sub.Subject] = ss
	}
	e.save()
	return sub, nil
}

func (e *Engine) checkLimits(owner string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	return e.limitsLocked(owner)
}

func (e *Engine) limitsLocked(owner string) error {
	if len(e.state.Subs) >= MaxTotal {
		return &ErrRefused{fmt.Sprintf("this workspace already has %d watches, the limit", MaxTotal)}
	}
	n := 0
	for _, s := range e.state.Subs {
		if s.Owner == owner {
			n++
		}
	}
	if n >= MaxPerOwner {
		return &ErrRefused{fmt.Sprintf("this session already has %d watches, the limit; end one with unwatch", MaxPerOwner)}
	}
	return nil
}

// Unsubscribe ends one of owner's subscriptions. It reports whether there was one.
func (e *Engine) Unsubscribe(owner, id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	for i, s := range e.state.Subs {
		if s.ID == id && s.Owner == owner {
			e.state.Subs = append(e.state.Subs[:i], e.state.Subs[i+1:]...)
			e.dropUnwatchedLocked()
			e.save()
			return true
		}
	}
	return false
}

// Listed is one subscription with its subject's health, for list_watches.
type Listed struct {
	Subscription
	LastPoll time.Time `json:"last_poll,omitempty"`
	NextPoll time.Time `json:"next_poll,omitempty"`
	LastErr  string    `json:"last_error,omitempty"`
}

// List returns owner's subscriptions, oldest first.
func (e *Engine) List(owner string) []Listed {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	out := []Listed{}
	for _, s := range e.state.Subs {
		if s.Owner != owner {
			continue
		}
		ss := e.state.Subjects[s.Subject]
		l := Listed{Subscription: s, LastPoll: ss.LastPoll, NextPoll: ss.NextPoll, LastErr: ss.LastErr}
		l.Delivered, l.Pending = nil, nil
		out = append(out, l)
	}
	return out
}

// Tick runs one sweep: end expired and orphaned subscriptions, read the subjects that are due,
// and deliver what is pending. ctx bounds the reads.
func (e *Engine) Tick(ctx context.Context) {
	e.mu.Lock()
	e.load()
	now := e.now()
	dirty := e.expireLocked(now)
	due := e.dueLocked(now)
	e.mu.Unlock()

	// Reads happen without the lock: a slow provider must not block watch / list_watches.
	type polled struct {
		id     SubjectID
		events []Event
		cursor json.RawMessage
		hint   Hint
		err    error
	}
	var results []polled
	for _, d := range due {
		src, ok := e.sources[d.source]
		if !ok {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, e.pollTimeout)
		evs, cur, hint, err := src.Poll(pctx, d.id, d.cursor)
		cancel()
		results = append(results, polled{d.id, evs, cur, hint, err})
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	now = e.now()
	for _, p := range results {
		ss, ok := e.state.Subjects[p.id]
		if !ok {
			continue // unwatched while it was being read
		}
		dirty = true
		ss.LastPoll = now.UTC()
		if p.err != nil {
			ss.Fails++
			ss.LastErr = p.err.Error()
			var rl *ErrRateLimited
			if errors.As(p.err, &rl) && rl.Until.After(now) {
				ss.NextPoll = rl.Until
			} else {
				ss.NextPoll = now.Add(backoff(ss.Fails))
			}
			e.state.Subjects[p.id] = ss
			continue
		}
		ss.Fails, ss.LastErr = 0, ""
		ss.Cursor = p.cursor
		ss.Events = p.events
		ss.Settled = p.hint.Settled
		after := p.hint.After
		if after <= 0 {
			after = time.Minute
		}
		ss.NextPoll = now.Add(after)
		e.state.Subjects[p.id] = ss
		e.matchLocked(p.id, p.events)
	}
	if e.deliverLocked(now) {
		dirty = true
	}
	if dirty {
		e.save()
	}
}

type dueSubject struct {
	id     SubjectID
	source string
	cursor json.RawMessage
}

// dueLocked lists the subjects whose next read has come, least recently read first, so one busy
// subject cannot keep the rest waiting.
func (e *Engine) dueLocked(now time.Time) []dueSubject {
	srcOf := map[SubjectID]string{}
	for _, s := range e.state.Subs {
		srcOf[s.Subject] = s.Source
	}
	var out []dueSubject
	for id, ss := range e.state.Subjects {
		src, ok := srcOf[id]
		if !ok || now.Before(ss.NextPoll) {
			continue
		}
		out = append(out, dueSubject{id, src, ss.Cursor})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := e.state.Subjects[out[i].id].LastPoll, e.state.Subjects[out[j].id].LastPoll
		if !a.Equal(b) {
			return a.Before(b)
		}
		return out[i].id < out[j].id
	})
	return out
}

// matchLocked queues, for every subscription to id, the events it selects and has not been told.
func (e *Engine) matchLocked(id SubjectID, events []Event) {
	for i := range e.state.Subs {
		s := &e.state.Subs[i]
		if s.Subject != id {
			continue
		}
		for _, ev := range events {
			if !selects(s.Types, ev.Type) || !TokenOK(ev.Type) {
				continue
			}
			key := ev.Type + "|" + ev.Version
			if _, done := s.Delivered[key]; done || hasPending(s.Pending, key) {
				continue
			}
			s.Pending = append(s.Pending, ev)
		}
	}
}

func selects(patterns []string, typ string) bool {
	for _, p := range patterns {
		if MatchType(p, typ) {
			return true
		}
	}
	return false
}

func hasPending(p []Event, key string) bool {
	for _, ev := range p {
		if ev.Type+"|"+ev.Version == key {
			return true
		}
	}
	return false
}

// deliverLocked hands each owner at most one pending event this tick (the next waits for the
// turn the first one starts). It reports whether anything changed.
func (e *Engine) deliverLocked(now time.Time) bool {
	changed := false
	told := map[string]bool{}
	var keep []Subscription
	for i := range e.state.Subs {
		s := e.state.Subs[i]
		ended := false
		if len(s.Pending) > 0 && !told[s.Owner] {
			ev := s.Pending[0]
			switch e.sink.Deliver(s, ev, now) {
			case Delivered:
				told[s.Owner] = true
				changed = true
				if s.Delivered == nil {
					s.Delivered = map[string]time.Time{}
				}
				s.Delivered[ev.Type+"|"+ev.Version] = now.UTC()
				s.Pending = s.Pending[1:]
				if s.Until == UntilFirst {
					ended = true
				}
			case Gone:
				ended, changed = true, true
			}
		}
		if !ended {
			keep = append(keep, s)
		}
	}
	if changed {
		e.state.Subs = keep
		e.dropUnwatchedLocked()
	}
	return changed
}

// expireLocked ends subscriptions past their deadline or whose owner is gone.
func (e *Engine) expireLocked(now time.Time) bool {
	var keep []Subscription
	exists := map[string]bool{}
	for _, s := range e.state.Subs {
		ok, seen := exists[s.Owner]
		if !seen {
			ok = e.sink.OwnerExists(s.Owner)
			exists[s.Owner] = ok
		}
		if ok && now.Before(s.Expires) {
			keep = append(keep, s)
		}
	}
	if len(keep) == len(e.state.Subs) {
		return false
	}
	e.state.Subs = keep
	e.dropUnwatchedLocked()
	return true
}

// dropUnwatchedLocked forgets subjects nobody subscribes to any more.
func (e *Engine) dropUnwatchedLocked() {
	live := map[SubjectID]bool{}
	for _, s := range e.state.Subs {
		live[s.Subject] = true
	}
	for id := range e.state.Subjects {
		if !live[id] {
			delete(e.state.Subjects, id)
		}
	}
}

// backoff after n consecutive failures: 1, 2, 4, 8, then 15 minutes.
func backoff(n int) time.Duration {
	d := time.Minute
	for i := 1; i < n && d < 15*time.Minute; i++ {
		d *= 2
	}
	return min(d, 15*time.Minute)
}

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "w" + hex.EncodeToString(b[:])
}
