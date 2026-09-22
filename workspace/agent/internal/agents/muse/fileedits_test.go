package muse

import (
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// The `args` strings below are VERBATIM from a live muse session's own view journal
// (~/.local/share/muse/sessions/.msp-view-v1/<sid>/journal-*.bin, measured 2026-09-22).
// Keeping them verbatim is the point: `edit_file` names its sides find/replace, which no
// amount of reading the schema would have told us — msp.Item carries `args` as an opaque
// string.
const (
	museWriteArgs = `{"content":"hello\n","path":"/home/dev/probe-muse/probe.txt"}`
	museEditArgs  = `{"find":"hello","path":"/home/dev/probe-muse/probe.txt","replace":"world"}`
)

// TestToolEditsReadsTheRecordedCalls pins what the strip and the inline diff are fed,
// including the verb and the +/- the reader sees (transcript.EditVerb / EditStat).
func TestToolEditsReadsTheRecordedCalls(t *testing.T) {
	cases := []struct {
		name             string
		tool, args       string
		wantFile         string
		wantVerb         string
		wantEdits        []transcript.Edit
		wantAdd, wantRem int
	}{
		{
			name: "write_file is an add of every line", tool: "write_file", args: museWriteArgs,
			wantFile:  "/home/dev/probe-muse/probe.txt",
			wantVerb:  "add",
			wantEdits: []transcript.Edit{{Old: "", New: "hello\n"}},
			wantAdd:   1, wantRem: 0,
		},
		{
			name: "edit_file carries find and replace as before/after", tool: "edit_file", args: museEditArgs,
			wantFile:  "/home/dev/probe-muse/probe.txt",
			wantVerb:  "edit",
			wantEdits: []transcript.Edit{{Old: "hello", New: "world"}},
			wantAdd:   1, wantRem: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file, verb, edits := toolEdits(c.tool, c.args)
			if file != c.wantFile {
				t.Fatalf("file = %q, want %q", file, c.wantFile)
			}
			if !reflect.DeepEqual(edits, c.wantEdits) {
				t.Fatalf("edits = %+v, want %+v", edits, c.wantEdits)
			}
			if got := transcript.EditVerb(verb, edits); got != c.wantVerb {
				t.Fatalf("verb = %q, want %q", got, c.wantVerb)
			}
			if a, r := transcript.EditStat(edits); a != c.wantAdd || r != c.wantRem {
				t.Fatalf("EditStat = +%d -%d, want +%d -%d", a, r, c.wantAdd, c.wantRem)
			}
		})
	}
}

// The negative control: only the two measured tools may produce a row. `bash` mutates files
// too and names none of them; `read_file` names one and changes nothing. Both would put files
// in the list that this session never edited.
func TestToolEditsIgnoresEverythingElse(t *testing.T) {
	cases := []struct{ tool, args string }{
		{"bash", `{"command":"sed -i s/a/b/ /home/dev/probe-muse/probe.txt"}`},
		{"read_file", `{"path":"/home/dev/probe-muse/probe.txt"}`},
		{"grep", `{"pattern":"hello","path":"/home/dev/probe-muse"}`},
		{"write_file", ``},                 // no args recorded
		{"write_file", `{"content":"x"}`},  // no path to place it at
		{"edit_file", `not json`},          // garbage
		{"apply_patch", `{"patch":"..."}`}, // a known mutator whose shape is NOT measured
	}
	for _, c := range cases {
		if file, verb, edits := toolEdits(c.tool, c.args); file != "" || verb != "" || edits != nil {
			t.Fatalf("toolEdits(%q, %q) = (%q, %q, %+v), want no edit record", c.tool, c.args, file, verb, edits)
		}
	}
}

// End to end from the item to what the aggregation folds, through the same toolPart() the
// mirror uses: the parser and the wiring can each look right while the chain stays broken.
func TestTurnsFromItemsCarryTheEditTarget(t *testing.T) {
	agent := item(msp.ItemKindAgentMessage, "i-text", 1)
	agent.Text = sp("editing")
	write := item(msp.ItemKindToolCall, "i-write", 1)
	write.Tool = sp("write_file")
	write.Args = sp(museWriteArgs)
	edit := item(msp.ItemKindToolCall, "i-edit", 1)
	edit.Tool = sp("edit_file")
	edit.Args = sp(museEditArgs)
	shell := item(msp.ItemKindToolCall, "i-shell", 1)
	shell.Tool = sp("bash")
	shell.CommandText = sp("go build ./...")

	turns := turnsFromItems([]msp.Item{agent, write, edit, shell})
	var edits []transcript.FileEdit
	for _, tn := range turns {
		edits = append(edits, transcript.FileEditsInTurn(tn)...)
	}
	// Two calls, one file: the bash call in the same turn is the negative control.
	if len(edits) != 2 {
		t.Fatalf("FileEditsInTurn over the session = %+v, want the two edit-family calls", edits)
	}
	for _, e := range edits {
		if e.Path != "/home/dev/probe-muse/probe.txt" {
			t.Fatalf("folded edit names %q", e.Path)
		}
		// Paths are absolute in this kind's items (measured), which is what lets a row be
		// placed without a turn Cwd — sessionx drops a relative path that has none.
		if !strings.HasPrefix(e.Path, "/") {
			t.Fatalf("path %q is not absolute; this kind would now need Turn.Cwd", e.Path)
		}
	}
	if edits[0].Verb != "add" || edits[1].Verb != "edit" {
		t.Fatalf("verbs = %q, %q, want add then edit", edits[0].Verb, edits[1].Verb)
	}
}
