package sessionx

// The memory review's view of the sessions (ADR 0117 decision 1): which ones have a transcript, and
// when each last finished a turn. The loop itself lives in internal/memreview; this is the part
// that needs the status store and the kinds' live sources.

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memreview"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// MemoryReviewSource is the memreview.Source of the Agent.
type MemoryReviewSource struct{}

// reviewKey identifies a session for the review's watermark: the status id and when the session
// was created, so a session recreated under the same name and directory starts again at turn 1
// instead of inheriting the old one's watermark.
func reviewKey(m session.Meta) string { return session.UUID(m.Dir, m.Name) + "@" + m.CreatedAt }

// Candidates lists every session with a transcript that is not archived, with its turn-end
// evidence.
func (MemoryReviewSource) Candidates() []memreview.Candidate {
	var out []memreview.Candidate
	for _, m := range session.ListMetas() {
		if m.Archived || !AgentOf(m.Kind).Caps().CanTranscript {
			continue
		}
		out = append(out, memreview.Candidate{Key: reviewKey(m), Name: m.Name, EndedAt: reviewEvidence(m, SessionAlive)})
	}
	return out
}

// Turns reads the whole conversation of the session the candidate names.
func (MemoryReviewSource) Turns(c memreview.Candidate) ([]transcript.Turn, error) {
	m, ok := session.ReadMeta(c.Name)
	if !ok || reviewKey(m) != c.Key {
		return nil, errReviewSessionGone
	}
	return exportTurns(m), nil
}

type reviewErr string

func (e reviewErr) Error() string { return string(e) }

const errReviewSessionGone = reviewErr("the session is gone or was recreated")

// reviewEvidence is when the session's newest turn really ended, zero when its status does not
// show one (a turn in flight, or an idle nobody can explain).
//
// The status record's TurnEnd is not enough on its own: for agy, copilot, cursor and kiro nothing
// reports a turn's end, so the record keeps saying "working" until somebody polls. The sessions
// list does that for every session a Console lists; an unattended session nobody opens would
// otherwise never look finished. So for those four kinds this polls the kind's live source itself
// and records WHEN the turn ended in the observation store (status.RecordTurnEnd), which is bound
// to the record's revision. It never persists an idle: that would consume the gate the completion
// notification and the operator's report use (turn_end_poll.go).
func reviewEvidence(m session.Meta, alive func(session.Meta) bool) time.Time {
	sid := session.UUID(m.Dir, m.Name)
	switch NormalizeKind(m.Kind) {
	case session.KindAgy, session.KindCopilot, session.KindCursor, session.KindKiro:
		// The cheap status-store gate first: a live source is a SQLite query or a file tail.
		if status.TurnEndUnrecorded(sid) && alive(m) {
			recordPolledTurnEnd(m, AgentOf(m.Kind).WireLive(m, true).State)
		}
	}
	at, err := time.Parse(time.RFC3339, lastTurnEndAt(m))
	if err != nil {
		return time.Time{}
	}
	return at
}
