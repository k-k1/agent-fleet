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

// Readiness is a sink's answer to "can this owner be told now?".
type Readiness int

const (
	// Wait: the owner cannot take a notice now (busy, starting, not ready); ask again later.
	Wait Readiness = iota
	// Ready: inject now.
	Ready
	// Gone: the owner no longer exists or was archived; its subscriptions end.
	Gone
)

// Sink delivers events to the session that owns a subscription. Its methods are called without
// the engine's lock held: starting a session or injecting a prompt can take seconds, and watch /
// list_watches must not wait for that.
type Sink interface {
	// OwnerExists is false for a session that was deleted or archived.
	OwnerExists(owner string) bool
	// Ready decides whether owner can take a notice now. It may start a stopped owner. It is
	// asked at most once per owner per tick.
	Ready(owner string, now time.Time) Readiness
	// Forget drops whatever the sink keeps about an owner whose subscriptions ended.
	Forget(owner string)
	// Inject tells the owner about ev.
	Inject(sub Subscription, ev Event) error
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
	NextPoll time.Time       `json:"next_poll"`
	LastPoll time.Time       `json:"last_poll,omitempty"`
	Fails    int             `json:"fails,omitempty"`
	LastErr  string          `json:"last_err,omitempty"`
	Settled  bool            `json:"settled,omitempty"`
}

// maxPollsPerTick bounds the reads of one tick. After a restart every subject is due at once;
// without a bound, 200 subscriptions would spend a GitHub budget in one sweep and the tick's
// deadline would cut the rest off.
const maxPollsPerTick = 20

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
	// A Done subscription was claimed for an injection the Agent did not live to confirm.
	var keep []Subscription
	for _, s := range e.state.Subs {
		if !s.Done {
			keep = append(keep, s)
		}
	}
	e.state.Subs = keep
	e.dropUnwatchedLocked()
	// A restart re-reads every live subject (ADR 0133 §5, the start-up poll); maxPollsPerTick
	// spreads the reads over the first ticks.
	for id, ss := range e.state.Subjects {
		ss.NextPoll = time.Time{}
		e.state.Subjects[id] = ss
	}
}

func (e *Engine) save() bool {
	if err := e.store.Save(e.state); err != nil {
		log.Printf("watch: could not save state: %v", err)
		return false
	}
	return true
}

// Request is one watch call.
type Request struct {
	Owner       string
	Requester   string // set when the owner is not the session that asked
	Source      string
	Ref         string
	DefaultRepo string
	Types       []string
	Until       string
	Lifetime    time.Duration // 0 = DefaultLifetime
}

// maker is the session that made the request.
func (r Request) maker() string {
	if r.Requester != "" {
		return r.Requester
	}
	return r.Owner
}

// maxRefLen bounds the reference a session may pass; it is kept on disk and shown back.
const maxRefLen = 300

// Subscribe resolves the reference, checks the limits and records the subscription. The
// subject is read on the next tick, so a condition that already holds is delivered then.
func (e *Engine) Subscribe(ctx context.Context, r Request) (Subscription, error) {
	src, ok := e.sources[r.Source]
	if !ok {
		return Subscription{}, &ErrRefused{fmt.Sprintf("unknown source %q", r.Source)}
	}
	if len(r.Ref) > maxRefLen {
		return Subscription{}, &ErrRefused{fmt.Sprintf("ref is longer than %d bytes", maxRefLen)}
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
	e.mu.Lock()
	err := e.requesterLimitLocked(r.maker())
	e.mu.Unlock()
	if err != nil {
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
		ID: newID(), Owner: r.Owner, Requester: r.Requester, Source: r.Source, Subject: res.Subject, Ref: r.Ref, URL: res.URL,
		Types: append([]string(nil), r.Types...), Until: until, Created: now.UTC(), Expires: now.Add(life).UTC(),
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	if err := e.limitsLocked(r.Owner); err != nil {
		return Subscription{}, err
	}
	if err := e.sourceLimitLocked(src); err != nil {
		return Subscription{}, err
	}
	if err := e.requesterLimitLocked(r.maker()); err != nil {
		return Subscription{}, err
	}
	e.state.Subs = append(e.state.Subs, sub)
	// Read the subject on the next tick, so even a subject another watch already reads
	// answers this one without waiting for its schedule.
	ss := e.state.Subjects[sub.Subject]
	ss.NextPoll = time.Time{}
	e.state.Subjects[sub.Subject] = ss
	if !e.save() {
		e.state.Subs = e.state.Subs[:len(e.state.Subs)-1]
		e.dropUnwatchedLocked()
		return Subscription{}, errors.New("the watch could not be saved")
	}
	return sub, nil
}

func (e *Engine) checkLimits(owner string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	return e.limitsLocked(owner)
}

// SourceLimiter is a source that bounds its own live subscriptions per workspace, because its
// provider's request budget is too small for the engine's general limits.
type SourceLimiter interface {
	MaxSubscriptions() int
}

func (e *Engine) sourceLimitLocked(src Source) error {
	l, ok := src.(SourceLimiter)
	if !ok {
		return nil
	}
	n := 0
	for _, s := range e.state.Subs {
		if !s.Done && s.Source == src.Kind() {
			n++
		}
	}
	if n >= l.MaxSubscriptions() {
		return &ErrRefused{fmt.Sprintf("this workspace already has %d %s watches, the limit for that provider's request budget", n, src.Kind())}
	}
	return nil
}

// requesterLimitLocked counts every watch a session made, its own and those it made for its
// parent, against that session's limit, so a child adds at most MaxPerOwner watches in all.
func (e *Engine) requesterLimitLocked(requester string) error {
	n := 0
	for _, s := range e.state.Subs {
		if !s.Done && (s.Owner == requester || s.Requester == requester) {
			n++
		}
	}
	if n >= MaxPerOwner {
		return &ErrRefused{fmt.Sprintf("this session already has %d watches of its own or for its parent, the limit", MaxPerOwner)}
	}
	return nil
}

func (e *Engine) limitsLocked(owner string) error {
	total, n := 0, 0
	for _, s := range e.state.Subs {
		if s.Done {
			continue
		}
		total++
		if s.Owner == owner {
			n++
		}
	}
	if total >= MaxTotal {
		return &ErrRefused{fmt.Sprintf("this workspace already has %d watches, the limit", MaxTotal)}
	}
	if n >= MaxPerOwner {
		return &ErrRefused{fmt.Sprintf("this session already has %d watches, the limit; end one with unwatch", MaxPerOwner)}
	}
	return nil
}

// Unsubscribe ends one of owner's subscriptions, pending events included. It reports whether
// there was one. When it was the owner's last, the sink forgets the owner, as on expiry, so a
// later watch is not held back by an earlier start.
func (e *Engine) Unsubscribe(owner, id string) bool {
	e.mu.Lock()
	found, last := false, true
	e.load()
	for i, s := range e.state.Subs {
		if s.ID == id && s.Owner == owner && !s.Done {
			e.state.Subs = append(e.state.Subs[:i], e.state.Subs[i+1:]...)
			e.dropUnwatchedLocked()
			e.save()
			found = true
			break
		}
	}
	for _, s := range e.state.Subs {
		if s.Owner == owner {
			last = false
		}
	}
	e.mu.Unlock()
	if found && last {
		e.sink.Forget(owner)
	}
	return found
}

// Watching reports whether owner has a live subscription to subject that selects typ. The PR
// feedback watch asks it so a session that watches its own PR is not told twice.
// A subscription past its deadline, or whose owner is gone, does not count: the engine drops it
// on its next tick without telling anyone.
func (e *Engine) Watching(owner string, subject SubjectID, typ string) bool {
	e.mu.Lock()
	now := e.now()
	e.load()
	found := false
	for _, s := range e.state.Subs {
		if s.Owner == owner && s.Subject == subject && !s.Done && now.Before(s.Expires) && selects(s.Types, typ) {
			found = true
			break
		}
	}
	e.mu.Unlock()
	return found && e.sink.OwnerExists(owner)
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
		if s.Owner != owner || s.Done {
			continue
		}
		ss := e.state.Subjects[s.Subject]
		l := Listed{Subscription: s, LastPoll: ss.LastPoll, NextPoll: ss.NextPoll, LastErr: ss.LastErr}
		l.Told, l.Pending = nil, nil
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
	ended := e.expireLocked(now)
	dirty := len(ended) > 0
	due := e.dueLocked(now)
	e.mu.Unlock()
	for _, o := range ended {
		e.sink.Forget(o)
	}

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
		if ctx.Err() != nil {
			break // the rest stay due for the next tick
		}
		src, ok := e.sources[d.source]
		if !ok {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, e.pollTimeout)
		evs, cur, hint, err := src.Poll(pctx, d.id, d.cursor, d.want)
		cancel()
		if err != nil && ctx.Err() != nil {
			break // cut off by the tick's deadline, not the provider's fault: no backoff
		}
		results = append(results, polled{d.id, evs, cur, hint, err})
	}

	e.mu.Lock()
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
		ss.Settled = p.hint.Settled
		after := p.hint.After
		if after <= 0 {
			after = time.Minute
		}
		ss.NextPoll = now.Add(after)
		e.state.Subjects[p.id] = ss
		e.matchLocked(p.id, p.events)
	}
	if dirty {
		e.save()
	}
	cands := e.candidatesLocked()
	e.mu.Unlock()

	e.deliver(cands, now)
}

// candidate is the first pending event of one owner's subscription.
type candidate struct {
	subID string
	owner string
	key   string // type|version of the event
}

// candidatesLocked picks, per owner, the first pending event of its oldest subscription that has
// one: one notice per owner per tick, the next waits for the turn the first one starts.
func (e *Engine) candidatesLocked() []candidate {
	seen := map[string]bool{}
	var out []candidate
	for _, s := range e.state.Subs {
		if s.Done || len(s.Pending) == 0 || seen[s.Owner] {
			continue
		}
		seen[s.Owner] = true
		ev := s.Pending[0]
		out = append(out, candidate{s.ID, s.Owner, ev.Type + "|" + ev.Version})
	}
	return out
}

// claimed is an event recorded as told on disk and about to be injected.
type claimed struct {
	sub    Subscription // as it was before the claim
	ev     Event
	wasEnd bool
}

// deliver asks each candidate's owner whether it can take a notice (without the lock), claims
// the ready ones on disk, injects them, and releases a claim whose injection failed. A crash
// between the claim and the injection loses that one notice rather than repeating it, as the
// PR feedback watch does.
func (e *Engine) deliver(cands []candidate, now time.Time) {
	if len(cands) == 0 {
		return
	}
	ready := map[string]Readiness{}
	for _, c := range cands {
		ready[c.owner] = e.sink.Ready(c.owner, now)
	}

	e.mu.Lock()
	var claims []claimed
	var gone []string
	changed := false
	for _, c := range cands {
		switch ready[c.owner] {
		case Gone:
			if e.dropOwnerLocked(c.owner) {
				changed = true
			}
			gone = append(gone, c.owner)
		case Ready:
			i := e.subIndexLocked(c.subID)
			if i < 0 {
				continue // unwatched since the candidates were picked
			}
			s := &e.state.Subs[i]
			if len(s.Pending) == 0 || s.Pending[0].Type+"|"+s.Pending[0].Version != c.key {
				continue // replaced by a newer state; the next tick offers that one
			}
			before := cloneSub(*s)
			ev := s.Pending[0]
			s.Pending = s.Pending[1:]
			if s.Told == nil {
				s.Told = map[string]string{}
			}
			s.Told[ev.Type] = ev.Version
			end := s.Until == UntilFirst
			if end {
				s.Done = true
			}
			claims = append(claims, claimed{before, ev, end})
			changed = true
		}
	}
	if changed && !e.save() {
		// Not on disk: injecting now could repeat after a restart. Undo and try next tick.
		for _, c := range claims {
			e.restoreLocked(c.sub)
		}
		claims = nil
	}
	e.mu.Unlock()
	for _, o := range gone {
		e.sink.Forget(o)
	}

	for _, c := range claims {
		err := e.sink.Inject(c.sub, c.ev)
		e.mu.Lock()
		if err != nil {
			e.restoreLocked(c.sub)
		} else if c.wasEnd {
			if i := e.subIndexLocked(c.sub.ID); i >= 0 {
				e.state.Subs = append(e.state.Subs[:i], e.state.Subs[i+1:]...)
				e.dropUnwatchedLocked()
			}
		}
		e.save()
		e.mu.Unlock()
		if err != nil {
			log.Printf("watch: could not deliver %s to %s: %v", c.ev.Type, c.sub.Owner, err)
		}
	}
}

func cloneSub(s Subscription) Subscription {
	c := s
	c.Pending = append([]Event(nil), s.Pending...)
	if s.Told != nil {
		c.Told = make(map[string]string, len(s.Told))
		for k, v := range s.Told {
			c.Told[k] = v
		}
	}
	return c
}

// restoreLocked puts a subscription back as it was before a claim, unless it was ended since.
func (e *Engine) restoreLocked(before Subscription) {
	if i := e.subIndexLocked(before.ID); i >= 0 {
		e.state.Subs[i] = before
	}
}

func (e *Engine) subIndexLocked(id string) int {
	for i, s := range e.state.Subs {
		if s.ID == id {
			return i
		}
	}
	return -1
}

func (e *Engine) dropOwnerLocked(owner string) bool {
	var keep []Subscription
	for _, s := range e.state.Subs {
		if s.Owner != owner {
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

type dueSubject struct {
	id     SubjectID
	source string
	cursor json.RawMessage
	want   []string
}

// dueLocked lists the subjects whose next read has come, least recently read first so one busy
// subject cannot keep the rest waiting, at most maxPollsPerTick of them.
func (e *Engine) dueLocked(now time.Time) []dueSubject {
	srcOf := map[SubjectID]string{}
	want := map[SubjectID][]string{}
	for _, s := range e.state.Subs {
		if !s.Done {
			srcOf[s.Subject] = s.Source
			want[s.Subject] = append(want[s.Subject], s.Types...)
		}
	}
	var out []dueSubject
	for id, ss := range e.state.Subjects {
		src, ok := srcOf[id]
		if !ok || now.Before(ss.NextPoll) {
			continue
		}
		out = append(out, dueSubject{id, src, ss.Cursor, want[id]})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := e.state.Subjects[out[i].id].LastPoll, e.state.Subjects[out[j].id].LastPoll
		if !a.Equal(b) {
			return a.Before(b)
		}
		return out[i].id < out[j].id
	})
	if len(out) > maxPollsPerTick {
		out = out[:maxPollsPerTick]
	}
	return out
}

// matchLocked queues, for every subscription to id, the events it selects and was not told in
// that version. A newer state of a type replaces an older one still pending, so a session that
// could not be told for a while hears the latest state per type, not the whole history.
func (e *Engine) matchLocked(id SubjectID, events []Event) {
	for i := range e.state.Subs {
		s := &e.state.Subs[i]
		if s.Subject != id || s.Done {
			continue
		}
		for _, ev := range events {
			if !selects(s.Types, ev.Type) || !TokenOK(ev.Type) {
				continue
			}
			if v, ok := s.Told[ev.Type]; ok && v == ev.Version {
				continue
			}
			replaced := false
			for j := range s.Pending {
				if s.Pending[j].Type == ev.Type {
					s.Pending[j], replaced = ev, true
					break
				}
			}
			if !replaced {
				s.Pending = append(s.Pending, ev)
			}
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

// expireLocked ends subscriptions past their deadline or whose owner is gone, and returns the
// owners whose subscriptions all ended.
func (e *Engine) expireLocked(now time.Time) []string {
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
		return nil
	}
	e.state.Subs = keep
	e.dropUnwatchedLocked()
	left := map[string]bool{}
	for _, s := range keep {
		left[s.Owner] = true
	}
	var ended []string
	for o := range exists {
		if !left[o] {
			ended = append(ended, o)
		}
	}
	return ended
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
