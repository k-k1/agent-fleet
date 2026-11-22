// Package memoafter is the dependencies between messages of a queue (ADR 0137, addendum 2026-11-19,
// part A): a message may name the messages of its queue it waits for (`after`), and the dispatcher
// claims it only once every one of them is satisfied.
//
// Satisfied is both steps of the addendum (store.AfterDeliverySatisfies, which the dispatcher's
// SQL statistics mirror): the prerequisite was delivered by a delivery (not sent by hand) that
// closed as clean and was not retired as overdue, and the PR snapshot of its session, read from
// the running Agent after the close, is final with every PR merged, or with no PR when the first
// turn is recorded and was not bad. A first turn that is never recorded (a kind whose ledger
// records none, a void ledger, a session gone before the collection window ended) cannot show a
// success, so only a merged PR satisfies then. A dependent is never fired over a prerequisite
// that went wrong.
package memoafter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Max bounds the prerequisites of one message.
const Max = 10

// ClosedTurnWindow is how long after its close a delivery is still read for the first turn of its
// session (the dispatcher's collectClosedTurns). After it nothing records one.
const ClosedTurnWindow = 24 * time.Hour

// The states of one prerequisite as a dependent sees it.
const (
	Satisfied = "satisfied"
	Waiting   = "waiting"
	Failed    = "failed"
)

// Why a prerequisite keeps its dependent waiting, or can never satisfy it. The codes are on the
// wire; the Console words them.
const (
	WhyHeld      = "held"       // waiting: not released yet, or back for a press
	WhyPending   = "pending"    // waiting: released, not fired yet
	WhyRunning   = "running"    // waiting: claimed, or its delivery is not closed
	WhyFirstTurn = "first_turn" // waiting: closed clean, its first turn not collected yet

	WhyDeleted      = "deleted"
	WhyMoved        = "moved"       // moved out of the queue
	WhyUnconfirmed  = "unconfirmed" // its create's outcome is unknown
	WhySuperseded   = "superseded"  // replaced by a tidy run
	WhyHandSent     = "hand_sent"   // sent by a person, not by a delivery
	WhyAborted      = "aborted"     // its delivery closed aborted
	WhyUnknown      = "unknown"     // its delivery closed unknown, or is gone
	WhyNotClean     = "not_clean"   // its delivery closed some other way than clean
	WhyBadFirstTurn = "bad_first_turn"
	WhyOverdue      = "overdue" // retired as overdue
	// WhyNoFirstTurn is a hold made before the PR snapshot existed: no first turn was recorded
	// before the window ended. Nothing writes it any more, and a held row may still carry it.
	WhyNoFirstTurn = "no_first_turn"

	WhyPR          = "pr"           // waiting: its session's PRs are not read yet, or not all merged
	WhyPRClosed    = "pr_closed"    // a PR of its session was closed unmerged
	WhyNoPR        = "no_pr"        // no first turn was recorded, and its session opened no PR
	WhySessionGone = "session_gone" // the Agent no longer had its session before its PRs were read
)

// PR is one PR of a delivery's snapshot, as memo_delivery.pr_snapshot keeps it and as the list
// shows it. State is "open", "draft", "merged", "closed", or "" for one the provider could not
// show.
type PR struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	State    string `json:"state"`
	URL      string `json:"url,omitempty"`
}

// ParseSnapshot reads memo_delivery.pr_snapshot, nil for none or one that does not parse.
func ParseSnapshot(raw string) []PR {
	var out []PR
	if raw == "" || json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}

// EncodeSnapshot is what memo_delivery.pr_snapshot holds for prs: "[]" for no PR.
func EncodeSnapshot(prs []PR) string {
	b, _ := json.Marshal(append([]PR{}, prs...))
	return string(b)
}

// SnapshotState sums a snapshot up as memo_delivery.pr_state: no PR, one closed unmerged, every
// one merged, or waiting (an open one, or one whose state is unknown).
func SnapshotState(prs []PR) string {
	if len(prs) == 0 {
		return store.PRStateNone
	}
	merged := 0
	for _, p := range prs {
		switch p.State {
		case "closed":
			return store.PRStateClosed
		case "merged":
			merged++
		}
	}
	if merged == len(prs) {
		return store.PRStateMerged
	}
	return store.PRStateWaiting
}

// Item is one prerequisite on the wire: its id, what the Console names it by (its reference key,
// else the start of its text), and how it stands.
type Item struct {
	ID     string `json:"id"`
	Label  string `json:"label,omitempty"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// PRs is the snapshot behind a reason of pr or pr_closed: the PRs it waits for, or the one
	// closed unmerged among them. Empty while no read answered.
	PRs []PR `json:"prs,omitempty"`
	// Delivery is the prerequisite's delivery, for the dispatcher's wakes. Never on the wire.
	Delivery string `json:"-"`
}

// HeldReason is memo.held_reason: the prerequisite that can no longer be satisfied, and why.
type HeldReason struct {
	After  string `json:"after"`
	Reason string `json:"reason"`
	At     string `json:"at,omitempty"`
}

// ParseHeldReason reads memo.held_reason, nil for none or one that does not parse.
func ParseHeldReason(raw string) *HeldReason {
	if raw == "" {
		return nil
	}
	var out HeldReason
	if json.Unmarshal([]byte(raw), &out) != nil || out.Reason == "" {
		return nil
	}
	return &out
}

// HashParts is appended to the content hash of a message that has prerequisites. A message
// without any hashes as it did before they existed, so no release already recorded changes.
func HashParts(m store.Memo) []string {
	if m.AfterIDs == "" {
		return nil
	}
	return []string{"after", m.AfterIDs}
}

// IDs is a message's prerequisites, nil for none (or a column that does not parse).
func IDs(m store.Memo) []string {
	var out []string
	if m.AfterIDs == "" || json.Unmarshal([]byte(m.AfterIDs), &out) != nil {
		return nil
	}
	return out
}

// Encode is what memo.after_ids holds for ids: "" for none.
func Encode(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

// OrderLess is the queue's order, (created_at, position, id), the order the claim sorts by: a
// prerequisite comes before its dependent in it, so no cycle can form.
func OrderLess(a, b store.Memo) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	if a.Position != b.Position {
		return a.Position < b.Position
	}
	return a.ID < b.ID
}

// Invalid is a refusal of a write's `after`, worded for the caller.
type Invalid struct{ Msg string }

func (e *Invalid) Error() string { return e.Msg }

func invalid(format string, a ...any) *Invalid { return &Invalid{fmt.Sprintf(format, a...)} }

// Resolve turns the `after` a write gave into the column it stores. An entry is a message id of
// the same queue, or a reference key (owner/repo#12, PROJ-12) that resolves to the newest message
// of the queue holding it that is not superseded. Every prerequisite must come before self in the
// queue's order and must not be superseded; one named twice is kept once. rows is self's bucket
// as it is now (self itself may be among them, or not yet inserted). An empty list clears.
func Resolve(self store.Memo, rows []store.Memo, in []string, inQueue bool) (string, *Invalid) {
	if len(in) == 0 {
		return "", nil
	}
	if !inQueue {
		return "", invalid("prerequisites are for a message in an auto-run category")
	}
	if len(in) > Max {
		return "", invalid("at most %d prerequisites", Max)
	}
	byID := make(map[string]store.Memo, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			return "", invalid("an empty prerequisite")
		}
		p, ok := byID[s]
		if !ok {
			var best *store.Memo
			for i := range rows {
				r := rows[i]
				if r.RefKey == "" || !strings.EqualFold(r.RefKey, s) || r.State == store.MemoStateSuperseded || r.ID == self.ID {
					continue
				}
				if best == nil || OrderLess(*best, r) {
					best = &rows[i]
				}
			}
			if best == nil {
				return "", invalid("no message of this category is %s", s)
			}
			p = *best
		}
		if p.ID == self.ID {
			return "", invalid("a message cannot wait for itself")
		}
		if p.State == store.MemoStateSuperseded {
			return "", invalid("message %s was replaced by a tidy run", p.ID)
		}
		if !OrderLess(p, self) {
			return "", invalid("message %s comes after this one in the queue: a prerequisite must come before", p.ID)
		}
		if seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		out = append(out, p.ID)
	}
	// The order rule keeps the graph acyclic as long as created_at and position never move a
	// prerequisite behind its dependent; a reorder can, so a cycle is refused outright.
	if reaches(byID, out, self.ID) {
		return "", invalid("this would make two messages wait for each other")
	}
	return Encode(out), nil
}

// reaches reports whether target is reachable from start along after lists.
func reaches(byID map[string]store.Memo, start []string, target string) bool {
	seen := map[string]bool{}
	stack := append([]string(nil), start...)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == target {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		stack = append(stack, IDs(byID[id])...)
	}
	return false
}

// Lookup finds what a dependent's prerequisites are now. Memo answers a row of the member by id
// (found false for a deleted one), Delivery a delivery by id.
type Lookup struct {
	Memo     func(id string) (store.Memo, bool, error)
	Delivery func(id string) (store.MemoDelivery, bool, error)
	Now      time.Time
}

// TxLookup reads through a MemoTx: rows first, then the store, each delivery read once.
func TxLookup(tx store.MemoTxOps, rows []store.Memo, now time.Time) Lookup {
	byID := make(map[string]store.Memo, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	ds := map[string]*store.MemoDelivery{}
	return Lookup{Now: now,
		Memo: func(id string) (store.Memo, bool, error) {
			if m, ok := byID[id]; ok {
				return m, true, nil
			}
			return tx.GetMemo(id)
		},
		Delivery: func(id string) (store.MemoDelivery, bool, error) {
			if d, ok := ds[id]; ok {
				if d == nil {
					return store.MemoDelivery{}, false, nil
				}
				return *d, true, nil
			}
			d, ok, err := tx.GetDelivery(id)
			if err != nil {
				return d, ok, err
			}
			if ok {
				ds[id] = &d
			} else {
				ds[id] = nil
			}
			return d, ok, nil
		}}
}

// ListLookup reads for the member's list: the rows it shows first, then the store for a
// prerequisite the list leaves out (one kept past retention because a message waits for it).
// Every read is checked against the member, so a forged id never reads another member's row.
func ListLookup(ctx context.Context, st store.MemoStore, membershipID string, rows []store.Memo) Lookup {
	byID := make(map[string]store.Memo, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	return Lookup{Now: time.Now(),
		Memo: func(id string) (store.Memo, bool, error) {
			if m, ok := byID[id]; ok {
				return m, true, nil
			}
			m, ok, err := st.GetMemo(ctx, id)
			if err != nil || !ok || m.MembershipID != membershipID {
				return store.Memo{}, false, err
			}
			byID[id] = m
			return m, true, nil
		},
		Delivery: func(id string) (store.MemoDelivery, bool, error) {
			d, ok, err := st.GetMemoDelivery(ctx, id)
			if err != nil || !ok || d.MembershipID != membershipID {
				return store.MemoDelivery{}, false, err
			}
			return d, true, nil
		}}
}

// prereq is how one prerequisite stands for dependent m.
func prereq(m store.Memo, id string, lk Lookup) (Item, error) {
	it := Item{ID: id}
	p, found, err := lk.Memo(id)
	if err != nil {
		return it, err
	}
	if !found {
		it.State, it.Reason = Failed, WhyDeleted
		return it, nil
	}
	it.Label = Label(p)
	if p.Repo != m.Repo || p.Category != m.Category {
		it.State, it.Reason = Failed, WhyMoved
		return it, nil
	}
	switch p.State {
	case store.MemoStateHeld:
		it.State, it.Reason = Waiting, WhyHeld
	case store.MemoStatePending:
		it.State, it.Reason = Waiting, WhyPending
	case store.MemoStateClaimed:
		it.State, it.Reason = Waiting, WhyRunning
	case store.MemoStateUnconfirmed:
		it.State, it.Reason = Failed, WhyUnconfirmed
	case store.MemoStateSuperseded:
		it.State, it.Reason = Failed, WhySuperseded
	case store.MemoStateDelivered:
		if p.DeliveryID == "" {
			it.State, it.Reason = Failed, WhyHandSent
			return it, nil
		}
		d, ok, err := lk.Delivery(p.DeliveryID)
		if err != nil {
			return it, err
		}
		it.Delivery = p.DeliveryID
		it.State, it.Reason = DeliveryState(d, ok, lk.Now)
		if it.Reason == WhyPR || it.Reason == WhyPRClosed {
			it.PRs = ParseSnapshot(d.PRSnapshot)
		}
	default:
		// A plain note: the queue's settings were removed, or it was moved out and back.
		it.State, it.Reason = Failed, WhyMoved
	}
	return it, nil
}

// DeliveryState is "satisfied" for the delivery that delivered a prerequisite, with the reason it
// waits or fails.
func DeliveryState(d store.MemoDelivery, found bool, now time.Time) (state, reason string) {
	switch {
	case !found:
		return Failed, WhyUnknown
	case d.State != store.DeliveryClosed:
		return Waiting, WhyRunning
	case d.Outcome == store.DeliveryDeferred && d.DeferResult == "":
		// A hand-back not decided yet: its message either returns (and the dependent waits for
		// it as pending) or the close becomes clean.
		return Waiting, WhyRunning
	case d.Outcome == store.DeliveryAborted:
		return Failed, WhyAborted
	case d.Outcome == store.DeliveryUnknown:
		return Failed, WhyUnknown
	case d.Outcome != store.DeliveryClean:
		return Failed, WhyNotClean
	case store.RetiredOverdue(d):
		return Failed, WhyOverdue
	case d.TurnEndedAt != "" && store.TurnBad(d):
		return Failed, WhyBadFirstTurn
	}
	switch d.PRState {
	case store.PRStateGone:
		return Failed, WhySessionGone
	case store.PRStateClosed:
		return Failed, WhyPRClosed
	case store.PRStateMerged:
	case store.PRStateNone:
		if d.TurnEndedAt == "" {
			// No PR cannot stand in for a first turn: it satisfies only once the turn is recorded,
			// and never when the window closes without one.
			closed, err := time.Parse("2006-01-02T15:04:05.000000Z", d.ClosedAt)
			if err == nil && now.Sub(closed) > ClosedTurnWindow {
				return Failed, WhyNoPR
			}
			return Waiting, WhyFirstTurn
		}
	default:
		// Not read yet, or a PR is open or of unknown state.
		return Waiting, WhyPR
	}
	if !store.AfterDeliverySatisfies(d) {
		// Unreachable while the cases above mirror it; a dependent is never fired on a doubt.
		return Waiting, WhyRunning
	}
	return Satisfied, ""
}

// Label names a prerequisite for a person: its reference key, else the start of its text.
func Label(p store.Memo) string {
	if p.RefKey != "" {
		return p.RefKey
	}
	s := strings.TrimSpace(p.Body)
	if s == "" {
		s = p.RefPath
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	return s
}

// Status is every prerequisite of m and how it stands, nil for a message with none.
func Status(m store.Memo, lk Lookup) ([]Item, error) {
	ids := IDs(m)
	if len(ids) == 0 {
		return nil, nil
	}
	out := make([]Item, 0, len(ids))
	for _, id := range ids {
		it, err := prereq(m, id, lk)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// Verdict sums a status up: every prerequisite satisfied (eligible), or the first that can never
// be (failed, nil when none).
func Verdict(items []Item) (eligible bool, failed *Item) {
	eligible = true
	for i := range items {
		switch items[i].State {
		case Failed:
			if failed == nil {
				failed = &items[i]
			}
			eligible = false
		case Waiting:
			eligible = false
		}
	}
	return eligible, failed
}

// EligibleIn answers, for the claim and the dispatcher's own count, which pending messages may be
// claimed now. A read that fails makes a message not eligible: it is never fired on a doubt.
func EligibleIn(lk Lookup) func(store.Memo) bool {
	return func(m store.Memo) bool {
		// A message handed back waits for its not_before as well (part B of the addendum), and one
		// that carries a start time for its start_after (addendum 2026-11-22).
		if Waits(m, lk.Now) {
			return false
		}
		if m.AfterIDs == "" {
			return true
		}
		items, err := Status(m, lk)
		if err != nil {
			return false
		}
		ok, _ := Verdict(items)
		return ok
	}
}

// Deferred reports whether m was handed back and its not_before has not passed at now.
func Deferred(m store.Memo, now time.Time) bool {
	return m.NotBefore != "" && m.NotBefore > store.DeliveryTS(now)
}

// Scheduled reports whether m carries a start time that has not passed at now.
func Scheduled(m store.Memo, now time.Time) bool {
	return m.StartAfter != "" && m.StartAfter > store.DeliveryTS(now)
}

// Waits reports whether m may not be claimed at now for a time it waits for, either one. The
// statistics' SQL (store.memoNotBeforeSQL) says the same.
func Waits(m store.Memo, now time.Time) bool {
	return Deferred(m, now) || Scheduled(m, now)
}

// WaitUntil is the time m becomes claimable as far as its times go: the later of its not_before
// and its start_after, "" for neither.
func WaitUntil(m store.Memo) string {
	if m.NotBefore > m.StartAfter {
		return m.NotBefore
	}
	return m.StartAfter
}

// Linked is the set of ids of a bucket that have or are a prerequisite, that wait for a
// not_before or a start time, or that are a continuation: a tidy run only keeps them (ADR 0137
// section 8, as the addenda amend it). A merge would drop or widen a start time.
func Linked(rows []store.Memo) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		if r.NotBefore != "" || r.StartAfter != "" || r.ChainID != "" {
			// A continuation is kept whole too: a merge or a move would make a message the
			// member's release of the chain does not cover (ADR 0137, addendum 2026-11-20).
			out[r.ID] = true
		}
		ids := IDs(r)
		if len(ids) == 0 {
			continue
		}
		out[r.ID] = true
		for _, id := range ids {
			out[id] = true
		}
	}
	return out
}

// Blocked is a pending message that waits for its prerequisites, with how each stands.
type Blocked struct {
	Memo  store.Memo
	Items []Item
}

// HoldUnsatisfiable puts to held every pending message of rows that waits for a prerequisite that
// can no longer be satisfied, with the reason. It answers the messages that have waited longer
// than since (when they became pending, else when they were created), and every message that still
// waits. Call it in the MemoTx that read rows.
func HoldUnsatisfiable(tx store.MemoTxOps, rows []store.Memo, now, since time.Time) (late, blocked []Blocked, err error) {
	lk := TxLookup(tx, rows, now)
	for _, m := range rows {
		// A row in a hand send keeps its state until the send settles, and one with a sent stamp or a
		// delivery is not waiting: the same rows pendingOf and the claim leave out.
		if m.State != store.MemoStatePending || m.AfterIDs == "" || m.SentAt != "" || m.SendToken != "" || m.DeliveryID != "" {
			continue
		}
		items, err := Status(m, lk)
		if err != nil {
			return nil, nil, err
		}
		ok, failed := Verdict(items)
		if failed != nil {
			reason, _ := json.Marshal(HeldReason{After: failed.ID, Reason: failed.Reason, At: store.DeliveryTS(now)})
			m.State, m.ReleaseHash, m.ReleasedAt, m.ReleasedBy, m.HeldReason = store.MemoStateHeld, "", "", "", string(reason)
			if err := tx.WriteMemo(m); err != nil {
				return nil, nil, err
			}
			continue
		}
		if ok {
			continue
		}
		blocked = append(blocked, Blocked{m, items})
		at := m.ReleasedAt
		if at == "" {
			at = m.CreatedAt
		}
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil && t.Before(since) {
			late = append(late, Blocked{m, items})
		}
	}
	return late, blocked, nil
}
