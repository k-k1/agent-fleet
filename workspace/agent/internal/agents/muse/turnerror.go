package muse

import (
	"strconv"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// itemKindTurnError is the kind of the store record AF writes itself for a turn that ended
// with `turn/completed.error`. The host writes no item for a failed turn (measured on Muse
// Code 1.4.20: `session/read` carries the error only as `lastTurn.error`), so without this
// record the mirror shows the failure as nothing at all. It is not a wire kind; the store's
// fold treats it like any item, which is what gives it a stable place in the conversation
// and keeps a repeated or replayed `turn/completed` from adding a second one.
const itemKindTurnError msp.ItemKind = "turnError"

// turnErrorItem is the store record for a failed turn. Its id is derived from the turn id, so
// a repeated completion folds into the same record (same id and revision). A completion with
// no turn id has nothing to dedupe on and gets a unique id instead.
func turnErrorItem(p msp.TurnCompletedParams) msp.Item {
	now := time.Now().UTC()
	id := "turnError:" + p.TurnID
	var turn *string
	if p.TurnID != "" {
		tid := p.TurnID
		turn = &tid
	} else {
		id += strconv.FormatInt(now.UnixNano(), 10)
	}
	kind, msg, at := string(p.Error.Kind), p.Error.Message, now.Format(time.RFC3339Nano)
	return msp.Item{
		ItemID: id, Kind: itemKindTurnError, Revision: 1, Status: msp.ItemStatusFailed,
		TurnID: turn, FailureKind: &kind, Message: &msg, RecordedAt: &at,
	}
}

// turnErrorPart renders the record as the mirror's error block. Cause is "auth" exactly for
// the host's `authRequired` kind, the one failure that signing in again clears; every other
// kind (a usage limit is a `modelError`) carries no recovery guidance.
func turnErrorPart(it msp.Item) transcript.Part {
	kind := str(it.FailureKind)
	label := kind
	if label == "" {
		label = "error"
	}
	p := transcript.Part{Kind: "error", Info: label, Text: transcript.CapOutput(str(it.Message))}
	if kind == museAuthRequired {
		p.Cause = transcript.CauseAuth
	}
	return p
}

// placeTurnErrors moves each turnError record to just after the last item of its own turn.
// First-seen order is wrong for it whenever the turn's items reach the store after the error
// (a backfill of a missed reply, a late revision): the error would sit in the previous reply,
// or ahead of its own turn's text. A record whose turn has no item stays where it was seen.
func placeTurnErrors(order []string, byID map[string]msp.Item) []string {
	isErr := func(id string) bool { return byID[id].Kind == itemKindTurnError }
	lastOf := map[string]string{} // turn id -> id of its last ordinary item
	for _, id := range order {
		if it := byID[id]; !isErr(id) && it.TurnID != nil {
			lastOf[*it.TurnID] = id
		}
	}
	after := map[string][]string{}
	moved := map[string]bool{}
	for _, id := range order {
		if !isErr(id) {
			continue
		}
		if t := byID[id].TurnID; t != nil {
			if anchor, ok := lastOf[*t]; ok {
				after[anchor] = append(after[anchor], id)
				moved[id] = true
			}
		}
	}
	if len(moved) == 0 {
		return order
	}
	out := make([]string, 0, len(order))
	for _, id := range order {
		if moved[id] {
			continue
		}
		out = append(out, id)
		out = append(out, after[id]...)
	}
	return out
}
