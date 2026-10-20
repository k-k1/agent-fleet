package sessionx

// The memory review's evidence of a finished turn (memory_review_source.go). The unattended
// hook-less kinds are the case ADR 0117 decision 1 is about: nothing ever calls DriveState or lists
// them, yet the review has to see their turns end - without consuming the notification gate.

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memreview"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

func alwaysAlive(session.Meta) bool { return true }
func neverAlive(session.Meta) bool  { return false }

func TestReviewEvidenceOfAHooklessKindComesFromItsOwnPollAndConsumesNothing(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_review_evidence")

	// Mid-turn: no evidence, and the poll recorded nothing.
	if at := reviewEvidence(m, alwaysAlive); !at.IsZero() {
		t.Fatalf("a turn in flight reported an end at %v", at)
	}
	turnEnded()

	// Nobody lists this session and nobody calls DriveState: the review's own poll finds the end.
	at := reviewEvidence(m, alwaysAlive)
	if at.IsZero() || time.Since(at) > time.Minute {
		t.Fatalf("evidence = %v: the unattended session's end was not found", at)
	}
	// The notification gate is untouched: still "working", no TurnEnd persisted.
	st, ok := status.Read(sid)
	if !ok || st.State != "working" || st.TurnEnd {
		t.Fatalf("the review's poll changed the state machine: %+v", st)
	}
	// Stable across polls.
	if again := reviewEvidence(m, alwaysAlive); !again.Equal(at) {
		t.Fatalf("evidence moved: %v -> %v", at, again)
	}
}

// A session whose process is gone is not polled (its live source says nothing about a turn that
// may still be running); the hook kinds' evidence is whatever their hooks persisted.
func TestReviewEvidenceDoesNotPollADeadHooklessSession(t *testing.T) {
	m, _, turnEnded := polledTurnEndFixture(t, "copilot_review_dead")
	turnEnded()
	if at := reviewEvidence(m, neverAlive); !at.IsZero() {
		t.Fatalf("a dead session's turn end was taken from its live source: %v", at)
	}
}

func TestReviewEvidenceOfAHookKindIsThePersistedEnd(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "claude_review_evidence", Kind: session.KindClaude, Dir: t.TempDir(), CreatedAt: time.Now().Format(time.RFC3339)}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	status.Persist(sid, "working")
	if at := reviewEvidence(m, neverAlive); !at.IsZero() {
		t.Fatalf("working reported an end: %v", at)
	}
	status.PersistTurnEnd(sid, "idle")
	if at := reviewEvidence(m, neverAlive); at.IsZero() {
		t.Fatal("a hook-persisted turn end was not evidence")
	}
	// An idle nobody can explain (no TurnEnd) is not evidence.
	status.Persist(sid, "idle")
	if at := reviewEvidence(m, neverAlive); !at.IsZero() {
		t.Fatalf("an unexplained idle counted as a turn end: %v", at)
	}
}

func TestReviewKeyChangesWhenASessionIsRecreated(t *testing.T) {
	a := session.Meta{Name: "s", Dir: "/d", CreatedAt: "2026-10-18T09:00:00Z"}
	b := a
	b.CreatedAt = "2026-10-18T10:00:00Z"
	if reviewKey(a) == reviewKey(b) {
		t.Fatal("a recreated session would inherit the old watermark")
	}
}

func TestMemoryReviewSourceListsOnlySessionsWithATranscriptThatAreNotArchived(t *testing.T) {
	withTempHome(t)
	now := time.Now().Format(time.RFC3339)
	for _, m := range []session.Meta{
		{Name: "a-claude", Kind: session.KindClaude, Dir: t.TempDir(), CreatedAt: now},
		{Name: "b-shell", Kind: session.KindShell, Dir: t.TempDir(), CreatedAt: now},
		{Name: "c-archived", Kind: session.KindClaude, Dir: t.TempDir(), CreatedAt: now, Archived: true},
	} {
		session.WriteMeta(m)
	}
	var got []memreview.Candidate
	for _, c := range (MemoryReviewSource{}).Candidates() {
		switch c.Name {
		case "a-claude", "b-shell", "c-archived":
			got = append(got, c)
		}
	}
	if len(got) != 1 || got[0].Name != "a-claude" || !got[0].EndedAt.IsZero() {
		t.Fatalf("candidates = %+v", got)
	}
	// Turns of a session that was recreated since the candidate was listed are refused.
	c := got[0]
	m, _ := session.ReadMeta("a-claude")
	m.CreatedAt = "2020-01-01T00:00:00Z"
	session.WriteMeta(m)
	if _, err := (MemoryReviewSource{}).Turns(c); err == nil {
		t.Fatal("turns of a recreated session were returned for the old watermark")
	}
}
