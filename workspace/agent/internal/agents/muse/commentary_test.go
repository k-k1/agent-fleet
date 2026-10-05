package muse

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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

func streamLine(stream, line string) string {
	return strings.Replace(line, `{"schema_version":1,`, fmt.Sprintf(`{"schema_version":1,"stream":{"kind":"session","id":%q},`, stream), 1)
}

func TestCommentaryIsHandedOutOnceByTheResponseItIntroduced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	appendFile(t, path, `{"payload":{"event":{"kind":"started"}}}`+"\n"+
		commentaryLine("m1", "r1", "the proposal: bug on #1690")+
		toolCallsLine("r1", "call-a", "call-b")+
		finalLine+
		toolCallsLine("r2", "call-c"))
	cs := commentaryFor(path)

	if got := cs.take("call-b"); len(got) != 1 || got[0].text != "the proposal: bug on #1690" || got[0].id != "m1" {
		t.Fatalf("take(call-b) = %+v", got)
	}
	if got := cs.take("call-a"); len(got) != 0 {
		t.Errorf("the sibling call got the same commentary again: %+v", got)
	}
	if got := cs.take("call-c"); len(got) != 0 {
		t.Errorf("a response with no commentary returned some: %+v", got)
	}
	// Each read is its own set: a second transcript gets the text again.
	if got := commentaryFor(path).take("call-a"); len(got) != 1 {
		t.Errorf("a fresh read lost the commentary: %+v", got)
	}
}

// A response that never logged tool calls (interrupted, or answered without tools) is not held
// once another response speaks, and subagent records interleaved under their own stream do not
// cut the main stream's response off from its calls.
func TestCommentaryHoldsOnlyTheResponseStillBeingProduced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	appendFile(t, path, streamLine("main", commentaryLine("m0", "r0", "interrupted"))+
		streamLine("main", commentaryLine("m1", "r1", "main says"))+
		streamLine("child", commentaryLine("m9", "r9", "child says"))+
		streamLine("main", toolCallsLine("r1", "call-a")))
	commentaryFor(path)
	commentaryLogsMu.Lock()
	l := commentaryLogs[path]
	commentaryLogsMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.byResp) != 1 || l.byResp["r1"] == nil || l.byResp["r1"][0].text != "main says" {
		t.Errorf("byResp = %v, want only r1", l.byResp)
	}
	if len(l.pending) != 1 || l.pending["child"].resp != "r9" {
		t.Errorf("pending = %v, want only the child's r9", l.pending)
	}
}

// A log replaced by another file (even a larger one) is read from its start: the old offset
// would skip the new file's commentary.
func TestCommentaryReaderRereadsAReplacedLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	appendFile(t, path, finalLine)
	commentaryFor(path)
	next := filepath.Join(dir, "next.jsonl")
	appendFile(t, next, commentaryLine("m1", "r1", "after the swap")+toolCallsLine("r1", "call-a")+finalLine+finalLine)
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	if got := commentaryFor(path).take("call-a"); len(got) != 1 || got[0].text != "after the swap" {
		t.Fatalf("take after the replacement = %+v", got)
	}
}

// museSlot is a stopped muse session (no live handle) whose store holds items and whose
// recorded log is at the returned path.
func museSlot(t *testing.T, items ...msp.Item) (session.Meta, string) {
	t.Helper()
	m := metaFor(t, "cm")
	mirrorWith(t, slotSid(m), items...)
	logPath := filepath.Join(t.TempDir(), "session.jsonl")
	writeSession(slotSid(m), museSession{ID: "01a0c1d6-0000-7000-8000-0000000000e1", Path: logPath})
	return m, logPath
}

func partsOf(t *testing.T, m session.Meta) [][]string {
	t.Helper()
	td, ok := New().Transcript(m)
	if !ok {
		t.Fatal("no transcript")
	}
	var out [][]string
	for _, tr := range td.Turns {
		var ps []string
		for _, p := range tr.Parts {
			if p.Kind == "text" {
				ps = append(ps, "text:"+p.Text)
			} else {
				ps = append(ps, p.Kind)
			}
		}
		out = append(out, ps)
	}
	return out
}

// The proposal shows in front of the call that asks about it — in that call's turn, wherever
// the store's order (a backfill, the host's order) has put the call.
func TestTranscriptPutsCommentaryInFrontOfItsCall(t *testing.T) {
	m, logPath := museSlot(t, u1, a1, u2, toolCall("tc1", "call-a", 2), a2)
	appendFile(t, logPath, commentaryLine("m1", "r1", "the proposal")+toolCallsLine("r1", "call-a"))
	got := fmt.Sprint(partsOf(t, m))
	want := fmt.Sprint([][]string{{"text:first"}, {"text:reply one"}, {"text:second"}, {"text:the proposal", "tool", "text:reply two"}})
	if got != want {
		t.Errorf("parts = %s\nwant    %s", got, want)
	}
}

// The host may log the commentary after the call's item went out, and a question then waits
// with nothing else arriving. The next read — the mirror polls — must show it.
func TestTranscriptShowsCommentaryLoggedAfterItsCall(t *testing.T) {
	m, logPath := museSlot(t, u1, toolCall("tc1", "call-a", 1))
	line := commentaryLine("m1", "r1", "the proposal")
	appendFile(t, logPath, line[:40])
	if got := fmt.Sprint(partsOf(t, m)); got != fmt.Sprint([][]string{{"text:first"}, {"tool"}}) {
		t.Fatalf("before the log line completed: %s", got)
	}
	appendFile(t, logPath, line[40:]+toolCallsLine("r1", "call-a"))
	if got := fmt.Sprint(partsOf(t, m)); got != fmt.Sprint([][]string{{"text:first"}, {"text:the proposal", "tool"}}) {
		t.Errorf("after the log line completed: %s", got)
	}
}

// A commentary the wire did deliver (an agentMessage carrying the commit's message_id) is not
// shown a second time.
func TestTranscriptDoesNotRepeatDeliveredCommentary(t *testing.T) {
	m, logPath := museSlot(t, u1, textItem("m1", msp.ItemKindAgentMessage, "the proposal", 1), toolCall("tc1", "call-a", 1))
	appendFile(t, logPath, commentaryLine("m1", "r1", "the proposal")+toolCallsLine("r1", "call-a"))
	if got := fmt.Sprint(partsOf(t, m)); got != fmt.Sprint([][]string{{"text:first"}, {"text:the proposal", "tool"}}) {
		t.Errorf("parts = %s", got)
	}
}

// No recorded log, or a file that is not there: the transcript is what it was.
func TestTranscriptWithoutARuntimeLogIsUnchanged(t *testing.T) {
	m, logPath := museSlot(t, u1, toolCall("tc1", "call-a", 1))
	_ = logPath // never written
	if got := fmt.Sprint(partsOf(t, m)); got != fmt.Sprint([][]string{{"text:first"}, {"tool"}}) {
		t.Errorf("parts = %s", got)
	}
	if got := turnsWithCommentary([]msp.Item{u1}, nil); len(got) != 1 {
		t.Errorf("nil set: %+v", got)
	}
}
