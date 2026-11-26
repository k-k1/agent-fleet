package main

// A run that hands its message back (ADR 0137, addendum 2026-11-19, part B, issue #4541).
//
// The delivery's session asks with defer_queue_item, which the Agent relays here with the
// session's name (deferDelivery): the request is only recorded on the delivery. It is applied by
// a clean close made on a running Agent's report (closeIfDoneReported, memoCloseClean), which
// closes the delivery as deferred and requests the retire of its tree; the messages stay with the
// closed delivery until that retire is confirmed and a read made after it still shows nothing
// that drops the request (settleDeferred). They are never held by two runs: a closed delivery
// holds no slot, and nothing claims a message whose delivery_id is set. The closes from the
// mirror while the workspace is stopped never apply it (tickKept and closeIfDoneUnreported leave
// such a delivery open), and an aborted or unknown close ignores it.
//
// A run that went on to do the work is not handed back: a turn of the delivery's session tree
// that started after the call, or a PR of the session that is open or merged in a provider answer
// newer than the call, drops the request and the close is clean. What cannot be read is a drop
// too: a hand-back that repeats done work costs more than one that is lost, and the person can
// still return the run's messages from the run history (returnDelivery).
//
// Anything in the workspace that holds the queue token can make the call, so the session rule is
// a guard rail. What a forged call can do is bounded: delay a released message, and after three
// send it back to the person. A deferred delivery is skipped by the breaker, so it cannot reset
// it either.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/control-plane/internal/memoafter"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	// memoDeferReasonMax bounds a hand-back's reason, in bytes. It is shown to the person and never
	// put into a later prompt.
	memoDeferReasonMax = 1 << 10
	// memoDeferMinAhead, memoDeferMaxAhead and memoDeferDefault bound not_before.
	memoDeferMinAhead = 10 * time.Minute
	memoDeferMaxAhead = 7 * 24 * time.Hour
	memoDeferDefault  = time.Hour
	// memoDeferGrace is how far outside those bounds a time is still taken (and moved onto the
	// bound): a model that asks for "ten minutes from now" is a few seconds late by the time the
	// call lands.
	memoDeferGrace = time.Minute
	// memoDeferClockSkew bounds how far the Agent's stamp of the call may be from this clock. The
	// stamp is what the session tree's turn starts (the Agent's clock too) are compared with.
	memoDeferClockSkew = 10 * time.Minute
)

// Error codes of the hand-back routes.
const (
	errCodeMemoDeferNotOpen  = "delivery_not_open"
	errCodeMemoDeferNotYours = "delivery_not_yours"
	errCodeMemoDeferBatch    = "defer_needs_batch_1"
	errCodeMemoDeferBad      = "bad_defer"
	errCodeMemoReturnNotDone = "delivery_not_closed"
	errCodeMemoReturnNothing = "nothing_to_return"
	errCodeMemoReturnRefHeld = "memo_ref_taken"
)

// memoDeferIn is POST /internal/memo-deliveries/{id}/defer's body, as the Agent relays it.
type memoDeferIn struct {
	Session   string `json:"session"`
	Reason    string `json:"reason"`
	NotBefore string `json:"notBefore"`
	Cancel    bool   `json:"cancel"`
	// At is when the Agent relayed the call, by its clock.
	At string `json:"at"`
}

// memoDeferOut is its answer: the request as it now stands on the delivery.
type memoDeferOut struct {
	Delivery  string `json:"delivery"`
	Deferred  bool   `json:"deferred"`
	Reason    string `json:"reason,omitempty"`
	NotBefore string `json:"notBefore,omitempty"`
}

// memoDeferNotBefore checks and normalizes the requested time against now.
func memoDeferNotBefore(raw string, now time.Time) (string, *apiError) {
	t := now.Add(memoDeferDefault)
	if strings.TrimSpace(raw) != "" {
		var err error
		if t, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(raw)); err != nil {
			return "", &apiError{http.StatusBadRequest, errCodeMemoDeferBad, "notBefore must be an RFC 3339 time"}
		}
	}
	lo, hi := now.Add(memoDeferMinAhead), now.Add(memoDeferMaxAhead)
	switch {
	case t.Before(lo.Add(-memoDeferGrace)) || t.After(hi.Add(memoDeferGrace)):
		return "", &apiError{http.StatusBadRequest, errCodeMemoDeferBad,
			fmt.Sprintf("notBefore must be between %s and %s from now", memoDeferMinAhead, memoDeferMaxAhead)}
	case t.Before(lo):
		t = lo
	case t.After(hi):
		t = hi
	}
	return store.DeliveryTS(t), nil
}

// deferDelivery (POST /internal/memo-deliveries/{id}/defer, token face): records, replaces or
// withdraws the hand-back request of an open delivery, for the session the delivery started. Only
// on a queue whose batch is 1: a batch would return work that is done along with work that is not.
func (a memoAPI) deferDelivery(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	var in memoDeferIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	now := time.Now()
	in.Session, in.Reason = strings.TrimSpace(in.Session), strings.TrimSpace(in.Reason)
	if in.Session == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeMemoDeferBad, "session is required"})
		return
	}
	at, err := time.Parse(time.RFC3339Nano, in.At)
	if err != nil || at.Sub(now) > memoDeferClockSkew || now.Sub(at) > memoDeferClockSkew {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeMemoDeferBad, "at must be the time of the call, by a clock within 10 minutes of the Control Plane's"})
		return
	}
	reason, notBefore := "", ""
	if !in.Cancel {
		switch {
		case in.Reason == "":
			writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeMemoDeferBad, "reason is required"})
			return
		case len(in.Reason) > memoDeferReasonMax || !utf8.ValidString(in.Reason):
			writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeMemoDeferBad, "reason must be valid text of at most 1 KiB"})
			return
		}
		var aerr *apiError
		if notBefore, aerr = memoDeferNotBefore(in.NotBefore, now); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		reason = in.Reason
	}
	id := r.PathValue("id")
	err = a.store.MemoTx(r.Context(), mv.MembershipID, func(tx store.MemoTxOps) error {
		d, ok, err := tx.GetDelivery(id)
		if err != nil {
			return err
		}
		if !ok {
			return memoTxFail(&apiError{http.StatusNotFound, "not_found", "run not found"})
		}
		if d.State != store.DeliveryOpen {
			return memoTxFail(&apiError{http.StatusConflict, errCodeMemoDeferNotOpen, "the run is not open: a message can be handed back only while its run is"})
		}
		if len(d.MessageIDs) != 1 {
			return memoTxFail(&apiError{http.StatusConflict, errCodeMemoDeferBatch, "a run of more than one message cannot hand its messages back"})
		}
		if d.Session != in.Session {
			return memoTxFail(&apiError{http.StatusConflict, errCodeMemoDeferNotYours, "the run was not started for this session"})
		}
		q, ok, err := tx.GetQueue(d.QueueID)
		if err != nil {
			return err
		}
		if !ok || q.Batch != 1 {
			return memoTxFail(&apiError{http.StatusConflict, errCodeMemoDeferBatch, "a message can be handed back only on a queue that sends one message per run"})
		}
		stamp := ""
		if !in.Cancel {
			stamp = store.DeliveryTS(at)
		}
		won, err := tx.SetDeliveryDefer(id, in.Session, reason, notBefore, stamp)
		if err != nil {
			return err
		}
		if !won {
			return memoTxFail(&apiError{http.StatusConflict, errCodeMemoDeferNotOpen, "the run closed meanwhile"})
		}
		return nil
	})
	if aerr := memoTxResult(err); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, memoDeferOut{Delivery: id, Deferred: !in.Cancel, Reason: reason, NotBefore: notBefore})
}

// memoDeferCheck is what a close read, outside the lock, about the hand-back request made at at:
// result is "" when it may be applied, else the store.DeferDropped* reason.
type memoDeferCheck struct {
	at     string
	result string
}

// deferCheck reads whether the request of d still stands: no turn of the tree started after the
// call, and the session holds no PR that is open or merged in an answer newer than the call.
func (r *memoQueueRunner) deferCheck(ctx context.Context, v *memoMemberView, d store.MemoDelivery, tree []*sessionWire) *memoDeferCheck {
	out := &memoDeferCheck{at: d.DeferAt}
	at, err := time.Parse(time.RFC3339Nano, d.DeferAt)
	if err != nil || !v.defers || !v.prs {
		out.result = store.DeferDroppedUnread
		return out
	}
	if treeTurnStartedAfter(tree, at) {
		out.result = store.DeferDroppedTurn
		return out
	}
	status, body, ok := r.askPRSnapshot(ctx, v.res, memoPRSnapshotRequest{Session: tree[0].Name, Since: d.DeferAt})
	var reply memoPRSnapshotReply
	if !ok || status != http.StatusOK || json.Unmarshal(body, &reply) != nil || reply.PRs == nil {
		out.result = store.DeferDroppedUnread
		return out
	}
	for _, p := range *reply.PRs {
		if p.State != "closed" {
			// Open, draft, merged, or a state the provider could not show: the run may have done
			// the work.
			out.result = store.DeferDroppedPR
			return out
		}
	}
	return out
}

// treeTurnStartedAfter reports whether a session of the tree started a turn after at. A stamp
// that does not parse counts as after: the request is not applied on a doubt.
func treeTurnStartedAfter(tree []*sessionWire, at time.Time) bool {
	for _, s := range tree {
		if s.LastTurnStartAt == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, s.LastTurnStartAt)
		if err != nil || t.After(at) {
			return true
		}
	}
	return false
}

// memoCloseClean closes an open delivery that nothing held on two reads in a row, in the MemoTx
// that counted the second read, and requests the retire of its session. A delivery with a
// hand-back request closes as deferred when dc, read for that same request, found nothing that
// drops it; its messages stay with it until settleDeferred has seen the retire end and read the
// tree again. A request dc was not read for (none read, or replaced since) keeps the delivery open
// for the next tick, which reads it. A run of more than one message is never handed back.
func memoCloseClean(tx store.MemoTxOps, id string, dc *memoDeferCheck) error {
	cur, ok, err := tx.GetDelivery(id)
	if err != nil || !ok || cur.State != store.DeliveryOpen {
		return err
	}
	if cur.DeferAt != "" && (dc == nil || dc.at != cur.DeferAt) {
		return nil
	}
	outcome := store.DeliveryClean
	if cur.DeferAt != "" && dc.result == "" && len(cur.MessageIDs) == 1 {
		outcome = store.DeliveryDeferred
	}
	won, err := tx.MoveDelivery(id, store.DeliveryOpen, store.DeliveryClosed, "", outcome, store.DeliveryTS(time.Now()))
	if err != nil || !won {
		return err
	}
	if err := tx.SetDeliveryRetirePending(id); err != nil {
		return err
	}
	if cur.DeferAt != "" && outcome == store.DeliveryClean {
		result := dc.result
		if result == "" {
			result = store.DeferDroppedUnread
		}
		return tx.SetDeliveryDeferResult(id, result, "")
	}
	return nil
}

// settleDeferred decides the deferred closes whose retire has ended. The close was decided on a
// read made before the retire, and a person, a peer or a watch may have started the tree again
// between that read and the retire: the messages go back only when the retire was confirmed (a
// released or done one means a person took the session over), and a list and a PR answer read
// after it still show no later turn and no PR. Anything else drops the hand-back, and the close
// becomes the clean one it would have been. Nothing is decided without a running Agent's answer.
func (r *memoQueueRunner) settleDeferred(ctx context.Context, views map[string]*memoMemberView, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	ds, err := r.mgr.store.ListDeferredToSettle(ctx)
	if err != nil {
		log.Printf("memo queue: list deferred closes: %v", err)
		return
	}
	for _, d := range ds {
		if ctx.Err() != nil {
			return
		}
		result := ""
		switch d.RetireResult {
		case string(retireConfirmed):
		case string(retireReleased), string(retireDone), string(retireFault):
			result = store.DeferDroppedReleased
		default:
			// gone, or a close an older binary made: nothing shows the tree stopped.
			result = store.DeferDroppedUnread
		}
		if result == "" {
			v := r.view(ctx, views, d.MembershipID)
			if v == nil || !v.liveOK || !v.report {
				continue // the next tick with a running Agent decides
			}
			result = r.deferRecheck(ctx, v, d)
			if result == "wait" {
				continue
			}
		}
		r.applyDeferred(ctx, d, result, now)
	}
}

// deferRecheck reads the tree and its PRs again after the retire: "" when the hand-back stands,
// a store.DeferDropped* reason when it does not, "wait" when the Agent did not answer the list.
func (r *memoQueueRunner) deferRecheck(ctx context.Context, v *memoMemberView, d store.MemoDelivery) string {
	cctx, cancel := context.WithTimeout(ctx, memoAgentCallWait)
	env, err := r.mgr.agentSessionsEnv(cctx, v.res.rt)
	cancel()
	if err != nil || !env.reportsQueue() {
		return "wait"
	}
	tree := deliveryTree(env.Sessions, d.CurrentSession())
	if len(tree) == 0 {
		return store.DeferDroppedUnread
	}
	fresh := *v
	fresh.defers, fresh.prs = env.readsDefers(), env.readsPRs()
	return r.deferCheck(ctx, &fresh, d, tree).result
}

// applyDeferred keeps the decision: the return transition when result is "", else the drop.
func (r *memoQueueRunner) applyDeferred(ctx context.Context, d store.MemoDelivery, result string, now time.Time) {
	wctx, cancel := memoWriteCtx(ctx)
	defer cancel()
	err := r.mgr.store.MemoTx(wctx, d.MembershipID, func(tx store.MemoTxOps) error {
		cur, ok, err := tx.GetDelivery(d.ID)
		if err != nil || !ok || cur.Outcome != store.DeliveryDeferred || cur.DeferResult != "" || cur.ReturnedAt != "" {
			return err
		}
		if result != "" {
			_, err := tx.SettleDeferResult(d.ID, result, "")
			return err
		}
		at := store.DeliveryTS(now)
		held, _ := json.Marshal(memoafter.HeldReason{Reason: store.MemoHeldDeferred, At: at})
		res, err := tx.ReturnDelivered(d.ID, store.MemoReturn{NotBefore: cur.DeferNotBefore,
			Entry: store.MemoDeferEntry{Reason: cur.DeferReason, At: at, Delivery: d.ID}, HeldReason: string(held)})
		if err != nil {
			return err
		}
		_, err = tx.SettleDeferResult(d.ID, store.DeferApplied, store.EncodeReturnBlocked(res.Blocked))
		return err
	})
	if err != nil {
		log.Printf("memo queue: delivery %s: settle the hand-back: %v", d.ID, err)
	}
}

// returnDelivery (POST /api/memo-queues/{categoryId}/deliveries/{id}/return): the person's "return
// to queue" of a closed run. The messages whose delivery_id is still that run go back to pending
// by the return transition, with not_before and the hand-backs cleared, as the person's own press.
// A message whose reference another open message of the queue holds is refused (409) with that
// message named, and nothing is returned. Console only: never the token face or /mcp.
func (a memoAPI) returnDelivery(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	queueID, id := r.PathValue("categoryId"), r.PathValue("id")
	var out store.MemoDelivery
	err := a.store.MemoTx(r.Context(), mv.MembershipID, func(tx store.MemoTxOps) error {
		d, ok, err := tx.GetDelivery(id)
		if err != nil {
			return err
		}
		if !ok || d.QueueID != queueID || d.Outcome == store.DeliveryWake {
			return memoTxFail(&apiError{http.StatusNotFound, "not_found", "run not found"})
		}
		if d.State != store.DeliveryClosed {
			return memoTxFail(&apiError{http.StatusConflict, errCodeMemoReturnNotDone, "only a run that has ended can return its messages"})
		}
		n := 0
		for _, mid := range d.MessageIDs {
			m, ok, err := tx.GetMemo(mid)
			if err != nil {
				return err
			}
			if !ok || m.DeliveryID != d.ID || (m.State != store.MemoStateDelivered && m.State != store.MemoStateUnconfirmed) {
				continue
			}
			by, err := tx.OpenRefHolder(m)
			if err != nil {
				return err
			}
			if by != "" {
				return memoTxFail(&apiError{http.StatusConflict, errCodeMemoReturnRefHeld,
					fmt.Sprintf("message %s cannot return: message %s of this queue already holds %s", m.ID, by, m.RefKey)})
			}
			n++
		}
		if n == 0 {
			return memoTxFail(&apiError{http.StatusConflict, errCodeMemoReturnNothing, "no message of this run can return to the queue"})
		}
		at := store.DeliveryTS(time.Now())
		res, err := tx.ReturnDelivered(id, store.MemoReturn{Person: true, Hash: memoContentHash, At: memoNow()})
		if err != nil {
			return err
		}
		if err := tx.MarkDeliveryReturned(id, at, store.EncodeReturnBlocked(res.Blocked)); err != nil {
			return err
		}
		// A hand-back not decided yet is settled by this press: the person's return stands, and the
		// settle pass must not record it as the run's own.
		if d.Outcome == store.DeliveryDeferred && d.DeferResult == "" {
			if _, err := tx.SettleDeferResult(id, store.DeferReturnedByPerson, store.EncodeReturnBlocked(res.Blocked)); err != nil {
				return err
			}
		}
		out, _, err = tx.GetDelivery(id)
		return err
	})
	if aerr := memoTxResult(err); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, memoDeliveryToDTO(out))
}
