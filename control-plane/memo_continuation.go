package main

import (
	"encoding/json"
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
}

// memoContinue decides whether message m, being created by via, is a continuation the member's
// release already covers. rows is the bucket m goes into. It claims one place of the chain as its
// last step, in the create's own transaction, so a create that fails after it gives the place back.
func memoContinue(tx store.MemoTxOps, membershipID string, m store.Memo, via memoVia, q store.MemoQueue, rows []store.Memo, now string) (memoContinuationVerdict, error) {
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
	if !ok || d.MembershipID != membershipID || d.QueueID != q.CategoryID || d.State != store.DeliveryOpen || d.Session == "" || d.Session != via.producer {
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
	if given.RefKey == "" || given.RefProvider != m.RefProvider || given.RefKey != m.RefKey ||
		given.Repo != m.Repo || given.Category != m.Category {
		return no(memoContHeldReference, given.ID)
	}
	root := given.ChainID
	if root == "" {
		// A chain starts at a message the member released: by a press or a Console write (released_by
		// empty), or by the switch they turned on. A tidy's, a continuation's and a hand-back's do not.
		if given.ReleasedBy != "" && given.ReleasedBy != store.MemoReleasedBySetting {
			return no(memoContHeldRoot, given.ID)
		}
		root = given.ID
	}
	max := memoContinuationMax(q)
	if max == 0 {
		return no(memoContHeldOff, given.ID)
	}
	for _, r := range rows {
		if r.ChainDelivery == d.ID {
			return no(memoContHeldUsed, given.ID)
		}
	}
	seq, stopped, won, err := tx.AdmitChain(store.MemoChain{ID: root, CategoryID: q.CategoryID,
		RefProvider: given.RefProvider, RefKey: given.RefKey, CreatedAt: now}, max)
	if err != nil {
		return memoContinuationVerdict{}, err
	}
	switch {
	case won:
		return memoContinuationVerdict{Admitted: true, Root: root, Seq: seq, After: given.ID}, nil
	case stopped:
		return no(memoContHeldStopped, given.ID)
	}
	return no(memoContHeldLimit, given.ID)
}

// memoContinuationApply puts the verdict on m, which memoQueueMove entered as held (a token-face
// create never enters pending except by the switch, and a queue session is not the switch's).
func memoContinuationApply(m store.Memo, v memoContinuationVerdict, delivery string) store.Memo {
	if v.Admitted {
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
