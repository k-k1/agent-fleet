package muse

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
)

// The two record shapes, trimmed from a real 1.4.0 session.jsonl (the commentary that a
// request_user_input asking "apply the proposal above?" followed).
func commentaryLine(msgID, respID, text string) string {
	return fmt.Sprintf(`{"schema_version":1,"record_type":"event","recorded_at":1791172664466221,"payload_type":"runtime.session","payload":{"event":{"kind":"assistant_message_committed","message_id":%q,"phase":"commentary","provider_item_id":"rs_1","response_id":%q,"text":%q},"kind":"run"}}`+"\n", msgID, respID, text)
}

func toolCallsLine(respID string, callIDs ...string) string {
	var calls []string
	for _, c := range callIDs {
		calls = append(calls, fmt.Sprintf(`{"args":"{}","call_id":%q,"id":"fc_1","name":"request_user_input"}`, c))
	}
	return fmt.Sprintf(`{"schema_version":1,"record_type":"event","payload_type":"runtime.session","payload":{"event":{"kind":"assistant_tool_calls_committed","message_id":"m-tc","response_id":%q,"tool_calls":[%s]},"kind":"run"}}`+"\n", respID, strings.Join(calls, ","))
}

const finalLine = `{"payload":{"event":{"kind":"assistant_message_committed","message_id":"m-final","phase":"final_answer","response_id":"r-final","text":"done"}}}` + "\n"

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func toolCall(id, callID string, rev int64) msp.Item {
	return msp.Item{ItemID: id, Kind: msp.ItemKindToolCall, CallID: strPtr(callID),
		Tool: strPtr("request_user_input"), Status: msp.ItemStatusInProgress, Revision: rev}
}

func TestCommentaryIsHandedOutOnceByTheCallItIntroduced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	appendFile(t, path, `{"payload":{"event":{"kind":"started"}}}`+"\n"+
		commentaryLine("m1", "r1", "the proposal: bug on #1690")+
		toolCallsLine("r1", "call-a", "call-b")+
		finalLine+
		toolCallsLine("r2", "call-c"))
	r := newCommentaryReader(path)

	got, known := r.lookup("call-b")
	if !known || len(got) != 1 || got[0].text != "the proposal: bug on #1690" || got[0].id != "m1" {
		t.Fatalf("lookup(call-b) = %+v, %v", got, known)
	}
	if got, known := r.lookup("call-a"); !known || len(got) != 0 {
		t.Errorf("the sibling call got %+v (known %v), want nothing and known", got, known)
	}
	if got, known := r.lookup("call-c"); !known || len(got) != 0 {
		t.Errorf("a response with no commentary = %+v, %v", got, known)
	}
	if _, known := r.lookup("call-z"); known {
		t.Error("a call the log has not recorded reads as known")
	}
}

// A turn's end drops everything; a resume keeps only a response whose tool calls are not in
// the log yet — the one still being produced.
func TestCommentaryReaderHoldsOnlyWhatIsInFlight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	appendFile(t, path, commentaryLine("m1", "r1", "settled")+toolCallsLine("r1", "call-a")+
		commentaryLine("m2", "r2", "in flight"))
	r := newCommentaryReader(path)
	r.scan()
	r.forgetSettled()
	if len(r.callResp) != 0 || len(r.byResp) != 1 || r.byResp["r2"] == nil {
		t.Fatalf("after forgetSettled: byResp %v callResp %v, want only r2", r.byResp, r.callResp)
	}
	appendFile(t, path, toolCallsLine("r2", "call-b"))
	if got, _ := r.lookup("call-b"); len(got) != 1 || got[0].text != "in flight" {
		t.Fatalf("the in-flight commentary was lost: %+v", got)
	}
	r.forgetTurn()
	if len(r.callResp) != 0 || len(r.byResp) != 0 {
		t.Errorf("after forgetTurn: byResp %v callResp %v", r.byResp, r.callResp)
	}
}

// A log replaced by another file (even a larger one) is read from its start: the old offset
// would skip the new file's commentary.
func TestCommentaryReaderRereadsAReplacedLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	appendFile(t, path, finalLine)
	r := newCommentaryReader(path)
	r.scan()
	next := filepath.Join(dir, "next.jsonl")
	appendFile(t, next, commentaryLine("m1", "r1", "after the swap")+toolCallsLine("r1", "call-a")+finalLine+finalLine)
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.lookup("call-a"); len(got) != 1 || got[0].text != "after the swap" {
		t.Fatalf("lookup after the replacement = %+v", got)
	}
}

// The host may record a call after its item went out. The call waits, a later item asks again,
// and the text lands in front of the call rather than under what followed it.
func TestCommentaryRecordedLateStillLandsAheadOfItsCall(t *testing.T) {
	h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000c3"}
	st := mirrorWith(t, h.slotSid, u1)
	h.path = filepath.Join(t.TempDir(), "session.jsonl")
	line := commentaryLine("m1", "r1", "the proposal")
	appendFile(t, h.path, line[:40])

	h.onItem(toolCall("tc1", "call-a", 1))
	h.onItem(a1)
	wantOrder(t, st, "u1", "tc1", "a1")

	appendFile(t, h.path, line[40:]+toolCallsLine("r1", "call-a"))
	h.onItem(a2)
	wantOrder(t, st, "u1", "m1", "tc1", "a1", "a2")
}

// A turn's end is the last chance: the call still waiting is asked once more.
func TestTurnEndAsksOnceMoreForAWaitingCall(t *testing.T) {
	h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000c4"}
	st := mirrorWith(t, h.slotSid, u1)
	h.path = filepath.Join(t.TempDir(), "session.jsonl")
	h.onItem(toolCall("tc1", "call-a", 1))
	appendFile(t, h.path, commentaryLine("m1", "r1", "the proposal")+toolCallsLine("r1", "call-a"))
	h.finishTurn(msp.TurnCompletedParams{Terminal: msp.TurnTerminalCompleted})
	wantOrder(t, st, "u1", "m1", "tc1")
	if h.pendingCalls != nil {
		t.Errorf("pending calls survived the turn: %v", h.pendingCalls)
	}
}

// A resume attaches the commentary of tool calls the mirror holds without it — a turn the Agent
// missed comes back through the backfill — and a live call the resume lets through ahead of
// its answer finds the log too, because the stored path is set before the resume is sent.
func TestResumeAttachesLoggedCommentary(t *testing.T) {
	h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000c5"}
	st := mirrorWith(t, h.slotSid, u1)
	host := newTestHandle(t, h)
	registerHandle(t, "cm-resume", h)
	logPath := filepath.Join(t.TempDir(), "session.jsonl")
	appendFile(t, logPath, commentaryLine("m1", "r1", "missed turn")+toolCallsLine("r1", "call-a")+
		commentaryLine("m2", "r2", "live turn")+toolCallsLine("r2", "call-b"))
	writeSession(h.slotSid, museSession{ID: "01a0c1d6-0000-7000-8000-0000000000d2", Path: logPath})
	tc1 := toolCall("tc1", "call-a", 2)
	resumeDeliveringLive(t, host, []msp.Item{u1, tc1, a1, u2}, toolCall("tc2", "call-b", 1))
	if err := h.openSession(h.cl, agents.ThreadSettings{Model: "muse-spark-1.3"}); err != nil {
		t.Fatalf("openSession: %v", err)
	}
	wantOrder(t, st, "u1", "m1", "tc1", "a1", "u2", "m2", "tc2")
	if len(h.comm.byResp) != 0 || len(h.comm.callResp) != 0 {
		t.Errorf("the resume kept settled history: byResp %v callResp %v", h.comm.byResp, h.comm.callResp)
	}
}

// The live path: the commentary is written to AF's store as an agentMessage AHEAD of the tool
// call, so the mirror shows the proposal above the question that refers to it.
func TestToolCallItemBringsItsCommentaryIntoTheMirror(t *testing.T) {
	h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000c1"}
	st := mirrorWith(t, h.slotSid, u1)
	h.path = filepath.Join(t.TempDir(), "session.jsonl")
	appendFile(t, h.path, commentaryLine("m1", "r1", "the proposal")+toolCallsLine("r1", "call-a"))

	call := toolCall("tc1", "call-a", 1)
	h.onItem(call)
	done := call
	done.Revision, done.Status = 2, msp.ItemStatusCompleted
	h.onItem(done) // a later revision must not add the text a second time

	wantOrder(t, st, "u1", "m1", "tc1")
	items, err := st.Items()
	if err != nil {
		t.Fatal(err)
	}
	turns := turnsFromItems(items)
	if len(turns) != 2 || len(turns[1].Parts) != 2 ||
		turns[1].Parts[0].Kind != "text" || turns[1].Parts[0].Text != "the proposal" ||
		turns[1].Parts[1].Kind != "tool" {
		t.Fatalf("turns = %+v", turns)
	}
}

// No known path, or a file that is not there: the tool call is written as before.
func TestToolCallWithoutARuntimeLogIsUnchanged(t *testing.T) {
	for _, p := range []string{"", "/nonexistent/session.jsonl"} {
		h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000c2", path: p}
		st := mirrorWith(t, h.slotSid)
		h.onItem(msp.Item{ItemID: "tc1", Kind: msp.ItemKindToolCall, CallID: strPtr("call-a"),
			Status: msp.ItemStatusCompleted, Revision: 1})
		wantOrder(t, st, "tc1")
	}
}
