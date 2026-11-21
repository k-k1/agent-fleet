package main

// Message queues, step 1c (ADR 0137 section 6, issue #4163): a delivery is one fire of a queue.
// "Fire now" reserves a slot and claims the messages in one transaction, the scheduler's tick
// sends the claimed deliveries, reconciles the ones a restart or a lost answer left behind, and
// closes the open ones whose session is done.
//
// Two rules hold every step here together:
//   - A slot is reserved inside store.MemoTx, which takes the membership row's lock first. A
//     queue belongs to one member, so the queue's slots and the member-wide limit are both
//     counted and taken behind that one lock, on SQLite and on Postgres, by every Control Plane.
//   - Every move of a delivery's state is a conditional write on the delivery's own row
//     (store MoveDelivery). Only the writer whose write succeeded acts on the move. Elapsed time
//     alone never decides that an old worker will not send: it only decides who tries the write.
//
// Nothing is ever fired twice. A create that got no answer keeps its slot until the Agent's
// abandon answer settles it, and a timeout or a stopped workspace closes nothing.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentkinds"
	"github.com/k-k1/agent-fleet/control-plane/internal/memoafter"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	// memoQueueMemberLimit bounds the deliveries that are not closed across all of a member's
	// queues (ADR 0137 section 7, provisional).
	memoQueueMemberLimit = 4
	// memoDeliveryListMax is how many deliveries GET .../deliveries answers.
	memoDeliveryListMax = 50
	// memoQueueSendsPerTick bounds the queue sends one tick starts. They share the tick's
	// schedulerMaxConcurrentFires with the schedules, and the tick waits for all of them, so
	// this keeps half the fires for schedules whatever the queues hold.
	memoQueueSendsPerTick = schedulerMaxConcurrentFires / 2
	// memoSettleBudget bounds the reconciliation and closing pass of one tick, and
	// memoAgentCallWait each Agent call in it. Both run before the tick's fires start.
	// memoClosedTurnBudget bounds the pass that reads the first turns of closed deliveries, apart
	// from memoSettleBudget.
	// memoRetireReserve is the part of memoSettleBudget kept for the retire pass: the read pass
	// stops that much before the budget ends, so a slow workspace in it cannot keep the retire pass
	// from running, and the two together never take longer than memoSettleBudget before the tick's
	// fires start. Time the read pass leaves over goes to the retire pass.
	memoClosedTurnBudget = 20 * time.Second
	memoSettleBudget     = 60 * time.Second
	memoRetireReserve    = memoSettleBudget / 3
	memoAgentCallWait    = 15 * time.Second
	// memoRetireCallWait bounds one retire call: it stops every session of a tree.
	memoRetireCallWait = 30 * time.Second
	// memoDeliveryWriteWait bounds a delivery write made where no caller deadline applies (a
	// send after its Agent call). A write that times out decides nothing: the next tick
	// reconciles the delivery from its state.
	memoDeliveryWriteWait = 30 * time.Second
	// memoDeliverySendMargin is the part of a fire's time budget beyond the wait for the
	// Agent: the reservation, the create (agentHTTPClient's two minutes) and slack.
	memoDeliverySendMargin = 10 * time.Minute
)

// The Agent's bounds on one item of a queue delivery (workspace/agent sessionx
// checkQueueDelivery). A message past them is refused by the Agent whole, so it is refused at
// the press instead, where the member can still edit it.
const (
	memoDeliveryItemBodyMax    = 64 << 10
	memoDeliveryAttachmentsMax = 20
	memoDeliveryPathMax        = 4096
)

// memoDeliveryItemProblem says why the Agent would refuse m as an item, "" when it would not.
func memoDeliveryItemProblem(m store.Memo) string {
	if len(m.Body) > memoDeliveryItemBodyMax {
		return fmt.Sprintf("its text is over %d bytes", memoDeliveryItemBodyMax)
	}
	if len(m.RefPath) > memoDeliveryPathMax {
		return fmt.Sprintf("its file path is over %d bytes", memoDeliveryPathMax)
	}
	switch memoEffectiveProvenance(m) {
	case store.MemoProvenanceMember, store.MemoProvenanceWorkspace, store.MemoProvenanceMCP:
	case store.MemoProvenanceCurator:
		if why := memoCuratorItemProblem(m); why != "" {
			return why
		}
	default:
		return "its origin cannot be sent to a session"
	}
	atts := parseMemoAttachments(m.Attachments)
	if strings.TrimSpace(m.Attachments) != "" && atts == nil {
		return "its attachments cannot be read"
	}
	if len(atts) > memoDeliveryAttachmentsMax {
		return fmt.Sprintf("it has more than %d attachments", memoDeliveryAttachmentsMax)
	}
	for _, a := range atts {
		if len(a.Path) > memoDeliveryPathMax || len(a.Name) > memoDeliveryPathMax || !filepath.IsAbs(a.Path) {
			return "an attachment's path or name cannot be sent"
		}
	}
	return ""
}

// memoCuratorSourcesMax is the most sources a curator item may name (the Agent's bound).
const memoCuratorSourcesMax = 100

// memoCuratorItemProblem says why a derived message is not what the Agent takes as a curator item:
// a plain text note whose sources' provenance and count are known. "" when it is.
func memoCuratorItemProblem(m store.Memo) string {
	switch {
	case m.Kind != "text" || m.RefPath != "" || m.RefKey != "" || m.Attachments != "":
		return "a derived message is a plain text note"
	case m.SourceProvenance != store.MemoProvenanceMember && m.SourceProvenance != store.MemoProvenanceWorkspace &&
		m.SourceProvenance != store.MemoProvenanceMCP:
		return "the origin of the notes it was derived from cannot be sent to a session"
	}
	if n := len(decodeIDs(m.DerivedFrom)); n < 1 || n > memoCuratorSourcesMax {
		return "the notes it was derived from cannot be counted"
	}
	return ""
}

// memoDeliveryKey is the durable Agent idempotency key of a delivery's create.
func memoDeliveryKey(deliveryID string) string { return "q_" + deliveryID }

// memoDeliveryDTO is a delivery on the wire. MessageIDs is never null.
type memoDeliveryDTO struct {
	ID         string   `json:"id"`
	State      string   `json:"state"`
	Outcome    string   `json:"outcome"`
	Session    string   `json:"session"`
	MessageIDs []string `json:"messageIds"`
	CreatedAt  string   `json:"createdAt"`
	SentAt     string   `json:"sentAt"`
	ClosedAt   string   `json:"closedAt"`
	// HeldBy is why an open delivery still has its slot at the Agent's last report: machine,
	// person, limit, wakers or unknown, "" when nothing holds it (or no report was read: a
	// delivery of an Agent that does not report). HeldSession is the session of the tree that
	// holds it, HeldSeenAt when that read began.
	HeldBy      string `json:"heldBy"`
	HeldSession string `json:"heldSession"`
	HeldSeenAt  string `json:"heldSeenAt"`
	// HeldWakers names the watches and timers behind a heldBy of wakers (id and source), never
	// null, empty when none was listed (an older Agent or Control Plane row).
	HeldWakers []memoHeldWakerDTO `json:"heldWakers"`
	// Overdue is set once the delivery passed its queue's maxTaskHours, OverdueAt when.
	Overdue   bool   `json:"overdue"`
	OverdueAt string `json:"overdueAt"`
	// Auto is set for a delivery the dispatcher fired by itself.
	Auto bool `json:"auto"`
	// Retire is where the retire of a closed delivery's session stands: "pending", or how it
	// ended (confirmed, released: a person took the session over, done, gone, fault), "" when
	// none was needed.
	Retire string `json:"retire"`
	// FirstTurn is how the first turn of the delivery's session ended, null until it did.
	FirstTurn *memoFirstTurnDTO `json:"firstTurn"`
	// Defer is the hand-back the delivery's session asked for (defer_queue_item), absent for
	// none. ReturnBlocked names the messages that stayed where they were because another message
	// of the queue holds their reference, and the message that does. ReturnedAt is when a person
	// returned the run's messages to the queue.
	Defer         *memoDeferDTO             `json:"defer,omitempty"`
	ReturnBlocked []store.MemoReturnBlocked `json:"returnBlocked,omitempty"`
	ReturnedAt    string                    `json:"returnedAt,omitempty"`
}

// memoDeferDTO is a delivery's hand-back request: the session's reason, the time it asked to wait
// for, when it asked (the Agent's clock) and, once closed, what the close made of it (applied, or
// the store.DeferDropped* reason it was dropped for; "" for a close that does not read one).
type memoDeferDTO struct {
	Reason    string `json:"reason"`
	NotBefore string `json:"notBefore"`
	At        string `json:"at"`
	Result    string `json:"result,omitempty"`
}

// memoFirstTurnDTO is the end of a delivery's first turn: outcome is clean, turn-failed or
// turn-aborted, and "" for a kind that records no reason.
type memoFirstTurnDTO struct {
	Outcome string `json:"outcome"`
	Ms      int64  `json:"ms"`
	EndedAt string `json:"endedAt"`
}

func memoDeliveryToDTO(d store.MemoDelivery) memoDeliveryDTO {
	ids := d.MessageIDs
	if ids == nil {
		ids = []string{}
	}
	out := memoDeliveryDTO{ID: d.ID, State: d.State, Outcome: d.Outcome, Session: d.Session, MessageIDs: ids,
		CreatedAt: d.CreatedAt, SentAt: d.SentAt, ClosedAt: d.ClosedAt, Overdue: d.OverdueAt != "", OverdueAt: d.OverdueAt, Auto: d.Auto}
	if d.State == store.DeliveryOpen && d.Reported {
		out.HeldBy, out.HeldSession, out.HeldSeenAt = d.HeldBy, d.HeldSession, d.HeldSeenAt
		if d.HeldBy == store.HeldWakers {
			out.HeldWakers = heldWakersOf(d.HeldDetail)
		}
	}
	if out.HeldWakers == nil {
		out.HeldWakers = []memoHeldWakerDTO{}
	}
	if d.DeferAt != "" {
		out.Defer = &memoDeferDTO{Reason: d.DeferReason, NotBefore: d.DeferNotBefore, At: d.DeferAt, Result: d.DeferResult}
	}
	out.ReturnBlocked, out.ReturnedAt = store.ParseReturnBlocked(d.ReturnBlocked), d.ReturnedAt
	out.Retire = d.RetireResult
	if d.RetirePending || (d.State == store.DeliveryClosed && d.Outcome == store.DeliveryClean && d.Reported && d.RetireResult == "") {
		out.Retire = "pending"
	}
	if d.TurnEndedAt != "" {
		out.FirstTurn = &memoFirstTurnDTO{Outcome: d.TurnOutcome, Ms: d.TurnMs, EndedAt: d.TurnEndedAt}
	}
	return out
}

// memoQueueRepoDir is the working copy a queue's sessions start from, relative to the
// workspace's home (the Agent joins a relative dir onto it). The repo of a queue is a folder
// name under ~/repos, and "" (the common bucket) has none. ok is false for a name that is not
// one folder.
func memoQueueRepoDir(repo string) (string, bool) {
	if repo == "" {
		return "", true
	}
	if repo == "." || repo == ".." || strings.ContainsAny(repo, `/\`) || filepath.Base(repo) != repo {
		return "", false
	}
	return "repos/" + repo, true
}

// --- Fire now: Console only ------------------------------------------------------------------

// fire (POST /api/memo-queues/{categoryId}/fire) reserves a slot of the queue and claims its
// oldest pending messages in one transaction, and answers 202 with the delivery. The send
// happens on the scheduler's next tick: a cold wake does not fit in a request. enabled gates
// the automatic fires of a later step, not this press. An instruction is required.
func (a memoAPI) fire(w http.ResponseWriter, r *http.Request, res *resolved) {
	d, aerr := memoFireNow(r.Context(), a.mgr, res, r.PathValue("categoryId"))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusAccepted, memoDeliveryToDTO(d))
}

// memoFireNow is the reservation and the claim. The running-session count of the quota is read
// before the transaction (an Agent call does not belong under the membership lock), and the
// deliveries not yet sent are added to it inside, where they cannot change.
func memoFireNow(ctx context.Context, mgr *manager, res *resolved, categoryID string) (store.MemoDelivery, *apiError) {
	on, err := mgr.store.GetTenantMemoQueues(ctx, res.mv.TenantID)
	if err != nil {
		return store.MemoDelivery{}, internalErr(err)
	}
	if !on {
		return store.MemoDelivery{}, &apiError{http.StatusConflict, errCodeMemoQueuesDisabled, "your tenant has not turned memo queues on"}
	}
	running, limit := -1, 0
	if !mgr.nativeRuntime {
		limit = mgr.sessionLimit(ctx, res)
		if limit > 0 && res.rt.State(ctx) == "running" {
			if n, err := mgr.countSessions(ctx, res.rt); err == nil {
				running = n
			}
		}
	}
	var out store.MemoDelivery
	var refusal *apiError
	err = mgr.store.MemoTx(ctx, res.mv.MembershipID, func(tx store.MemoTxOps) error {
		d, aerr, err := memoReserveAndClaim(tx, categoryID, running, limit, false)
		if err != nil {
			return err
		}
		// A refusal still commits: the messages it put back to held stay held.
		out, refusal = d, aerr
		return nil
	})
	if err != nil {
		return store.MemoDelivery{}, memoTxResult(err)
	}
	return out, refusal
}

// memoReserveAndClaim runs inside a MemoTx. auto marks a delivery the dispatcher fires by itself. running is the member's running metered sessions
// (-1 when unknown, then the quota is not checked, as the Console's create does), limit the
// max_sessions cap (0 = none).
func memoReserveAndClaim(tx store.MemoTxOps, categoryID string, running, limit int, auto bool) (store.MemoDelivery, *apiError, error) {
	none := store.MemoDelivery{}
	c, ok, err := tx.GetCategory(categoryID)
	if err != nil {
		return none, nil, err
	}
	var q store.MemoQueue
	if ok {
		q, ok, err = tx.GetQueue(c.ID)
		if err != nil {
			return none, nil, err
		}
	}
	if !ok {
		return none, &apiError{http.StatusNotFound, "not_found", "queue not found"}, nil
	}
	if strings.TrimSpace(q.Instruction) == "" {
		return none, &apiError{http.StatusConflict, errCodeMemoQueueNoInstruction, "a queue without an instruction cannot fire"}, nil
	}
	if _, ok := memoQueueRepoDir(c.Repo); !ok {
		return none, &apiError{http.StatusConflict, errCodeMemoQueueBadRepo, "the queue's repository is not a folder under repos"}, nil
	}
	inQueue, inMember, err := tx.CountLiveDeliveries(c.ID)
	if err != nil {
		return none, nil, err
	}
	switch memoPlaceTaken(q, inQueue, inMember) {
	case "category":
		return none, &apiError{http.StatusConflict, errCodeMemoQueueNoSlot,
			fmt.Sprintf("every slot of this queue is taken (%d of %d)", inQueue, q.MaxActive)}, nil
	case "member":
		return none, &apiError{http.StatusConflict, errCodeMemoQueueMemberLimit,
			fmt.Sprintf("your queues already run %d deliveries, the most at once", inMember)}, nil
	}
	if limit > 0 {
		// The deliveries not yet sent have no session to count yet. With the running count
		// unknown (the workspace is not running) they are still a lower bound.
		unsent, err := tx.CountUnsentDeliveries()
		if err != nil {
			return none, nil, err
		}
		if running < 0 {
			running = 0
		}
		if running+unsent >= limit {
			return none, &apiError{http.StatusTooManyRequests, errCodeQuotaSessions,
				fmt.Sprintf("concurrent session limit reached (%d running, %d starting, max %d)", running, unsent, limit)}, nil
		}
	}
	rows, err := tx.ListMemosIn(c.Repo, c.Name)
	if err != nil {
		return none, nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		// created_at has a second's precision: two messages added in one second are told
		// apart by the position each was appended at.
		if rows[i].CreatedAt != rows[j].CreatedAt {
			return rows[i].CreatedAt < rows[j].CreatedAt
		}
		if rows[i].Position != rows[j].Position {
			return rows[i].Position < rows[j].Position
		}
		return rows[i].ID < rows[j].ID
	})
	now := store.DeliveryTS(time.Now())
	d := store.MemoDelivery{ID: store.NewID(), QueueID: c.ID, State: store.DeliveryClaimed, MessageIDs: []string{},
		CreatedAt: now, UpdatedAt: now, Auto: auto}
	// Pick the batch and check every message of it before anything is claimed: a refusal
	// commits (the holds below stay), so a claim made before it would be left with no delivery.
	var batch []store.Memo
	eligible := memoafter.EligibleIn(memoafter.TxLookup(tx, rows, time.Now()))
	for _, m := range rows {
		if len(batch) >= q.Batch {
			break
		}
		if m.State != store.MemoStatePending || m.SentAt != "" || m.SendToken != "" || m.DeliveryID != "" {
			continue
		}
		// A message whose prerequisites are not all satisfied is skipped, a press included: it holds
		// no slot and does not stop the messages behind it. A prerequisite in this same batch is
		// pending, so not satisfied, and a dependent never rides with it.
		if !eligible(m) {
			continue
		}
		// A fire by itself leaves a reference where it is while the Agent cannot show one: the
		// message stays pending and the queue says why (waitNoRefs). A press is the member's own
		// try: it claims the reference, and the send asks the Agent again before it sends.
		if auto && m.RefKey != "" && q.NoRefsAt != "" {
			continue
		}
		// The same for a derived message while the Agent cannot take one: it waits, and the queue
		// says why (waitNoTidy). Nothing derived is ever sent to an Agent without the marker.
		if auto && m.Provenance == store.MemoProvenanceCurator && q.NoTidyAt != "" {
			continue
		}
		// A release is bound to the content it showed. One that no longer matches goes back
		// for a new press instead of being fired.
		if m.ReleaseHash != memoContentHash(m) {
			m.State, m.ReleaseHash, m.ReleasedAt, m.ReleasedBy = store.MemoStateHeld, "", "", ""
			if err := tx.WriteMemo(m); err != nil {
				return none, nil, err
			}
			continue
		}
		if why := memoDeliveryItemProblem(m); why != "" {
			return none, &apiError{http.StatusConflict, errCodeMemoQueueItemInvalid,
				fmt.Sprintf("message %s cannot be sent to a session: %s", m.ID, why)}, nil
		}
		batch = append(batch, m)
	}
	for _, m := range batch {
		won, err := tx.ClaimMemo(m, d.ID)
		if err != nil {
			return none, nil, err
		}
		if won {
			d.MessageIDs = append(d.MessageIDs, m.ID)
		}
	}
	if len(d.MessageIDs) == 0 {
		return none, &apiError{http.StatusConflict, errCodeMemoQueueNothingPending, "nothing in this queue is waiting to be sent"}, nil
	}
	if err := tx.InsertDelivery(d); err != nil {
		return none, nil, err
	}
	if q.Draining {
		// Fire now or by itself: if this batch returns as not sent, the drain it belonged to
		// goes on (ReturnDeliveryMessages).
		if err := tx.MarkDeliveryDrain(d.ID); err != nil {
			return none, nil, err
		}
	}
	return d, nil, nil
}

// listDeliveries (GET /api/memo-queues/{categoryId}/deliveries, and the same read on
// /internal/memo-queues for the fleet operator): a queue's deliveries, newest first, at most
// memoDeliveryListMax. With ?include=open the answer also holds every delivery that is not
// closed, however old (one read, so one snapshot), so a view that lists live runs never loses one
// to the limit. ?closed_since=<RFC3339 time> implies include=open and adds every delivery closed at
// or after that time, so a run that closed after it left the newest fifty still shows its end;
// a time that does not parse is a 400. The token face leaves out the sessions' hand-back reasons.
func (a memoAPI) listDeliveries(via memoVia) func(http.ResponseWriter, *http.Request, store.Identity, store.MembershipView) {
	return func(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
		list := a.store.ListMemoDeliveries
		if r.URL.Query().Get("include") == "open" {
			list = a.store.ListMemoDeliveriesWithOpen
		}
		var ds []store.MemoDelivery
		var err error
		if r.URL.Query().Has("closed_since") {
			t, perr := time.Parse(time.RFC3339Nano, r.URL.Query().Get("closed_since"))
			// A bound past year 9999 would format wider than every stored stamp and match none of
			// the text order's expectations; refuse it.
			bound := ceilMicro(t).UTC()
			if perr != nil || bound.Year() > 9999 {
				writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "closed_since must be an RFC 3339 time"})
				return
			}
			ds, err = a.store.ListMemoDeliveriesClosedSince(r.Context(), mv.MembershipID, r.PathValue("categoryId"), memoDeliveryListMax, store.DeliveryTS(bound))
		} else {
			ds, err = list(r.Context(), mv.MembershipID, r.PathValue("categoryId"), memoDeliveryListMax)
		}
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		out := make([]memoDeliveryDTO, 0, len(ds))
		for _, d := range ds {
			dto := memoDeliveryToDTO(d)
			// A session's reason for handing a message back is the person's to read (ADR 0137,
			// section on hand-backs): the token face is every session's, so it never carries one.
			if via.face == memoFaceToken && dto.Defer != nil {
				dto.Defer.Reason = ""
			}
			out = append(out, dto)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// ceilMicro rounds t up to the microsecond a stored stamp has, so a bound that falls between two
// stamps never admits the one before it.
func ceilMicro(t time.Time) time.Time {
	if f := t.Truncate(time.Microsecond); !f.Equal(t) {
		return f.Add(time.Microsecond)
	}
	return t
}

// endDelivery (POST /api/memo-queues/{categoryId}/deliveries/{id}/end) finishes an open run by a
// person's word: the delivery closes as "ended" (which frees its slot, the slot being any
// delivery not closed) and its session tree is retired by the durable retire pass, unconditionally.
// The session is stopped, never archived, so the run is not recorded as an abort. One conditional
// write on the delivery's own row decides it, so a second press, or a press after the tick closed
// the run, changes nothing and answers the run as it is. A run that has no session yet (claimed,
// sending) is refused: the tick settles those by itself. Scoped by the membership lock, so a run of
// another member or queue is not found. Console only.
func (a memoAPI) endDelivery(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	queueID, id := r.PathValue("categoryId"), r.PathValue("id")
	var out store.MemoDelivery
	err := a.store.MemoTx(r.Context(), mv.MembershipID, func(tx store.MemoTxOps) error {
		cur, ok, err := tx.GetDelivery(id)
		if err != nil {
			return err
		}
		if !ok || cur.QueueID != queueID {
			return memoTxFail(&apiError{http.StatusNotFound, "not_found", "run not found"})
		}
		out = cur
		switch cur.State {
		case store.DeliveryClosed:
			return nil
		case store.DeliveryOpen:
		default:
			return memoTxFail(&apiError{http.StatusConflict, "delivery_not_open", "the run has not started a session yet"})
		}
		won, err := tx.MoveDelivery(id, store.DeliveryOpen, store.DeliveryClosed, "", store.DeliveryEnded, store.DeliveryTS(time.Now()))
		if err != nil {
			return err
		}
		// SetDeliveryRetireForced also marks the retire pending, so no separate pending write.
		if won {
			if err := tx.SetDeliveryRetireForced(id); err != nil {
				return err
			}
		}
		// Re-read: a lost move (the tick closed it first) must answer the row as it is now.
		out, _, err = tx.GetDelivery(id)
		return err
	})
	if aerr := memoTxResult(err); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, memoDeliveryToDTO(out))
}

// --- The tick: send, reconcile, close ------------------------------------------------------

// memoQueueTick is the queue work of a scheduler tick. settle runs first: it reconciles and
// closes, and answers the claimed deliveries this tick should send. send runs each of them in
// one of the tick's fire slots.
type memoQueueTick interface {
	settle(ctx context.Context, now time.Time) []store.MemoDelivery
	// dispatch fires the queues that are open by themselves, at most room deliveries, and
	// answers the claimed ones for the tick to send (memo_dispatch.go).
	dispatch(ctx context.Context, now time.Time, room int) ([]store.MemoDelivery, []func(context.Context))
	send(ctx context.Context, d store.MemoDelivery)
}

// withQueue arms the scheduler's queue work.
func (sc *scheduler) withQueue(q memoQueueTick) *scheduler {
	sc.queue = q
	return sc
}

// memoQueueRunner sends, reconciles and closes deliveries. It wakes and waits the way a
// schedule's fire does, through the wakeFirer.
type memoQueueRunner struct {
	mgr *manager
	f   *wakeFirer
	// budget is a fire's own time budget: a claimed or sending delivery older than it is
	// reconciled. It is above everything a live send waits for.
	budget time.Duration
	// settleBudget bounds one tick's settle and retire passes together (memoSettleBudget), and
	// retireReserve is the end of it kept for the retire pass (memoRetireReserve).
	settleBudget  time.Duration
	retireReserve time.Duration
	// staleReadMax is how old a session list may be when a delivery is decided on it; an older one
	// closes nothing (closeIfDoneReported).
	staleReadMax time.Duration
	// settleOrder and retireOrder keep which member each pass tried last, so that a pass that runs
	// out of budget inside one workspace starts with the members behind it on the next tick. They
	// live in this process only: fairness never needs them to agree between Control Planes, and
	// correctness never depends on them. Only the tick's goroutine touches them.
	settleOrder memoFairOrder
	retireOrder memoFairOrder
	// tickViews is the settle pass's view of each member's workspace in this tick, which the
	// dispatch pass that follows reuses. A tick is one goroutine, so no lock.
	tickViews map[string]*memoMemberView
	// wakeJobs are the wakes made only to settle that this tick's dispatch pass decided on, and
	// tidyJobs the tidy runs it reserved. Both run in the tick's fire slots.
	wakeJobs []func(context.Context)
	tidyJobs []func(context.Context)
	// tidyCallWait is the wait for the Agent's tidy answer, memoTidyCallWait when zero.
	tidyCallWait time.Duration
}

func newMemoQueueRunner(mgr *manager, f *wakeFirer) *memoQueueRunner {
	ready := f.readyTimeout
	if ready <= 0 {
		ready = 5 * time.Minute
	}
	return &memoQueueRunner{mgr: mgr, f: f, budget: ready + memoDeliverySendMargin, settleBudget: memoSettleBudget, retireReserve: memoRetireReserve, staleReadMax: memoSettleBudget}
}

// memoMemberView is what settle knows of one member's workspace in this tick.
type memoMemberView struct {
	res *resolved
	// live is the Agent's session list, and liveOK whether it answered. mirrorOK is set
	// instead when the workspace is stopped, and mirror then holds the DB mirror's rows. With
	// neither, nothing is known of the sessions this tick: a workspace that runs but does not
	// answer, or one that is starting or stopping, proves no session done.
	live     []sessionWire
	liveOK   bool
	mirror   []sessionWire
	mirrorOK bool
	// report is set when the Agent that answered sends the queue report (queueReport): only
	// then do the rows say what would start a session, and may a delivery be closed on it and
	// its sessions retired. gens is the number the database handed out for each open delivery
	// of the member before that read began (the order of two reads, whatever the clocks say),
	// and readAt when it began by the local clock, for display.
	gens   map[string]int64
	report bool
	// refs is set when the Agent that answered can show an item that carries a reference.
	refs bool
	// tidy is set when the Agent that answered has the tidy endpoint and takes a derived message.
	tidy bool
	// prs is set when the Agent that answered has the PR snapshot endpoint (queuePRs).
	prs bool
	// defers is set when the Agent that answered reports the turn starts (queueDefer).
	defers bool
	readAt string
	// readDone is when the session list finished arriving by the local clock: a list that waited
	// behind a long tick is stale for the close decision.
	readDone time.Time
	// actMark is the envelope's actMark of that read, 0 when the Agent sent none.
	actMark int64
	// actIncarnation is the envelope's actIncarnation of that read: "" when the Agent sent none or
	// sent no mark, and then no incarnation is stored or sent.
	actIncarnation string
}

func (r *memoQueueRunner) settle(ctx context.Context, now time.Time) []store.MemoDelivery {
	tick := ctx
	// One budget for both passes: the read pass ends retireReserve early, the retire pass at the end.
	rctx, rcancel := context.WithTimeout(tick, r.settleBudget)
	defer rcancel()
	ctx, cancel := context.WithTimeout(tick, r.settleBudget-min(r.retireReserve, r.settleBudget/3))
	defer cancel()
	ds, err := r.mgr.store.ListLiveMemoDeliveries(ctx)
	if err != nil {
		log.Printf("memo queue: list deliveries: %v", err)
		return nil
	}
	stale := store.DeliveryTS(now.Add(-r.budget))
	views := map[string]*memoMemberView{}
	r.tickViews = views // the dispatcher of this tick reads each workspace once, through the same views
	hours := map[string]map[string]int{}
	// Claimed deliveries need no workspace read, so they are settled first, in list order: the
	// stale ones are closed and the rest are the sends, oldest first. Nothing behind a slow member
	// can delay them or put a newer one of a queue ahead of an older one.
	var sends []store.MemoDelivery
	var reading []store.MemoDelivery
	for _, d := range ds {
		if ctx.Err() != nil {
			break
		}
		if d.State != store.DeliveryClaimed {
			reading = append(reading, d)
			continue
		}
		if d.CreatedAt < stale {
			// The worker that claimed it is gone or past its budget. If it still moves the
			// delivery to sending, it wins and this write loses: nothing is returned twice.
			r.closeNotSent(ctx, d, store.DeliveryClaimed)
			continue
		}
		if len(sends) < memoQueueSendsPerTick {
			sends = append(sends, d)
		}
	}
	// The rest need the member's workspace, one read per member and tick. Members go least
	// recently tried first, so a workspace that eats the budget is not first again next tick.
	byMember := map[string][]store.MemoDelivery{}
	for _, id := range r.settleOrder.arrange(memberOrder(reading, byMember)) {
		if ctx.Err() != nil {
			break
		}
		r.settleOrder.tried(id)
		for _, d := range byMember[id] {
			if ctx.Err() != nil {
				break
			}
			switch d.State {
			case store.DeliverySending:
				if d.SentAt >= stale {
					continue
				}
				if v := r.view(ctx, views, d.MembershipID); v != nil && v.liveOK {
					r.reconcileSending(ctx, v, d)
				}
				// While the workspace is stopped, or the Agent does not answer, the delivery keeps
				// its slot: its session may exist.
			case store.DeliveryOpen:
				d = r.markOverdue(ctx, hours, d, now)
				if v := r.view(ctx, views, d.MembershipID); v != nil {
					r.closeIfDone(ctx, v, d, now)
				}
			}
		}
	}
	// The retire pass has the reserved end of the budget (and what the read pass left of the rest).
	r.retirePending(rctx, views, now)
	// A deferred close returns its messages only after its retire ended (memo_defer.go).
	r.settleDeferred(rctx, views, now)
	// The first-turn pass has a budget of its own: the reads above may have used all of the pass's,
	// and a pass that never runs would leave the breaker blind.
	tctx, tcancel := context.WithTimeout(tick, memoClosedTurnBudget)
	r.collectClosedTurns(tctx, views, now)
	tcancel()
	// The PR snapshots have a budget of their own too: a dependent waits on them.
	pctx, pcancel := context.WithTimeout(tick, memoPRSnapshotBudget)
	r.collectPRSnapshots(pctx, views, now)
	pcancel()
	return sends
}

// view resolves a member's workspace once per tick, without waking it.
func (r *memoQueueRunner) view(ctx context.Context, views map[string]*memoMemberView, membershipID string) *memoMemberView {
	if v, ok := views[membershipID]; ok {
		return v
	}
	views[membershipID] = nil
	identityID, ok, err := r.mgr.store.IdentityIDForMembership(ctx, membershipID)
	if err != nil || !ok {
		return nil
	}
	res, aerr := r.mgr.resolveByMembership(ctx, identityID, membershipID)
	if aerr != nil {
		return nil
	}
	v := &memoMemberView{res: res}
	switch res.rt.State(ctx) {
	case "running":
		// The read is numbered before it begins: of two reads of one delivery, by any Control
		// Plane, the higher number is the later one. Without numbers nothing is kept.
		gerr := r.mgr.store.MemoTx(ctx, membershipID, func(tx store.MemoTxOps) (err error) {
			v.gens, err = tx.ReserveDeliveryReads()
			return err
		})
		if gerr != nil {
			log.Printf("memo queue: reserve reads of %s: %v", membershipID, gerr)
			v.gens = nil
		}
		v.readAt = store.DeliveryTS(time.Now())
		cctx, cancel := context.WithTimeout(ctx, memoAgentCallWait)
		var env agentSessionsEnvelope
		env, err = r.mgr.agentSessionsEnv(cctx, res.rt)
		cancel()
		v.readDone = time.Now()
		v.live, v.liveOK, v.report = env.Sessions, err == nil, err == nil && env.reportsQueue()
		v.refs = err == nil && env.showsReferences()
		v.tidy = err == nil && env.showsTidy()
		v.prs = err == nil && env.readsPRs()
		v.defers = err == nil && env.readsDefers()
		if v.liveOK {
			v.actMark = env.ActMark
			if env.ActMark > 0 {
				v.actIncarnation = env.ActIncarnation
			}
		}

	case "stopped":
		rows, err := r.mgr.store.ListSessions(ctx, res.ws.ID)
		if err != nil {
			break
		}
		v.mirrorOK = true
		for _, s := range rows {
			// A row the mirror still records as running says nothing about an end.
			v.mirror = append(v.mirror, sessionWire{Name: s.Name, Kind: s.Kind, Carried: s.Carried, Alive: s.State == "running"})
		}
	}
	views[membershipID] = v
	return v
}

// reconcileSending settles a sending delivery past its budget from the Agent's abandon answer.
// A lookup would not do: it answers not_found both for a key only reserved and for one it has
// no record of, and a create under a reserved key may still begin. Abandon settles the key
// under the lock a create begins under.
func (r *memoQueueRunner) reconcileSending(ctx context.Context, v *memoMemberView, d store.MemoDelivery) {
	status, body, ok := r.abandon(ctx, v.res, d.ID)
	if !ok {
		return // no answer, or not the Agent's: nothing is decided
	}
	switch {
	case status == http.StatusOK:
		if name := sessionNameOf(body); name != "" {
			r.open(ctx, d, name)
		}
	case status == http.StatusAccepted:
		// A create is in flight: the slot stays, and the next tick asks again.
	case status == http.StatusNotFound && agentErrCode(body) == "not_created":
		r.closeNotSent(ctx, d, store.DeliverySending)
	case status == http.StatusServiceUnavailable && agentErrCode(body) == "create_outcome_unknown":
		// No record, an unreadable one, or a create cut off by a restart: the session may
		// exist. Look for it by the delivery it carries, in a list read after the answer.
		cctx, cancel := context.WithTimeout(ctx, memoAgentCallWait)
		list, err := r.mgr.agentSessions(cctx, v.res.rt)
		cancel()
		if err != nil {
			return
		}
		for _, s := range list {
			if s.QueueDelivery == d.ID && s.Name != "" {
				r.open(ctx, d, s.Name)
				return
			}
		}
		r.closeUnknown(ctx, d)
	}
}

// abandon asks the Agent to settle the delivery's key. ok is false when there is no answer, or
// one that does not name the key: a gateway's or proxy's own error proves nothing.
func (r *memoQueueRunner) abandon(ctx context.Context, res *resolved, deliveryID string) (int, []byte, bool) {
	key := memoDeliveryKey(deliveryID)
	cctx, cancel := context.WithTimeout(ctx, memoAgentCallWait)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost,
		res.rt.Endpoint()+"/sessions-idempotency/"+url.PathEscape(key)+"/abandon", nil)
	if err != nil {
		return 0, nil, false
	}
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentRelayClient.Do(req)
	if err != nil {
		return 0, nil, false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.Header.Get(createKeyHeader) != key {
		return 0, nil, false
	}
	return resp.StatusCode, b, true
}

func sessionNameOf(body []byte) string {
	var s struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(body, &s)
	return s.Name
}

// closeIfDone decides an open delivery for this tick. A workspace whose Agent sends the queue
// report gets the occupancy rule of ADR 0137 section 6 (closeIfDoneReported, and the kept last
// report while the workspace is stopped). Every other delivery keeps the rule that needs no
// report, and nothing is retired for it: an Agent that does not say what would start a session
// again cannot be told to end it either.
func (r *memoQueueRunner) closeIfDone(ctx context.Context, v *memoMemberView, d store.MemoDelivery, now time.Time) {
	switch {
	case v.liveOK && v.report:
		r.closeIfDoneReported(ctx, v, d, now)
	case !v.liveOK && v.mirrorOK && d.Reported:
		r.closeIfDoneKept(ctx, v, d)
	default:
		r.closeIfDoneUnreported(ctx, v, d, now)
	}
}

// closeIfDoneUnreported closes an open delivery whose session is done by what needs no report
// from the Agent: archived, deleted, stopped with no interaction carried, or idle on two
// consecutive ticks. A session where a machine is working, or a person has to act, keeps it
// open. The rows of other states (limited, unknown) keep it open too.
func (r *memoQueueRunner) closeIfDoneUnreported(ctx context.Context, v *memoMemberView, d store.MemoDelivery, now time.Time) {
	// A hand-back request is decided by a running Agent's report alone: the mirror can tell
	// neither a later turn nor a PR, so the delivery stays open for it (settleWake wakes it).
	if !v.liveOK && !v.mirrorOK {
		log.Printf("memo queue: delivery %s: skipped close tick: session list unreadable", d.ID)
	}
	if !v.liveOK && d.DeferAt != "" {
		r.markIdle(ctx, d, 0)
		return
	}
	if !v.liveOK && !v.mirrorOK {
		// No observation this tick: the idle ticks are no longer consecutive.
		r.markIdle(ctx, d, 0)
		return
	}
	rows := v.live
	if !v.liveOK {
		rows = v.mirror
	}
	var row *sessionWire
	for i := range rows {
		if rows[i].Name == d.Session {
			row = &rows[i]
			break
		}
	}
	switch {
	case row == nil:
		// Only the Agent's own list proves a deletion. The mirror may predate the session.
		if v.liveOK {
			r.closeOpen(ctx, d, store.DeliveryAborted)
		}
	case row.Archived:
		r.closeOpen(ctx, d, store.DeliveryAborted)
	case !row.Alive:
		if row.Carried == "" {
			r.closeOpen(ctx, d, store.DeliveryClean)
		} else {
			r.markIdle(ctx, d, 0)
		}
	case row.State == stateIdle && row.CarriedLive == "" && sessionActivityAt(*row, now, 0) == activityIdleWait:
		if d.IdleTicks >= 1 {
			r.closeOpen(ctx, d, store.DeliveryClean)
		} else {
			r.markIdle(ctx, d, 1)
		}
	default:
		r.markIdle(ctx, d, 0)
	}
}

// --- Occupancy: the delivery's tree, what holds it, and what closing does ---------------------

// deliveryTree is the delivery's session and every session it started, at any depth: a row with
// origin "session" whose originSession chain leads to root. The root comes first. The walk keeps
// the names it has seen, so a cycle in originSession ends it, and a chain through a session that
// is gone ends at the gap (the Agent's list holds no row to continue from).
func deliveryTree(rows []sessionWire, root string) []*sessionWire {
	children := map[string][]*sessionWire{}
	var rootRow *sessionWire
	for i := range rows {
		s := &rows[i]
		if s.Name == root {
			rootRow = s
		} else if s.Origin == "session" && s.OriginSession != "" {
			children[s.OriginSession] = append(children[s.OriginSession], s)
		}
	}
	if rootRow == nil {
		return nil
	}
	seen := map[string]bool{root: true}
	out := []*sessionWire{rootRow}
	for i := 0; i < len(out); i++ {
		for _, c := range children[out[i].Name] {
			if !seen[c.Name] {
				seen[c.Name] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// hold ranks (store Held*): what is shown for a tree is the strongest reason found in it.
var memoHoldRank = map[string]int{"": 0, store.HeldUnknown: 1, store.HeldWakers: 2, store.HeldLimit: 3, store.HeldPerson: 4, store.HeldMachine: 5}

// sessionHold is why one session of a delivery's tree keeps the delivery open, "" when it does
// not (ADR 0137 section 6, the table). bgMax is 0 as the rule has always been: the reaper's
// background bound is about the workspace, not about a task.
func sessionHold(s sessionWire, now time.Time) string {
	why := ""
	switch act := sessionActivityAt(s, now, 0); {
	case !s.Alive:
		// Stopped with the interaction carried: a person still has to answer.
		if s.Carried != "" {
			why = store.HeldPerson
		}
	case act == activityMachineBusy || busyState(s.State):
		// A busy row whose progress has lapsed is not classed busy by the reaper, but it still
		// has its slot: the overdue retire, not the reading of one row, ends such a task.
		why = store.HeldMachine
	case act == activityHumanWait:
		why = store.HeldPerson
	case act == activityUnknown:
		why = store.HeldUnknown
	}
	// Started again with the question / plan / permission still unanswered: a person has to
	// act, until the card is answered, discarded or expired. A machine at work outranks it.
	if s.Alive && s.CarriedLive != "" && memoHoldRank[why] < memoHoldRank[store.HeldPerson] {
		why = store.HeldPerson
	}
	if why == "" && (s.State == stateLimited || s.RateLimitResumeAt != "") {
		why = store.HeldLimit
	}
	if why == "" && s.Wakers != nil && (s.Wakers.Watches > 0 || s.Wakers.Timers > 0) {
		why = store.HeldWakers
	}
	if why == "" && s.WakersUnknown {
		why = store.HeldUnknown
	}
	return why
}

// sessionProgress reports whether the row shows work going on that is not a stale one: a busy
// row whose progress is fresh, or a background job younger than the progress bound. The keep-awake
// pin is a person's wish and not progress.
func sessionProgress(s sessionWire) bool {
	if !s.Alive {
		return false
	}
	if s.BackgroundBusy {
		return !backgroundLapsed(s, busyProgressLapse)
	}
	return busyState(s.State) && !progressLapsed(s)
}

// treeLimitKinds is the agent kinds of the sessions of a tree that wait for a usage limit
// (limited, or a booked resume), as ",a,b," or "". It is kept apart from the strongest hold of
// treeHold: a parent that waits for a limit while its child works is held by a machine, and the
// limit still has to stop a new session of that kind.
func treeLimitKinds(tree []*sessionWire) string {
	set := map[string]bool{}
	for _, s := range tree {
		if s.Archived || !(s.State == stateLimited || s.RateLimitResumeAt != "") {
			continue
		}
		set[scheduleInjectKind(s.Kind)] = true
	}
	if len(set) == 0 {
		return ""
	}
	kinds := make([]string, 0, len(set))
	for k := range set {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return "," + strings.Join(kinds, ",") + ","
}

// treeHold is the strongest hold in a tree, the session that has it, and whether any session of
// it shows progress. An archived session of the tree holds nothing.
func treeHold(tree []*sessionWire, now time.Time) (why, session string, progress bool) {
	for _, s := range tree {
		if s.Archived {
			continue
		}
		if sessionProgress(*s) {
			progress = true
		}
		if h := sessionHold(*s, now); memoHoldRank[h] > memoHoldRank[why] {
			why, session = h, s.Name
		}
	}
	return why, session, progress
}

// Bounds on what a delivery keeps of the watches behind a wakers hold: a run's reason names a
// few of them, and a source or id longer than this is not a name.
const (
	memoHeldDetailMax    = 5
	memoHeldDetailFieldN = 80
)

// memoHeldWakerDTO is one watch or timer behind a wakers hold, as the delivery keeps it and the
// Console shows it. Id and source only: never a watch's payload or subject.
type memoHeldWakerDTO struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Builtin bool   `json:"builtin,omitempty"`
	Timer   bool   `json:"timer,omitempty"`
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// treeHeldDetail is the JSON the delivery keeps for a hold: the holders of the sessions of the
// tree that are held by wakers, only when wakers is the tree's strongest hold, "" otherwise (and
// for an Agent that lists none). Another hold outranking wakers must not leave a stale name behind.
func treeHeldDetail(tree []*sessionWire, why string, now time.Time) string {
	if why != store.HeldWakers {
		return ""
	}
	var out []memoHeldWakerDTO
collect:
	for _, s := range tree {
		if s.Archived || s.Wakers == nil || sessionHold(*s, now) != store.HeldWakers {
			continue
		}
		for _, h := range s.Wakers.Holders {
			if len(out) == memoHeldDetailMax {
				break collect
			}
			// A blank id is no name: it must not use up one of the few places.
			id := strings.TrimSpace(h.ID)
			if id == "" {
				continue
			}
			out = append(out, memoHeldWakerDTO{ID: clipRunes(id, memoHeldDetailFieldN), Source: clipRunes(strings.TrimSpace(h.Source), memoHeldDetailFieldN), Builtin: h.Builtin, Timer: h.Timer})
		}
	}
	if len(out) == 0 {
		return ""
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

// heldWakersOf reads a stored HeldDetail. Anything that does not parse (an old row, a newer
// shape) is no names.
func heldWakersOf(detail string) []memoHeldWakerDTO {
	out := []memoHeldWakerDTO{}
	if detail != "" {
		if err := json.Unmarshal([]byte(detail), &out); err != nil || out == nil {
			return []memoHeldWakerDTO{}
		}
	}
	return out
}

// memoChildGrace is how long a child's finished turn keeps its parent's run from closing: time
// for an idle parent to read the answer. Without it the run is closed and stopped about two
// ticks after the child's last turn, before a parent that waits for the child can act on it.
const memoChildGrace = 15 * time.Minute

// memoClockSkew is how far ahead of the Control Plane's clock a child's end may read and still
// count as just ended, up to memoClockSkew. A stamp further ahead is a bad clock, not an answer to wait for: left
// alone it would hold the run until the stamp's own time passed.
const memoClockSkew = time.Minute

// childAnswerUnread reports whether a live member of the tree has a child whose turn ended less
// than memoChildGrace ago and has not started a turn since. The Agent stamps an end to the
// second, so it stands for that whole second: only a parent start after it counts as later. An end
// that reads as in the future is clock skew or a turn that ended after the tick began, and counts
// as just ended. It reads only the Agent's turn
// times on the rows, so every Control Plane and every restart answers alike. Times that do not
// parse prove nothing: when in doubt, answer false.
func childAnswerUnread(tree []*sessionWire, now time.Time) bool {
	byName := map[string]*sessionWire{}
	for _, s := range tree {
		byName[s.Name] = s
	}
	for _, c := range tree {
		if c.Archived || c.Origin != "session" || c.LastTurnEndAt == "" {
			continue
		}
		end, err := time.Parse(time.RFC3339Nano, c.LastTurnEndAt)
		if err != nil || now.Sub(end) >= memoChildGrace || end.Sub(now) > memoClockSkew {
			continue
		}
		p := byName[c.OriginSession]
		if p == nil || p.Archived || !p.Alive {
			continue
		}
		if p.LastTurnStartAt != "" {
			if st, err := time.Parse(time.RFC3339Nano, p.LastTurnStartAt); err != nil || !st.Before(end.Add(time.Second)) {
				continue
			}
		}
		return true
	}
	return false
}

// staleRead reports whether more than r.staleReadMax has passed since the session list arrived, as when the
// tick waited behind a slow pass. When in doubt it answers true: one more tick costs nothing
// against closing a run that is working.
func (r *memoQueueRunner) staleRead(v *memoMemberView) bool {
	return !v.readDone.IsZero() && time.Since(v.readDone) > r.staleReadMax
}

// resetIdle writes the idle count 0 whatever this tick's copy of the delivery says (another
// Control Plane may have stored a 1 since), and on a context that outlives the settle pass's
// deadline: by the time a list is judged stale, that deadline has usually passed.
func (r *memoQueueRunner) resetIdle(ctx context.Context, d store.MemoDelivery) {
	wctx, cancel := memoProgressCtx(ctx)
	defer cancel()
	if _, err := r.mgr.store.MarkMemoDeliveryIdle(wctx, d.ID, 0); err != nil {
		log.Printf("memo queue: delivery %s: idle mark: %v", d.ID, err)
	}
}

// closeIfDoneReported is the rule for an Agent that reports. It keeps what it read with the
// delivery first, then closes the delivery when nothing has held it on two reads in a row.
func (r *memoQueueRunner) closeIfDoneReported(ctx context.Context, v *memoMemberView, d store.MemoDelivery, now time.Time) {
	// A list that is old must not count toward a close: the tick starts the count again and says
	// why in the log. A member of unknown state needs no check here: treeHold holds on it.
	if r.staleRead(v) {
		log.Printf("memo queue: delivery %s: skipped close tick: session list is stale", d.ID)
		r.resetIdle(ctx, d)
		return
	}
	tree := deliveryTree(v.live, d.Session)
	if len(tree) == 0 {
		// Only the Agent's own list proves a deletion.
		log.Printf("memo queue: delivery %s: root session %q missing from the list, aborting", d.ID, d.Session)
		r.closeOpen(ctx, d, store.DeliveryAborted)
		return
	}
	if tree[0].Archived {
		r.closeOpen(ctx, d, store.DeliveryAborted)
		return
	}
	why, who, progress := treeHold(tree, now)
	// Not a hold: no reason is kept, so a stopped workspace and the overdue retire behave as
	// they do for any idle tree. It only keeps this read from closing the delivery.
	unread := why == "" && childAnswerUnread(tree, now)
	gen, numbered := v.gens[d.ID]
	if !numbered {
		// No number for this read (the delivery opened after the numbers were handed out, or the
		// database refused them): it cannot be ordered against another, so nothing is kept and
		// the count of reads with nothing held starts again.
		r.markIdle(ctx, d, 0)
		return
	}
	o := store.DeliveryObservation{Gen: gen, At: v.readAt, HeldBy: why, HeldSession: who, HeldDetail: treeHeldDetail(tree, why, now), Progress: progress,
		ProgressAt: store.DeliveryTS(now), LimitKinds: treeLimitKinds(tree), ActMark: v.actMark, ActIncarnation: v.actIncarnation}
	if t := tree[0].QueueTurn; t != nil && t.EndedAt != "" {
		o.TurnOutcome, o.TurnMs, o.TurnEndedAt = t.Outcome, t.Ms, t.EndedAt
	}
	// A hand-back is decided from reads that need the Agent (its PRs), made before the lock and
	// only when this read may close the delivery: nothing holds it and the last one was clear.
	var dc *memoDeferCheck
	if d.DeferAt != "" && why == "" && !unread && d.Reported && d.HeldBy == "" && d.IdleTicks >= 1 {
		dc = r.deferCheck(ctx, v, d, tree)
	}
	// Keeping the read, counting it, and closing on it are one transaction behind the membership
	// lock, and the count is worked out from the stored row: another Control Plane's newer read
	// that holds the delivery cannot be overwritten by a close made on this one's older copy.
	var fresh *store.MemoDelivery
	wctx, cancel := memoWriteCtx(ctx)
	err := r.mgr.store.MemoTx(wctx, d.MembershipID, func(tx store.MemoTxOps) error {
		ticks, kept, err := tx.ObserveDelivery(d.ID, o)
		if err != nil || !kept {
			return err // closed meanwhile, or a later read is kept: that one decides
		}
		if why != "" {
			// The row as it is now, behind the lock: what an overdue retire decides on, not the
			// copy this tick began with.
			cur, ok, err := tx.GetDelivery(d.ID)
			if err == nil && ok && cur.State == store.DeliveryOpen && cur.HeldGen == o.Gen {
				fresh = &cur
			}
			return err
		}
		if ticks < 2 || unread {
			return nil
		}
		return memoCloseClean(tx, d.ID, dc)
	})
	cancel()
	if err != nil {
		log.Printf("memo queue: delivery %s: keep the report: %v", d.ID, err)
		return
	}
	if fresh != nil {
		fresh.OverdueAt = d.OverdueAt // set by this tick when the stored copy was written after it
		if d.OverdueAt == "" {
			return
		}
		r.retireOverdue(ctx, v, *fresh, progress, now)
	}
}

// closeIfDoneKept decides while the workspace is stopped, from the report last kept. A delivery
// last seen held stays open whatever the mirror shows: nothing was read that could change it.
// One last seen with nothing holding it may close from the mirror, on a second tick, unless the
// mirror shows the session itself running or an interaction carried.
func (r *memoQueueRunner) closeIfDoneKept(ctx context.Context, v *memoMemberView, d store.MemoDelivery) {
	if d.HeldBy != "" {
		return
	}
	var row *sessionWire
	for i := range v.mirror {
		if v.mirror[i].Name == d.Session {
			row = &v.mirror[i]
			break
		}
	}
	switch {
	case row == nil:
		// The mirror may predate the session: it proves nothing.
	case row.Alive || row.Carried != "":
		r.markIdle(ctx, d, 0)
	default:
		r.tickKept(ctx, d)
	}
}

// tickKept counts one tick on the mirror for a delivery last seen with nothing holding it, and
// closes it clean (retire recorded in the same transaction) on the second. Everything is read
// and written behind the membership lock, from the stored row: a report another Control Plane
// kept in the meantime that holds the delivery stops the close.
func (r *memoQueueRunner) tickKept(ctx context.Context, d store.MemoDelivery) {
	wctx, cancel := memoWriteCtx(ctx)
	defer cancel()
	err := r.mgr.store.MemoTx(wctx, d.MembershipID, func(tx store.MemoTxOps) error {
		cur, ok, err := tx.GetDelivery(d.ID)
		if err != nil || !ok || cur.State != store.DeliveryOpen || cur.HeldBy != "" || !cur.Reported {
			return err
		}
		// The mirror can tell neither a later turn nor a PR: a delivery with a hand-back request
		// stays open until a running Agent reports (settleWake wakes the workspace for it).
		if cur.DeferAt != "" {
			return nil
		}
		if cur.IdleTicks < 1 {
			return tx.SetDeliveryIdleTicks(d.ID, 1)
		}
		ok, err = tx.MoveDelivery(d.ID, store.DeliveryOpen, store.DeliveryClosed, "", store.DeliveryClean, store.DeliveryTS(time.Now()))
		if err != nil || !ok {
			return err
		}
		return tx.SetDeliveryRetirePending(d.ID)
	})
	if err != nil {
		log.Printf("memo queue: delivery %s: tick on the mirror: %v", d.ID, err)
	}
}

// --- Overdue ---------------------------------------------------------------------------------

// memoOverdueKind is the notification kind of an overdue delivery.
const memoOverdueKind = "memo-delivery-overdue"

// taskHours is the overdue bound of the delivery's queue.
func (r *memoQueueRunner) taskHours(ctx context.Context, cache map[string]map[string]int, d store.MemoDelivery) int {
	byQueue, ok := cache[d.MembershipID]
	if !ok {
		byQueue = map[string]int{}
		cache[d.MembershipID] = byQueue
		qs, err := r.mgr.store.ListMemoQueues(ctx, d.MembershipID)
		if err != nil {
			log.Printf("memo queue: list queues of %s: %v", d.MembershipID, err)
		}
		for _, q := range qs {
			byQueue[q.CategoryID] = memoQueueTaskHours(q)
		}
	}
	if h, ok := byQueue[d.QueueID]; ok {
		return h
	}
	return memoQueueMaxTaskHoursDefault
}

// markOverdue notes, and tells the member once, that an open delivery passed its queue's
// max_task_hours. It returns d with OverdueAt set when the delivery is marked. Overdue frees
// nothing: it only lets the retire below begin. The notification goes first and is idempotent
// through its EventID, so two Control Planes, or a write that fails and is tried again, tell the
// member once.
func (r *memoQueueRunner) markOverdue(ctx context.Context, cache map[string]map[string]int, d store.MemoDelivery, now time.Time) store.MemoDelivery {
	if d.OverdueAt != "" {
		return d
	}
	since := d.OpenedAt
	if since == "" {
		since = d.CreatedAt
	}
	hours := r.taskHours(ctx, cache, d)
	if since > store.DeliveryTS(now.Add(-time.Duration(hours)*time.Hour)) {
		return d
	}
	label := d.QueueID
	if cats, err := r.mgr.store.ListCategories(ctx, d.MembershipID); err == nil {
		for _, c := range cats {
			if c.ID == d.QueueID {
				label = c.Repo + "/" + c.Name
			}
		}
	}
	payload, _ := json.Marshal(map[string]any{"delivery_id": d.ID, "queue_id": d.QueueID, "session": d.Session,
		"label": label, "max_task_hours": hours})
	at := now.UTC().Format(time.RFC3339)
	if err := r.mgr.store.InsertNotification(ctx, store.Notification{
		EventID: "memo-overdue-" + d.ID, MembershipID: d.MembershipID, Kind: memoOverdueKind,
		TargetType: "memo-queue", TargetID: d.QueueID, DisplayName: label, Payload: string(payload), CreatedAt: at,
	}); err != nil {
		log.Printf("memo queue: delivery %s: notify overdue: %v", d.ID, err)
		return d
	}
	stamp := store.DeliveryTS(now)
	if _, err := r.mgr.store.MarkMemoDeliveryOverdue(ctx, d.ID, stamp); err != nil {
		log.Printf("memo queue: delivery %s: mark overdue: %v", d.ID, err)
		return d
	}
	d.OverdueAt = stamp
	return d
}

// retireOverdue retires the tree of an overdue delivery that still has its slot, once no session
// of the tree has shown progress for busyProgressLapse. It frees nothing by itself: the ordinary
// rule does, when the next report says that nothing holds the tree. An unconfirmed retire is
// repeated on the next tick, a confirmed one is not (progress seen later clears the record).
func (r *memoQueueRunner) retireOverdue(ctx context.Context, v *memoMemberView, d store.MemoDelivery, progress bool, now time.Time) {
	if d.OverdueAt == "" || d.RetiredAt != "" || progress || !v.report {
		return
	}
	last := d.OpenedAt
	if d.ProgressSeenAt > last {
		last = d.ProgressSeenAt
	}
	if last > store.DeliveryTS(now.Add(-busyProgressLapse)) {
		return
	}
	// Whatever the answer, only a retry-worthy one is repeated: a complete retire, a person who
	// took the session over (the Agent's word: released) and a fault that repeating cannot mend
	// are recorded, and progress seen later clears the record.
	// The mark is fixed with the first attempt: a retry whose answer was lost must carry the
	// condition of the first, not of the newer read this row has since kept.
	// One conditional write: it takes the mark of the report this decision was made on, and only while
	// that report is still the kept one; a newer report (or progress) kept first means nothing is
	// sent and the next tick decides again.
	fctx, fcancel := memoProgressCtx(ctx)
	mark, inc, gen, ok, ferr := r.mgr.store.FreezeDeliveryRetireMark(fctx, d.ID, d.HeldGen, d.ActMark, d.ActIncarnation)
	fcancel()
	if ferr != nil {
		log.Printf("memo queue: delivery %s: freeze the retire mark: %v", d.ID, ferr)
		return
	}
	if !ok {
		return
	}
	res := r.retire(ctx, v.res, d.Session, mark, inc)
	if res != retireRepeat {
		wctx, cancel := memoProgressCtx(ctx)
		defer cancel()
		if _, err := r.mgr.store.MarkMemoDeliveryRetired(wctx, d.ID, store.DeliveryTS(time.Now()), gen); err != nil {
			log.Printf("memo queue: delivery %s: mark retired: %v", d.ID, err)
		}
	}
}

// --- Retire ----------------------------------------------------------------------------------

// retireWire is the Agent's answer to POST /sessions/{name}/retire (workspace/agent sessionx
// retireResp): the session and every session it started, each with what it could not end, and
// Complete, true only when every session was retired: none released, nothing unended.
type retireWire struct {
	Sessions []struct {
		Name    string `json:"name"`
		Stopped bool   `json:"stopped"`
		// Released: a person started the session (or one above it) after the retire began, and
		// the Agent left it and everything under it alone.
		Released bool     `json:"released"`
		Unended  []string `json:"unended"`
	} `json:"sessions"`
	Complete bool `json:"complete"`
}

// retireResult is what a retire call came to.
type retireResult string

const (
	// retireRepeat: no confirmation (no answer, an error, a wait that was cancelled, a session the
	// Agent could not end or stop). The call is safe to repeat and the caller does, next tick.
	retireRepeat retireResult = ""
	// retireConfirmed: the Agent said complete.
	retireConfirmed retireResult = "confirmed"
	// retireReleased: a person started the delivery's session after the retire began, and the
	// Agent left it alone. That is the Agent's word that the session is theirs now.
	retireReleased retireResult = "released"
	// retireDone: the root was stopped and everything under it that was not released is stopped
	// and ended, but a session below the root was released to a person. Not complete, and
	// repeating would only meet the same released session: the retire stops here.
	retireDone retireResult = "done"
	// retireFault: the Agent refused it as not a root (409 retire_not_root). A delivery's session
	// is always a root, so this is a fault, logged and not repeated.
	retireFault retireResult = "fault"
)

// retire asks the Agent to end everything that would start the session or the sessions it started
// and to stop them all, and reports what came of it. Only the Agent's own `complete` is a
// confirmation. A 404 is not one: a gateway or an Agent without the route answers it as well.
// That a session is gone is proven by a successful report that no longer lists it
// (retirePending), never by this call. The Agent runs one retire of a root at a time and makes
// a second request wait for the first, so the call keeps its own timeout, and a timeout or a
// cancelled wait (503 retire_wait_cancelled) is a repeat on a later tick.
func (r *memoQueueRunner) retire(ctx context.Context, res *resolved, session string, actMark int64, incarnation string) retireResult {
	cctx, cancel := context.WithTimeout(ctx, memoRetireCallWait)
	defer cancel()
	// ifNoActSince is the actMark of the report the retire was decided on: the Agent leaves a
	// session a person started or typed into after that read, and answers it as released. No mark
	// (an Agent that sends none, a delivery never read with one) sends no body: the retire as
	// before, which an Agent without the condition would ignore anyway.
	var body io.Reader
	if actMark > 0 {
		// The incarnation rides with the mark: the Agent judges each session's act against it. None
		// stored (an older Agent, a row from before the upgrade) sends none, and the mark is compared as before.
		b, _ := json.Marshal(struct {
			IfNoActSince   int64  `json:"ifNoActSince"`
			ActIncarnation string `json:"actIncarnation,omitempty"`
		}{actMark, incarnation})
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(cctx, http.MethodPost,
		res.rt.Endpoint()+"/sessions/"+url.PathEscape(session)+"/retire", body)
	if err != nil {
		return retireRepeat
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		log.Printf("memo queue: retire %s: %v", session, err)
		return retireRepeat
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusConflict && agentErrCode(b) == "retire_not_root" {
		log.Printf("memo queue: retire %s: the Agent says it is not a root, which a delivery's session always is", session)
		return retireFault
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("memo queue: retire %s: the Agent answered %s (%s)", session, resp.Status, agentErrCode(b))
		return retireRepeat
	}
	var out retireWire
	if json.Unmarshal(b, &out) != nil || len(out.Sessions) == 0 || out.Sessions[0].Name != session {
		return retireRepeat
	}
	if out.Complete {
		return retireConfirmed
	}
	if out.Sessions[0].Released {
		return retireReleased
	}
	released := false
	for _, s := range out.Sessions {
		if s.Released {
			released = true
			continue
		}
		if !s.Stopped || len(s.Unended) > 0 {
			log.Printf("memo queue: retire %s: %s is not done: %v", session, s.Name, s.Unended)
			return retireRepeat
		}
	}
	if released {
		return retireDone
	}
	return retireRepeat // not complete and nothing to explain it: do not guess
}

// retirePending retires the sessions of the deliveries a clean close left to it, until the Agent
// confirms. It never wakes a workspace: a stopped one, or one that does not answer or does not
// send the report, is left for a later tick. A session that works again is retired like any
// other: a watch that fired after a failed retire looks exactly like that, and it is the case the
// retire is for. What ends a pending retire is the Agent's word (complete, or released: a person
// started the session after the retire began) or the session's absence from a successful report,
// never elapsed time, which proves neither a person's resume nor that the retire is not needed.
// The result is stored with the delivery. Between our read and the Agent's start of the retire a
// person may resume the session: that window is #4218.
func (r *memoQueueRunner) retirePending(ctx context.Context, views map[string]*memoMemberView, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	ds, err := r.mgr.store.ListRetirePendingDeliveries(ctx)
	if err != nil {
		log.Printf("memo queue: list pending retires: %v", err)
		return
	}
	// Members go least recently tried first (see settle); a member's own deliveries stay oldest
	// first.
	byMember := map[string][]store.MemoDelivery{}
	order := r.retireOrder.arrange(memberOrder(ds, byMember))
	for _, id := range order {
		if ctx.Err() != nil {
			return
		}
		r.retireOrder.tried(id)
		for _, d := range byMember[id] {
			if ctx.Err() != nil {
				return
			}
			r.retireOne(ctx, views, d)
		}
	}
}

// retireOne retires the session of one pending delivery, from the member's view of this tick.
func (r *memoQueueRunner) retireOne(ctx context.Context, views map[string]*memoMemberView, d store.MemoDelivery) {
	v := r.view(ctx, views, d.MembershipID)
	if v == nil || !v.liveOK || !v.report {
		return
	}
	tree := deliveryTree(v.live, d.Session)
	if len(tree) == 0 {
		// A root missing from a report is a deleted session only when the report began after
		// the delivery closed: the tick's cached list may be older than the close (another
		// Control Plane created the session, read it clear twice and closed it after this
		// list was taken). The delivery was read as closed before this read begins, so a
		// fresh read is later than the close. Anything but a successful reporting answer
		// decides nothing.
		cctx, cancel := context.WithTimeout(ctx, memoAgentCallWait)
		env, err := r.mgr.agentSessionsEnv(cctx, v.res.rt)
		cancel()
		if err != nil || !env.reportsQueue() {
			return
		}
		if len(deliveryTree(env.Sessions, d.Session)) == 0 {
			r.finishRetire(ctx, d, "gone")
			return
		}
	}
	res := r.retire(ctx, v.res, d.Session, d.RetireActMark, d.RetireActIncarnation)
	if res != retireRepeat {
		r.finishRetire(ctx, d, string(res))
	}
}

// memberOrder groups ds by member into by, keeping each member's deliveries in list order, and
// returns the members in the order they first appear.
func memberOrder(ds []store.MemoDelivery, by map[string][]store.MemoDelivery) []string {
	var ids []string
	for _, d := range ds {
		if _, ok := by[d.MembershipID]; !ok {
			ids = append(ids, d.MembershipID)
		}
		by[d.MembershipID] = append(by[d.MembershipID], d)
	}
	return ids
}

// memoFairOrder remembers when a pass last tried each member, so that a pass which stops at its
// budget in the middle of the list resumes with the members it did not reach. A member is stamped
// before it is tried, the way a delivery is stamped before its first turn is read: a member that
// consumes the whole budget has still been tried, and goes behind the others next tick. Every
// tick tries at least the first member, so every member is tried within as many ticks as there
// are members. The zero value is ready; it is not safe for concurrent use.
type memoFairOrder struct {
	seq  uint64
	last map[string]uint64
}

// arrange returns ids, never tried first, then by how long ago they were tried, ties in the order
// given (the oldest delivery first). Members no longer listed are forgotten.
func (f *memoFairOrder) arrange(ids []string) []string {
	kept := make(map[string]uint64, len(ids))
	for _, id := range ids {
		kept[id] = f.last[id]
	}
	f.last = kept
	out := append([]string(nil), ids...)
	sort.SliceStable(out, func(i, j int) bool { return f.last[out[i]] < f.last[out[j]] })
	return out
}

// tried records that the pass is about to try id.
func (f *memoFairOrder) tried(id string) {
	f.seq++
	if f.last == nil {
		f.last = map[string]uint64{}
	}
	f.last[id] = f.seq
}

// collectClosedTurns copies the first turn of a session onto a delivery that closed before the
// turn showed: a session archived or stopped before the Agent recorded the end, and one whose
// turn ended after the close. The circuit breaker counts a delivery only once its first turn is
// recorded. A running workspace whose Agent reports is read (the cached list of the tick), for a
// day after the close; a session deleted before its turn was seen leaves nothing to read, and
// that delivery is never counted.
func (r *memoQueueRunner) collectClosedTurns(ctx context.Context, views map[string]*memoMemberView, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	ds, err := r.mgr.store.ListClosedWithoutTurn(ctx, store.DeliveryTS(now.Add(-memoClosedTurnWindow)), memoClosedTurnsPerTick)
	if err != nil {
		log.Printf("memo queue: list closed deliveries without a turn: %v", err)
		return
	}
	for _, d := range ds {
		if ctx.Err() != nil {
			return
		}
		// Stamped before it is tried, and only the ones reached: a pass that runs out of budget
		// inside a slow workspace has still moved on past this delivery, so the next tick starts
		// with the ones not reached. Fifty that never fill cannot keep the rest from being read.
		sctx, cancel := memoProgressCtx(ctx)
		err := r.mgr.store.MarkTurnTried(sctx, []string{d.ID}, store.DeliveryTS(time.Now()))
		cancel()
		if err != nil {
			log.Printf("memo queue: delivery %s: stamp the first turn tried: %v", d.ID, err)
		}
		v := r.view(ctx, views, d.MembershipID)
		if v == nil || !v.liveOK || !v.report {
			continue
		}
		for i := range v.live {
			t := v.live[i].QueueTurn
			if v.live[i].Name != d.Session || t == nil || t.EndedAt == "" {
				continue
			}
			wctx, wcancel := memoProgressCtx(ctx)
			_, err := r.mgr.store.RecordDeliveryFirstTurn(wctx, d.ID, t.Outcome, t.Ms, t.EndedAt)
			wcancel()
			if err != nil {
				log.Printf("memo queue: delivery %s: record the first turn: %v", d.ID, err)
			}
		}
	}
}

// finishRetire ends a pending retire and keeps why: confirmed, released, done, gone or fault.
func (r *memoQueueRunner) finishRetire(ctx context.Context, d store.MemoDelivery, result string) {
	// The Agent's answer (or the fresh read behind a gone) is in hand: recorded whatever is left of
	// the pass's budget.
	wctx, cancel := memoProgressCtx(ctx)
	defer cancel()
	if _, err := r.mgr.store.FinishDeliveryRetire(wctx, d.ID, store.DeliveryTS(time.Now()), result); err != nil {
		log.Printf("memo queue: delivery %s: finish retire: %v", d.ID, err)
	}
}

func (r *memoQueueRunner) markIdle(ctx context.Context, d store.MemoDelivery, ticks int) {
	if d.IdleTicks == ticks {
		return
	}
	wctx, cancel := memoWriteCtx(ctx)
	defer cancel()
	if _, err := r.mgr.store.MarkMemoDeliveryIdle(wctx, d.ID, ticks); err != nil {
		log.Printf("memo queue: delivery %s: idle mark: %v", d.ID, err)
	}
}

// --- The settles: each a conditional write on the delivery's state ---------------------------

// memoWriteCtx is the context of a delivery write. A caller's deadline (the settle pass's) is
// kept: a write that waits on another Control Plane's membership lock must not hold the tick,
// and every schedule behind it, past its budget. A caller without one (a send, whose outcome
// has to be recorded even if the tick is cancelled) gets memoDeliveryWriteWait. Either way a
// write that does not finish decides nothing, and the next tick reconciles from the state.
func memoWriteCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(context.WithoutCancel(ctx), memoDeliveryWriteWait)
}

// memoProgressCtx is the context of a write that records progress the next tick builds on: a
// retire the Agent confirmed, a first turn read, a delivery stamped as tried. The work it records
// has been done (or begun), so a settle pass whose budget ran out meanwhile must not lose it, or the
// same work is repeated first on every tick and starves what is behind it. It does not inherit the
// pass's deadline. Writes that decide something keep memoWriteCtx.
func memoProgressCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), memoDeliveryWriteWait)
}

// memoDeliveryMove moves a delivery and, only when that write won, applies then to its
// messages, in one MemoTx. It reports whether the move won.
func memoDeliveryMove(ctx context.Context, st store.MemoStore, d store.MemoDelivery, from, to, session, outcome string,
	then func(tx store.MemoTxOps, at string) error) (bool, error) {
	won := false
	wctx, cancel := memoWriteCtx(ctx)
	defer cancel()
	err := st.MemoTx(wctx, d.MembershipID, func(tx store.MemoTxOps) error {
		at := store.DeliveryTS(time.Now())
		ok, err := tx.MoveDelivery(d.ID, from, to, session, outcome, at)
		if err != nil || !ok {
			return err
		}
		won = true
		if then != nil {
			return then(tx, at)
		}
		return nil
	})
	return won && err == nil, err
}

// closeNotSent closes a delivery that provably sent nothing: its messages return to pending.
func (r *memoQueueRunner) closeNotSent(ctx context.Context, d store.MemoDelivery, from string) {
	if _, err := memoDeliveryMove(ctx, r.mgr.store, d, from, store.DeliveryClosed, "", store.DeliveryNotSent,
		func(tx store.MemoTxOps, _ string) error { return tx.ReturnDeliveryMessages(d.ID) }); err != nil {
		log.Printf("memo queue: delivery %s: close as not sent: %v", d.ID, err)
	}
}

// closeUnknown closes a delivery whose create may or may not have happened and whose session
// cannot be found: its messages become unconfirmed, shown to the person and never fired again
// by themselves. A duplicate would do the work twice.
func (r *memoQueueRunner) closeUnknown(ctx context.Context, d store.MemoDelivery) {
	won, err := memoDeliveryMove(ctx, r.mgr.store, d, store.DeliverySending, store.DeliveryClosed, "", store.DeliveryUnknown,
		func(tx store.MemoTxOps, _ string) error { return tx.UnconfirmDeliveryMessages(d.ID) })
	if err != nil {
		log.Printf("memo queue: delivery %s: close as unknown: %v", d.ID, err)
		return
	}
	if won {
		// The messages are shown to the person and never fired again by themselves: tell them
		// once. A close that lost the write to another Control Plane is told by that one.
		r.notifyQueue(ctx, d, "memo-delivery-unconfirmed", "memo-unconfirmed-"+d.ID, map[string]any{"messages": len(d.MessageIDs)})
	}
}

// open records the session a sending delivery created: its messages are delivered.
func (r *memoQueueRunner) open(ctx context.Context, d store.MemoDelivery, session string) {
	if _, err := memoDeliveryMove(ctx, r.mgr.store, d, store.DeliverySending, store.DeliveryOpen, session, "",
		func(tx store.MemoTxOps, at string) error { return tx.DeliverDeliveryMessages(d.ID, at) }); err != nil {
		log.Printf("memo queue: delivery %s: open with %s: %v", d.ID, session, err)
	}
}

func (r *memoQueueRunner) closeOpen(ctx context.Context, d store.MemoDelivery, outcome string) {
	// A clean close made without a report (an older Agent) cannot apply a hand-back: it says so.
	var then func(tx store.MemoTxOps, at string) error
	if outcome == store.DeliveryClean && d.DeferAt != "" {
		then = func(tx store.MemoTxOps, _ string) error {
			return tx.SetDeliveryDeferResult(d.ID, store.DeferDroppedUnread, "")
		}
	}
	if _, err := memoDeliveryMove(ctx, r.mgr.store, d, store.DeliveryOpen, store.DeliveryClosed, "", outcome, then); err != nil {
		log.Printf("memo queue: delivery %s: close: %v", d.ID, err)
	}
}

// --- Send -------------------------------------------------------------------------------------

// memoSendPlan is what a claimed delivery sends: the queue, its category and the messages.
type memoSendPlan struct {
	q        store.MemoQueue
	c        store.MemoCategory
	messages []store.Memo
}

// send runs one claimed delivery: wake and wait as a schedule does, reserve the durable key,
// move the delivery to sending, create the session. Every failure before the move to sending is
// provably not sent, and closes the delivery as such. After it, only the Agent's own refusal
// does: anything else leaves it sending for reconciliation.
func (r *memoQueueRunner) send(ctx context.Context, d store.MemoDelivery) {
	notSent := func(why string) {
		log.Printf("memo queue: delivery %s not sent: %s", d.ID, why)
		r.closeNotSent(ctx, d, store.DeliveryClaimed)
	}
	plan, ok, err := r.plan(ctx, d)
	if err != nil {
		log.Printf("memo queue: delivery %s: %v", d.ID, err)
		return
	}
	if !ok {
		return // no longer claimed: another worker or reconciliation has it
	}
	if len(plan.messages) == 0 {
		notSent("no message is claimed by it")
		return
	}
	kind := scheduleInjectKind(plan.q.AgentKind)
	if err := agentkinds.CheckCreateKind(kind); err != nil {
		notSent("agent kind: " + err.Error())
		return
	}
	if aerr := providedRouteCreateRefusal(ctx, d.MembershipID, plan.q.Route); aerr != nil {
		notSent("route: " + aerr.message)
		return
	}
	identityID, ok, err := r.mgr.store.IdentityIDForMembership(ctx, d.MembershipID)
	if err != nil || !ok {
		notSent("the member is gone")
		return
	}
	res, aerr := r.mgr.resolveByMembership(ctx, identityID, d.MembershipID)
	if aerr != nil {
		notSent("resolve membership: " + aerr.message)
		return
	}
	if on, err := r.mgr.store.GetTenantMemoQueues(ctx, res.mv.TenantID); err != nil || !on {
		notSent("memo queues are off for the tenant")
		return
	}
	if !r.wake(ctx, res, d) {
		return
	}
	if d.Auto && !r.autoAgentReports(ctx, res, d) {
		notSent("the Agent does not send the queue report, so nothing is fired into it by itself")
		return
	}
	if planHasReference(plan) && !r.agentShowsReferences(ctx, res, d) {
		notSent("the Agent cannot show a reference, so a delivery that holds one is not sent to it")
		return
	}
	if planHasCurator(plan) && !r.agentShowsTidy(ctx, res, d) {
		notSent("the Agent cannot take a derived message, so a delivery that holds one is not sent to it")
		return
	}
	// The quota: the running sessions as the Agent counts them now, and, in the move to
	// sending below, every delivery whose session the count may not show yet.
	running, lim, countedAt := 0, 0, store.DeliveryTS(time.Now())
	if !r.mgr.nativeRuntime {
		lim = r.mgr.sessionLimit(ctx, res)
	}
	if lim > 0 {
		n, err := r.mgr.countSessions(ctx, res.rt)
		if err != nil {
			notSent("count sessions: " + err.Error())
			return
		}
		running = n
	}
	if err := reserveDeliveryCreate(ctx, res, d.ID); err != nil {
		notSent("reserve the key: " + err.Error())
		return
	}
	won, over, err := r.startSending(ctx, d, running, lim, countedAt)
	if err != nil || !won {
		// Lost to reconciliation (or another Control Plane's send): that writer owns it now.
		return
	}
	if over != "" {
		log.Printf("memo queue: delivery %s not sent: %s", d.ID, over)
		return
	}
	status, h, body, err := r.create(ctx, res, memoDeliveryCreateBody(d, plan))
	if err != nil {
		log.Printf("memo queue: delivery %s: create got no answer (%v); reconciliation settles it", d.ID, err)
		return
	}
	key := memoDeliveryKey(d.ID)
	outcome := ""
	if h.Get(createKeyHeader) == key {
		outcome = h.Get(createOutcomeHeader)
	}
	switch {
	case outcome == "created" && status >= 200 && status < 300 && sessionNameOf(body) != "":
		r.open(ctx, d, sessionNameOf(body))
	case outcome == "refused" && (status < 200 || status >= 300):
		log.Printf("memo queue: delivery %s: the Agent refused the create (%d %s)", d.ID, status, strings.TrimSpace(string(body)))
		r.closeNotSent(ctx, d, store.DeliverySending)
	default:
		// An answer that does not carry the create's outcome: the Agent's own refusal of the
		// body before it took the key, or a proxy's error. Neither proves anything, but the
		// abandon answer does, now: not_created closes it as not sent, a create that began is
		// reported, and anything else is left to reconciliation.
		log.Printf("memo queue: delivery %s: create answered %d (outcome %q); asking the Agent", d.ID, status, outcome)
		cur := d
		cur.State = store.DeliverySending
		r.reconcileSending(ctx, &memoMemberView{res: res, liveOK: true}, cur)
	}
}

func planHasReference(p memoSendPlan) bool {
	for _, m := range p.messages {
		if m.RefKey != "" {
			return true
		}
	}
	return false
}

func planHasCurator(p memoSendPlan) bool {
	for _, m := range p.messages {
		if m.Provenance == store.MemoProvenanceCurator {
			return true
		}
	}
	return false
}

// agentShowsTidy is agentShowsReferences for a delivery that holds a derived message: the Agent
// must send the queueTidy marker. The answer is recorded on the queue (NoTidyAt), so that a fire
// leaves derived messages where they are while the Agent cannot take them.
func (r *memoQueueRunner) agentShowsTidy(ctx context.Context, res *resolved, d store.MemoDelivery) bool {
	cctx, cancel := context.WithTimeout(ctx, memoAgentCallWait)
	defer cancel()
	env, err := r.mgr.agentSessionsEnv(cctx, res.rt)
	if err != nil {
		return false
	}
	at := ""
	if !env.showsTidy() {
		at = store.DeliveryTS(time.Now())
	}
	if err := r.mgr.store.SetMemoQueueNoTidy(ctx, d.QueueID, at); err != nil {
		log.Printf("memo queue: queue %s: note the Agent's tidy: %v", d.QueueID, err)
	}
	return env.showsTidy()
}

// agentShowsReferences is the check of a delivery that holds a reference, made after the wake
// and before anything is reserved: the Agent must send the queueRefs marker. It records what it
// found on the queue, so that a fire leaves the references where they are while the Agent
// cannot show them. An Agent that does not answer is no evidence, and nothing is sent.
func (r *memoQueueRunner) agentShowsReferences(ctx context.Context, res *resolved, d store.MemoDelivery) bool {
	cctx, cancel := context.WithTimeout(ctx, memoAgentCallWait)
	defer cancel()
	env, err := r.mgr.agentSessionsEnv(cctx, res.rt)
	if err != nil {
		return false
	}
	at := ""
	if !env.showsReferences() {
		at = store.DeliveryTS(time.Now())
	}
	if err := r.mgr.store.SetMemoQueueNoRefs(ctx, d.QueueID, at); err != nil {
		log.Printf("memo queue: queue %s: note the Agent's references: %v", d.QueueID, err)
	}
	return env.showsReferences()
}

// startSending moves a claimed delivery to sending, with the quota check in the same
// transaction: running sessions counted at countedAt, plus every other delivery of the member
// whose session that count may miss (sending, or sent or opened since). The membership lock
// orders two sends of one member, on any Control Plane, so the second sees the first. won is
// whether this send owns the delivery; over is set when the quota closed it as not sent.
func (r *memoQueueRunner) startSending(ctx context.Context, d store.MemoDelivery, running, lim int, countedAt string) (won bool, over string, err error) {
	wctx, cancel := memoWriteCtx(ctx)
	defer cancel()
	err = r.mgr.store.MemoTx(wctx, d.MembershipID, func(tx store.MemoTxOps) error {
		at := store.DeliveryTS(time.Now())
		to, outcome := store.DeliverySending, ""
		if lim > 0 {
			starting, err := tx.CountDeliveriesStartedSince(countedAt, d.ID)
			if err != nil {
				return err
			}
			if running+starting >= lim {
				to, outcome = store.DeliveryClosed, store.DeliveryNotSent
				over = fmt.Sprintf("concurrent session limit reached (%d running, %d starting, max %d)", running, starting, lim)
			}
		}
		ok, err := tx.MoveDelivery(d.ID, store.DeliveryClaimed, to, "", outcome, at)
		if err != nil || !ok {
			return err
		}
		won = true
		if to == store.DeliveryClosed {
			return tx.ReturnDeliveryMessages(d.ID)
		}
		return nil
	})
	return won && err == nil, over, err
}

// plan reads what a claimed delivery sends. ok is false when it is no longer claimed.
func (r *memoQueueRunner) plan(ctx context.Context, d store.MemoDelivery) (memoSendPlan, bool, error) {
	var p memoSendPlan
	claimed := false
	err := r.mgr.store.MemoTx(ctx, d.MembershipID, func(tx store.MemoTxOps) error {
		cur, ok, err := tx.GetDelivery(d.ID)
		if err != nil || !ok || cur.State != store.DeliveryClaimed {
			return err
		}
		claimed = true
		q, ok, err := tx.GetQueue(cur.QueueID)
		if err != nil || !ok {
			return err
		}
		c, ok, err := tx.GetCategory(cur.QueueID)
		if err != nil || !ok {
			return err
		}
		p.q, p.c = q, c
		rows, err := tx.ListMemosIn(c.Repo, c.Name)
		if err != nil {
			return err
		}
		byID := map[string]store.Memo{}
		for _, m := range rows {
			if m.DeliveryID == d.ID && m.State == store.MemoStateClaimed {
				byID[m.ID] = m
			}
		}
		for _, id := range cur.MessageIDs {
			if m, ok := byID[id]; ok {
				p.messages = append(p.messages, m)
			}
		}
		return nil
	})
	return p, claimed, err
}

// wake brings the workspace up and waits for the Agent, the schedule's path: the reaper
// keep-alive is taken before the wake, so a start that overruns cannot have its fresh
// container reclaimed under it. A failure here sent nothing, and closes the delivery as not
// sent. It reports whether the Agent is ready.
func (r *memoQueueRunner) wake(ctx context.Context, res *resolved, d store.MemoDelivery) bool {
	if why := r.wakeAndWait(ctx, res); why != "" {
		log.Printf("memo queue: delivery %s not sent: %s", d.ID, why)
		r.closeNotSent(ctx, d, store.DeliveryClaimed)
		return false
	}
	return true
}

// wakeAndWait is the wake of a send and of a tidy run: the keep-alive first, then the start of a
// workspace that is not running, then the wait for the Agent. It answers why it failed, "" when
// the Agent is ready.
func (r *memoQueueRunner) wakeAndWait(ctx context.Context, res *resolved) string {
	release, err := r.mgr.trackWorkspaceConnection(ctx, res.ws.ID, "")
	if err != nil {
		if errors.Is(err, errWorkspaceStopping) {
			return "the workspace is stopping"
		}
		return "keep-alive: " + err.Error()
	}
	r.f.scheduleRelease(release)
	var wakeErr error
	if res.rt.State(ctx) != "running" {
		if aerr := r.f.wsAPI.ensureWorkspaceStartedUnattended(ctx, res); aerr != nil {
			if aerr.code == "quota_workspaces" {
				return "the tenant is at its running-workspace cap"
			}
			wakeErr = fmt.Errorf("wake: %s", aerr.message)
		}
	}
	if err := r.f.awaitAgentReady(ctx, res.rt); err != nil {
		if wakeErr != nil {
			err = fmt.Errorf("%w (after %v)", err, wakeErr)
		}
		return "agent not ready: " + err.Error()
	}
	if wakeErr != nil {
		_ = r.mgr.store.SetWorkspaceState(ctx, res.ws.ID, "running")
		_ = r.mgr.touchWorkspace(ctx, res.ws.ID)
	}
	return ""
}

// reserveDeliveryCreate records the delivery's key on the Agent before its create is sent, so
// that abandon can later answer "not created" for it. Only an answer that names the key counts:
// an Agent without the route could never settle the key, and a create there could not be
// reconciled, so nothing is sent to it.
func reserveDeliveryCreate(ctx context.Context, res *resolved, deliveryID string) error {
	key := memoDeliveryKey(deliveryID)
	cctx, cancel := context.WithTimeout(ctx, memoAgentCallWait)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost,
		res.rt.Endpoint()+"/sessions-idempotency/"+url.PathEscape(key)+"/reserve", nil)
	if err != nil {
		return err
	}
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentRelayClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK || resp.Header.Get(createKeyHeader) != key {
		return fmt.Errorf("the Agent answered %s without the key", resp.Status)
	}
	return nil
}

// create posts the session create. An error is no answer at all.
func (r *memoQueueRunner) create(ctx context.Context, res *resolved, body []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, res.rt.Endpoint()+"/sessions", bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header, b, nil
}

// memoDeliveryCreate is the Agent's POST /sessions body for a queue fire (the Agent slice's
// contract, issue #4162). The Agent composes the first prompt from QueueDelivery.
type memoDeliveryCreate struct {
	Dir                string               `json:"dir"`
	Worktree           bool                 `json:"worktree"`
	Kind               string               `json:"kind"`
	Driver             string               `json:"driver"`
	Model              string               `json:"model"`
	Effort             string               `json:"effort"`
	Route              *memoDeliveryRoute   `json:"route,omitempty"`
	SpendCapUSD        float64              `json:"spend_cap_usd"`
	IdempotencyKey     string               `json:"idempotency_key"`
	IdempotencyDurable bool                 `json:"idempotency_durable"`
	Source             string               `json:"source"`
	QueueDelivery      memoDeliveryEnvelope `json:"queue_delivery"`
	// WorkItem is the session's work item, so its branch name and link follow the rules of a
	// session started from a work item. Set only when the items carry exactly one reference
	// between them.
	WorkItem *memoDeliveryWorkItem `json:"work_item,omitempty"`
}

// memoDeliveryWorkItem is the Agent's session_create work_item: the same object a launch from a
// work item in the Console sends, without the title, type and labels, which a reference does not
// keep (the item is never copied).
type memoDeliveryWorkItem struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
}

type memoDeliveryRoute struct {
	Route string `json:"route"`
}

type memoDeliveryEnvelope struct {
	ID          string             `json:"id"`
	Queue       string             `json:"queue"`
	Label       string             `json:"label"`
	Instruction string             `json:"instruction"`
	Items       []memoDeliveryItem `json:"items"`
}

type memoDeliveryItem struct {
	// Reference is the work item the message points at, absent for none. Only an Agent that
	// sends the queueRefs marker is ever given one.
	Reference      *memoRefWire     `json:"reference,omitempty"`
	ID             string           `json:"id"`
	Kind           string           `json:"kind"`
	Body           string           `json:"body"`
	RefPath        string           `json:"refPath"`
	Attachments    []memoAttachment `json:"attachments"`
	Provenance     string           `json:"provenance"`
	Producer       string           `json:"producer"`
	ProducerOrigin string           `json:"producerOrigin"`
	// SourceProvenance and DerivedFrom are set on a curator item only: the provenance of the notes a
	// tidy run derived it from, and how many there were. Producer and ProducerOrigin are theirs.
	SourceProvenance string `json:"sourceProvenance,omitempty"`
	DerivedFrom      int    `json:"derivedFrom,omitempty"`
}

// memoDeliveryCreateBody builds the create. A queue with a repository gets a new worktree off
// it unless its settings say otherwise: two sessions in one working copy corrupt each other.
// The common bucket has no repository, so its sessions start where the Agent starts them.
func memoDeliveryCreateBody(d store.MemoDelivery, p memoSendPlan) []byte {
	dir, _ := memoQueueRepoDir(p.c.Repo)
	kind := scheduleInjectKind(p.q.AgentKind)
	body := memoDeliveryCreate{
		Dir: dir, Worktree: p.q.Worktree && dir != "", Kind: kind, Driver: injectDriver(kind),
		Model: p.q.Model, Effort: p.q.Effort, SpendCapUSD: p.q.SpendCapUSD,
		IdempotencyKey: memoDeliveryKey(d.ID), IdempotencyDurable: true, Source: "queue",
		QueueDelivery: memoDeliveryEnvelope{ID: d.ID, Queue: p.c.ID, Label: p.c.Repo + "/" + p.c.Name,
			Instruction: p.q.Instruction, Items: make([]memoDeliveryItem, 0, len(p.messages))},
	}
	if p.q.Route != "" {
		body.Route = &memoDeliveryRoute{Route: p.q.Route}
	}
	refs := 0
	var item *memoDeliveryWorkItem
	for _, m := range p.messages {
		atts := parseMemoAttachments(m.Attachments)
		if atts == nil {
			atts = []memoAttachment{}
		}
		it := memoDeliveryItem{ID: m.ID, Kind: m.Kind, Body: m.Body,
			RefPath: m.RefPath, Attachments: atts, Provenance: memoEffectiveProvenance(m), Producer: m.Producer,
			ProducerOrigin: m.ProducerOrigin, Reference: memoRefToWire(m)}
		if m.Provenance == store.MemoProvenanceCurator {
			it.SourceProvenance, it.DerivedFrom = m.SourceProvenance, len(decodeIDs(m.DerivedFrom))
		}
		body.QueueDelivery.Items = append(body.QueueDelivery.Items, it)
		if m.RefKey != "" {
			refs++
			item = &memoDeliveryWorkItem{Provider: m.RefProvider, Key: m.RefKey}
		}
	}
	if refs == 1 {
		body.WorkItem = item
	}
	b, _ := json.Marshal(body)
	return b
}
