package watch

import (
	"sort"
	"time"
)

// pokeMinGap is the least time between two reads of one subject that pokes can cause. A webhook
// is only a hint that something changed: a replayed or forged delivery therefore costs at most
// one read per gap and per subject, whatever its rate.
const pokeMinGap = 10 * time.Second

// Subjects returns the distinct subjects of the live watches, sorted, with their source. The
// Agent registers exactly this set with the Control Plane (ADR 0133 §4), so the first watch of a
// subject adds it and the end of the last one removes it.
func (e *Engine) Subjects() []SubjectRef {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	seen := map[SubjectID]bool{}
	var out []SubjectRef
	for _, s := range e.state.Subs {
		if s.Done || s.Builtin != "" || seen[s.Subject] {
			continue
		}
		seen[s.Subject] = true
		out = append(out, SubjectRef{Source: s.Source, Subject: s.Subject})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Subject < out[j].Subject })
	return out
}

// SubjectRef names a watched subject and its source.
type SubjectRef struct {
	Source  string
	Subject SubjectID
}

// Poke brings the next read of subject forward, to now or, when it was read less than
// pokeMinGap ago, to the end of that gap. It reports whether a live watch holds the subject.
// A subject in backoff keeps its backoff: a failing provider is not helped by more requests.
// Poke carries no data; the read is made by the source with the member's own connection.
func (e *Engine) Poke(source string, subject SubjectID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	held := false
	for _, s := range e.state.Subs {
		if s.Source == source && s.Subject == subject && !s.Done && s.Builtin == "" {
			held = true
			break
		}
	}
	if !held {
		return false
	}
	ss, ok := e.state.Subjects[subject]
	if !ok || ss.Fails > 0 {
		return true
	}
	at := e.now()
	if next := ss.LastPoll.Add(pokeMinGap); next.After(at) {
		at = next
	}
	if at.Before(ss.NextPoll) {
		ss.NextPoll = at
		e.state.Subjects[subject] = ss
		e.save()
	}
	return true
}
