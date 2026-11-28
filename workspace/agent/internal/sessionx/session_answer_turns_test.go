package sessionx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
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
// front of the answer is not kept as the answer, blank parts leave the turn without an answer, the
// head is the first line without the whitespace around it, the part kinds that end a response and
// an error anywhere in the turn survive, and the ids and times travel as they are.
func TestSlimTurnsKeepsWhatTheReadDecidesOn(t *testing.T) {
	long := strings.Repeat("\n", answerTextMax*2) + "Partial: still blocked"
	spaced := "Done:" + strings.Repeat(" ", 2*answerTextMax) + "details"
	text := func(s string) transcript.Part { return transcript.Part{Kind: "text", Text: s} }
	kinds := func(parts []answerPart) string {
		var k []string
		for _, p := range parts {
			k = append(k, p.Kind)
		}
		return strings.Join(k, ",")
	}
	got := slimTurns([]transcript.Turn{
		{Role: "assistant", Parts: []transcript.Part{text(long)}},
		{Role: "assistant", Parts: []transcript.Part{text(strings.Repeat(" \n", answerTextMax)), {Kind: "thinking", Text: "hmm"}}},
		{Role: "assistant", Text: long},
		{Role: "assistant", Parts: []transcript.Part{{Kind: "thinking", Text: "hmm"}, {Kind: "error", Text: "boom"}}},
		{Role: "assistant", Parts: []transcript.Part{text(spaced)}},
		// Ended on a tool call: the text before it is not the answer.
		{Role: "assistant", Parts: []transcript.Part{text("Done: early"), {Kind: "tool", Tool: "Bash"}, text("  "), {Kind: "thinking"}}},
		// An error in the middle of a turn that went on to an answer.
		{Role: "assistant", Parts: []transcript.Part{{Kind: "error"}, {Kind: "tool"}, text("a"), text(""), text("b")}},
		{Role: "user", Sidechain: true, TS: "2026-11-05T10:00:00Z", AnchorID: "msg_1", Parts: []transcript.Part{text("x")}},
	})
	if got[0].Parts[0].Text != "Partial: still blocked" || got[2].Text != "Partial: still blocked" {
		t.Errorf("blank run kept: %q / %q", got[0].Parts[0].Text, got[2].Text)
	}
	if len(got[1].Parts) != 0 || got[1].Text != "" {
		t.Errorf("a blank turn became %+v", got[1])
	}
	if kinds(got[3].Parts) != "error" || got[3].Parts[0].Text != "" {
		t.Errorf("thinking/error turn = %+v", got[3].Parts)
	}
	if got[4].Parts[0].Text != "Done:"+strings.Repeat(" ", answerTextMax-len("Done:")) && got[4].Parts[0].Text != "Done:" {
		t.Errorf("head = %q", got[4].Parts[0].Text)
	}
	if kinds(got[5].Parts) != "tool" || got[5].Parts[0].Text != "" {
		t.Errorf("tool-ended turn = %+v", got[5].Parts)
	}
	if kinds(got[6].Parts) != "error,tool,text" || got[6].Parts[2].Text != "a" {
		t.Errorf("mixed turn = %+v", got[6].Parts)
	}
	if u := got[7]; !u.Sidechain || u.TS != "2026-11-05T10:00:00Z" || u.AnchorID != "msg_1" {
		t.Errorf("wire fields lost: %+v", u)
	}
}

// A turn's slim size does not depend on how many parts it holds: 30 parts of 1 KiB, 5000 thinking
// parts and a megabyte of text all come out as a few hundred bytes, so a page of 50 lines is far
// under the Control Plane's reply bound whatever the transcript holds.
func TestSlimTurnsAreBounded(t *testing.T) {
	var parts []transcript.Part
	for i := 0; i < 5000; i++ {
		parts = append(parts, transcript.Part{Kind: "text", Text: strings.Repeat("<", 1024)}, transcript.Part{Kind: "thinking", Text: "t"})
	}
	parts = append(parts, transcript.Part{Kind: "tool", Tool: "Bash"}, transcript.Part{Kind: "text", Text: "Done: fixed\n" + strings.Repeat("x", 1<<20)})
	b, err := json.Marshal(slimTurns([]transcript.Turn{{Role: "assistant", Parts: parts}}))
	if err != nil || len(b) > 8<<10 {
		t.Fatalf("slim turn is %d bytes (%v)", len(b), err)
	}
	if !strings.Contains(string(b), "Done: fixed") {
		t.Fatalf("the answer's head is gone: %.200s", b)
	}
}

// The fields the pin decides on come through the route's own wire (ts, anchorId, sidechain, the
// error kind), not only through slimTurns.
func TestAnswerTurnsWireCarriesThePinFields(t *testing.T) {
	name := claudeLog(t, "answer_turns_wire", []string{
		claudeUser(0, "go"),
		claudeAssistant(1, "Done: run"),
	})
	_, body := getAnswerTurns(t, name, "limit=50")
	var reply struct {
		Messages []struct {
			Role string `json:"role"`
			TS   string `json:"ts"`
			// anchorId is the kind's own id; claude writes its uuid.
			AnchorID string `json:"anchorId"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &reply); err != nil || len(reply.Messages) != 2 {
		t.Fatalf("%v: %.300s", err, body)
	}
	for _, m := range reply.Messages {
		if m.TS != "2026-11-05T10:00:00Z" && m.TS != "2026-11-05T10:00:01Z" {
			t.Errorf("%s turn ts = %q", m.Role, m.TS)
		}
	}
	// A generic-path kind carries anchorId and sidechain from its own turns.
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(agentreg.SwapForTest(session.KindMuse, fakeTurnsAgent{n: 3, anchor: true}))
	session.WriteMeta(session.Meta{Name: "answer-turns-wire-g", Dir: t.TempDir(), Kind: session.KindMuse, Driver: session.DriverManaged})
	_, body = getAnswerTurns(t, "answer-turns-wire-g", "limit=50")
	s := string(body)
	if !strings.Contains(s, `"anchorId":"id1"`) || !strings.Contains(s, `"sidechain":true`) || !strings.Contains(s, `"ts":"2026-11-05T10:00:02Z"`) {
		t.Fatalf("pin fields lost on the wire: %.600s", s)
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
		if reply.FirstLine <= 0 || len(firsts) > 1000 || (len(firsts) > 1 && reply.FirstLine >= firsts[len(firsts)-2]) {
			t.Fatalf("firstLine %v does not move back to a positive line: a page repeats", firsts)
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
	// anchor gives each turn an anchor id, a time and (the last one) the sidechain mark.
	anchor bool
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
		if a.anchor {
			turns[i].AnchorID = "id" + strconv.Itoa(i)
			turns[i].TS = fmt.Sprintf("2026-11-05T10:00:%02dZ", i)
			turns[i].Sidechain = i == a.n-1
		}
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

// stepIndexedAnswerAgent serves turns with sparse source indexes, the way agy numbers them.
type stepIndexedAnswerAgent struct {
	agents.Agent
	turns *[]transcript.Turn
}

func (a stepIndexedAnswerAgent) Transcript(session.Meta) (agents.TranscriptData, bool) {
	return agents.TranscriptData{Turns: *a.turns, StepIndexed: true}, true
}

// On a step-indexed kind before and firstLine are source indexes, so a turn that materializes
// between two pages neither repeats one nor leaves a gap.
func TestAnswerTurnsPagesStepIndexedAcrossInsertedTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var turns []transcript.Turn
	text := func(idx int) transcript.Turn {
		return transcript.Turn{Role: "assistant", Idx: idx, Parts: []transcript.Part{{Kind: "text", Text: "m" + strconv.Itoa(idx)}}}
	}
	for i := 0; i < 180; i += 3 {
		turns = append(turns, text(i))
	}
	t.Cleanup(agentreg.SwapForTest(session.KindAgy, stepIndexedAnswerAgent{AgentOf(session.KindAgy), &turns}))
	session.WriteMeta(session.Meta{Name: "answer-turns-step", Dir: t.TempDir(), Kind: session.KindAgy})
	read := func(query string) answerTurnsReply {
		t.Helper()
		code, body := getAnswerTurns(t, "answer-turns-step", query)
		var reply answerTurnsReply
		if code != http.StatusOK || json.Unmarshal(body, &reply) != nil {
			t.Fatalf("%s: status %d body %.200s", query, code, body)
		}
		return reply
	}
	tail := read("limit=50")
	if !tail.HasMore || len(tail.Messages) != 50 || tail.FirstLine != 30 {
		t.Fatalf("tail = %+v, want 50 turns from index 30", tail)
	}
	// A row lands inside the page already read and one below the next page's boundary.
	turns = append(turns, text(31), text(7))
	slices.SortFunc(turns, func(a, b transcript.Turn) int { return a.Idx - b.Idx })
	older := read("limit=50&before=" + strconv.Itoa(tail.FirstLine))
	var got []string
	for _, m := range older.Messages {
		got = append(got, m.Parts[0].Text)
	}
	want := []string{"m0", "m3", "m6", "m7", "m9", "m12", "m15", "m18", "m21", "m24", "m27"}
	if older.HasMore || older.FirstLine != 0 || !slices.Equal(got, want) {
		t.Fatalf("older = %v %+v, want %v ending at the start", got, older, want)
	}
}
