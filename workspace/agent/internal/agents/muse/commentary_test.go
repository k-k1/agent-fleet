package muse

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestCommentaryIsTakenOnceByTheCallItIntroduced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	appendFile(t, path, `{"payload":{"event":{"kind":"started"}}}`+"\n"+
		commentaryLine("m1", "r1", "the proposal: bug on #1690")+
		toolCallsLine("r1", "call-a", "call-b")+
		finalLine+
		toolCallsLine("r2", "call-c"))
	r := newCommentaryReader(path)

	got := r.take("call-b")
	if len(got) != 1 || got[0].text != "the proposal: bug on #1690" || got[0].id != "m1" {
		t.Fatalf("take(call-b) = %+v", got)
	}
	if got := r.take("call-a"); len(got) != 0 {
		t.Errorf("the same commentary was handed out twice: %+v", got)
	}
	if got := r.take("call-c"); len(got) != 0 {
		t.Errorf("a response with no commentary returned some: %+v", got)
	}
	if len(r.byResp) != 0 || len(r.byCall) != 0 {
		t.Errorf("nothing in flight, but the reader still holds %v / %v", r.byResp, r.byCall)
	}
}

// The host appends while AF reads: a line with no newline yet is not a record, and must be
// read whole once the rest arrives rather than lost or decoded in halves.
func TestCommentaryReaderWaitsForAWholeLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	line := commentaryLine("m1", "r1", "half then whole")
	appendFile(t, path, line[:40])
	r := newCommentaryReader(path)
	if got := r.take("call-a"); len(got) != 0 {
		t.Fatalf("take before the line was complete = %+v", got)
	}
	appendFile(t, path, line[40:]+toolCallsLine("r1", "call-a"))
	if got := r.take("call-a"); len(got) != 1 || got[0].text != "half then whole" {
		t.Fatalf("take after the line completed = %+v", got)
	}
}

// The live path: the commentary is written to AF's store as an agentMessage AHEAD of the tool
// call, so the mirror shows the proposal above the question that refers to it.
func TestToolCallItemBringsItsCommentaryIntoTheMirror(t *testing.T) {
	h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000c1"}
	st := mirrorWith(t, h.slotSid, u1)
	h.path = filepath.Join(t.TempDir(), "session.jsonl")
	appendFile(t, h.path, commentaryLine("m1", "r1", "the proposal")+toolCallsLine("r1", "call-a"))

	call := msp.Item{ItemID: "tc1", Kind: msp.ItemKindToolCall, CallID: strPtr("call-a"),
		Tool: strPtr("request_user_input"), Status: msp.ItemStatusInProgress, Revision: 1}
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
