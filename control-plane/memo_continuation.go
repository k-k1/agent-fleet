package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/memoafter"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// A run of a queue may add a continuation of the message it was given, and the Control Plane admits
// it as pending when the member's release of that message covers it (ADR 0137, addendum
// 2026-11-20, #4606). Every rule reads the Control Plane's own rows: the only things taken from the
// request are the claim (a boolean the model sets) and the delivery id the Agent stamped from the
// calling session's meta. Both are guard rails (the workspace holds the memo token), so what
// bounds a forged claim is the count, the reference and the queue below, never who made it.

const (
	// memoContinuationDefault is the continuation_max every queue gets unless the member sets it.
	memoContinuationDefault = 4
	// memoContinuationCeiling is the most the Control Plane lets a queue allow, whatever the row says.
	memoContinuationCeiling = 10
)

// The held_reason reasons of a continuation that was not admitted. They name the first rule that
// failed, and the Console says each in words.
const (
	memoContHeldOrigin    = "continuation_origin"    // not the token face, or not a session a queue started
	memoContHeldDelivery  = "continuation_delivery"  // no such delivery, not open, another member's or queue's, or another session's
	memoContHeldBatch     = "continuation_batch"     // the run was given more than one message
	memoContHeldGiven     = "continuation_given"     // the message the run was given is gone
	memoContHeldReference = "continuation_reference" // not the same reference, repo or category
	memoContHeldRoot      = "continuation_root"      // the given message was not released by a person or the switch
	memoContHeldOff       = "continuation_off"       // the queue's continuation_max is 0
	memoContHeldUsed      = "continuation_used"      // this run already added one
	memoContHeldLimit     = "continuation_limit"     // the chain has admitted M
	memoContHeldStopped   = "continuation_stopped"   // the member stopped the chain
)

// memoContinuationMax is the queue's allowance, clamped to the ceiling.
func memoContinuationMax(q store.MemoQueue) int {
	switch {
	case q.ContinuationMax < 0:
		return 0
	case q.ContinuationMax > memoContinuationCeiling:
		return memoContinuationCeiling
	}
	return q.ContinuationMax
}

// memoContinuationVerdict is the outcome of memoContinue: Admitted with the chain's root and the
// continuation's place in it, or Reason (one of the memoContHeld values) for a held create.
type memoContinuationVerdict struct {
	Admitted bool
	Reason   string
	// After is the message the run was given, named in the held reason.
	After string
	Root  string
	Seq   int
	// RefURL is the given message's URL, which an admitted continuation carries instead of its own.
	RefURL string
	// Recipe is the given message's recipe, which an admitted continuation inherits unless it names its own.
	Recipe string
}

// memoContinue decides whether message m, being created by via, is a continuation the member's
// release already covers. It claims the run's one place and one place of the chain as its last
// steps, in the create's own transaction, so a create that fails after them gives both back.
func memoContinue(tx store.MemoTxOps, membershipID string, m store.Memo, via memoVia, q store.MemoQueue, now string) (memoContinuationVerdict, error) {
	no := func(reason, after string) (memoContinuationVerdict, error) {
		return memoContinuationVerdict{Reason: reason, After: after}, nil
	}
	if via.face != memoFaceToken || via.producerOrigin != store.MemoOriginQueue || via.queueDelivery == "" {
		return no(memoContHeldOrigin, "")
	}
	d, ok, err := tx.GetDelivery(via.queueDelivery)
	if err != nil {
		return memoContinuationVerdict{}, err
	}
	launch := via.queueLaunch
	if launch == "" {
		launch = via.producer
	}
	if !ok || d.MembershipID != membershipID || d.QueueID != q.CategoryID || d.State != store.DeliveryOpen || d.Session == "" || d.Session != launch ||
		memoDeliveryPastDue(d, q, time.Now()) {
		return no(memoContHeldDelivery, "")
	}
	if len(d.MessageIDs) != 1 {
		return no(memoContHeldBatch, "")
	}
	given, ok, err := tx.GetMemo(d.MessageIDs[0])
	if err != nil {
		return memoContinuationVerdict{}, err
	}
	if !ok {
		return no(memoContHeldGiven, d.MessageIDs[0])
	}
	// The work item is the provider and key. The URL is not compared: it is taken from the given
	// message below, so a continuation can never point the next session at another place.
	if given.RefKey == "" || given.RefProvider != m.RefProvider || given.RefKey != m.RefKey ||
		given.Repo != m.Repo || given.Category != m.Category {
		return no(memoContHeldReference, given.ID)
	}
	// The approval must be the one the delivery really carried: the given message is this
	// delivery's delivered row and still holds its release. A queue's settings deleted and made
	// again clear both, and that would read an old run's message as a fresh root.
	if given.State != store.MemoStateDelivered || given.DeliveryID != d.ID || given.ReleaseHash == "" || given.ReleasedAt == "" {
		return no(memoContHeldRoot, given.ID)
	}
	root := given.ChainID
	if root == "" {
		// A chain starts at a message the member released: by a press or a Console write
		// (released_by empty), or by the switch they turned on. A tidy's, a continuation's and a
		// hand-back's do not, and a switch-accepted message starts one only while the switch is
		// still on: turning it off does not leave a chain that can still be started. A chain that
		// already started goes on within its M.
		if given.ReleasedBy != "" && given.ReleasedBy != store.MemoReleasedBySetting {
			return no(memoContHeldRoot, given.ID)
		}
		if given.ReleasedBy == store.MemoReleasedBySetting && !q.AcceptOwnSessions {
			if _, found, err := tx.GetChain(given.ID); err != nil {
				return memoContinuationVerdict{}, err
			} else if !found {
				return no(memoContHeldRoot, given.ID)
			}
		}
		root = given.ID
	}
	max := memoContinuationMax(q)
	if max == 0 {
		return no(memoContHeldOff, given.ID)
	}
	// The chain row was made for one queue and one work item. A message whose reference a person
	// edited, or that was merged into another queue, does not extend it.
	c, found, err := tx.GetChain(root)
	if err != nil {
		return memoContinuationVerdict{}, err
	}
	if found {
		switch {
		case c.Stopped:
			return no(memoContHeldStopped, given.ID)
		case (c.CategoryID != "" && c.CategoryID != q.CategoryID) || (c.RefKey != "" && (c.RefProvider != given.RefProvider || c.RefKey != given.RefKey)):
			return no(memoContHeldReference, given.ID)
		case c.Admitted >= max:
			return no(memoContHeldLimit, given.ID)
		}
	}
	// One conditional UPDATE on the delivery row: its close, overdue mark and retire change it
	// from outside the member's lock, and the record outlives the child message.
	taken, err := tx.TakeDeliveryContinuation(d.ID)
	if err != nil {
		return memoContinuationVerdict{}, err
	}
	if !taken {
		// Nothing was written. Read the delivery again to say why: the close, the overdue mark
		// and the retire change it outside the member's lock, between the read above and here.
		if now, found, err := tx.GetDelivery(d.ID); err != nil {
			return memoContinuationVerdict{}, err
		} else if !found || now.State != store.DeliveryOpen || memoDeliveryPastDue(now, q, time.Now()) {
			return no(memoContHeldDelivery, "")
		}
		return no(memoContHeldUsed, given.ID)
	}
	seq, _, won, err := tx.AdmitChain(store.MemoChain{ID: root, CategoryID: q.CategoryID,
		RefProvider: given.RefProvider, RefKey: given.RefKey, CreatedAt: now}, max)
	if err != nil {
		return memoContinuationVerdict{}, err
	}
	if !won {
		// The member's lock makes this unreachable after the check above. Rolling back also gives
		// the delivery's place back.
		return memoContinuationVerdict{}, errors.New("memo chain: no place after the check")
	}
	return memoContinuationVerdict{Admitted: true, Root: root, Seq: seq, After: given.ID, RefURL: given.RefURL, Recipe: given.Recipe}, nil
}

// memoDeliveryPastDue is a delivery that is overdue, waiting to be retired, or older than the
// queue's task bound: the dispatcher marks the first two outside the member's lock and may not
// have reached the third yet.
func memoDeliveryPastDue(d store.MemoDelivery, q store.MemoQueue, now time.Time) bool {
	if d.OverdueAt != "" || d.RetirePending {
		return true
	}
	// The origin and the boundary of the dispatcher's own overdue mark (markOverdue): from when the
	// delivery was opened, or created, and overdue once that is not after now minus the bound.
	since := d.OpenedAt
	if since == "" {
		since = d.CreatedAt
	}
	return since != "" && !(since > store.DeliveryTS(now.Add(-time.Duration(memoQueueTaskHours(q))*time.Hour)))
}

// memoContinuationApply puts the verdict on m, which memoQueueMove entered as held (a token-face
// create never enters pending except by the switch, and a queue session is not the switch's).
func memoContinuationApply(m store.Memo, v memoContinuationVerdict, delivery string) store.Memo {
	if v.Admitted {
		m.RefURL = v.RefURL
		// A continuation runs as the message it continues unless it names a recipe of its own.
		if m.Recipe == "" {
			m.Recipe = v.Recipe
		}
		m.State, m.ReleaseHash, m.ReleasedAt, m.ReleasedBy = store.MemoStatePending, memoContentHash(m), memoNow(), store.MemoReleasedByContinuation
		m.ChainID, m.ChainSeq, m.ChainDelivery, m.HeldReason = v.Root, v.Seq, delivery, ""
		return m
	}
	if m.State == store.MemoStateHeld {
		raw, _ := json.Marshal(memoafter.HeldReason{After: v.After, Reason: v.Reason, At: store.DeliveryTS(time.Now())})
		m.HeldReason = string(raw)
	}
	return m
}

// memoLinked is memoafter.Linked plus the messages that have a chain row: a root carries no
// chain_id itself, and a tidy only keeps it whole (ADR 0137, addendum 2026-11-20).
func memoLinked(tx store.MemoTxOps, rows []store.Memo) (map[string]bool, error) {
	linked := memoafter.Linked(rows)
	ids, err := tx.ListChainIDs()
	if err != nil {
		return nil, err
	}
	for id := range ids {
		linked[id] = true
	}
	return linked, nil
}

// memoChainStopped is the answer to a stop-chain.
type memoChainStopped struct {
	ChainID string `json:"chainId"`
	Stopped bool   `json:"stopped"`
}

// memoChainStop is POST /api/memos/{id}/stop-chain: the member's Stop chain on any message of a
// chain (a continuation, or the root). The next continuation of the chain is held.
func (a memoAPI) stopChain(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	id := r.PathValue("id")
	var chain string
	err := a.store.MemoTx(r.Context(), mv.MembershipID, func(tx store.MemoTxOps) error {
		m, ok, err := tx.GetMemo(id)
		if err != nil {
			return err
		}
		if !ok {
			return memoTxFail(&apiError{http.StatusNotFound, "not_found", "memo not found"})
		}
		chain = m.ChainID
		if chain == "" {
			chain = m.ID
		}
		_, err = tx.StopChain(chain, memoNow())
		return err
	})
	if aerr := memoTxResult(err); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, memoChainStopped{ChainID: chain, Stopped: true})
}
