package main

// (ref, idx) deduplication for the ledger (docs/log/46 §3-b / ADR0029 §5).
//
// Idempotency of the fold rests on the watermark (`usage/state.json`), but a crash right after
// the rows are appended and before the watermark is written makes the next pass append a few
// turns of that session a second time. commitSessionUsageFold narrowed the window to one
// session, but the window cannot be removed: the append target (raw jsonl) and the watermark
// (state.json) are separate files and cannot be written atomically. Since the writer cannot
// close it completely, the reader drops the duplicates.
//
// The shape of the ledger supplies the way to drop them. On feature=session rows, (ref, idx)
// identifies a logical turn uniquely (idx is the running number of the logical turn within the
// transcript, ADR0029 §5), so it is enough never to count the same (ref, idx) twice.
//
// What is kept is one entry per ref (the largest idx counted and the largest ts observed), not
// the set of (ref, idx) itself: holding a set of 158 sessions × thousands of turns forever
// would break the rollup's "small" premise. The set is unnecessary because appends only ever
// take these shapes:
//
//   - idx is appended per session, monotonically increasing from 1 (foldTurnRows numbers from
//     the head of the transcript and writes only what follows the watermark).
//   - A duplicate always appears as "the already-appended tail, again, later".
//
// So "at or below the largest idx already counted for that ref" is a sufficient condition for a
// duplicate. It also absorbs the degenerate case where the watermark is lost (state.json gone
// or corrupted) and everything is redone from idx 1.
//
// The ts condition is insurance against slug reuse. A session name is a 30-bit random slug and
// is collision-checked only against live metadata (session_name.go), so a deleted session's
// name can eventually be handed out again. idx then restarts at 1, and judging on idx alone
// would drop the new session's usage as a duplicate — a silent undercount, which is worse than
// a duplicate. A new incarnation's usage is always later than the old one's, so a row is
// dropped only when idx is at or below the counted one AND ts is at or below the observed one.
// Doubtful cases fall on the side of keeping the duplicate: a duplicate can be found in raw,
// but dropped usage never comes back.
//
// The key is the first half of the SHA-256 of ref. The rollup is a region that lives forever,
// and refs (session name / conversation id) are exactly what ADR0029 §8 decided not to keep
// there in clear text. Deduplication needs only equality, so a hash is enough (to audit, run
// the ref you want to match through the same function).

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
	"time"
)

// usageDedupMark is the high-water mark counted for one ref.
type usageDedupMark struct {
	Idx int   `json:"idx"` // largest logical turn number counted
	TS  int64 `json:"ts"`  // largest usage time observed (Unix seconds)
}

// usageDedupIndex maps a ref hash to its high-water mark. It rides in the rollup state.json and
// stays there indefinitely, so duplicates can still be dropped after raw has been pruned.
// 1 ref ≈ 40B.
type usageDedupIndex map[string]usageDedupMark

// usageRefKey maps a ref to its index key. The hash keeps plain refs out of the indefinite
// region; concealment is not the goal (16 hex = 64 bits, collisions effectively never happen).
func usageRefKey(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(sum[:8])
}

// accept reports whether a row may be counted. false means (ref, idx) is a duplicate and the
// row is dropped from the aggregate. ts is the row's usage time (the result of usageRowTime),
// taken as a parameter because the caller has already resolved it.
//
// Call this in append order and before narrowing by period. If which row counts as "the first
// one" changes with the query period, the totals move when only the period changed (the same
// day's number would wobble depending on the range asked for).
func (d usageDedupIndex) accept(r usagex.Record, ts time.Time) bool {
	// Auxiliary calls carry no idx: one call writes one row on the spot, so there is no path
	// on which a duplicate can arise.
	if r.Feature != usagex.FeatureSession || r.Ref == "" || r.Idx <= 0 {
		return true
	}
	k := usageRefKey(r.Ref)
	m, seen := d[k]
	sec := ts.Unix()
	if seen && r.Idx <= m.Idx && sec <= m.TS {
		return false
	}
	if !seen || r.Idx > m.Idx {
		m.Idx = r.Idx
	}
	if !seen || sec > m.TS {
		m.TS = sec
	}
	d[k] = m
	return true
}

// clone copies the index. The read path (collectUsageSamples) starts from the marks of what has
// already been folded, but marks advanced during that scan must not be carried back into the
// rollup's state.
func (d usageDedupIndex) clone() usageDedupIndex {
	out := make(usageDedupIndex, len(d))
	for k, v := range d {
		out[k] = v
	}
	return out
}
