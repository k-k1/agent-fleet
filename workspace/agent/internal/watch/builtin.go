package watch

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// Built-in subscriptions (ADR 0133 §10 step 3) are the ones the Agent keeps for itself: the PR
// feedback watch and the PR notices. Nobody asks for them with watch; the feature that owns a
// purpose states the whole set it wants with SyncBuiltins on each of its sweeps, and feeds what
// it already read from the session list's PR caches with ObserveBuiltin. So a built-in costs no
// request of its own, is never polled by a source, and counts against no watch limit (the
// Bitbucket limit of 10 would otherwise be spent by the sessions' own PRs alone).
//
// They differ from a session's watch in four ways:
//   - the owner can change from sweep to sweep (the PR's strongest holder), and the delivery
//     record (Told, Sent) stays with the subscription, so a PR is told once whoever holds it;
//   - a purpose's subscription that leaves the set is retired, not forgotten: its record is kept
//     for builtinRetain, so a switch turned off and on again, or a link that drops out for a
//     sweep, repeats nothing;
//   - Pending is the conditions that hold at the latest observation and were not told: a CI
//     failure superseded by a push is no longer owed;
//   - a session's own watch on the same subject and type tells it first, and the built-in then
//     stays quiet (coveredLocked, noteUserDeliveryLocked), so a session is not told twice.

// Delivery routes of a built-in subscription.
const (
	// NotifySelf delivers into the owner session ("" means the same).
	NotifySelf = "self"
	// NotifyMember delivers to the member's notification outbox.
	NotifyMember = "member"
)

// builtinRetain is how long a retired built-in keeps its record; the PR feedback records it
// replaces were dropped after 30 days unseen too.
const builtinRetain = 30 * 24 * time.Hour

// builtinRefresh is how often a live built-in's deadline is pushed out by its sweeps. A purpose
// whose feature no longer sweeps (renamed, removed) leaves live built-ins that nothing retires;
// they lapse builtinRetain after the last sweep that named them.
const builtinRefresh = 24 * time.Hour

// tombWindow is how long a session's own watch delivery stands in for the built-in's notice of
// the same type. The built-in reads a cache that can trail the watch's own reads by a few
// minutes; the window covers that lag and no more, so a later condition is told again.
const tombWindow = 30 * time.Minute

// BuiltinSpec is one built-in subscription as its feature wants it now.
type BuiltinSpec struct {
	Subject SubjectID // the subject as a session's watch would name it: "github:owner/repo#12"
	Owner   string    // the session it is about, and the one told unless Notify is member
	Ref     string    // the reference shown in notices: "owner/repo#12"
	URL     string
	Types   []string
	// Notify is NotifySelf or NotifyMember; Fallback NotifyMember sends to the member when the
	// owner is archived or of a kind that takes no prompt.
	Notify   string
	Fallback string
	// Max caps the deliveries; 0 is no cap.
	Max int
	// Combine delivers every pending event in one notice, counted as one delivery.
	Combine bool
	// Baseline types take their first observed state as told: a PR first seen merged is
	// history, not news.
	Baseline []string
	// Tag is the feature's own key for the subject (a PR's node ID), for the sink.
	Tag string
	// Told, Sent and Cursor seed a subscription that has no record yet, so a feature moving
	// onto the engine keeps what it already told. Ignored once the engine has a record.
	Told   map[string]string
	Sent   int
	Cursor json.RawMessage
}

// BuiltinID is the ID of purpose's subscription to subject.
func BuiltinID(purpose string, subject SubjectID) string {
	return "b:" + purpose + ":" + string(subject)
}

// SyncBuiltins makes purpose's built-in subscriptions the given set: missing ones are made
// (from their retired record when there is one), present ones take the spec's owner and
// routes, and the rest are retired.
func (e *Engine) SyncBuiltins(purpose string, specs []BuiltinSpec) {
	if purpose == "" {
		return // "" is a session's own watches, which no sweep may retire
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	now := e.now()
	want := map[string]BuiltinSpec{}
	var order []string
	for _, sp := range specs {
		if !TokenOK(string(sp.Subject)) {
			continue
		}
		id := BuiltinID(purpose, sp.Subject)
		if _, dup := want[id]; !dup {
			want[id] = sp
			order = append(order, id)
		}
	}
	changed := false
	var keep []Subscription
	have := map[string]bool{}
	for _, s := range e.state.Subs {
		if s.Builtin != purpose {
			keep = append(keep, s)
			continue
		}
		sp, ok := want[s.ID]
		if !ok {
			e.retireLocked(s, now)
			changed = true
			continue
		}
		have[s.ID] = true
		if applySpec(&s, sp) {
			changed = true
		}
		if refreshExpiry(&s, now) {
			changed = true
		}
		keep = append(keep, s)
	}
	for _, id := range order {
		if have[id] {
			continue
		}
		sp := want[id]
		s, ok := e.state.Retired[id]
		if ok {
			delete(e.state.Retired, id)
		} else {
			s = Subscription{ID: id, Builtin: purpose, Subject: sp.Subject, Source: sourceOf(sp.Subject), Created: now.UTC(),
				Until: UntilDeadline, Told: cloneTold(sp.Told), Sent: sp.Sent, Baseline: append([]string(nil), sp.Baseline...)}
			if _, has := e.state.Cursors[sp.Subject]; !has && len(sp.Cursor) > 0 {
				e.state.Cursors[sp.Subject] = append(json.RawMessage(nil), sp.Cursor...)
			}
		}
		applySpec(&s, sp)
		s.Expires = now.Add(builtinRetain).UTC()
		keep = append(keep, s)
		changed = true
	}
	e.state.Subs = keep
	if e.pruneBuiltinLocked(now) {
		changed = true
	}
	if changed {
		e.save()
	}
}

// applySpec copies what the feature decides afresh on each sweep, and reports a change.
func applySpec(s *Subscription, sp BuiltinSpec) bool {
	before := cloneSub(*s)
	s.Owner, s.Ref, s.URL, s.Tag = sp.Owner, sp.Ref, SafeURL(sp.URL), sp.Tag
	s.Types = append([]string(nil), sp.Types...)
	s.Notify, s.Fallback, s.Max, s.Combine = route(sp.Notify), route(sp.Fallback), sp.Max, sp.Combine
	s.Baseline = append([]string(nil), sp.Baseline...)
	// Narrowed types: what is pending under a type no longer asked for is not owed.
	s.Pending = slices.DeleteFunc(s.Pending, func(ev Event) bool { return !selects(s.Types, ev.Type) })
	return s.Owner != before.Owner || s.Ref != before.Ref || s.URL != before.URL || s.Tag != before.Tag ||
		!slices.Equal(s.Types, before.Types) || s.Notify != before.Notify || s.Fallback != before.Fallback ||
		s.Max != before.Max || s.Combine != before.Combine || !slices.Equal(s.Baseline, before.Baseline) ||
		len(s.Pending) != len(before.Pending)
}

// route keeps the one other route there is; anything else is the owner session.
func route(r string) string {
	if r == NotifyMember {
		return NotifyMember
	}
	return ""
}

func refreshExpiry(s *Subscription, now time.Time) bool {
	if s.Expires.Sub(now) > builtinRetain-builtinRefresh {
		return false
	}
	s.Expires = now.Add(builtinRetain).UTC()
	return true
}

func sourceOf(id SubjectID) string {
	for i, c := range id {
		if c == ':' {
			return string(id[:i])
		}
	}
	return ""
}

func cloneTold(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// retireLocked keeps a built-in's record without its pending events: those are recomputed from
// the next observation if it comes back.
func (e *Engine) retireLocked(s Subscription, now time.Time) {
	s.Pending = nil
	s.Expires = now.Add(builtinRetain).UTC()
	if e.state.Retired == nil {
		e.state.Retired = map[string]Subscription{}
	}
	e.state.Retired[s.ID] = s
}

// pruneBuiltinLocked drops retired records past their time, the cursors no built-in refers to
// any more, and stale tombs.
func (e *Engine) pruneBuiltinLocked(now time.Time) bool {
	changed := false
	for id, s := range e.state.Retired {
		if !now.Before(s.Expires) {
			delete(e.state.Retired, id)
			changed = true
		}
	}
	keep := e.state.Subs[:0:0]
	for _, s := range e.state.Subs {
		if s.Builtin != "" && !now.Before(s.Expires) {
			changed = true // no sweep named it for builtinRetain
			continue
		}
		keep = append(keep, s)
	}
	e.state.Subs = keep
	live := map[SubjectID]bool{}
	for _, s := range e.state.Subs {
		if s.Builtin != "" {
			live[s.Subject] = true
		}
	}
	for _, s := range e.state.Retired {
		live[s.Subject] = true
	}
	for id := range e.state.Cursors {
		if !live[id] {
			delete(e.state.Cursors, id)
			changed = true
		}
	}
	for k, t := range e.state.Tombs {
		if now.Sub(t.At) >= tombWindow {
			delete(e.state.Tombs, k)
			changed = true
		}
	}
	return changed
}

// ObserveBuiltin feeds what a feature read about subject to its live built-in subscriptions.
// observe turns the subject's cursor into the conditions that hold now and the next cursor; it
// is not called when no live built-in watches subject, so a retired subject's cursor stays as
// it was, as the features it replaces kept their records while switched off.
//
// observe answers ok false when it has nothing to go on (the cache has not answered): nothing
// changes then, so a baseline is taken only from a real first sight.
func (e *Engine) ObserveBuiltin(subject SubjectID, observe func(cursor json.RawMessage) ([]Event, json.RawMessage, bool)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	live := false
	for _, s := range e.state.Subs {
		if s.Builtin != "" && s.Subject == subject {
			live = true
			break
		}
	}
	if !live {
		return
	}
	events, next, ok := observe(e.state.Cursors[subject])
	if !ok {
		return
	}
	changed := false
	if !bytes.Equal(next, e.state.Cursors[subject]) {
		if len(next) == 0 {
			delete(e.state.Cursors, subject)
		} else {
			e.state.Cursors[subject] = append(json.RawMessage(nil), next...)
		}
		changed = true
	}
	now := e.now()
	for i := range e.state.Subs {
		s := &e.state.Subs[i]
		if s.Builtin == "" || s.Subject != subject {
			continue
		}
		if e.matchBuiltinLocked(s, events, now) {
			changed = true
		}
	}
	if changed {
		e.save()
	}
}

// matchBuiltinLocked sets s's pending events to the conditions that hold now and were not told,
// and reports a change.
func (e *Engine) matchBuiltinLocked(s *Subscription, events []Event, now time.Time) bool {
	before := cloneSub(*s)
	var pending []Event
	for _, ev := range events {
		if ev.Subject != s.Subject || !selects(s.Types, ev.Type) || !TokenOK(ev.Type) {
			continue
		}
		if !s.Observed && slices.Contains(s.Baseline, ev.Type) {
			s.setTold(ev)
			continue
		}
		if v, ok := s.Told[ev.Type]; ok && v == ev.Version {
			continue
		}
		if s.Max > 0 && s.Sent >= s.Max {
			continue
		}
		if s.Notify == "" && e.takeTombLocked(s.Owner, ev, now) {
			// The session's own watch told it this a moment ago.
			s.setTold(ev)
			continue
		}
		pending = append(pending, ev)
	}
	s.Pending = pending
	s.Observed = true
	return !sameSub(before, *s)
}

func (s *Subscription) setTold(ev Event) {
	if s.Told == nil {
		s.Told = map[string]string{}
	}
	s.Told[ev.Type] = ev.Version
}

// sameSub compares what matchBuiltinLocked may change, ignoring each event's At, which moves on
// every observation.
func sameSub(a, b Subscription) bool {
	if a.Observed != b.Observed || len(a.Pending) != len(b.Pending) || len(a.Told) != len(b.Told) {
		return false
	}
	for i := range a.Pending {
		// Summary and Count can move within one version (fewer threads): saved, so a restart
		// does not word the notice with the older count.
		pa, pb := a.Pending[i], b.Pending[i]
		if pa.Type != pb.Type || pa.Version != pb.Version || pa.Summary != pb.Summary || pa.Count != pb.Count {
			return false
		}
	}
	for k, v := range a.Told {
		if b.Told[k] != v {
			return false
		}
	}
	return true
}

// Tomb is a session's own watch delivery that stands in for a built-in's notice.
type Tomb struct {
	At      time.Time `json:"at"`
	Version string    `json:"version,omitempty"`
	Count   int       `json:"count,omitempty"`
}

// tombKey joins with "|", which neither a session name, a subject (a header token) nor a type
// can contain.
func tombKey(owner string, subject SubjectID, typ string) string {
	return owner + "|" + string(subject) + "|" + typ
}

// takeTombLocked reports whether a tomb stands in for ev, and consumes it when it does. A tomb
// for a head (failed checks, ready) stands only for that head: the built-in's version is the
// head, a watch's starts with it. A threads tomb stands for at most the count the watch told.
// A tomb that does not match stays: the built-in's cache may still be showing an older state,
// and the matching one comes on a later observation.
func (e *Engine) takeTombLocked(owner string, ev Event, now time.Time) bool {
	k := tombKey(owner, ev.Subject, ev.Type)
	t, ok := e.state.Tombs[k]
	if !ok {
		return false
	}
	if now.Sub(t.At) >= tombWindow {
		delete(e.state.Tombs, k)
		return false
	}
	if !t.standsFor(ev) {
		return false
	}
	delete(e.state.Tombs, k)
	return true
}

// standsFor reports whether what a watch told (t) is what a built-in's ev would tell. Change
// requests, conflicts and closes have versions the two cannot compare, so any of the type
// counts.
func (t Tomb) standsFor(ev Event) bool {
	switch ev.Type {
	case typeChecksFailed, typeReady:
		return t.Version == ev.Version || strings.HasPrefix(t.Version, ev.Version+":")
	case typeThreads:
		return ev.Count <= t.Count
	}
	return true
}

const (
	typeChecksFailed    = "pr.checks.failed"
	typeChecksCompleted = "pr.checks.completed"
	typeReady           = "pr.ready"
)

// coverTypes are the types a watch may select to tell what a built-in's type tells: a failure
// is reported under pr.checks.completed too, with its result.
func coverTypes(typ string) []string {
	if typ == typeChecksFailed {
		return []string{typeChecksFailed, typeChecksCompleted}
	}
	return []string{typ}
}

// toldAs is the built-in type a watch's delivered event tells, "" for none: a completed run
// stands for a failure only when it failed.
func toldAs(ev Event) string {
	if ev.Type == typeChecksCompleted {
		if ev.Failed {
			return typeChecksFailed
		}
		return ""
	}
	return ev.Type
}

// coveredLocked reports whether owner has a live watch of its own that tells typ on subject:
// that watch tells it, so the built-in does not.
func (e *Engine) coveredLocked(owner string, subject SubjectID, typ string, now time.Time) bool {
	for _, s := range e.state.Subs {
		if s.Builtin != "" || s.Done || s.Owner != owner || s.Subject != subject || !now.Before(s.Expires) {
			continue
		}
		for _, t := range coverTypes(typ) {
			if selects(s.Types, t) {
				return true
			}
		}
	}
	return false
}

// noteUserDeliveryLocked runs after a session's own watch told its owner evs. A built-in to the
// same owner that has the same type pending takes it as told; one that has not seen it yet
// (its cache trails the watch) gets a tomb that stands in for its next notice of that type
// within tombWindow. A built-in that told the owner within that window keeps no tomb: the
// watch was the late one, and the next condition is news.
func (e *Engine) noteUserDeliveryLocked(owner string, evs []Event, now time.Time) {
	for _, ev := range evs {
		typ := toldAs(ev)
		if typ == "" {
			continue
		}
		matched := false
		for i := range e.state.Subs {
			s := &e.state.Subs[i]
			if s.Builtin == "" || s.Notify != "" || s.Owner != owner || s.Subject != ev.Subject || !selects(s.Types, typ) {
				continue
			}
			matched = true
			told := Tomb{Version: ev.Version, Count: ev.Count}
			if j := slices.IndexFunc(s.Pending, func(p Event) bool { return p.Type == typ }); j >= 0 {
				if told.standsFor(s.Pending[j]) {
					s.setTold(s.Pending[j])
					s.Pending = slices.Delete(s.Pending, j, j+1)
					continue
				}
				// The built-in already holds a newer state (another head, more threads) than the
				// watch told: that stays owed. The tomb still covers the told one should a later
				// observation show it.
				e.addTombLocked(owner, typ, ev, now)
				continue
			}
			if !s.SentAt.IsZero() && now.Sub(s.SentAt) < tombWindow {
				continue
			}
			e.addTombLocked(owner, typ, ev, now)
		}
		if !matched {
			// No built-in yet: one may be made on the next sweep (the session just reached its
			// PR's branch) and must not repeat what the watch said.
			e.addTombLocked(owner, typ, ev, now)
		}
	}
}

func (e *Engine) addTombLocked(owner, typ string, ev Event, now time.Time) {
	if e.state.Tombs == nil {
		e.state.Tombs = map[string]Tomb{}
	}
	e.state.Tombs[tombKey(owner, ev.Subject, typ)] = Tomb{At: now.UTC(), Version: ev.Version, Count: ev.Count}
}
