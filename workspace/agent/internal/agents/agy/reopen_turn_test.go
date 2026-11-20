package agy

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// A turn agy starts by itself after its interim end was reported (the status record idle) puts
// the record back to "working", so the end of that turn can be notified; a turn an input route
// already opened is left alone, its Rev and turn mark intact.
func TestPreInvocationReopensATurnNoInputAnnounced(t *testing.T) {
	_, sid := hookFixture(t, "slot-reopen")
	recordGen("slot-reopen", "SessionStart", hookInput{ConversationID: "c1"}, time.Now())

	status.Persist(sid, "working")
	status.PersistTurnEnd(sid, "idle")
	if st, _ := status.Read(sid); st.State != "idle" {
		t.Fatalf("control: the interim end should have settled idle, got %+v", st)
	}
	recordGen("slot-reopen", "PreInvocation", hookInput{ConversationID: "c1"}, time.Now())
	st, _ := status.Read(sid)
	if st.State != "working" || st.TurnEnd {
		t.Fatalf("a self-started turn was not reopened: %+v", st)
	}

	// A Stop of that turn captured the reopen's Rev, so it reads as this turn's end.
	recordGen("slot-reopen", "Stop", hookInput{ConversationID: "c1"}, time.Now())
	if rec, _ := currentRecord(sid); rec.StatusRev == nil || *rec.StatusRev != st.Rev {
		t.Fatalf("the Stop did not capture the reopened turn's Rev: %+v", rec)
	}

	rev := status.PersistRev(sid, "working")
	status.RecordTurnMark(sid, rev, "41")
	recordGen("slot-reopen", "PreInvocation", hookInput{ConversationID: "c1"}, time.Now())
	if st, _ := status.Read(sid); st.Rev != rev {
		t.Fatalf("an input route's working write was replaced: rev %q, want %q", st.Rev, rev)
	}
	if mark, ok := status.TurnMarkOf(sid); !ok || mark != "41" {
		t.Fatalf("the input's turn mark was lost: %q ok=%v", mark, ok)
	}
}

// An input route that writes its "working" between the hook's read and its write keeps it: the
// Rev its delivery check withdraws by, and the turn mark EndedAfter pairs the end with.
func TestPreInvocationLosesTheRaceToAnInputRoute(t *testing.T) {
	_, sid := hookFixture(t, "slot-race")
	recordGen("slot-race", "SessionStart", hookInput{ConversationID: "c1"}, time.Now())
	status.Persist(sid, "working")
	status.PersistTurnEnd(sid, "idle")

	var rev string
	prev := reopenGap
	reopenGap = func() {
		rev = status.PersistRev(sid, "working")
		status.RecordTurnMark(sid, rev, "41")
	}
	t.Cleanup(func() { reopenGap = prev })
	recordGen("slot-race", "PreInvocation", hookInput{ConversationID: "c1"}, time.Now())
	if st, _ := status.Read(sid); st.Rev != rev || st.State != "working" {
		t.Fatalf("the hook overwrote the input route's write: %+v, want rev %q", st, rev)
	}
	if mark, ok := status.TurnMarkOf(sid); !ok || mark != "41" {
		t.Fatalf("the input's turn mark was lost: %q ok=%v", mark, ok)
	}
}
