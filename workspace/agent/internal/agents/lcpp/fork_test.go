package lcpp

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
)

// seedTwoExchanges writes two complete user/assistant round trips to sid's store and returns
// the record IDs in append order, for tests to pick anchors from.
func seedTwoExchanges(t *testing.T, sid string) []string {
	t.Helper()
	st := Open(sid)
	var ids []string
	rec, err := st.AppendUser("first question")
	if err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	ids = append(ids, rec.ID)
	rec, err = st.AppendMessage(harness.Message{Role: harness.RoleAssistant, Content: "first answer"})
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	ids = append(ids, rec.ID)
	rec, err = st.AppendUser("second question")
	if err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	ids = append(ids, rec.ID)
	rec, err = st.AppendMessage(harness.Message{Role: harness.RoleAssistant, Content: "second answer"})
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	ids = append(ids, rec.ID)
	return ids
}

// TestForkWholeConversation is the positive control for a plain (non-point) fork: ForkSource
// names the source sid, ensureForked (via Resume with ForkFrom set and ForkAt empty) copies
// every record.
func TestForkWholeConversation(t *testing.T) {
	testHome(t)
	src := testMeta(t, "sess-fork-src")
	sid := sidFor(src)
	seedTwoExchanges(t, sid)

	ag := agentImpl{}
	forkFrom, err := ag.ForkSource(src)
	if err != nil {
		t.Fatalf("ForkSource: %v", err)
	}
	if forkFrom != sid {
		t.Fatalf("ForkSource = %q, want %q", forkFrom, sid)
	}

	dst := testMeta(t, "sess-fork-dst")
	dst.ForkFrom = forkFrom
	d := NewDriver()
	if _, err := d.Resume(dst); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	recs, _, err := Open(sidFor(dst)).Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 4 {
		t.Fatalf("forked record count = %d, want 4: %+v", len(recs), recs)
	}
}

// TestForkAtExcludesClickedTurnByDefault is the positive control for a point fork
// (Include=false): the new session must have the history up to, but NOT including, the
// clicked user turn.
func TestForkAtExcludesClickedTurnByDefault(t *testing.T) {
	testHome(t)
	src := testMeta(t, "sess-forkat-src")
	sid := sidFor(src)
	ids := seedTwoExchanges(t, sid)
	secondQuestionID := ids[2]

	ag := agentImpl{}
	anchor, err := ag.ResolveForkAt(src, agents.ForkPoint{Anchor: secondQuestionID, Include: false})
	if err != nil {
		t.Fatalf("ResolveForkAt: %v", err)
	}
	if anchor != ids[1] {
		t.Fatalf("anchor = %q, want the record right before the clicked turn (%q)", anchor, ids[1])
	}

	dst := testMeta(t, "sess-forkat-dst")
	dst.ForkFrom = sid
	dst.ForkAt = anchor
	d := NewDriver()
	if _, err := d.Resume(dst); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	recs, _, _ := Open(sidFor(dst)).Records()
	if len(recs) != 2 || recs[len(recs)-1].Content != "first answer" {
		t.Fatalf("forked records = %+v, want exactly the first exchange", recs)
	}
}

// TestForkAtIncludesClickedTurnAndReply is the positive control for Include=true: the clicked
// turn and the reply it got must be kept.
func TestForkAtIncludesClickedTurnAndReply(t *testing.T) {
	testHome(t)
	src := testMeta(t, "sess-forkat-inc-src")
	sid := sidFor(src)
	ids := seedTwoExchanges(t, sid)
	firstQuestionID := ids[0]

	ag := agentImpl{}
	anchor, err := ag.ResolveForkAt(src, agents.ForkPoint{Anchor: firstQuestionID, Include: true})
	if err != nil {
		t.Fatalf("ResolveForkAt: %v", err)
	}
	if anchor != ids[1] {
		t.Fatalf("anchor = %q, want the first answer's own id (%q)", anchor, ids[1])
	}
}

// TestForkAtLastExchangeIncludeIsWholeConversation is the edge case ForkPoint's own doc
// comment calls out: Include on the LAST exchange resolves to "" (whole conversation).
func TestForkAtLastExchangeIncludeIsWholeConversation(t *testing.T) {
	testHome(t)
	src := testMeta(t, "sess-forkat-last")
	sid := sidFor(src)
	ids := seedTwoExchanges(t, sid)
	secondQuestionID := ids[2]

	ag := agentImpl{}
	anchor, err := ag.ResolveForkAt(src, agents.ForkPoint{Anchor: secondQuestionID, Include: true})
	if err != nil {
		t.Fatalf("ResolveForkAt: %v", err)
	}
	if anchor != "" {
		t.Fatalf("anchor = %q, want empty (whole conversation)", anchor)
	}
}

// TestForkAtBeforeFirstTurnRefuses is the negative control for the one case ResolveForkAt
// cannot express as a real fork target (excluding everything, including the first turn).
func TestForkAtBeforeFirstTurnRefuses(t *testing.T) {
	testHome(t)
	src := testMeta(t, "sess-forkat-empty")
	sid := sidFor(src)
	ids := seedTwoExchanges(t, sid)

	ag := agentImpl{}
	if _, err := ag.ResolveForkAt(src, agents.ForkPoint{Anchor: ids[0], Include: false}); err == nil {
		t.Fatal("ResolveForkAt succeeded excluding the very first turn, want an error")
	}
}

// TestForkSourceRefusesEmptyConversation is the negative control for ForkSource: a session
// with no turns yet cannot be forked.
func TestForkSourceRefusesEmptyConversation(t *testing.T) {
	testHome(t)
	m := testMeta(t, "sess-fork-empty")
	ag := agentImpl{}
	if _, err := ag.ForkSource(m); err == nil {
		t.Fatal("ForkSource succeeded on an empty conversation, want an error")
	}
}

// TestForkDoesNotDoubleCopyOnSecondResume guards ensureForked's own idempotency claim: Resume
// is called twice for the same forked slot (matching how handleManagedTurn re-Resumes on
// every /turn), and the destination store must not be re-copied/duplicated.
func TestForkDoesNotDoubleCopyOnSecondResume(t *testing.T) {
	testHome(t)
	src := testMeta(t, "sess-fork-twice-src")
	sid := sidFor(src)
	seedTwoExchanges(t, sid)

	dst := testMeta(t, "sess-fork-twice-dst")
	dst.ForkFrom = sid
	d := NewDriver()
	if _, err := d.Resume(dst); err != nil {
		t.Fatalf("Resume 1: %v", err)
	}
	if _, err := d.Resume(dst); err != nil {
		t.Fatalf("Resume 2: %v", err)
	}
	recs, _, _ := Open(sidFor(dst)).Records()
	if len(recs) != 4 {
		t.Fatalf("record count after two Resumes = %d, want 4 (no duplication)", len(recs))
	}
}

// A fork cut before a model switch starts with a bookkeeping model-change note (ForkAt). Forking
// that copy "before" its first user turn must still be refused, not produce a conversation made
// of the note alone.
func TestForkAtBeforeFirstTurnIgnoresLeadingModelNote(t *testing.T) {
	testHome(t)
	src := testMeta(t, "sess-forknote-src")
	ids := seedTwoExchanges(t, sidFor(src))
	if _, err := Open(sidFor(src)).AppendModelChangeNote("model-b"); err != nil {
		t.Fatal(err)
	}
	copyMeta := testMeta(t, "sess-forknote-copy")
	cp, err := Open(sidFor(src)).ForkAt(sidFor(copyMeta), ids[1])
	if err != nil {
		t.Fatal(err)
	}
	recs, _, _ := cp.Records()
	if len(recs) != 3 || recs[0].Note != NoteModelChange {
		t.Fatalf("premise: copy should start with the model note, got %+v", recs)
	}
	if _, err := (agentImpl{}).ResolveForkAt(copyMeta, agents.ForkPoint{Anchor: recs[1].ID}); err == nil {
		t.Fatal("forking before the copy's first user turn was accepted")
	}
}
