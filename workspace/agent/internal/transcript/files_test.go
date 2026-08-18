package transcript

import "testing"

func TestEditStatMatchesLineDiff(t *testing.T) {
	// The expectations below are what console/src/features/viewer/DiffView.tsx lineDiff
	// produces for the same inputs (added = "add" rows, removed = "del" rows). If this
	// test and that function ever disagree, the strip's +N −M would contradict the diff
	// the row opens.
	cases := []struct {
		name             string
		old, new         string
		wantAdd, wantDel int
	}{
		{"unchanged", "a\nb\nc", "a\nb\nc", 0, 0},
		{"one line replaced", "a\nb\nc", "a\nB\nc", 1, 1},
		{"pure insert", "a\nc", "a\nb\nc", 1, 0},
		{"pure delete", "a\nb\nc", "a\nc", 0, 1},
		{"write (no old side)", "", "a\nb", 2, 0},
		{"delete (no new side)", "a\nb", "", 0, 2},
		{"both empty", "", "", 0, 0},
		{"trailing newline is not a line", "a\n", "a\nb\n", 1, 0},
		{"reordered keeps the longest common run", "a\nb\nc\nd", "b\nc\nd\na", 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			add, del := EditStat([]Edit{{Old: c.old, New: c.new}})
			if add != c.wantAdd || del != c.wantDel {
				t.Fatalf("EditStat = +%d -%d, want +%d -%d", add, del, c.wantAdd, c.wantDel)
			}
		})
	}
}

func TestEditStatSumsEveryEdit(t *testing.T) {
	add, del := EditStat([]Edit{{Old: "a", New: "A"}, {Old: "", New: "x\ny"}})
	if add != 3 || del != 1 {
		t.Fatalf("EditStat = +%d -%d, want +3 -1", add, del)
	}
}

func TestEditVerb(t *testing.T) {
	cases := []struct {
		name     string
		explicit string
		edits    []Edit
		want     string
	}{
		{"parser says add", "add", nil, "add"},
		{"parser says delete", "delete", nil, "delete"},
		{"codex patch wording is normalised", "update", []Edit{{Old: "a", New: "b"}}, "edit"},
		{"all-insert is an add", "", []Edit{{Old: "", New: "x"}}, "add"},
		{"any old side makes it an edit", "", []Edit{{Old: "", New: "x"}, {Old: "y", New: "z"}}, "edit"},
		// ⚠️ The regression this guards: a kind that carries no diff bodies at all
		// (cursor / copilot) must NOT have every file it touched labelled 削除.
		{"no bodies and no verdict is an edit, never a delete", "", nil, "edit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EditVerb(c.explicit, c.edits); got != c.want {
				t.Fatalf("EditVerb = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFileEditsInTurn(t *testing.T) {
	turn := Turn{
		Idx: 7, TS: "2026-08-17T12:00:00+09:00", Cwd: "/home/dev/repos/x", Sidechain: true,
		Parts: []Part{
			{Kind: "text", Text: "書きます"},
			{Kind: "tool", Tool: "Bash", Info: "ls"}, // no File — not an edit
			{Kind: "tool", Tool: "Read", File: ""},   // ditto
			{Kind: "tool", Tool: "Write", File: "a.txt", Edits: []Edit{{Old: "", New: "1\n2"}}},
			{Kind: "tool", Tool: "apply_patch", File: "b.txt", Verb: "delete"},
		},
	}
	got := FileEditsInTurn(turn)
	if len(got) != 2 {
		t.Fatalf("got %d edits, want 2: %+v", len(got), got)
	}
	if got[0].Path != "a.txt" || got[0].Verb != "add" || got[0].Added != 2 {
		t.Fatalf("first edit = %+v", got[0])
	}
	if got[1].Path != "b.txt" || got[1].Verb != "delete" {
		t.Fatalf("second edit = %+v", got[1])
	}
	for _, e := range got {
		if e.Idx != 7 || e.TS != turn.TS || e.Cwd != turn.Cwd || !e.Sidechain {
			t.Fatalf("turn context not carried: %+v", e)
		}
	}
}
