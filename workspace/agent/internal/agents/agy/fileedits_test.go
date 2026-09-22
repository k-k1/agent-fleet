package agy

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// The step contents below are VERBATIM from a live agy session's own transcript_full.jsonl
// (measured 2026-09-22), including the trailing model-facing sentences and the diff block's
// own explanation. They are the whole contract this kind has — agy records no tool arguments —
// so pinning them verbatim is what makes a reworded upstream sentence show up as a red test
// instead of an empty strip.
const (
	agyCreateStep = `Created file file:///home/dev/probe-agy/probe.txt with requested content.
If relevant, proactively run terminal commands to execute this code for the USER. Don't ask for permission.`

	agyEditStep = `The following changes were made by the replace_file_content tool to: /home/dev/probe-agy/probe.txt. If relevant, proactively run terminal commands to execute this code for the USER. Don't ask for permission.
[diff_block_start]
@@ -1,2 +1,2 @@
-hello
+world

[diff_block_end]

Please note that the above snippet only shows the MODIFIED lines from the last change. It shows up to 3 lines of unchanged lines before and after the modified lines. The actual file contents may have many more lines not shown.`

	// A READ. It names a path at least as prominently as the two above, and counting it
	// would put files in the list that the session only looked at.
	agyReadStep = "File Path: `file:///home/dev/probe-agy/probe.txt`\n" +
		`Total Lines: 2
Total Bytes: 6
Showing lines 1 to 2
1: world
2: `
)

func TestStepEditReadsTheRecordedSentences(t *testing.T) {
	file, verb, edits := stepEdit(agyCreateStep)
	if file != "/home/dev/probe-agy/probe.txt" {
		t.Fatalf("create step file = %q", file)
	}
	// The file:// scheme has to come off — the mirror opens a path, not a URL.
	if strings.Contains(file, "file://") {
		t.Fatalf("create step path still carries its URL scheme: %q", file)
	}
	if transcript.EditVerb(verb, edits) != "add" {
		t.Fatalf("create step verb = %q, want add", transcript.EditVerb(verb, edits))
	}
	if edits != nil {
		t.Fatalf("create step edits = %+v, want none (agy never echoes what it wrote)", edits)
	}

	file, verb, edits = stepEdit(agyEditStep)
	if file != "/home/dev/probe-agy/probe.txt" {
		t.Fatalf("edit step file = %q (the trailing period must not be part of the path)", file)
	}
	if transcript.EditVerb(verb, edits) != "edit" {
		t.Fatalf("edit step verb = %q, want edit", transcript.EditVerb(verb, edits))
	}
	if len(edits) != 1 {
		t.Fatalf("edit step edits = %+v, want the diff block as one before/after", edits)
	}
	if !strings.Contains(edits[0].Old, "hello") || strings.Contains(edits[0].Old, "world") {
		t.Fatalf("before-image = %q", edits[0].Old)
	}
	if !strings.Contains(edits[0].New, "world") || strings.Contains(edits[0].New, "hello") {
		t.Fatalf("after-image = %q", edits[0].New)
	}
	// The +/- the strip shows comes from those two sides, so it has to match the diff.
	if a, r := transcript.EditStat(edits); a != 1 || r != 1 {
		t.Fatalf("EditStat = +%d -%d, want +1 -1", a, r)
	}
}

// The negative control this text contract lives or dies by.
func TestStepEditIgnoresReadsAndOutput(t *testing.T) {
	cases := []string{
		agyReadStep,
		"", // a step with no content at all
		"ls -la\nprobe.txt\nprobe2.txt",
		// The sentence, but quoted inside a command's own output rather than being the
		// step's own report. Both patterns are anchored at the start for exactly this.
		"$ cat log.txt\nCreated file file:///tmp/x.txt with requested content.",
		"Error invalid tool call: parse problem",
	}
	for _, c := range cases {
		if file, verb, edits := stepEdit(c); file != "" || verb != "" || edits != nil {
			t.Fatalf("stepEdit(%q) = (%q, %q, %+v), want no edit record", c, file, verb, edits)
		}
	}
}

// End to end through the real transcript reader: the step's bookkeeping lines are stripped
// before the prose is matched, and a row reaches what the aggregation folds. The step TYPE is
// deliberately GENERIC here — that is what a live agy session actually recorded for these
// steps in 2026-09, even though docs/log/32 saw CODE_ACTION in 2026-07, so nothing may gate
// on the type.
func TestTranscriptStepsCarryTheWrittenFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/probe-agy"
	m := session.Meta{Dir: dir, Name: "slot-files", Kind: session.KindAgy}
	sids.Write(session.UUID(dir, "slot-files"), "conv-files")

	step := func(i int, typ, content string) string {
		return `{"step_index":` + itoa(i) + `,"source":"MODEL","type":"` + typ + `","status":"DONE","content":` +
			quote("Created At: 2026-09-22T17:05:36+09:00\nCompleted At: 2026-09-22T17:05:36+09:00\n"+content) + "}"
	}
	body := strings.Join([]string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>\nwrite it\n</USER_REQUEST>"}`,
		step(2, "GENERIC", agyCreateStep),
		step(4, "GENERIC", agyEditStep),
		step(6, "GENERIC", agyReadStep),
		`{"step_index":8,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"done"}`,
	}, "\n") + "\n"
	writeTranscript(t, "conv-files", "transcript_full.jsonl", body)

	td, ok := agentImpl{}.Transcript(m)
	if !ok {
		t.Fatal("Transcript reported unsupported")
	}
	var edits []transcript.FileEdit
	for _, tn := range td.Turns {
		edits = append(edits, transcript.FileEditsInTurn(tn)...)
	}
	// Two writes, one read: the read is the negative control, in the same conversation.
	if len(edits) != 2 {
		t.Fatalf("FileEditsInTurn over the session = %+v, want the create and the edit only", edits)
	}
	if edits[0].Verb != "add" || edits[1].Verb != "edit" {
		t.Fatalf("verbs = %q, %q, want add then edit", edits[0].Verb, edits[1].Verb)
	}
	for _, e := range edits {
		if e.Path != filepath.Join(dir, "probe.txt") {
			t.Fatalf("folded edit names %q", e.Path)
		}
	}
}

func itoa(i int) string { return string(rune('0' + i)) }

// quote JSON-quotes a step's content the way the store does.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
