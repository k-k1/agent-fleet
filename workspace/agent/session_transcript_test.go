package main

import "testing"

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
