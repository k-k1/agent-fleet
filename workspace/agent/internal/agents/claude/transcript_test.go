package claude

import (
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

// TestQueuedCommandTurn checks that a mid-run steering prompt — logged only as an
// attachment/queued_command event, never as a user line (claude ≥2.1.207) — parses
// into a plain user turn, and that non-human / non-queued attachments stay invisible.
func TestQueuedCommandTurn(t *testing.T) {
	line := []byte(`{"type":"attachment","attachment":{"type":"queued_command","prompt":" origin/mainをマージして ","commandMode":"prompt","origin":{"kind":"human"}},"timestamp":"2026-07-11T09:16:51.851Z","gitBranch":"feat/x","cwd":"/w"}`)
	turn, ok := parseTurn(line, 7)
	if !ok {
		t.Fatalf("queued_command: not parsed")
	}
	if turn.Role != "user" || turn.Text != "origin/mainをマージして" || turn.Idx != 7 ||
		turn.TS != "2026-07-11T09:16:51.851Z" || turn.Branch != "feat/x" || turn.Cwd != "/w" {
		t.Errorf("turn = %+v", turn)
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
