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

func textRow(id, text string) string {
	b, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{
		"id": id, "content": []map[string]any{{"type": "text", "text": text}},
	}})
	return string(b)
}

func rowsOf(rows ...string) [][]byte {
	out := make([][]byte, len(rows))
	for i, r := range rows {
		out[i] = []byte(r)
	}
	return out
}

const livePrompt = `{"type":"user","message":{"role":"user","content":"go"}}`

func TestLiveReplyTextWhileStreaming(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	status.AppendLiveText("s", "t1", "m2", 0, false, "1. first\n")
	status.AppendLiveText("s", "t1", "m2", 1, false, "2. second\n")
	// The transcript holds only the previous message of the turn.
	lines := rowsOf(livePrompt, textRow("msg_1", "Reading the file."))
	if got := liveReplyText("s", lines, time.Now()); got != "1. first\n2. second" {
		t.Fatalf("got %q, want the two streamed lines", got)
	}
}

// Once the message's row is in the transcript the mirror shows it there; sending it again
// would show the reply twice.
func TestLiveReplyTextHiddenOnceLanded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	status.AppendLiveText("s", "t1", "m2", 0, false, "1. first\n")
	// The row can land before the final flush arrives: the streamed text is then a prefix of it.
	lines := rowsOf(livePrompt, textRow("msg_2", "1. first\n2. second"))
	if got := liveReplyText("s", lines, time.Now()); got != "" {
		t.Fatalf("got %q after the row landed, want nothing", got)
	}
}

// A response with text on both sides of a server tool call lands one text block at a time.
// The block already in the transcript is not sent again.
func TestLiveReplyTextSendsOnlyWhatHasNotLanded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	status.AppendLiveText("s", "t1", "m2", 0, false, "Searching.\n")
	status.AppendLiveText("s", "t1", "m2", 1, false, "Found it:\n")
	lines := rowsOf(livePrompt, textRow("msg_2", "Searching.\n"))
	if got := liveReplyText("s", lines, time.Now()); got != "Found it:" {
		t.Fatalf("got %q, want only the part that has not landed", got)
	}
}

// A finished message whose row the text match cannot find (display and transcript differ)
// stops being sent after the grace period instead of duplicating the reply until the turn ends.
func TestLiveReplyTextFinalMessageExpires(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	status.AppendLiveText("s", "t1", "m2", 0, false, "shown\n")
	status.AppendLiveText("s", "t1", "m2", 1, true, "tail")
	lines := rowsOf(livePrompt)
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
		status.AppendLiveText("s", "t1", "m1", i, false, line)
	}
	status.AppendLiveText("s", "t1", "m1", n, false, "last line\n")
	got := liveReplyText("s", rowsOf(livePrompt), time.Now())
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
	if got := liveReplyText("s", rowsOf(livePrompt), time.Now()); got != "" {
		t.Fatalf("got %q with no live-text file", got)
	}
}

// The hook is what fills the store: the session's own flushes go in, a subagent's do not, and
// the turn's end clears it.
func TestMessageDisplayHookFeedsLiveText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "sess-live"
	feedStatusHook(t, "message", `{"session_id":"`+sid+`","turn_id":"t1","message_id":"m1","index":0,"final":false,"delta":"from a subagent\n","agent_id":"a1"}`)
	if _, ok := status.ReadLiveText(sid); ok {
		t.Fatal("a subagent's flush reached the session's streamed reply")
	}
	feedStatusHook(t, "message", `{"session_id":"`+sid+`","turn_id":"t1","message_id":"m1","index":0,"final":false,"delta":"line one\n"}`)
	feedStatusHook(t, "message", `{"session_id":"`+sid+`","turn_id":"t1","message_id":"m1","index":1,"final":true,"delta":"end"}`)
	lr, ok := status.ReadLiveText(sid)
	if !ok || lr.Text != "line one\nend" || !lr.Final {
		t.Fatalf("got %+v (ok=%v), want the two flushes and a final message", lr, ok)
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

// A stopped session never carries liveText, even when the Console asks for it.
func TestMessagesLiveTextAbsentWhenStopped(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	dir := t.TempDir()
	const name = "live_stopped"
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude, Title: "t"})
	sid := session.UUID(dir, name)
	writeFile(t, filepath.Join(home, ".claude", "projects", "p", sid+".jsonl"), livePrompt+"\n")
	status.Persist(sid, "working")
	status.AppendLiveText(sid, "t1", "m1", 0, false, "streaming\n")

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
