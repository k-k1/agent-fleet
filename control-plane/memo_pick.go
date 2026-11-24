package main

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/memoafter"
	"github.com/k-k1/agent-fleet/control-plane/internal/memorecipe"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// memoSortOldestFirst orders a queue's rows the way the claim reads them. created_at has a
// second's precision, so two messages added in one second are told apart by the position each was
// appended at, then by id.
func memoSortOldestFirst(rows []store.Memo) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].CreatedAt != rows[j].CreatedAt {
			return rows[i].CreatedAt < rows[j].CreatedAt
		}
		if rows[i].Position != rows[j].Position {
			return rows[i].Position < rows[j].Position
		}
		return rows[i].ID < rows[j].ID
	})
}

// memoHoldKind says why pickBatch sends a message back to a person.
type memoHoldKind int

const (
	// memoHoldStaleRelease: the release no longer matches the content it showed.
	memoHoldStaleRelease memoHoldKind = iota
	// memoHoldRecipe: the effective recipe cannot create a run under the queue's settings.
	memoHoldRecipe
)

// memoHold is a write the pick asks of its caller: the pick itself touches nothing.
type memoHold struct {
	Memo store.Memo
	Kind memoHoldKind
	Why  string // set for memoHoldRecipe
}

// memoPickInput is everything the choice of a batch reads. rows must already be in claim order
// (memoSortOldestFirst); Eligible answers the prerequisite / start-time question (a read that
// fails is "not eligible" there), so the pick does no I/O and reads no clock.
type memoPickInput struct {
	Queue    store.MemoQueue
	Rows     []store.Memo
	Only     string // a press for one message: the batch is that message alone or a refusal
	Auto     bool   // the dispatcher's own fire, not a press
	Now      time.Time
	Eligible func(store.Memo) bool
}

// memoPickResult is the decision. Holds are applied by the caller in order, also when Refusal is
// set (a refusal commits, so the holds made before it stay).
type memoPickResult struct {
	Batch   []store.Memo
	Holds   []memoHold
	Refusal *apiError
}

// memoPickBatch decides which rows a claim takes, in what order, within the queue's batch size.
// Marked ("run next", #5021) messages are looked at first, then every row oldest first. A marked
// message the Agent would refuse is skipped, never allowed to refuse a batch the mark started:
// that batch's recipe can differ from the one the oldest-first claim would pick, so the refusal
// would stop messages the claim takes without the mark. It is refused at its own place once the
// oldest-first order starts a batch.
func memoPickBatch(in memoPickInput) memoPickResult {
	var res memoPickResult
	q, only := in.Queue, in.Only
	limitBatch := q.Batch
	if only != "" {
		limitBatch = 1
	}
	notReady := func(why string) memoPickResult {
		res.Batch = nil
		res.Refusal = &apiError{http.StatusConflict, errCodeMemoQueueMemoNotReady, "this message cannot run now: " + why}
		return res
	}
	// A run has one recipe: the first eligible message fixes it, one with another recipe waits
	// for its own run.
	var batchRecipe memorecipe.Recipe
	order := make([]store.Memo, 0, len(in.Rows))
	for _, m := range in.Rows {
		if m.RunNext {
			order = append(order, m)
		}
	}
	marked := len(order)
	order = append(order, in.Rows...)
	unsendable := map[string]bool{}
	startedByMark := false
	for i, m := range order {
		if len(res.Batch) >= limitBatch {
			break
		}
		if only != "" && m.ID != only {
			continue
		}
		markedPass := i < marked
		if !markedPass && m.RunNext && !unsendable[m.ID] {
			continue
		}
		if m.State != store.MemoStatePending || m.SentAt != "" || m.SendToken != "" || m.DeliveryID != "" {
			if only != "" {
				if m.State == store.MemoStateHeld {
					return notReady(memoNotReadyHeld)
				}
				return notReady(memoNotReadyClaimed)
			}
			continue
		}
		// A message whose prerequisites are not all satisfied holds no slot and does not stop the
		// messages behind it; a press is refused with the reason.
		if !in.Eligible(m) {
			if only != "" {
				switch {
				case memoafter.Deferred(m, in.Now):
					return notReady(memoNotReadyDeferred)
				case memoafter.Scheduled(m, in.Now):
					return notReady(memoNotReadyScheduled)
				}
				return notReady(memoNotReadyPrerequisites)
			}
			continue
		}
		// A fire by itself leaves a reference (waitNoRefs) or a derived message (waitNoTidy) where
		// it is while the Agent cannot take it; a press claims it and the send asks again.
		if in.Auto && m.RefKey != "" && q.NoRefsAt != "" {
			continue
		}
		if in.Auto && m.Provenance == store.MemoProvenanceCurator && q.NoTidyAt != "" {
			continue
		}
		// A release is bound to the content it showed.
		if m.ReleaseHash != memoContentHash(m) {
			m.State, m.ReleaseHash, m.ReleasedAt, m.ReleasedBy = store.MemoStateHeld, "", "", ""
			res.Holds = append(res.Holds, memoHold{Memo: m, Kind: memoHoldStaleRelease})
			if only != "" {
				return notReady(memoNotReadyHeld)
			}
			continue
		}
		if why := memoRecipeEffectiveProblem(q, m); why != "" {
			res.Holds = append(res.Holds, memoHold{Memo: m, Kind: memoHoldRecipe, Why: why})
			if only != "" {
				return notReady(memoNotReadyHeld)
			}
			continue
		}
		rc := memorecipe.Effective(q, m)
		if len(res.Batch) > 0 && rc != batchRecipe {
			continue
		}
		if why := memoDeliveryItemProblem(m); why != "" {
			if only == "" && (markedPass || startedByMark) {
				unsendable[m.ID] = true
				continue
			}
			res.Batch = nil
			res.Refusal = &apiError{http.StatusConflict, errCodeMemoQueueItemInvalid,
				fmt.Sprintf("message %s cannot be sent to a session: %s", m.ID, why)}
			return res
		}
		if len(res.Batch) == 0 {
			batchRecipe, startedByMark = rc, markedPass
		}
		res.Batch = append(res.Batch, m)
	}
	return res
}
