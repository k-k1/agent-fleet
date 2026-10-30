package routehealth

import (
	"sync"
	"time"
)

const (
	// BackoffMin and BackoffMax bound the cooldown for a failure that names no reset time:
	// the first strike parks for BackoffMin, each further strike on the same key doubles it.
	BackoffMin = time.Minute
	BackoffMax = 4 * time.Hour
	// NearRecovery is how close to its end a cooldown may be and still not cause a switch:
	// waiting that long on the configured backend beats moving the turn to a different one.
	NearRecovery = 2 * time.Minute
	// maxResetAhead discards a provider-named reset further out than any real window (the
	// weekly one is the longest). A time beyond it is a misread, and parking on it would take
	// the backend out for good.
	maxResetAhead = 8 * 24 * time.Hour
)

// Key names one backend route. Wide is the kind as a whole (a ScopeKind park); otherwise Model
// names one model, and Model "" is the CLI's own default model. The two are separate keys: a
// 5xx on the default model must not overwrite an account window, nor park the named models.
type Key struct {
	Kind  string
	Model string
	Wide  bool
}

type entry struct {
	until   time.Time
	at      time.Time // when the park was last set; a success that started before it cannot lift it
	class   Class
	named   bool // until is a reset the provider named, not a backoff guess
	strikes int  // consecutive cooling failures; drives the backoff
}

// Table holds the cooldowns. It lives in memory only: every park ends within BackoffMax or
// at a provider-named reset, and after an Agent restart the next failure re-learns it at the
// cost of one failed call.
type Table struct {
	mu sync.Mutex
	m  map[Key]*entry
}

// NewTable returns an empty table.
func NewTable() *Table { return &Table{m: map[Key]*entry{}} }

// Default is the table the Agent's resolvers share.
var Default = NewTable()

// Fail records a failed call on (kind, model) and returns the verdict it was classified as. A
// verdict that does not cool (config, auth, other) leaves the table untouched.
func (t *Table) Fail(kind, model, msg string, now time.Time) Verdict {
	v := Classify(kind, msg, now)
	t.Park(kind, model, v, now)
	return v
}

// Park applies an already-classified verdict.
func (t *Table) Park(kind, model string, v Verdict, now time.Time) {
	if !v.Cools() {
		return
	}
	k := Key{Kind: kind, Model: model}
	if v.Scope == ScopeKind {
		k = Key{Kind: kind, Wide: true}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.m[k]
	// A park that ended long ago is not part of the current streak: a 429 yesterday must not
	// double today's first cooldown.
	if e == nil || now.Sub(e.until) > BackoffMax {
		e = &entry{}
		t.m[k] = e
	}
	e.strikes++
	e.at = now
	var until time.Time
	named := false
	switch {
	case v.Class == ClassSpend:
		until = now.Add(BackoffMax)
	case !v.ResetAt.IsZero() && v.ResetAt.After(now) && v.ResetAt.Sub(now) <= maxResetAhead:
		until, named = v.ResetAt, true
	default:
		until = now.Add(backoff(e.strikes))
	}
	// A guess never shortens a live park: a concurrent request's "quota exhausted" with no
	// reset, arriving just after one that named 12:00, says nothing new about when it lifts. A
	// named reset always replaces, as the newer word from the provider.
	if !named && e.until.After(until) {
		return
	}
	e.until, e.named, e.class = until, named, v.Class
}

// Succeed records a successful call on (kind, model) that started at started. It clears that
// model's park and the kind-wide one, since a call that went through proves the account window
// open again, but only parks set before the call started: a request accepted before the limit
// can still succeed after another one hit it, and that proves nothing about the limit. A zero
// started clears unconditionally.
func (t *Table) Succeed(kind, model string, started time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, k := range []Key{{Kind: kind, Model: model}, {Kind: kind, Wide: true}} {
		if e := t.m[k]; e != nil && (started.IsZero() || e.at.Before(started)) {
			delete(t.m, k)
		}
	}
}

// Until reports when (kind, model) becomes usable again and why it is parked. ok=false means
// it is usable now. A kind-wide park covers every model; the later of the two ends wins.
func (t *Table) Until(kind, model string, now time.Time) (until time.Time, class Class, ok bool) {
	for _, e := range t.parks(kind, model, now) {
		if e.until.After(until) {
			until, class, ok = e.until, e.class, true
		}
	}
	return until, class, ok
}

// Avoid reports whether a resolver should pass over (kind, model) for the next candidate. A
// park that ends within NearRecovery does not count, unless it is a usage window or spend cap
// whose end is only a backoff guess: those last hours, and the short first backoff step says
// nothing about when they really lift. Each live park is judged on its own: the one that ends
// last can be a near 5xx while a kind-wide window with a guessed end sits under it.
func (t *Table) Avoid(kind, model string, now time.Time) bool {
	for _, e := range t.parks(kind, model, now) {
		if !e.named && (e.class == ClassWindow || e.class == ClassSpend) {
			return true
		}
		if e.until.Sub(now) > NearRecovery {
			return true
		}
	}
	return false
}

// parks returns copies of the live kind-wide and model parks.
func (t *Table) parks(kind, model string, now time.Time) []entry {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []entry
	for _, k := range []Key{{Kind: kind, Wide: true}, {Kind: kind, Model: model}} {
		if e := t.m[k]; e != nil && e.until.After(now) {
			out = append(out, *e)
		}
	}
	return out
}

func backoff(strikes int) time.Duration {
	d := BackoffMin
	for i := 1; i < strikes && d < BackoffMax; i++ {
		d *= 2
	}
	return min(d, BackoffMax)
}
