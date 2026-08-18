package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// TestCollectTurnsWindow checks that a windowed read returns turns in chronological order
// carrying their ABSOLUTE line index (stable across windows/pages), for a sub-range and
// for the whole file.
func TestCollectTurnsWindow(t *testing.T) {
	asst := func(text string) string {
		return `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}`
	}
	user := func(text string) string {
		return `{"type":"user","message":{"content":"` + text + `"}}`
	}
	toLines := func(ss ...string) [][]byte {
		out := make([][]byte, 0, len(ss))
		for _, s := range ss {
			out = append(out, []byte(s))
		}
		return out
	}
	lines := toLines(user("u0"), asst("a1"), user("u2"), asst("a3"), user("u4"))

	got := CollectTurns(lines, 2, 5) // window [2,5): u2, a3, u4
	wantIdx := []int{2, 3, 4}
	wantText := []string{"u2", "a3", "u4"}
	if len(got) != 3 {
		t.Fatalf("window len=%d want 3 (%+v)", len(got), got)
	}
	for i := range got {
		if got[i].Idx != wantIdx[i] || got[i].Text != wantText[i] {
			t.Errorf("turn[%d] = idx %d %q, want idx %d %q", i, got[i].Idx, got[i].Text, wantIdx[i], wantText[i])
		}
	}

	all := CollectTurns(lines, 0, len(lines)) // whole file, chronological, idx from 0
	if len(all) != 5 || all[0].Idx != 0 || all[0].Text != "u0" || all[4].Idx != 4 || all[4].Text != "u4" {
		t.Errorf("full window mismatch: %+v", all)
	}
}

// TestCollectTasks folds a transcript's TaskCreate/TaskUpdate calls (incremental,
// unlike TodoWrite) back into the current ToDo list: sequential ids, a tasks[] batch,
// status/subject merges, and ignoring the hash-id TaskStop / read-only TaskList.
func TestCollectTasks(t *testing.T) {
	asstTool := func(name, input string) string {
		return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"` + name + `","input":` + input + `}]}}`
	}
	toLines := func(ss ...string) [][]byte {
		out := make([][]byte, 0, len(ss))
		for _, s := range ss {
			out = append(out, []byte(s))
		}
		return out
	}

	lines := toLines(
		asstTool("TaskCreate", `{"subject":"A","activeForm":"doing A"}`),                           // #1
		asstTool("TaskCreate", `{"tasks":[{"subject":"B"},{"subject":"C","activeForm":"C-ing"}]}`), // #2, #3
		asstTool("TaskList", `{}`),                                                     // read: ignored
		asstTool("TaskStop", `{"task_id":"b6ivf5eax"}`),                                // bg-agent stop: ignored
		asstTool("TaskUpdate", `{"taskId":"1","status":"completed"}`),                  // A done
		asstTool("TaskUpdate", `{"taskId":"2","status":"in_progress","subject":"B2"}`), // B active + renamed
		`{"type":"user","message":{"content":"noise"}}`,
	)

	got := CollectTasks(lines)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (%v)", len(got), got)
	}
	want := []transcript.Task{
		{ID: "1", Subject: "A", Active: "doing A", Status: "completed"},
		{ID: "2", Subject: "B2", Active: "", Status: "in_progress"},
		{ID: "3", Subject: "C", Active: "C-ing", Status: "pending"},
	}
	for i, w := range want {
		g := got[i]
		if g.ID != w.ID || g.Subject != w.Subject || g.Active != w.Active || g.Status != w.Status {
			t.Errorf("task[%d] = %+v, want %+v", i, g, w)
		}
	}

	if got := CollectTasks(toLines(`{"type":"user","message":{"content":"hi"}}`)); len(got) != 0 {
		t.Errorf("no Task calls: want empty, got %v", got)
	}
}

// TestSendUserFilePart checks that a SendUserFile tool_use becomes a "userfile" part
// carrying its (raw, unresolved) paths + caption, while empty/whitespace paths drop and
// an all-empty files list falls back to a plain tool trace.
func TestSendUserFilePart(t *testing.T) {
	asstTool := func(input string) []byte {
		return []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"SendUserFile","input":` + input + `}]}}`)
	}

	turn, ok := parseTurn(asstTool(`{"files":["repos/x/report.md"," /tmp/a.png ",""],"caption":"見て"}`), 0)
	if !ok || len(turn.Parts) != 1 {
		t.Fatalf("parse: ok=%v parts=%d (%+v)", ok, len(turn.Parts), turn.Parts)
	}
	p := turn.Parts[0]
	if p.Kind != "userfile" || p.Caption != "見て" {
		t.Fatalf("part = kind %q caption %q, want userfile/見て", p.Kind, p.Caption)
	}
	if len(p.Files) != 2 || p.Files[0] != "repos/x/report.md" || p.Files[1] != "/tmp/a.png" {
		t.Errorf("files = %v, want [repos/x/report.md /tmp/a.png]", p.Files)
	}

	// No usable files → not a userfile part (falls through to a faint tool trace).
	turn, ok = parseTurn(asstTool(`{"files":[""," "]}`), 0)
	if !ok || len(turn.Parts) != 1 || turn.Parts[0].Kind != "tool" {
		t.Errorf("empty files: want a single tool part, got ok=%v %+v", ok, turn.Parts)
	}
}

func TestClaudeDelegationPart(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a1","name":"Agent","input":{"description":"調査する","prompt":"詳しく調べて","subagent_type":"Explore","model":"haiku"}}]}}`),
		[]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a1","content":"調査結果です"}]}}`),
	}
	turns := CollectTurns(lines, 0, len(lines))
	if len(turns) != 1 || len(turns[0].Parts) != 1 {
		t.Fatalf("turns = %+v, want one delegation turn", turns)
	}
	p := turns[0].Parts[0]
	if p.Kind != "delegation" || p.Info != "調査する" || p.Prompt != "詳しく調べて" ||
		p.AgentType != "Explore" || p.Model != "haiku" || p.Status != "completed" || p.Output != "調査結果です" {
		t.Fatalf("delegation = %+v", p)
	}

	background := [][]byte{
		[]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"bg1","name":"Agent","input":{"description":"並列調査","prompt":"調べて","subagent_type":"Explore","run_in_background":true}}]}}`),
		[]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"bg1","content":"agent launched"}]}}`),
	}
	bg := CollectTurns(background, 0, len(background))[0].Parts[0]
	if bg.Status != "requested" || bg.Output != "" {
		t.Fatalf("background delegation = %+v, want requested without launch acknowledgement", bg)
	}
}

// TestCollectInteractionAnswers models the reported "AUQ回答がミラーに反映されない" bug:
// claude writes an AskUserQuestion tool_use at ASK time and its tool_result lands many
// lines later (bookkeeping in between), so a live increment / page boundary can split them
// and CollectTurns' window-local resolution leaves the question unanswered. The
// whole-transcript map recovers the answer regardless of any window.
func TestCollectInteractionAnswers(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"どちらにしますか"}]}}`),
		[]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"q1","name":"AskUserQuestion","input":{"questions":[{"header":"範囲","question":"どれ","options":[{"label":"全部"},{"label":"一部"}]}]}}]}}`),
		// Bookkeeping the CLI writes while the question is pending (a real ~10-min gap here).
		[]byte(`{"type":"custom-title","title":"x"}`),
		[]byte(`{"type":"mode","mode":"normal"}`),
		[]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"q1","content":"Your questions have been answered: \"どれ\"=\"全部\". You can now continue."}]}}`),
		// A plan and a foreground/background delegation, to lock in the three tool classes.
		[]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"p1","name":"ExitPlanMode","input":{"plan":"# Plan"}}]}}`),
		[]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"p1","content":"User approved the plan"}]}}`),
		[]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"bg1","name":"Agent","input":{"description":"並列","prompt":"go","subagent_type":"Explore","run_in_background":true}}]}}`),
		[]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"bg1","content":"agent launched"}]}}`),
	}
	ans := CollectInteractionAnswers(lines)
	if got := ans["q1"]; got.Text != `Your questions have been answered: "どれ"="全部". You can now continue.` || got.Declined {
		t.Errorf("question answer = %+v", got)
	}
	if got := ans["p1"]; got.Text != "User approved the plan" || got.Declined {
		t.Errorf("plan answer = %+v", got)
	}
	// A background delegation only ever gets a launch ack, never a final report — excluded,
	// mirroring assistantParts' QID gating, so its card is never falsely marked completed.
	if _, ok := ans["bg1"]; ok {
		t.Errorf("background delegation must not be in the map: %v", ans["bg1"])
	}
	// The split that triggers the bug: a window with the question but NOT its tool_result
	// yields an empty in-window Answer — which the map then covers.
	split := CollectTurns(lines, 0, 4) // holds q1's tool_use (line 1), not its result (line 4)
	var q *transcript.Part
	for i := range split {
		for pi := range split[i].Parts {
			if split[i].Parts[pi].Kind == "question" {
				q = &split[i].Parts[pi]
			}
		}
	}
	if q == nil || q.Answer != "" {
		t.Fatalf("windowed question = %+v, want present with empty Answer", q)
	}
	if ans[q.QID].Text == "" {
		t.Errorf("map must carry the answer for the split-out question qid %q", q.QID)
	}
}

// TestCollectInteractionAnswers_Declined pins the fix for "回答済みと表示されるのに
// 中身は却下の定型文" (docs/dev/92 §6): an Escape/interrupt out of AskUserQuestion
// (e.g. the preview free-text bug — a free-text answer lands on the unnumbered "Chat
// about this" row and Enter activates it) surfaces as an is_error tool_result carrying
// claude's own "wants to clarify"/"(No answer provided)" boilerplate — real transcript
// text, captured from the reported session. That must be flagged Declined so the
// Console can show "却下" instead of rendering it as an answered card.
func TestCollectInteractionAnswers_Declined(t *testing.T) {
	// Real transcript text (captured from the reported session) contains nested quotes and
	// newlines, so build the JSON line with json.Marshal instead of hand-quoting it.
	declineText := "The user doesn't want to proceed with this tool use. The tool use was rejected " +
		"(eg. if it was a file edit, the new_string was NOT written to the file). To tell you how " +
		"to proceed, the user said:\nThe user wants to clarify these questions.\n\n    " +
		"Questions asked:\n- \"どれにしますか？\"\n  (No answer provided)"
	declineResult, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"content": []map[string]any{{"type": "tool_result", "tool_use_id": "q1", "is_error": true, "content": declineText}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := [][]byte{
		[]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"q1","name":"AskUserQuestion","input":{"questions":[{"header":"方式","question":"どれにしますか？","options":[{"label":"案A"},{"label":"案B"}]}]}}]}}`),
		declineResult,
		// A genuine free-text answer also has is_error=false normally, but even an
		// unrelated is_error tool_result (e.g. a validation failure) must not be
		// misread as a decline — only THIS specific boilerplate should flag it.
		[]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"q2","name":"AskUserQuestion","input":{"questions":[{"header":"方式","question":"別の質問","options":[{"label":"案A"},{"label":"案B"}]}]}}]}}`),
		[]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"q2","is_error":true,"content":"malformed AskUserQuestion input"}]}}`),
	}
	ans := CollectInteractionAnswers(lines)
	if got := ans["q1"]; !got.Declined {
		t.Errorf("q1 = %+v, want Declined=true (claude's decline boilerplate)", got)
	}
	if got := ans["q2"]; got.Declined {
		t.Errorf("q2 = %+v, want Declined=false (is_error alone isn't a decline)", got)
	}

	// Same signal must reach CollectTurns' window-local resolution (Part.Declined), not
	// only the whole-transcript map — a live window that holds both the tool_use and its
	// declined tool_result must not wait for the Console's late patch to show it.
	turns := CollectTurns(lines, 0, len(lines))
	var q1 *transcript.Part
	for i := range turns {
		for pi := range turns[i].Parts {
			if turns[i].Parts[pi].QID == "q1" {
				q1 = &turns[i].Parts[pi]
			}
		}
	}
	if q1 == nil || !q1.Declined {
		t.Fatalf("windowed q1 = %+v, want Declined=true", q1)
	}
}

// TestQuestionOptionPreview pins the option `preview` — the mockup an agent attaches so
// the choices can be compared — surviving into the question part. It used to be dropped
// by transcript.Option, leaving the mirror with two labels whose difference was visible
// only in the CLI. Whitespace is the content here, so it must arrive byte-for-byte.
func TestQuestionOptionPreview(t *testing.T) {
	line := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"q1","name":"AskUserQuestion",` +
		`"input":{"questions":[{"header":"様式","question":"どれ","options":[` +
		`{"label":"左レール","description":"縦に並べる","preview":"┌───┬───┐\n│ a │ b │\n└───┴───┘"},` +
		`{"label":"タブのまま"}]}]}}]}}`)
	turn, ok := parseTurn(line, 0)
	if !ok || len(turn.Parts) == 0 || turn.Parts[0].Kind != "question" {
		t.Fatalf("question turn = %+v", turn)
	}
	opts := turn.Parts[0].Questions[0].Options
	if want := "┌───┬───┐\n│ a │ b │\n└───┴───┘"; opts[0].Preview != want {
		t.Errorf("preview = %q, want %q", opts[0].Preview, want)
	}
	if opts[1].Preview != "" {
		t.Errorf("option without a preview must stay empty, got %q", opts[1].Preview)
	}
}

// TestQueuedCommandTurn checks that a mid-run steering prompt — logged only as an
// attachment/queued_command event, never as a user line (claude ≥2.1.207) — parses
// into a plain user turn, and that non-human / non-queued attachments stay invisible.
//
// AnchorID が空であることも固定する: 付けると「ここから分岐」の導線が出て、cutIndex が
// type:"user" 以外を拒むので必ず 400 になる（実セッションで再現）。
func TestQueuedCommandTurn(t *testing.T) {
	line := []byte(`{"type":"attachment","uuid":"f023bf25-4b3e-4d2d-aad3-f601d9a035c2","attachment":{"type":"queued_command","prompt":" origin/mainをマージして ","commandMode":"prompt","origin":{"kind":"human"}},"timestamp":"2026-07-11T09:16:51.851Z","gitBranch":"feat/x","cwd":"/w"}`)
	turn, ok := parseTurn(line, 7)
	if !ok {
		t.Fatalf("queued_command: not parsed")
	}
	if turn.Role != "user" || turn.Text != "origin/mainをマージして" || turn.Idx != 7 ||
		turn.TS != "2026-07-11T09:16:51.851Z" || turn.Branch != "feat/x" || turn.Cwd != "/w" {
		t.Errorf("turn = %+v", turn)
	}
	if turn.AnchorID != "" {
		t.Errorf("AnchorID = %q, want empty (割り込み発言からは分岐できない)", turn.AnchorID)
	}
	if len(turn.Parts) != 1 || turn.Parts[0].Kind != "text" || turn.Parts[0].Text != "origin/mainをマージして" {
		t.Errorf("parts = %+v", turn.Parts)
	}

	for name, ln := range map[string]string{
		"other attachment": `{"type":"attachment","attachment":{"type":"task_reminder"}}`,
		"non-human origin": `{"type":"attachment","attachment":{"type":"queued_command","prompt":"x","origin":{"kind":"assistant"}}}`,
		"empty prompt":     `{"type":"attachment","attachment":{"type":"queued_command","prompt":"  ","origin":{"kind":"human"}}}`,
	} {
		if _, ok := parseTurn([]byte(ln), 0); ok {
			t.Errorf("%s: parsed as a turn, want dropped", name)
		}
	}
}

// TestCollectQueued reconstructs the live mid-run queue: enqueue adds, remove drops its
// first match, a content-less op clears, and a real user prompt line clears leftovers
// (while tool_result / meta user lines don't).
func TestCollectQueued(t *testing.T) {
	qop := func(op, content string) string {
		return `{"type":"queue-operation","operation":"` + op + `","content":"` + content + `"}`
	}
	toLines := func(ss ...string) [][]byte {
		out := make([][]byte, 0, len(ss))
		for _, s := range ss {
			out = append(out, []byte(s))
		}
		return out
	}

	got := CollectQueued(toLines(qop("enqueue", "a"), qop("enqueue", "b"), qop("remove", "a")))
	if len(got) != 1 || got[0] != "b" {
		t.Errorf("enqueue/remove: got %v, want [b]", got)
	}

	// tool_result and meta user lines keep the queue; a real prompt clears it.
	got = CollectQueued(toLines(
		qop("enqueue", "a"),
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
		`{"type":"user","isMeta":true,"message":{"content":"<command-name>/foo</command-name>"}}`,
	))
	if len(got) != 1 || got[0] != "a" {
		t.Errorf("tool_result/meta: got %v, want [a]", got)
	}
	got = CollectQueued(toLines(qop("enqueue", "a"), `{"type":"user","message":{"content":"next prompt"}}`))
	if len(got) != 0 {
		t.Errorf("user prompt should clear stale queue, got %v", got)
	}

	// A content-less queue op clears everything.
	got = CollectQueued(toLines(qop("enqueue", "a"), qop("enqueue", "b"), `{"type":"queue-operation","operation":"clear"}`))
	if len(got) != 0 {
		t.Errorf("clear: got %v, want empty", got)
	}
}

func TestHasConversation(t *testing.T) {
	toLines := func(ss ...string) [][]byte {
		out := make([][]byte, 0, len(ss))
		for _, s := range ss {
			out = append(out, []byte(s))
		}
		return out
	}
	cases := []struct {
		name  string
		lines [][]byte
		want  bool
	}{
		{"empty", nil, false},
		{"bridge stub only", toLines(`{"type":"bridge-session"}`, `{"type":"summary","summary":"x"}`), false},
		{"has user turn", toLines(`{"type":"summary"}`, `{"type":"user","message":{"content":"hi"}}`), true},
		{"has assistant turn", toLines(`{"type":"assistant","message":{"content":[]}}`), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HasConversation(c.lines); got != c.want {
				t.Fatalf("HasConversation = %v, want %v", got, c.want)
			}
		})
	}
}

// TestReadJSONLLinesPartialTail pins the cursor-safety rule: a line that is still being
// written must not be counted. claude appends in 4 KiB chunks, so a poll can read a log
// mid-line; counting the fragment would advance /messages' cursor (= line count) past a
// line the parser can't read, and the client — which only ever asks for lines AFTER its
// cursor — would never receive that turn (the bug that lost a session's first prompt and
// left its optimistic echo stuck at 「反映待ち」).
func TestReadJSONLLinesPartialTail(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "x.jsonl")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	user := `{"type":"user","message":{"content":"hi"}}`
	asst := `{"type":"assistant","message":{"content":[{"type":"text","text":"yo"}]}}`

	t.Run("complete file keeps every line", func(t *testing.T) {
		got := readJSONLLines(write(t, user+"\n"+asst+"\n"))
		if len(got) != 2 {
			t.Fatalf("len=%d want 2 (%q)", len(got), got)
		}
	})
	t.Run("half-written last line is not a line yet", func(t *testing.T) {
		// The mid-write state: two whole events, then the first 4 KiB chunk of a third.
		got := readJSONLLines(write(t, user+"\n"+asst+"\n"+user[:20]))
		if len(got) != 2 {
			t.Fatalf("len=%d want 2 — the fragment must not advance the cursor (%q)", len(got), got)
		}
		if string(got[0]) != user || string(got[1]) != asst {
			t.Fatalf("kept lines = %q, want the two complete events", got)
		}
	})
	t.Run("nothing but a fragment reads as empty", func(t *testing.T) {
		if got := readJSONLLines(write(t, user[:20])); len(got) != 0 {
			t.Fatalf("len=%d want 0 (%q)", len(got), got)
		}
	})
}

func TestCollectFileEdits(t *testing.T) {
	line := func(ts, cwd string, sidechain bool, blocks string) string {
		s := `{"type":"assistant","timestamp":"` + ts + `","cwd":"` + cwd + `"`
		if sidechain {
			s += `,"isSidechain":true`
		}
		return s + `,"message":{"content":[` + blocks + `]}}`
	}
	tool := func(name, input string) string {
		return `{"type":"tool_use","name":"` + name + `","input":` + input + `}`
	}
	toLines := func(ss ...string) [][]byte {
		out := make([][]byte, 0, len(ss))
		for _, s := range ss {
			out = append(out, []byte(s))
		}
		return out
	}

	lines := toLines(
		`{"type":"user","message":{"content":"直して"}}`,
		line("2026-08-17T10:00:00Z", "/h/repos/r", false,
			tool("Edit", `{"file_path":"/h/repos/r/a.ts","old_string":"x","new_string":"y"}`)+","+
				tool("Bash", `{"command":"ls"}`)), // no file coordinate → not an edit
		line("2026-08-17T10:01:00Z", "/h/repos/r", true,
			tool("Write", `{"file_path":"b.ts","content":"1\n2\n"}`)),
		line("2026-08-17T10:02:00Z", "/h/repos/r", false,
			tool("Read", `{"file_path":"/h/repos/r/a.ts"}`)), // reads carry file_path but aren't edits
	)

	got := CollectFileEdits(lines, 0)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (%+v)", len(got), got)
	}
	if got[0].Path != "/h/repos/r/a.ts" || got[0].Verb != "edit" || got[0].Idx != 1 || got[0].Sidechain {
		t.Fatalf("first = %+v", got[0])
	}
	if got[0].Added != 1 || got[0].Removed != 1 {
		t.Fatalf("first stat = +%d -%d, want +1 -1", got[0].Added, got[0].Removed)
	}
	// A Write is all-insert, so it reads as an addition; the relative path travels with
	// the turn's cwd and is anchored by the caller, not here.
	if got[1].Path != "b.ts" || got[1].Cwd != "/h/repos/r" || got[1].Verb != "add" || got[1].Added != 2 {
		t.Fatalf("second = %+v", got[1])
	}
	if !got[1].Sidechain {
		t.Fatal("second edit happened in a subagent sidechain")
	}

	// `from` skips already-folded lines: the jsonl is append-only, so a prefix's answer
	// never changes and the caller only pays for the tail.
	tail := CollectFileEdits(lines, 2)
	if len(tail) != 1 || tail[0].Path != "b.ts" {
		t.Fatalf("tail = %+v", tail)
	}
}
