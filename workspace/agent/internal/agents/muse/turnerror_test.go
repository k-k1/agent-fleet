package muse

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func turnItem(id, turn string, kind msp.ItemKind, text string) msp.Item {
	return msp.Item{ItemID: id, Kind: kind, Revision: 1, Status: msp.ItemStatusCompleted, TurnID: &turn, Text: &text}
}

// errorParts returns the error parts of every assistant turn the store renders.
func errorParts(t *testing.T, h *threadHandle) []transcript.Part {
	t.Helper()
	items, err := openStore(h.slotSid).Items()
	if err != nil {
		t.Fatal(err)
	}
	var out []transcript.Part
	for _, tr := range turnsFromItems(items) {
		if tr.Role != "assistant" {
			continue
		}
		for _, p := range tr.Parts {
			if p.Kind == "error" {
				out = append(out, p)
			}
		}
	}
	return out
}

// finish drives one turn that ends with the given terminal and error.
func finish(t *testing.T, h *threadHandle, host interface {
	Notify(string, any)
}, turn string, terminal msp.TurnTerminal, e *msp.TurnError, want agents.TurnState) {
	t.Helper()
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{TurnID: turn, SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)
	host.Notify(msp.NotificationTurnCompleted, msp.TurnCompletedParams{TurnID: turn, SessionID: h.sid, Terminal: terminal, Error: e})
	waitEvent(t, h, want)
}

func TestFailedTurnWritesAnErrorPart(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       *msp.TurnError
		terminal  msp.TurnTerminal
		state     agents.TurnState
		wantParts int
		wantCause string
		wantText  string
	}{
		{"auth", &msp.TurnError{Kind: "authRequired", Message: "not logged in: run /login to add an API key"},
			msp.TurnTerminalFailed, agents.TurnAborted, 1, transcript.CauseAuth, "not logged in: run /login to add an API key"},
		{"model error", &msp.TurnError{Kind: "modelError", Message: "model `nope-model` does not exist or you lack access [request_id=r1]"},
			msp.TurnTerminalFailed, agents.TurnFailed, 1, "", "model `nope-model` does not exist or you lack access [request_id=r1]"},
		{"usage limit still shows the error", &msp.TurnError{Kind: "modelError", Message: measuredQuotaMessage},
			msp.TurnTerminalFailed, agents.TurnFailed, 1, "", measuredQuotaMessage},
		{"completed", nil, msp.TurnTerminalCompleted, agents.TurnCompleted, 0, "", ""},
		{"cancelled", &msp.TurnError{Kind: "modelError", Message: "x"}, msp.TurnTerminalCancelled, agents.TurnCancelled, 0, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			h := &threadHandle{}
			host := newTestHandle(t, h)
			registerHandle(t, h.name, h)
			st := openStore(h.slotSid)
			if err := st.Append(turnItem("u1", "t-1", msp.ItemKindUserMessage, "hello")); err != nil {
				t.Fatal(err)
			}
			finish(t, h, host, "t-1", tc.terminal, tc.err, tc.state)
			ps := errorParts(t, h)
			if len(ps) != tc.wantParts {
				t.Fatalf("error parts = %+v, want %d", ps, tc.wantParts)
			}
			if tc.wantParts == 0 {
				return
			}
			if ps[0].Cause != tc.wantCause || ps[0].Text != tc.wantText || ps[0].Info != string(tc.err.Kind) {
				t.Errorf("part = %+v, want cause %q text %q info %q", ps[0], tc.wantCause, tc.wantText, tc.err.Kind)
			}
			if tc.name == "usage limit still shows the error" && !IsRateLimited(h.name) {
				t.Error("the rate-limit badge was lost")
			}
		})
	}
}

// The error rides in the assistant turn the failure ended: after the answer it had so far,
// before the next user message; and a replayed completion, a restart's backfill of the host's
// history and repeated reads add no second block.
func TestTurnErrorPartIsPlacedOnceAcrossReplayAndBackfill(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := &threadHandle{}
	host := newTestHandle(t, h)
	registerHandle(t, h.name, h)
	st := openStore(h.slotSid)
	u1, a1 := turnItem("u1", "t-1", msp.ItemKindUserMessage, "hi"), turnItem("a1", "t-1", msp.ItemKindAgentMessage, "partial")
	u2 := turnItem("u2", "t-2", msp.ItemKindUserMessage, "again")
	for _, it := range []msp.Item{u1, a1} {
		if err := st.Append(it); err != nil {
			t.Fatal(err)
		}
	}
	e := &msp.TurnError{Kind: "modelError", Message: "API error 500"}
	finish(t, h, host, "t-1", msp.TurnTerminalFailed, e, agents.TurnFailed)
	if err := st.Append(u2); err != nil {
		t.Fatal(err)
	}
	// A replayed completion of the same turn.
	host.Notify(msp.NotificationTurnCompleted, msp.TurnCompletedParams{TurnID: "t-1", SessionID: h.sid, Terminal: msp.TurnTerminalFailed, Error: e})
	// Notifications are handled in order, so a later one that has been seen means the replay
	// above has been too.
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{TurnID: "t-2", SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)
	// The host's history carries no error item; backfilling it must not drop or repeat ours.
	if _, err := st.backfill([]msp.Item{u1, a1, u2}); err != nil {
		t.Fatal(err)
	}
	for range 2 { // repeated reads
		items, _ := st.Items()
		turns := turnsFromItems(items)
		if len(turns) != 3 || turns[1].Role != "assistant" || turns[2].Role != "user" {
			t.Fatalf("turns = %+v", turns)
		}
		var kinds []string
		for _, p := range turns[1].Parts {
			kinds = append(kinds, p.Kind)
		}
		if len(kinds) != 2 || kinds[0] != "text" || kinds[1] != "error" {
			t.Fatalf("assistant parts = %v, want [text error]", kinds)
		}
	}
}

// The error follows the last item of its own turn even when that turn's items reach the store
// after it (history the mirror missed, backfilled later), never the previous reply.
func TestTurnErrorFollowsItsOwnTurnAfterBackfill(t *testing.T) {
	for _, tc := range []struct {
		name string
		have []msp.Item // already in the store, with the error after them
		host []msp.Item
	}{
		{"whole turn missed", nil, nil},
		{"user message only", []msp.Item{turnItem("u2", "t-2", msp.ItemKindUserMessage, "again")}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000e1"}
			st := openStore(h.slotSid)
			u1, a1 := turnItem("u1", "t-1", msp.ItemKindUserMessage, "hi"), turnItem("a1", "t-1", msp.ItemKindAgentMessage, "one")
			u2, a2 := turnItem("u2", "t-2", msp.ItemKindUserMessage, "again"), turnItem("a2", "t-2", msp.ItemKindAgentMessage, "two")
			for _, it := range []msp.Item{u1, a1} {
				_ = st.Append(it)
			}
			_ = st.Append(turnErrorItem(msp.TurnCompletedParams{TurnID: "t-2", Error: &msp.TurnError{Kind: "modelError", Message: "boom"}}))
			for _, it := range tc.have {
				_ = st.Append(it)
			}
			if _, err := st.backfill([]msp.Item{u1, a1, u2, a2}); err != nil {
				t.Fatal(err)
			}
			items, _ := st.Items()
			var ids []string
			for _, it := range items {
				ids = append(ids, it.ItemID)
			}
			want := []string{"u1", "a1", "u2", "a2", "turnError:t-2"}
			if len(ids) != len(want) {
				t.Fatalf("order = %v, want %v", ids, want)
			}
			for i := range want {
				if ids[i] != want[i] {
					t.Fatalf("order = %v, want %v", ids, want)
				}
			}
			// A fork keeps it with its turn.
			if err := st.ForkAt("00000000-0000-5000-8000-0000000000e2", ""); err != nil {
				t.Fatal(err)
			}
			fi, _ := openStore("00000000-0000-5000-8000-0000000000e2").Items()
			if len(fi) != 5 || fi[4].ItemID != "turnError:t-2" {
				t.Errorf("fork items = %d, last %q", len(fi), fi[len(fi)-1].ItemID)
			}
		})
	}
}

// A completion for a turn the handle no longer tracks still records its failure.
func TestStaleFailedCompletionStillRecordsItsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := &threadHandle{running: true, turnID: "t-new"}
	newTestHandle(t, h)
	registerHandle(t, h.name, h)
	_ = openStore(h.slotSid).Append(turnItem("u-old", "t-old", msp.ItemKindUserMessage, "hi"))
	h.finishTurn(msp.TurnCompletedParams{TurnID: "t-old", Terminal: msp.TurnTerminalFailed, Error: &msp.TurnError{Kind: "modelError", Message: "late"}})
	if ps := errorParts(t, h); len(ps) != 1 || ps[0].Text != "late" {
		t.Fatalf("error parts = %+v", ps)
	}
	if !h.running || h.turnID != "t-new" {
		t.Error("the stale completion ended the newer turn")
	}
}
