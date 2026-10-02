package agents

// TurnQueue is the input queue every Managed driver keeps between accept and the runtime, and
// the whole of ADR 0105's stop rules: the stop episode (decision 2), the discard-queue stop and
// the cancellable → committed → received hand-over of the entry the pump has taken (decision 3),
// the kept discards (decision 4) and removal by id (decision 5). The rules live here once so
// seven drivers cannot disagree about what a stop means.
//
// TurnQueue has no lock of its own. Every method must be called with the driver's handle
// mutex held — the one its accept takes — because "under the lock accept takes" is what draws
// the line between a discard and a commit (decision 3). A second lock here would reopen the gap
// between checking a mark and sending that the ADR forbids.
//
// The pump's side of the contract, per entry:
//
//	t := q.Take()               // under the lock; nil = nothing to start
//	... wait behind another client's turn: q.Hold(t, true) / q.Hold(t, false)
//	if !q.Commit(t) { ... }     // last act under the lock before calling the runtime;
//	                            // false = a stop or a removal cancelled it: do not send
//	unlock; send; lock
//	if q.Received(t) { stop }   // the runtime holds it (muse/codex: it named the turn)
//	... the turn runs ...
//	q.Settle(t)                 // the turn has settled, or the start failed
//
// and Interrupt's HeadAction tells the stopping side what to do with that entry. Exactly one
// side delivers a stop to a committed entry: Interrupt sets stop-pending and Received returns
// it, or Interrupt finds the entry already received and answers HeadStopNow.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

// LedgerPoint says when a driver records a ClientMessageID in the MsgLedger. codex, opencode and
// muse record at accept; copilot, cursor, kiro and lcpp at take (a crash that loses the queue
// must not leave the lost input marked as seen). The queue does the recording itself, because
// a driver that recorded first would make Accept read its own new input as a resend.
type LedgerPoint int

const (
	LedgerAtAccept LedgerPoint = iota
	LedgerAtTake
)

// EntryState is where a queued entry stands, as the messages payload shows it (decision 5).
type EntryState string

const (
	// EntryQueued can still be removed or discarded: in the queue, or taken by the pump but
	// not yet committed.
	EntryQueued EntryState = "queued"
	// EntryCommitted is being handed to the runtime. It is shown without actions.
	EntryCommitted EntryState = "committed"
	// EntrySent is held by the runtime behind a turn this driver did not start (muse's
	// host-side queue, disposition "queued"). Shown without actions.
	EntrySent EntryState = "sent"
)

// QueueItem is one queued or discarded input on the wire (messages payload queuedItems and
// discardedInputs[].items, /turn remove's answer).
type QueueItem struct {
	ID          string     `json:"id"`
	Text        string     `json:"text"`
	Attachments []string   `json:"attachments,omitempty"`
	Origin      Origin     `json:"origin"`
	State       EntryState `json:"state,omitempty"` // empty inside a Discard and a removal
}

// Discard reasons.
const (
	DiscardSecondStop = "second_stop"
	DiscardQueue      = "discard_queue"
	// DiscardFirstStop is the input a first stop stopped before it was sent: the turn being
	// stopped had not reached the runtime yet, so its text would otherwise be nowhere — not in
	// the transcript, not in the queue.
	DiscardFirstStop = "first_stop"
)

// Discard is what one discarding stop threw away, kept so the member can take it back
// (decision 4). Only input that was still cancellable is in it.
type Discard struct {
	ID     string      `json:"id"`
	At     string      `json:"at"` // RFC 3339
	Reason string      `json:"reason"`
	Items  []QueueItem `json:"items"`
}

// maxDiscards is how many discards a session keeps (decision 4): a sixth drops the oldest.
const maxDiscards = 5

// StopKind is what a stop turned out to be.
type StopKind string

const (
	StopFirst   StopKind = "first"   // the running turn only; the queue continues
	StopSecond  StopKind = "second"  // inside a stop episode: the rest is discarded
	StopDiscard StopKind = "discard" // the explicit discard-queue action (decision 3)
)

// InterruptOpts is the argument of ThreadHandle.Interrupt.
type InterruptOpts struct {
	// DiscardQueue is decision 3's emergency brake: discard everything unsent whatever the
	// episode state.
	DiscardQueue bool
}

// InterruptResult is what ThreadHandle.Interrupt reports, and what /turn interrupt answers.
type InterruptResult struct {
	Stop    StopKind `json:"stop"`
	Discard *Discard `json:"discard"` // nil when nothing cancellable was queued
}

// HeadAction is what a stop does to the entry the pump has taken.
type HeadAction int

const (
	HeadNone HeadAction = iota // nothing taken
	// HeadKept: the entry waits behind another turn (Hold) and a first stop lets it continue.
	HeadKept
	// HeadCancelled: the entry was cancellable. Commit returns false and it never starts.
	HeadCancelled
	// HeadStopPending: the entry is committed. The pump delivers the stop when Received.
	HeadStopPending
	// HeadStopNow: the runtime holds the entry. The caller delivers the stop itself, at once.
	HeadStopNow
)

// InterruptOutcome is Interrupt's answer to the driver.
type InterruptOutcome struct {
	Result InterruptResult
	Head   HeadAction
}

// ErrAlreadyStarted is Remove's answer for an entry that is committed or already with the
// runtime (/turn remove → 409 already_started).
var ErrAlreadyStarted = errors.New("already started")

// ErrNotQueued is Remove's answer for an id the queue does not hold (/turn remove → 404
// not_queued).
var ErrNotQueued = errors.New("not queued")

type entryPhase int

const (
	phaseTaken entryPhase = iota
	phaseCommitted
	phaseReceived
)

// Taken is the pump's token for the entry it took. It identifies the entry across the unlocked
// send, so a stale pump (an earlier generation, a cancelled entry) cannot commit or settle the
// entry that replaced it.
type Taken struct {
	In    TurnInput
	phase entryPhase
	held  bool
}

// ID is the entry's id (its ClientMessageID).
func (t *Taken) ID() string { return t.In.ClientMessageID }

// TurnQueue: see the file comment. The zero value is not usable; use NewTurnQueue.
type TurnQueue struct {
	name   string
	ledger *MsgLedger
	at     LedgerPoint
	now    func() time.Time

	queue       []TurnInput
	head        *Taken
	stopPending bool
	// pendingFirst: the pending stop came from a first stop only. It was aimed at the head as
	// the turn being started; if the head turns out to wait behind another turn (Hold), that
	// stop belongs to the running turn instead and the head continues.
	pendingFirst bool
	episode      bool
	discards     []Discard
	// recorded holds the ids Requeue put back after LedgerAtTake's Take had recorded them, so
	// the next Take does not read them as resends and drop them.
	recorded map[string]bool
}

// NewTurnQueue builds the queue of session name. ledger may be nil (no deduplication).
func NewTurnQueue(name string, ledger *MsgLedger, at LedgerPoint) *TurnQueue {
	return &TurnQueue{name: name, ledger: ledger, at: at, now: time.Now}
}

// Accept queues in and returns its id (ClientMessageID, minted when empty). dup reports a
// resend: an id the ledger has seen, or (LedgerAtTake) one still queued or taken. It is not
// queued, and the caller answers it as accepted.
//
// New member input ends the stop episode (decision 2); a resend does not, and neither does
// input of any other origin.
func (q *TurnQueue) Accept(in TurnInput) (id string, dup bool) {
	in.ClientMessageID = NormalizeMsgID(in.ClientMessageID)
	if in.restored {
		return q.acceptRestored(in)
	}
	switch q.at {
	case LedgerAtAccept:
		if q.ledger != nil && q.ledger.SeenOrRecord(q.name, in.ClientMessageID) {
			return in.ClientMessageID, true
		}
	case LedgerAtTake:
		// Not queued a second time: Take would drop it anyway, but until then it would show
		// twice in Items and in a discard, and the Console would restore the same text twice.
		if q.holds(in.ClientMessageID) || (q.ledger != nil && q.ledger.Seen(q.name, in.ClientMessageID)) {
			return in.ClientMessageID, true
		}
	}
	q.queue = append(q.queue, in)
	q.holdPeer(&q.queue[len(q.queue)-1])
	q.noteAccepted(in)
	return in.ClientMessageID, false
}

// acceptRestored queues a held peer message DeliverHeld sends again. The ledger is not asked:
// the id was recorded when the message was first accepted (LedgerAtAccept), or by a Take whose
// commit a crash cut short (LedgerAtTake), and either way the message never reached the
// runtime — its file is the proof. A second delivery is refused here, under the handle lock:
// one already queued or taken, and one whose file is gone because it was committed meanwhile.
func (q *TurnQueue) acceptRestored(in TurnInput) (string, bool) {
	id := in.ClientMessageID
	if q.holds(id) || !heldExists(q.name, id) {
		return id, true
	}
	q.queue = append(q.queue, in)
	if q.at == LedgerAtTake {
		if q.recorded == nil {
			q.recorded = map[string]bool{}
		}
		q.recorded[id] = true
	}
	return id, false
}

// holdPeer writes a newly queued peer message through to disk (heldpeers.go). Written at
// accept rather than at teardown, so a crash that runs no teardown does not lose it.
func (q *TurnQueue) holdPeer(in *TurnInput) {
	if !isHeldPeer(*in) || in.restored {
		return
	}
	if in.queuedAt.IsZero() {
		in.queuedAt = q.now()
	}
	putHeld(q.name, *in)
}

// releasePeer removes a peer message's file once it no longer waits: handed to the runtime,
// discarded by a stop, or removed by id.
func (q *TurnQueue) releasePeer(in TurnInput) {
	if isHeldPeer(in) {
		releaseHeld(q.name, in.ClientMessageID)
	}
}

// AcceptRecorded queues input AcceptOutside has already recorded (codex: a native turn/steer
// that failed and falls back to the queue). The episode was already ended for it.
func (q *TurnQueue) AcceptRecorded(in TurnInput) string {
	in.ClientMessageID = NormalizeMsgID(in.ClientMessageID)
	q.queue = append(q.queue, in)
	q.holdPeer(&q.queue[len(q.queue)-1])
	return in.ClientMessageID
}

// AcceptOutside accepts input the driver delivers without queueing it (codex's native
// turn/steer into the running turn). It does Accept's resend check and ledger recording, so the
// caller does neither: dup = a resend, deliver nothing. New member input ends the episode
// however it is delivered; a resend does not.
func (q *TurnQueue) AcceptOutside(in TurnInput) (id string, dup bool) {
	in.ClientMessageID = NormalizeMsgID(in.ClientMessageID)
	if q.holds(in.ClientMessageID) {
		return in.ClientMessageID, true
	}
	// Recorded on either LedgerPoint: the input goes to the runtime now, which is the point
	// LedgerAtTake records at too.
	if q.ledger != nil && q.ledger.SeenOrRecord(q.name, in.ClientMessageID) {
		return in.ClientMessageID, true
	}
	q.noteAccepted(in)
	return in.ClientMessageID, false
}

func (q *TurnQueue) noteAccepted(in TurnInput) {
	if in.Origin.IsMember() {
		q.episode = false
	}
}

// holds reports whether id is queued or taken.
func (q *TurnQueue) holds(id string) bool {
	if q.head != nil && q.head.ID() == id {
		return true
	}
	for _, e := range q.queue {
		if e.ClientMessageID == id {
			return true
		}
	}
	return false
}

// Len is the number of entries waiting in the queue, the taken one excluded.
func (q *TurnQueue) Len() int { return len(q.queue) }

// Head is the entry the pump has taken, or nil.
func (q *TurnQueue) Head() *Taken { return q.head }

// Episode reports whether a stop episode is open (the next stop is a second stop).
func (q *TurnQueue) Episode() bool { return q.episode }

// Take moves the first entry out of the queue as cancellable. nil when the queue is empty or
// an entry is already taken (one at a time). On LedgerAtTake, a resend is recorded and dropped
// here, as the drivers did before.
func (q *TurnQueue) Take() *Taken {
	if q.head != nil {
		return nil
	}
	for len(q.queue) > 0 {
		in := q.queue[0]
		q.queue = q.queue[1:]
		if q.recorded[in.ClientMessageID] {
			delete(q.recorded, in.ClientMessageID)
		} else if q.at == LedgerAtTake && q.ledger != nil && q.ledger.SeenOrRecord(q.name, in.ClientMessageID) {
			q.releasePeer(in)
			continue
		}
		q.head = &Taken{In: in}
		q.stopPending, q.pendingFirst = false, false
		return q.head
	}
	q.maybeEndEpisode()
	return nil
}

// Hold marks the taken entry as waiting behind a turn this driver did not start (opencode's
// waitIdle, muse's host-side queue). A first stop lets a held entry continue.
//
// redirect reports that a first stop had set stop-pending on t while it looked like the turn
// being started (muse: turn/start sent, its "queued" answer not back yet). t is queued after
// all, so the pending stop is lifted and the caller delivers it to the turn t waits behind
// instead; the stop episode opens, since t is still to run. A pending stop from a second stop or
// a discard stays: t is stopped when it starts.
func (q *TurnQueue) Hold(t *Taken, held bool) (redirect bool) {
	if t != q.head {
		return false
	}
	t.held = held
	if held && q.stopPending && q.pendingFirst {
		q.stopPending, q.pendingFirst = false, false
		q.episode = true
		return true
	}
	return false
}

// Commit is the pump's last act under the lock before it hands t to the runtime. false: a stop
// or a removal cancelled t (or t is stale); the pump must not send it.
func (q *TurnQueue) Commit(t *Taken) bool {
	if t != q.head {
		return false
	}
	t.phase = phaseCommitted
	q.releasePeer(t.In)
	return true
}

// Received records that the runtime holds t. It returns true when a stop is pending for t: the
// pump delivers it now, itself (decision 3's hand-over).
func (q *TurnQueue) Received(t *Taken) (deliverStop bool) {
	if t != q.head || t.phase != phaseCommitted {
		return false
	}
	t.phase, t.held = phaseReceived, false
	deliverStop, q.stopPending, q.pendingFirst = q.stopPending, false, false
	return deliverStop
}

// Settle releases t: its turn has settled, or its start failed and no turn was made. The
// episode ends here once the queue is empty.
func (q *TurnQueue) Settle(t *Taken) {
	if t != q.head {
		return
	}
	q.head, q.stopPending, q.pendingFirst = nil, false, false
	q.maybeEndEpisode()
}

// Requeue puts t back at the head of the queue as cancellable: the runtime went away before it
// received t (muse's msp.ErrClosed). A t with a stop pending is not put back — the stop was
// aimed at it — and Requeue reports false; the caller settles it as cancelled.
func (q *TurnQueue) Requeue(t *Taken) bool {
	if t != q.head {
		return false
	}
	q.head = nil
	if q.stopPending {
		q.stopPending, q.pendingFirst = false, false
		q.maybeEndEpisode()
		return false
	}
	q.queue = append([]TurnInput{t.In}, q.queue...)
	if isHeldPeer(t.In) {
		putHeld(q.name, t.In) // Commit released it; it waits again
	}
	if q.at == LedgerAtTake {
		if q.recorded == nil {
			q.recorded = map[string]bool{}
		}
		q.recorded[t.ID()] = true
	}
	return true
}

// Interrupt applies a stop to the queue (decisions 1-3). The driver then stops what it has to:
// the head per Head, or — with HeadNone — a turn it is running that did not come from this
// queue (a turn taken over after a restart).
//
// busy says whether any turn is running on the runtime right now, this driver's or another
// client's. With nothing taken and nothing busy, the first queued entry is input whose start is
// in flight — accepted, but the pump has not taken it yet — and a first stop stops it like a
// taken one (decision 1): it is removed and not kept as a discard (HeadCancelled).
func (q *TurnQueue) Interrupt(opts InterruptOpts, busy bool) InterruptOutcome {
	second := opts.DiscardQueue || q.episode
	var out InterruptOutcome
	if !second {
		out.Result.Stop = StopFirst
		var stopped *TurnInput
		if h := q.head; h != nil && h.phase == phaseTaken && !h.held {
			in := h.In
			stopped = &in
		}
		out.Head = q.stopHead(false)
		if out.Head == HeadNone && !busy && len(q.queue) > 0 {
			in := q.queue[0]
			stopped = &in
			q.queue = q.queue[1:]
			out.Head = HeadCancelled
		}
		if stopped != nil {
			out.Result.Discard = q.keepDiscard(DiscardFirstStop, []QueueItem{itemOf(*stopped, "")})
		}
		q.episode = len(q.queue) > 0 || out.Head == HeadKept
		return out
	}
	out.Result.Stop = StopSecond
	reason := DiscardSecondStop
	if opts.DiscardQueue {
		out.Result.Stop, reason = StopDiscard, DiscardQueue
	}
	var items []QueueItem
	if h := q.head; h != nil && h.phase == phaseTaken {
		items = append(items, itemOf(h.In, ""))
	}
	out.Head = q.stopHead(true)
	for _, in := range q.queue {
		items = append(items, itemOf(in, ""))
	}
	q.queue = nil
	q.episode = false
	if len(items) > 0 {
		out.Result.Discard = q.keepDiscard(reason, items)
	}
	return out
}

// keepDiscard records a discard for return (decision 4), keeping the last maxDiscards.
func (q *TurnQueue) keepDiscard(reason string, items []QueueItem) *Discard {
	for _, it := range items {
		q.recordGone(it.ID)
		if it.Origin.Kind == OriginPeer {
			releaseHeld(q.name, it.ID)
		}
	}
	d := Discard{ID: mintID("dsc_"), At: q.now().Format(time.RFC3339), Reason: reason, Items: items}
	q.discards = append(q.discards, d)
	if len(q.discards) > maxDiscards {
		q.discards = q.discards[len(q.discards)-maxDiscards:]
	}
	return &d
}

// recordGone marks a discarded or removed id as seen on LedgerAtTake, where Take never got to
// record it. Without it a retry under the same ClientMessageID would run on copilot, cursor,
// kiro and lcpp and be dropped on codex, opencode and muse; now it is dropped everywhere. What
// the member puts back and sends again goes out under a new id.
func (q *TurnQueue) recordGone(id string) {
	if q.at == LedgerAtTake && q.ledger != nil {
		q.ledger.SeenOrRecord(q.name, id)
	}
}

// stopHead applies a stop to the taken entry. A first stop spares a held entry: it is queued,
// not the turn being stopped (decision 1).
func (q *TurnQueue) stopHead(second bool) HeadAction {
	h := q.head
	switch {
	case h == nil:
		return HeadNone
	case h.held && !second:
		return HeadKept
	case h.phase == phaseTaken:
		q.head = nil
		return HeadCancelled
	case h.phase == phaseCommitted:
		// A second stop on top of a pending first one makes it a second stop's.
		q.pendingFirst = !second && (!q.stopPending || q.pendingFirst)
		q.stopPending = true
		return HeadStopPending
	default:
		return HeadStopNow
	}
}

// Remove takes the entry id out while it is cancellable (decision 5). It returns the entry, or
// ErrAlreadyStarted once it is committed, or ErrNotQueued. Every queued copy of id goes (a
// LedgerAtTake resend can sit in the queue twice).
func (q *TurnQueue) Remove(id string) (QueueItem, error) {
	if h := q.head; h != nil && h.ID() == id {
		if h.phase != phaseTaken {
			return QueueItem{}, ErrAlreadyStarted
		}
		q.head = nil
		q.dropQueued(id)
		q.maybeEndEpisode()
		q.recordGone(id)
		q.releasePeer(h.In)
		return itemOf(h.In, ""), nil
	}
	for _, in := range q.queue {
		if in.ClientMessageID == id {
			q.dropQueued(id)
			q.maybeEndEpisode()
			q.recordGone(id)
			q.releasePeer(in)
			return itemOf(in, ""), nil
		}
	}
	return QueueItem{}, ErrNotQueued
}

func (q *TurnQueue) dropQueued(id string) {
	kept := q.queue[:0]
	for _, in := range q.queue {
		if in.ClientMessageID != id {
			kept = append(kept, in)
		}
	}
	q.queue = kept
}

// DropAll is teardown (decision 8): everything unsent goes, nothing is kept for return, and
// the episode ends. A committed or received head is left for the driver's own teardown.
// Peer messages keep their files: DeliverHeld hands them to the session's next start (#1255).
func (q *TurnQueue) DropAll() {
	q.queue = nil
	if h := q.head; h != nil && h.phase == phaseTaken {
		q.head = nil
	}
	q.episode = false
}

// DismissDiscard drops a kept discard once the member restored or dismissed it. false when it
// is gone already (another tab, or pushed out by newer discards).
func (q *TurnQueue) DismissDiscard(id string) bool {
	for i, d := range q.discards {
		if d.ID == id {
			q.discards = append(q.discards[:i:i], q.discards[i+1:]...)
			return true
		}
	}
	return false
}

// Discards returns the kept discards, oldest first.
func (q *TurnQueue) Discards() []Discard {
	if len(q.discards) == 0 {
		return nil
	}
	return append([]Discard(nil), q.discards...)
}

// Items is the queue as the messages payload shows it: the taken entry first while it is not
// yet a turn of its own, then the queue in order.
func (q *TurnQueue) Items() []QueueItem {
	var out []QueueItem
	if h := q.head; h != nil {
		switch {
		case h.phase == phaseTaken:
			out = append(out, itemOf(h.In, EntryQueued))
		case h.phase == phaseCommitted && h.held:
			out = append(out, itemOf(h.In, EntrySent))
		case h.phase == phaseCommitted:
			out = append(out, itemOf(h.In, EntryCommitted))
		}
	}
	for _, in := range q.queue {
		out = append(out, itemOf(in, EntryQueued))
	}
	return out
}

// Texts is Items' prompts, for the older queuedPrompts field.
func (q *TurnQueue) Texts() []string {
	var out []string
	for _, it := range q.Items() {
		out = append(out, it.Text)
	}
	return out
}

func (q *TurnQueue) maybeEndEpisode() {
	if len(q.queue) == 0 && q.head == nil {
		q.episode = false
	}
}

func itemOf(in TurnInput, st EntryState) QueueItem {
	return QueueItem{ID: in.ClientMessageID, Text: in.Prompt, Attachments: in.Attachments, Origin: in.Origin, State: st}
}

func mintID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}
