package muse

import (
	"log"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
)

// The mirror (transcript.go) is written from the live item stream, so a turn that ran while
// the Agent was not listening — it died mid-turn, and the host finished the turn without it —
// is missing from it although the host still has it. A resume is the first moment AF can ask,
// so it backfills there from the host's own fold.

// historyItems returns the folded items a SessionHistory carries, and whether it carried any
// answer at all. `inline` is the item array itself; the two snapshot modes carry the same
// items inside the snapshot state; `none` (a cursor resume, excludeItems, or a history over the
// host's budget) carries nothing.
func historyItems(hist msp.SessionHistory) ([]msp.Item, bool) {
	switch hist.Mode {
	case msp.HistoryModeInline:
		return hist.Items, true
	case msp.HistoryModeSnapshot, msp.HistoryModeAnchoredSnapshot:
		if hist.Snapshot != nil {
			return hist.Snapshot.State.Items, true
		}
	}
	return nil, false
}

// resumeHistory is the host's folded history as of the resume. session/resume already carries
// it (excludeItems defaults to false), so the common case costs no second round trip; only a
// resume that served no items asks session/read, the protocol's point-in-time read of the same
// fold. ok=false means neither answered: the caller then knows nothing about the history, which
// is not the same as an empty one. Never fatal — the session is resumed either way.
func (h *threadHandle) resumeHistory(cl *msp.Client, sid string, hist msp.SessionHistory) ([]msp.Item, bool) {
	if items, ok := historyItems(hist); ok {
		return items, true
	}
	exclude := false
	var res msp.SessionReadResult
	if err := cl.CallInto(msp.MethodSessionRead, msp.SessionReadParams{SessionID: sid, ExcludeItems: &exclude}, callTimeout, &res); err != nil {
		log.Printf("muse: %s: session/read for the resume backfill: %v", h.name, err)
		return nil, false
	}
	return historyItems(res.History)
}

// backfillMirror appends every item of the host's history the mirror lacks: an item it never
// saw, or a later revision than the one it holds (a tool call whose completion arrived while
// nobody was listening). The store folds by revision on read, so an appended revision simply
// wins, and a live revision racing this one cannot be undone by it. A missing item lands after
// what the mirror already has, which is where a turn the Agent missed belongs; one missing from
// the middle of what it saw would land late, which only a lost notification can cause.
//
// Failure is logged and dropped, the rule every mirror write follows: the host owns the
// conversation, and a gap in AF's copy is no reason to refuse the session.
func (h *threadHandle) backfillMirror(items []msp.Item) {
	n, err := openStore(h.slotSid).backfill(items)
	if err != nil {
		log.Printf("muse: %s: transcript backfill: %v", h.name, err)
	}
	if n > 0 {
		log.Printf("muse: %s: transcript backfill: %d item revision(s) the mirror lacked", h.name, n)
	}
}

// backfill appends the items the store lacks (see backfillMirror) and returns how many.
func (s *store) backfill(items []msp.Item) (int, error) {
	have, _, err := s.itemsWithMeta()
	if err != nil {
		return 0, err
	}
	rev := make(map[string]int64, len(have))
	for _, it := range have {
		rev[it.ItemID] = it.Revision
	}
	n := 0
	for _, it := range items {
		if it.ItemID == "" {
			continue
		}
		if r, ok := rev[it.ItemID]; ok && r >= it.Revision {
			continue
		}
		if err := s.appendRecord(record{Item: it}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
