package sessionx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func getAnswerTurns(t *testing.T, name, query string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/sessions/"+name+"/answer-turns?"+query, nil)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleSessionAnswerTurns(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func getMessagesBody(t *testing.T, name, query string) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/sessions/"+name+"/messages?"+query, nil)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleSessionMessages(rec, req)
	return rec.Body.Bytes()
}

// claudeLog writes a claude transcript for a new session and returns its name.
func claudeLog(t *testing.T, name string, lines []string) string {
	t.Helper()
	home := withTempHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude})
	writeFile(t, filepath.Join(home, ".claude", "projects", "p", session.UUID(dir, name)+".jsonl"), strings.Join(lines, "\n")+"\n")
	return name
}

// jsonStr is s as a JSON string (Go's %q writes escapes JSON does not have).
func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func claudeUser(i int, text string) string {
	return fmt.Sprintf(`{"type":"user","uuid":"u%d","parentUuid":null,"timestamp":"2026-11-05T10:00:00Z","message":{"role":"user","content":%s}}`, i, jsonStr(text))
}

func claudeAssistant(i int, text string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":"a%d","parentUuid":null,"timestamp":"2026-11-05T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":%s}]}}`, i, jsonStr(text))
}

// The slim reply of a page full of edits: the plain route's page of the same 50 lines is over the
// Control Plane's 8 MiB reply bound, the slim one stays far under it, keeps the tool part's kind
// and the answer's head, and carries no edit, output or command key at all (issue #4891 item 7).
func TestAnswerTurnsDropsEditBodies(t *testing.T) {
	// \x01 is escaped to six bytes on the wire, and the edit cap (20,000 characters) keeps the
	// old and the new text whole.
	blob := strings.Repeat("\x01", 20000)
	var lines []string
	lines = append(lines, claudeUser(0, "go"))
	for i := 0; i < 48; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"assistant","uuid":"e%d","parentUuid":null,"timestamp":"2026-11-05T10:00:01Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t%d","name":"Edit","input":{"file_path":"/a.txt","old_string":%s,"new_string":%s}}]}}`, i, i, jsonStr(blob), jsonStr(blob)))
	}
	lines = append(lines, claudeAssistant(1, "Done: shipped"))
	name := claudeLog(t, "answer_turns_slim", lines)

	if full := getMessagesBody(t, name, "since=0&tail=1&limit=50"); len(full) < 8<<20 {
		t.Fatalf("the plain page is %d bytes: the test no longer builds the failure it guards: %.300s", len(full), full)
	}
	code, body := getAnswerTurns(t, name, "limit=50")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %.200s", code, body)
	}
	if len(body) > 64<<10 {
		t.Fatalf("slim reply is %d bytes: edit bodies travelled", len(body))
	}
	for _, key := range []string{`"edits"`, `"output"`, `"command"`, `"info"`, `"file"`} {
		if strings.Contains(string(body), key) {
			t.Errorf("slim reply carries %s", key)
		}
	}
	var reply answerTurnsReply
	if err := json.Unmarshal(body, &reply); err != nil {
		t.Fatal(err)
	}
	var tools int
	var last answerPart
	for _, m := range reply.Messages {
		for _, p := range m.Parts {
			if p.Kind == "tool" {
				tools++
			}
			last = p
		}
	}
	if tools == 0 || last.Kind != "text" || last.Text != "Done: shipped" {
		t.Fatalf("tools=%d, last part %+v", tools, last)
	}
}

// What the read concludes from a slim turn is what it concludes from the full one: a blank run in
// front of the answer is not kept as the answer, a blank part stays blank, a part cut mid-rune
// stays valid, and thinking and error parts keep their kind alone.
func TestSlimTurnsKeepsWhatTheReadDecidesOn(t *testing.T) {
	long := strings.Repeat("\n", answerTextMax*2) + "Partial: still blocked"
	got := slimTurns([]transcript.Turn{
		{Role: "assistant", Parts: []transcript.Part{{Kind: "text", Text: long}}},
		{Role: "assistant", Parts: []transcript.Part{{Kind: "text", Text: strings.Repeat(" \n", answerTextMax)}}},
		{Role: "assistant", Text: long},
		{Role: "assistant", Parts: []transcript.Part{{Kind: "thinking", Text: "hmm"}, {Kind: "error", Text: "boom"}}},
	})
	if got[0].Parts[0].Text != "Partial: still blocked" || got[2].Text != "Partial: still blocked" {
		t.Errorf("blank run kept: %q / %q", got[0].Parts[0].Text, got[2].Text)
	}
	if got[1].Parts[0].Text != "" {
		t.Errorf("a blank part became %q", got[1].Parts[0].Text)
	}
	if got[3].Parts[0].Text != "" || got[3].Parts[1].Text != "" || got[3].Parts[1].Kind != "error" {
		t.Errorf("thinking/error parts = %+v", got[3].Parts)
	}
}

// pageAll reads a session's transcript back through answer-turns in pages of limit, and returns
// the turn texts oldest first with the pages' firstLine values. before must be the previous
// page's firstLine, so a route that ignored it, or a firstLine that did not match what was read,
// shows as a duplicate or a gap.
func pageAll(t *testing.T, name string, limit int) (texts []string, firsts []int) {
	t.Helper()
	query := "limit=" + strconv.Itoa(limit)
	for {
		code, body := getAnswerTurns(t, name, query)
		if code != http.StatusOK {
			t.Fatalf("status = %d, body = %.200s", code, body)
		}
		var reply answerTurnsReply
		if err := json.Unmarshal(body, &reply); err != nil {
			t.Fatal(err)
		}
		var page []string
		for _, m := range reply.Messages {
			page = append(page, m.Parts[0].Text)
		}
		texts = append(page, texts...)
		firsts = append(firsts, reply.FirstLine)
		if !reply.HasMore {
			return texts, firsts
		}
		query = "limit=" + strconv.Itoa(limit) + "&before=" + strconv.Itoa(reply.FirstLine)
	}
}

func wantTurns(t *testing.T, texts []string, n int) {
	t.Helper()
	if len(texts) != n {
		t.Fatalf("%d turns read, want %d", len(texts), n)
	}
	for i, s := range texts {
		if s != "m"+strconv.Itoa(i) {
			t.Fatalf("turn %d is %q: a gap or a repeat between pages", i, s)
		}
	}
}

// Pages follow each other through before on a claude log, with no line unread between them even
// when a window holds more than the 1 MiB /messages caps it at (a capped window dropped its
// oldest turns while firstLine stayed, and the next page began below them).
func TestAnswerTurnsPagesBackWithoutAGap(t *testing.T) {
	const n = 130
	big := strings.Repeat("z", 700<<10)
	var lines []string
	for i := 0; i < n; i++ {
		text := "m" + strconv.Itoa(i)
		role := claudeAssistant
		if i%2 == 0 {
			role = claudeUser
		}
		lines = append(lines, role(i, text))
	}
	// Two turns of the page of lines 30..79 hold 700 KiB each: over the 1 MiB the plain route caps a window at.
	lines[75] = strings.Replace(lines[75], `"text":"m75"`, `"text":"m75 `+big+`"`, 1)
	lines[77] = strings.Replace(lines[77], `"text":"m77"`, `"text":"m77 `+big+`"`, 1)
	name := claudeLog(t, "answer_turns_pages", lines)
	texts, firsts := pageAll(t, name, 50)
	for i := range texts {
		texts[i], _, _ = strings.Cut(texts[i], " ")
	}
	wantTurns(t, texts, n)
	if fmt.Sprint(firsts) != "[80 30 0]" {
		t.Fatalf("firstLine of the pages = %v, want [80 30 0]", firsts)
	}
}

// fakeTurnsAgent is a store-backed kind with n plain turns (the generic path), and the same for
// the fork case below.
type fakeTurnsAgent struct {
	agents.Agent
	n int
}

func (fakeTurnsAgent) Kind() string { return session.KindMuse }
func (fakeTurnsAgent) Caps() agents.Caps {
	return agents.Caps{CanTranscript: true, ManagedOnly: true}
}
func (fakeTurnsAgent) BuildLaunch(session.Meta, agents.LaunchOpts) (agents.LaunchPlan, error) {
	return agents.LaunchPlan{}, errors.New("no terminal route")
}
func (fakeTurnsAgent) WireLive(session.Meta, bool) agents.LiveInfo { return agents.LiveInfo{} }
func (fakeTurnsAgent) ClearResume(string)                          {}
func (a fakeTurnsAgent) Transcript(session.Meta) (agents.TranscriptData, bool) {
	turns := make([]transcript.Turn, a.n)
	for i := range turns {
		role := "assistant"
		if i%2 == 0 {
			role = "user"
		}
		turns[i] = transcript.Turn{Role: role, Idx: i, Parts: []transcript.Part{{Kind: "text", Text: "m" + strconv.Itoa(i)}}}
	}
	return agents.TranscriptData{Turns: turns}, true
}

func TestAnswerTurnsPagesBackOnTheGenericPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(agentreg.SwapForTest(session.KindMuse, fakeTurnsAgent{n: 130}))
	session.WriteMeta(session.Meta{Name: "answer-turns-generic", Dir: t.TempDir(), Kind: session.KindMuse, Driver: session.DriverManaged})
	texts, firsts := pageAll(t, "answer-turns-generic", 50)
	wantTurns(t, texts, 130)
	if fmt.Sprint(firsts) != "[80 30 0]" {
		t.Fatalf("firstLine of the pages = %v, want [80 30 0]", firsts)
	}
}

// A fork that has not written its own log answers with no turns, where /messages previews its
// parent's history: the parent's answer is not the fork's.
func TestAnswerTurnsForkWithoutOwnLogIsEmpty(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	dir := t.TempDir()
	parent := session.UUID(dir, "answer_turns_parent")
	writeFile(t, filepath.Join(home, ".claude", "projects", "p", parent+".jsonl"),
		claudeUser(0, "task")+"\n"+claudeAssistant(1, "Done: the parent's")+"\n")
	session.WriteMeta(session.Meta{Name: "answer_turns_fork", Dir: dir, Kind: session.KindClaude, ForkFrom: parent})
	code, body := getAnswerTurns(t, "answer_turns_fork", "limit=50")
	var reply answerTurnsReply
	if code != http.StatusOK || json.Unmarshal(body, &reply) != nil || len(reply.Messages) != 0 {
		t.Fatalf("status %d, reply %.300s", code, body)
	}
	if full := string(getMessagesBody(t, "answer_turns_fork", "since=0&tail=1")); !strings.Contains(full, "the parent's") {
		t.Fatalf("/messages no longer previews the parent (%.200s): the contrast this test states is gone", full)
	}
}

func TestHeadOfCutsOnARuneBoundary(t *testing.T) {
	s := strings.Repeat("あ", answerTextMax) // 3 bytes each
	got := headOf(s)
	if len(got) > answerTextMax || len(got)%3 != 0 || !strings.HasPrefix(s, got) {
		t.Fatalf("len = %d", len(got))
	}
	if headOf("short") != "short" {
		t.Fatal("a short text must pass through")
	}
}
