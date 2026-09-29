package sessionx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

func TestWantsLiveReply(t *testing.T) {
	for _, c := range []struct {
		query string
		alive bool
		state string
		want  bool
	}{
		{"?since=3&live=1", true, "working", true},
		{"?since=3", true, "working", false},        // setting off: the response stays as it always was
		{"?since=3&live=0", true, "working", false}, // only the Console's own spelling turns it on
		{"?since=3&live=1", false, "working", false},
		{"?since=3&live=1", true, "idle", false}, // at rest: no field, so unchanged polls stay byte-identical
		{"?since=3&live=1", true, "question", false},
		{"?since=3&live=1", true, "permission", false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/sessions/x/messages"+c.query, nil)
		if got := wantsLiveReply(r, c.alive, c.state); got != c.want {
			t.Errorf("wantsLiveReply(%q, alive=%v, %q) = %v, want %v", c.query, c.alive, c.state, got, c.want)
		}
	}
}

// flush appends one MessageDisplay flush of message msg in the turn answering prompt.
func flush(prompt, msg string, index int, final bool, delta string) {
	status.AppendLiveText("s", status.LiveFlush{Prompt: prompt, Turn: "turn-" + prompt, Msg: msg, Index: index, Final: final, Delta: delta})
}

func jsonRow(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func promptRow(id string) string {
	return jsonRow(map[string]any{"type": "user", "promptId": id, "message": map[string]any{"role": "user", "content": "go " + id}})
}

func resultRow(id string) string {
	return jsonRow(map[string]any{"type": "user", "promptId": id, "message": map[string]any{
		"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t", "content": "ok"}},
	}})
}

func textRow(msgID, text string) string {
	return jsonRow(map[string]any{"type": "assistant", "message": map[string]any{
		"id": msgID, "content": []map[string]any{{"type": "text", "text": text}},
	}})
}

func toolRow(msgID string) string {
	return jsonRow(map[string]any{"type": "assistant", "message": map[string]any{
		"id": msgID, "content": []map[string]any{{"type": "tool_use", "id": "t", "name": "Read", "input": map[string]any{}}},
	}})
}

// stamped sets a row's transcript timestamp.
func stamped(row string, at time.Time) string {
	var m map[string]any
	_ = json.Unmarshal([]byte(row), &m)
	m["timestamp"] = at.UTC().Format(time.RFC3339Nano)
	return jsonRow(m)
}

func rowsOf(rows ...string) [][]byte {
	out := make([][]byte, len(rows))
	for i, r := range rows {
		out[i] = []byte(r)
	}
	return out
}

// The turn answering p1 so far: its first message said something and ran a tool.
func afterFirstTool() [][]byte {
	return rowsOf(promptRow("p1"), textRow("msg_1", "Reading the file."), toolRow("msg_1"), resultRow("p1"))
}

func TestLiveReplyTextWhileStreaming(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	flush("p1", "m2", 0, false, "1. first\n")
	flush("p1", "m2", 1, false, "2. second\n")
	if got := liveReplyText("s", afterFirstTool(), time.Now()); got != "1. first\n2. second" {
		t.Fatalf("got %q, want the two streamed lines", got)
	}
}

// Once the message's row is in the transcript the mirror shows it there; sending it again
// would show the reply twice. The row lands before the final flush.
func TestLiveReplyTextHiddenOnceLanded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	flush("p1", "m2", 0, false, "1. first\n")
	lines := append(afterFirstTool(), []byte(textRow("msg_2", "1. first\n2. second")))
	if got := liveReplyText("s", lines, time.Now()); got != "" {
		t.Fatalf("got %q after the row landed, want nothing", got)
	}
}

// A response with text on both sides of a server tool call lands one text block at a time.
// The block already in the transcript is not sent again.
func TestLiveReplyTextSendsOnlyWhatHasNotLanded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	flush("p1", "m2", 0, false, "Searching.\n")
	flush("p1", "m2", 1, false, "Found it:\n")
	lines := append(afterFirstTool(), []byte(textRow("msg_2", "Searching.\n")))
	if got := liveReplyText("s", lines, time.Now()); got != "Found it:" {
		t.Fatalf("got %q, want only the part that has not landed", got)
	}
}

// The previous turn's answer is not this message, even when this one starts the same way: it
// must not hide the reply, streaming or finished.
func TestLiveReplyTextNotConfusedByThePreviousTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lines := rowsOf(promptRow("p1"), textRow("msg_1", "Done.\nNext, the tests.\nAll green."), promptRow("p2"))
	flush("p2", "m1", 0, false, "Done.\n")
	if got := liveReplyText("s", lines, time.Now()); got != "Done." {
		t.Fatalf("got %q, want the new reply although the last answer said the same", got)
	}
	flush("p2", "m1", 1, false, "Next, the tests.\n")
	if got := liveReplyText("s", lines, time.Now()); got != "Done.\nNext, the tests." {
		t.Fatalf("got %q, want it whole, not hidden behind the previous answer", got)
	}
	flush("p2", "m1", 2, true, "")
	if got := liveReplyText("s", lines, time.Now()); got != "Done.\nNext, the tests." {
		t.Fatalf("finished but not landed got %q, want it still shown", got)
	}
}

// Nor is an earlier message of the same turn.
func TestLiveReplyTextNotConfusedByThePreviousMessage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	flush("p1", "m2", 0, false, "Reading the file.\n")
	flush("p1", "m2", 1, false, "It says alpha.\n")
	if got := liveReplyText("s", afterFirstTool(), time.Now()); got != "Reading the file.\nIt says alpha." {
		t.Fatalf("got %q, want the whole second message", got)
	}
}

// A finished message is looked for among the messages of its own turn.
func TestLiveReplyTextFinalMessageLandedInItsTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	flush("p1", "m2", 0, false, "It says alpha.\n")
	flush("p1", "m2", 1, true, "")
	lines := append(afterFirstTool(), []byte(textRow("msg_2", "It says alpha.\n")), []byte(toolRow("msg_2")), []byte(resultRow("p1")))
	if got := liveReplyText("s", lines, time.Now()); got != "" {
		t.Fatalf("got %q for a finished message whose row is in the transcript, want nothing", got)
	}
}

// A message of an earlier turn — left behind by a turn that ended without Stop, or re-created
// by a flush that landed after it — is not the reply of the turn now running. The running turn
// is what its hooks recorded, so this holds before the new prompt's row reaches the transcript,
// and however long the previous turn was.
func TestLiveReplyTextSupersededTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	flush("p1", "m9", 0, false, "Half a reply\n")
	status.WriteLivePrompt("s", "p1")
	lines := rowsOf(promptRow("p1"))
	if got := liveReplyText("s", lines, time.Now()); got != "Half a reply" {
		t.Fatalf("got %q while p1 is the running turn, want it", got)
	}
	status.WriteLivePrompt("s", "p2") // p2's UserPromptSubmit; its row is not in the transcript yet
	if got := liveReplyText("s", lines, time.Now()); got != "" {
		t.Fatalf("got %q from the previous turn, want nothing", got)
	}
	long := rowsOf(promptRow("p1"))
	for range 3000 {
		long = append(long, []byte(toolRow("msg_x")), []byte(resultRow("p1")))
	}
	long = append(long, []byte(promptRow("p2")))
	if got := liveReplyText("s", long, time.Now()); got != "" {
		t.Fatalf("got %q after a 6000-row previous turn, want nothing", got)
	}
}

// A finished message is not taken for an earlier message of its turn that said the same thing:
// rows written before the previous message's final flush are that message's, not this one's.
func TestLiveReplyTextSameTextAsThePreviousMessage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lines := rowsOf(promptRow("p1"), stamped(textRow("msg_1", "Done."), time.Now().Add(-time.Minute)), toolRow("msg_1"), resultRow("p1"))
	flush("p1", "m1", 0, true, "Done.")
	flush("p1", "m2", 0, true, "Done.")
	if got := liveReplyText("s", lines, time.Now()); got != "Done." {
		t.Fatalf("got %q before m2's row landed, want m2 shown", got)
	}
	lines = append(lines, []byte(stamped(textRow("msg_2", "Done."), time.Now().Add(time.Second))))
	if got := liveReplyText("s", lines, time.Now()); got != "" {
		t.Fatalf("got %q after m2's row landed, want nothing", got)
	}
}

// A message that stopped mid-way, with no final flush, is dropped once it has been silent for
// liveStaleAfter.
func TestLiveReplyTextStaleWithoutFinal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	flush("p1", "m2", 0, false, "cut off\n")
	lines := afterFirstTool()
	if got := liveReplyText("s", lines, time.Now()); got != "cut off" {
		t.Fatalf("got %q, want it while still recent", got)
	}
	if got := liveReplyText("s", lines, time.Now().Add(liveStaleAfter+time.Second)); got != "" {
		t.Fatalf("got %q after %v of silence, want nothing", got, liveStaleAfter)
	}
}

// claude stores memory citation tags that it does not display. The stored text still counts as
// the streamed one.
func TestLiveReplyTextMemoryTags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	flush("p1", "m2", 0, false, "Use the wrapper.\n")
	stored := `Use <cc-memory filenames="build.md">the wrapper</cc-memory>.` + "\n"
	lines := append(afterFirstTool(), []byte(textRow("msg_2", stored)))
	if got := liveReplyText("s", lines, time.Now()); got != "" {
		t.Fatalf("got %q while the row (with memory tags) has landed, want nothing", got)
	}
	flush("p1", "m2", 1, true, "")
	lines = append(lines, []byte(toolRow("msg_2")), []byte(resultRow("p1")))
	if got := liveReplyText("s", lines, time.Now()); got != "" {
		t.Fatalf("got %q for the finished message, want nothing", got)
	}
}

// A finished message whose row cannot be matched stops being sent after the grace period,
// instead of duplicating the reply until the turn ends.
func TestLiveReplyTextFinalMessageExpires(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	flush("p1", "m2", 0, false, "shown\n")
	flush("p1", "m2", 1, true, "tail")
	lines := afterFirstTool()
	if got := liveReplyText("s", lines, time.Now()); got != "shown\ntail" {
		t.Fatalf("right after the final flush got %q, want the whole message", got)
	}
	if got := liveReplyText("s", lines, time.Now().Add(liveFinalGrace+time.Second)); got != "" {
		t.Fatalf("past the grace period got %q, want nothing", got)
	}
}

func TestLiveReplyTextKeepsTheTailOfALongReply(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	line := strings.Repeat("あ", 99) + "\n" // multibyte, so a byte cut could split a rune
	n := liveTextMax/len(line) + 20
	for i := range n {
		flush("p1", "m1", i, false, line)
	}
	flush("p1", "m1", n, false, "last line\n")
	got := liveReplyText("s", rowsOf(promptRow("p1")), time.Now())
	if !strings.HasPrefix(got, "…\n") || !strings.HasSuffix(got, "last line") {
		t.Fatalf("got %q…%q, want an ellipsis then the tail", got[:12], got[len(got)-12:])
	}
	if len(got) > liveTextMax+len("…\n") {
		t.Fatalf("got %d bytes, want at most %d", len(got), liveTextMax+len("…\n"))
	}
	if !strings.HasPrefix(strings.TrimPrefix(got, "…\n"), "あ") {
		t.Fatal("the cut did not land on a line start")
	}
}

func TestLiveReplyTextNothingStreamed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := liveReplyText("s", rowsOf(promptRow("p1")), time.Now()); got != "" {
		t.Fatalf("got %q with no live-text file", got)
	}
}

// The hook is what fills the store: the session's own flushes go in with their prompt id, a
// subagent's do not, and the turn's end clears it.
func TestMessageDisplayHookFeedsLiveText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "sess-live"
	feedStatusHook(t, "message", `{"session_id":"`+sid+`","prompt_id":"p1","turn_id":"t1","message_id":"m1","index":0,"final":false,"delta":"from a subagent\n","agent_id":"a1"}`)
	if _, ok := status.ReadLiveText(sid); ok {
		t.Fatal("a subagent's flush reached the session's streamed reply")
	}
	feedStatusHook(t, "message", `{"session_id":"`+sid+`","prompt_id":"p1","turn_id":"t1","message_id":"m1","index":0,"final":false,"delta":"line one\n"}`)
	feedStatusHook(t, "message", `{"session_id":"`+sid+`","prompt_id":"p1","turn_id":"t1","message_id":"m1","index":1,"final":true,"delta":"end"}`)
	lr, ok := status.ReadLiveText(sid)
	if !ok || lr.Text != "line one\nend" || !lr.Final || lr.Prompt != "p1" {
		t.Fatalf("got %+v (ok=%v), want the two flushes, final, of prompt p1", lr, ok)
	}
	// A tool call in the middle of the turn keeps it: the reply goes on after the tool.
	feedStatusHook(t, "working", `{"session_id":"`+sid+`","tool_name":"Read"}`)
	if _, ok := status.ReadLiveText(sid); !ok {
		t.Fatal("PostToolUse's working dropped the streamed reply mid-turn")
	}
	feedStatusHook(t, "idle", `{"session_id":"`+sid+`"}`)
	if _, ok := status.ReadLiveText(sid); ok {
		t.Fatal("the streamed reply survived the end of the turn")
	}
}

// Every hook of the running turn that carries its prompt_id records it — UserPromptSubmit and
// PostToolUse alike — except a subagent's.
func TestWorkingHooksRecordTheTurnPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "sess-prompt"
	feedStatusHook(t, "working", `{"session_id":"`+sid+`","hook_event_name":"UserPromptSubmit","prompt_id":"p1","prompt":"go"}`)
	if got := status.ReadLivePrompt(sid); got != "p1" {
		t.Fatalf("after UserPromptSubmit got %q, want p1", got)
	}
	feedStatusHook(t, "working", `{"session_id":"`+sid+`","tool_name":"Bash","prompt_id":"px","agent_id":"a1"}`)
	if got := status.ReadLivePrompt(sid); got != "p1" {
		t.Fatalf("a subagent's PostToolUse moved it to %q", got)
	}
	feedStatusHook(t, "working", `{"session_id":"`+sid+`","tool_name":"Read","prompt_id":"p2"}`)
	if got := status.ReadLivePrompt(sid); got != "p2" {
		t.Fatalf("after PostToolUse of p2 got %q, want p2", got)
	}
}

// A stopped session never carries liveText, even when the Console asks for it.
func TestMessagesLiveTextAbsentWhenStopped(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	dir := t.TempDir()
	const name = "live_stopped"
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude, Title: "t"})
	sid := session.UUID(dir, name)
	writeFile(t, filepath.Join(home, ".claude", "projects", "p", sid+".jsonl"), promptRow("p1")+"\n")
	status.Persist(sid, "working")
	status.AppendLiveText(sid, status.LiveFlush{Prompt: "p1", Turn: "t1", Msg: "m1", Delta: "streaming\n"})

	req := httptest.NewRequest(http.MethodGet, "/sessions/"+name+"/messages?since=1&live=1", nil)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleSessionMessages(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, has := body["liveText"]; has {
		t.Fatalf("a stopped session sent liveText: %s", rec.Body.String())
	}
}
