package kiro

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// The toolUse payloads below are VERBATIM lines from a live kiro session's own store
// (~/.kiro/sessions/cli/<sid>.jsonl, measured 2026-09-22): three write calls and one that is
// not a write at all. Keeping them verbatim is the point — the v2 engine records `write` with
// camelCase arguments, not the `fs_write`/snake_case spelling the binary's tool catalogue
// documents, and a hand-written fixture would have quietly encoded the wrong one.
const (
	kiroCreateCall = `{"toolUseId":"tooluse_A2kidZunt4af5NTEMI5sKY","name":"write","input":{"__tool_use_purpose":"Create probe.txt with content \"hello\"","command":"create","path":"/home/dev/probe-kiro/probe.txt","content":"hello\n"}}`
	kiroReplaceCal = `{"toolUseId":"tooluse_SeLca1xDbthOJ0ICAHzFO8","name":"write","input":{"__tool_use_purpose":"Replace \"hello\" with \"world\" in probe.txt","command":"strReplace","path":"/home/dev/probe-kiro/probe.txt","oldStr":"hello","newStr":"world"}}`
	kiroPeerCall   = `{"toolUseId":"tooluse_91aJ3XYgo04xnTMNb7iCWa","name":"send_to_peer_session","input":{"__tool_use_purpose":"Reporting task completion","name":"s46ndzx","intent":"answer","message":"done"}}`
)

func parseToolUse(t *testing.T, raw string) toolUseData {
	t.Helper()
	var tu toolUseData
	if err := json.Unmarshal([]byte(raw), &tu); err != nil {
		t.Fatalf("the captured payload no longer parses as a toolUse block: %v", err)
	}
	return tu
}

// TestToolEditsReadsTheRecordedWriteCalls pins what the strip and the inline diff are fed,
// including the derived verb and the +/- the reader sees (transcript.EditVerb / EditStat).
func TestToolEditsReadsTheRecordedWriteCalls(t *testing.T) {
	cases := []struct {
		name             string
		raw              string
		wantFile         string
		wantVerb         string
		wantEdits        []transcript.Edit
		wantAdd, wantRem int
	}{
		{
			name:     "create is an add of every line",
			raw:      kiroCreateCall,
			wantFile: "/home/dev/probe-kiro/probe.txt", wantVerb: "add",
			wantEdits: []transcript.Edit{{Old: "", New: "hello\n"}},
			wantAdd:   1, wantRem: 0,
		},
		{
			name:     "strReplace carries both sides",
			raw:      kiroReplaceCal,
			wantFile: "/home/dev/probe-kiro/probe.txt", wantVerb: "edit",
			wantEdits: []transcript.Edit{{Old: "hello", New: "world"}},
			wantAdd:   1, wantRem: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tu := parseToolUse(t, c.raw)
			file, verb, edits := toolEdits(tu)
			if file != c.wantFile {
				t.Fatalf("file = %q, want %q", file, c.wantFile)
			}
			if !reflect.DeepEqual(edits, c.wantEdits) {
				t.Fatalf("edits = %+v, want %+v", edits, c.wantEdits)
			}
			// The verb the mirror shows is the stated one when there is one, and the derived
			// one otherwise — check what the reader actually ends up with.
			if got := transcript.EditVerb(verb, edits); got != c.wantVerb {
				t.Fatalf("verb = %q, want %q", got, c.wantVerb)
			}
			if a, r := transcript.EditStat(edits); a != c.wantAdd || r != c.wantRem {
				t.Fatalf("EditStat = +%d -%d, want +%d -%d", a, r, c.wantAdd, c.wantRem)
			}
		})
	}
}

// The legacy spelling the binary's catalogue documents (fs_write / snake_case) must work too:
// a session on the older engine records that shape, and reading only one spelling is how this
// kind ends up with an empty strip that looks like "it edited nothing".
func TestToolEditsAcceptsTheLegacySpelling(t *testing.T) {
	tu := parseToolUse(t, `{"name":"fs_write","input":{"command":"str_replace","path":"/w/a.go","old_str":"a\n","new_str":"b\n"}}`)
	file, verb, edits := toolEdits(tu)
	if file != "/w/a.go" || transcript.EditVerb(verb, edits) != "edit" {
		t.Fatalf("file = %q verb = %q", file, transcript.EditVerb(verb, edits))
	}
	if len(edits) != 1 || edits[0].Old != "a\n" || edits[0].New != "b\n" {
		t.Fatalf("edits = %+v", edits)
	}
	create := parseToolUse(t, `{"name":"fs_write","input":{"command":"create","path":"/w/n.go","file_text":"x\ny\n"}}`)
	if f, verb, es := toolEdits(create); f != "/w/n.go" || transcript.EditVerb(verb, es) != "add" || es[0].New != "x\ny\n" {
		t.Fatalf("legacy create = (%q, %q, %+v)", f, verb, es)
	}
}

// insert/append add lines to a file that already exists, so they must not be labelled "add":
// the derived verb would say exactly that, since neither carries a before-image.
func TestInsertIsAnEditNotAnAdd(t *testing.T) {
	tu := parseToolUse(t, `{"name":"write","input":{"command":"insert","path":"/w/a.go","content":"line\n"}}`)
	file, verb, edits := toolEdits(tu)
	if file != "/w/a.go" {
		t.Fatalf("file = %q", file)
	}
	if got := transcript.EditVerb(verb, edits); got != "edit" {
		t.Fatalf("verb = %q, want edit (a pure insertion into an existing file is not an add)", got)
	}
}

// The negative control the feature rests on: anything that is not the write tool must produce
// no row, or the strip names files nobody edited. A file READ is the dangerous one — it names
// a path just as prominently as a write does.
func TestToolEditsIgnoresEverythingThatIsNotAWrite(t *testing.T) {
	cases := []string{
		kiroPeerCall,
		`{"name":"read","input":{"path":"/home/dev/probe-kiro/probe.txt"}}`,
		`{"name":"fsRead","input":{"path":"/w/a.go","mode":"Line"}}`,
		`{"name":"shell","input":{"command":"sed -i s/a/b/ /w/a.go"}}`,
		`{"name":"grep","input":{"pattern":"func","path":"/w"}}`,
		`{"name":"write","input":{"command":"create","content":"x"}}`, // no path to place it at
	}
	for _, raw := range cases {
		tu := parseToolUse(t, raw)
		if file, verb, edits := toolEdits(tu); file != "" || verb != "" || edits != nil {
			t.Fatalf("toolEdits(%s) = (%q, %q, %+v), want no edit record", raw, file, verb, edits)
		}
	}
}

// End to end from the store line to what the aggregation folds: the whole point is that a row
// reaches the strip, and the two halves (parser, wiring) can each look fine while the chain
// stays broken.
func TestTranscriptTurnsCarryTheWriteTarget(t *testing.T) {
	jsonl := `{"version":"v1","kind":"Prompt","data":{"content":[{"kind":"text","data":"\"fix it\""}],"meta":{"timestamp":1758524700}}}
{"version":"v1","kind":"AssistantMessage","data":{"content":[{"kind":"toolUse","data":` + kiroReplaceCal + `},{"kind":"toolUse","data":` + kiroPeerCall + `}]}}
`
	turns := parseTranscript(writeFixture(t, jsonl))
	var edits []transcript.FileEdit
	for _, tn := range turns {
		edits = append(edits, transcript.FileEditsInTurn(tn)...)
	}
	if len(edits) != 1 {
		t.Fatalf("FileEditsInTurn over the session = %+v, want exactly the one write call", edits)
	}
	if got := edits[0]; got.Path != "/home/dev/probe-kiro/probe.txt" || got.Verb != "edit" || got.Added != 1 || got.Removed != 1 {
		t.Fatalf("folded edit = %+v", got)
	}
	// Paths are absolute in this kind's store (measured), which is what lets the row be
	// placed without a turn Cwd — sessionx drops a relative path that has none.
	if !strings.HasPrefix(edits[0].Path, "/") {
		t.Fatalf("path %q is not absolute; this kind would now need Turn.Cwd", edits[0].Path)
	}
}
