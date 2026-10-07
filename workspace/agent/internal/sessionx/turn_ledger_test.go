package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// The chat/mirror chip's reverse-heal (DriveState) follows the same ledger as the list's.
func TestDriveStateStopHookSpinnerDoesNotReopenClosedTurn(t *testing.T) {
	isolateAgentState(t)
	old := readPane
	readPane = func(string) tmuxx.PaneRead { return tmuxx.PaneRead{OK: true, Busy: true} }
	t.Cleanup(func() { readPane = old })
	m := session.Meta{Dir: t.TempDir(), Name: "ledger-drive", Kind: session.KindClaude}
	sid := session.UUID(m.Dir, m.Name)

	status.PersistOpen(sid, "working", "P1")
	status.PersistTurnEndFor(sid, "idle", "", "P1")
	if got := DriveState(m, true, true); got != "idle" {
		t.Fatalf("busy pane after Stop: got %q, want idle", got)
	}
	if st, _ := status.Read(sid); st.State != "idle" {
		t.Fatalf("working was persisted: %+v", st)
	}

	status.PersistOpen(sid, "working", "P2")
	if got := DriveState(m, true, true); got != "working" {
		t.Fatalf("new prompt: got %q, want working", got)
	}

	status.Remove(sid)
	status.Persist(sid, "idle")
	if got := DriveState(m, true, true); got != "working" {
		t.Fatalf("no ledger + busy pane: got %q, want working", got)
	}
}

// The hooks are what fill the ledger: a working hook opens its turn, the Stop hook closes it —
// by its own prompt_id, or by the one the opening hooks recorded when it carries none.
func TestHooksWriteTheTurnLedger(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "sess-ledger"
	feedStatusHook(t, "working", `{"session_id":"`+sid+`","hook_event_name":"UserPromptSubmit","prompt_id":"p1"}`)
	if st, _ := status.Read(sid); st.State != "working" || st.PromptID != "p1" || st.TurnEnd {
		t.Fatalf("after UserPromptSubmit: %+v", st)
	}
	feedStatusHook(t, "working", `{"session_id":"`+sid+`","tool_name":"Bash","prompt_id":"px","agent_id":"a1"}`)
	if st, _ := status.Read(sid); st.PromptID != "p1" {
		t.Fatalf("a subagent's hook moved the ledger: %+v", st)
	}
	feedStatusHook(t, "idle", `{"session_id":"`+sid+`","hook_event_name":"Stop","prompt_id":"p1"}`)
	if st, _ := status.Read(sid); st.State != "idle" || !st.TurnEnd || st.PromptID != "p1" {
		t.Fatalf("after Stop: %+v", st)
	}
	feedStatusHook(t, "working", `{"session_id":"`+sid+`","hook_event_name":"UserPromptSubmit","prompt_id":"p2"}`)
	feedStatusHook(t, "idle", `{"session_id":"`+sid+`","hook_event_name":"Stop"}`)
	if st, _ := status.Read(sid); st.PromptID != "p2" || !st.TurnEnd {
		t.Fatalf("a Stop without prompt_id must close the recorded turn: %+v", st)
	}
}
