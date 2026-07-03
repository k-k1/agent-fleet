package main

import "testing"

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

	got := collectTurns(lines, 2, 5) // window [2,5): u2, a3, u4
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

	all := collectTurns(lines, 0, len(lines)) // whole file, chronological, idx from 0
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

	got := collectTasks(lines)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (%v)", len(got), got)
	}
	want := []taskItem{
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

	if got := collectTasks(toLines(`{"type":"user","message":{"content":"hi"}}`)); len(got) != 0 {
		t.Errorf("no Task calls: want empty, got %v", got)
	}
}
