package lcpp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// TestToolEditsShapes pins what the strip and the inline diff are fed for each edit-family
// builtin, INCLUDING the derived verb and the +/- counts: those are what the reader sees, and
// they come out of transcript.EditVerb / transcript.EditStat rather than out of this package,
// so pinning them here is what keeps "the number in the strip" honest for this kind.
func TestToolEditsShapes(t *testing.T) {
	cases := []struct {
		name             string
		tool, args       string
		wantFile         string
		wantEdits        []transcript.Edit
		wantVerb         string
		wantAdd, wantRem int
	}{
		{
			name: "write is an add of every line",
			tool: "write", args: `{"path":"pkg/a.txt","content":"one\ntwo\n"}`,
			wantFile:  "pkg/a.txt",
			wantEdits: []transcript.Edit{{Old: "", New: "one\ntwo\n"}},
			wantVerb:  "add", wantAdd: 2, wantRem: 0,
		},
		{
			name: "edit carries both sides and reads as an edit",
			tool: "edit", args: `{"path":"a.go","old_string":"old\n","new_string":"new\nnewer\n"}`,
			wantFile:  "a.go",
			wantEdits: []transcript.Edit{{Old: "old\n", New: "new\nnewer\n"}},
			wantVerb:  "edit", wantAdd: 2, wantRem: 1,
		},
		{
			// An overwrite of an existing file still reads as an add: the call carries no
			// before-image (fileedits.go). Pinned so the day someone gives write a before-image
			// this test is what tells them the verb changes with it.
			name: "write onto an existing file is still an add",
			tool: "write", args: `{"path":"a.go","content":"x\n"}`,
			wantFile:  "a.go",
			wantEdits: []transcript.Edit{{Old: "", New: "x\n"}},
			wantVerb:  "add", wantAdd: 1, wantRem: 0,
		},
		{
			// replace_all applied N times is still reported once (fileedits.go).
			name: "replace_all is one hunk",
			tool: "edit", args: `{"path":"a.go","old_string":"x","new_string":"y","replace_all":true}`,
			wantFile:  "a.go",
			wantEdits: []transcript.Edit{{Old: "x", New: "y"}},
			wantVerb:  "edit", wantAdd: 1, wantRem: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file, edits := toolEdits(c.tool, c.args)
			if file != c.wantFile {
				t.Fatalf("toolEdits(%s) file = %q, want %q", c.tool, file, c.wantFile)
			}
			if !reflect.DeepEqual(edits, c.wantEdits) {
				t.Fatalf("toolEdits(%s) edits = %+v, want %+v", c.tool, edits, c.wantEdits)
			}
			if got := transcript.EditVerb("", edits); got != c.wantVerb {
				t.Fatalf("EditVerb = %q, want %q", got, c.wantVerb)
			}
			if a, r := transcript.EditStat(edits); a != c.wantAdd || r != c.wantRem {
				t.Fatalf("EditStat = +%d -%d, want +%d -%d", a, r, c.wantAdd, c.wantRem)
			}
		})
	}
}

// TestToolEditsIgnoresEverythingElse is the negative control the whole feature rests on: a
// kind that labels the wrong calls as edits lists files nobody touched, and docs/log/68
// §68.2.1's own trap (reading "no before/after" as a delete) is the same mistake one step
// further on. bash is in here deliberately — it MUTATES, and it is still not an edit record,
// because its arguments never name the file it writes.
func TestToolEditsIgnoresEverythingElse(t *testing.T) {
	cases := []struct{ tool, args string }{
		{"bash", `{"command":"sed -i s/a/b/ a.go"}`},
		{"read", `{"path":"a.go"}`},
		{"ls", `{"path":"."}`},
		{"glob", `{"pattern":"**/*.go","path":"."}`},
		{"grep", `{"pattern":"func","path":"."}`},
		{"todo_write", `{"todos":[]}`},
		{"ask_user", `{"question":"which?"}`},
		{"some_mcp_tool", `{"path":"a.go","content":"x"}`},
		{"write", ``},                // no arguments recorded at all
		{"write", `{"content":"x"}`}, // no path to place it at
		{"edit", `not json`},         // a model that emitted garbage
		{"edit", `{"old_string":"a","new_string":"b"}`},
	}
	for _, c := range cases {
		file, edits := toolEdits(c.tool, c.args)
		if file != "" || edits != nil {
			t.Fatalf("toolEdits(%q, %q) = (%q, %+v), want no edit record", c.tool, c.args, file, edits)
		}
	}
}

// TestToolEditsNamesTheFileTheBuiltinWrote is the seam between the parser and the tools it
// parses: the SAME argument JSON is handed to the real tool out of harness.BuiltinTools(),
// and the file the parser named is then read off disk. A renamed argument (`path` →
// `file_path`) or a renamed tool makes this red instead of silently emptying the strip —
// which is the failure mode that hides, because an unparsed transcript and a session that
// edited nothing look identical.
func TestToolEditsNamesTheFileTheBuiltinWrote(t *testing.T) {
	dir := t.TempDir()
	rt := &harness.Runtime{Cwd: dir}
	run := func(name, args string) {
		t.Helper()
		for _, tl := range harness.BuiltinTools() {
			if tl.Def.Name != name {
				continue
			}
			if _, err := tl.Run(context.Background(), rt, args); err != nil {
				t.Fatalf("%s(%s): %v", name, args, err)
			}
			return
		}
		t.Fatalf("harness.BuiltinTools() has no tool named %q — the parser's case is dead code", name)
	}
	// The parser hands back the path as the model wrote it (relative to cwd), which is exactly
	// what sessionx anchors against Turn.Cwd; do the same joining here.
	readParsed := func(file string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
		if err != nil {
			t.Fatalf("the parsed path %q does not name the file the tool wrote: %v", file, err)
		}
		return string(b)
	}

	const writeArgs = `{"path":"pkg/a.txt","content":"one\ntwo\n"}`
	run("write", writeArgs)
	file, edits := toolEdits("write", writeArgs)
	if len(edits) != 1 {
		t.Fatalf("toolEdits(write) edits = %+v", edits)
	}
	if got := readParsed(file); got != edits[0].New {
		t.Fatalf("file on disk = %q, but the mirror would show %q", got, edits[0].New)
	}

	const editArgs = `{"path":"pkg/a.txt","old_string":"two","new_string":"three"}`
	run("edit", editArgs)
	file, edits = toolEdits("edit", editArgs)
	if len(edits) != 1 {
		t.Fatalf("toolEdits(edit) edits = %+v", edits)
	}
	after := readParsed(file)
	if strings.Contains(after, edits[0].Old) {
		t.Fatalf("file still contains the before-text %q the mirror claims was replaced: %q", edits[0].Old, after)
	}
	if !strings.Contains(after, edits[0].New) {
		t.Fatalf("file does not contain the after-text %q the mirror claims: %q", edits[0].New, after)
	}
}

// TestEveryMutatingPathTakingBuiltinIsParsed is the drift guard for a builtin added LATER. A
// tool that both mutates and names a path in its arguments is an edit-family tool by
// construction, and one the parser does not know about is invisible in the changed-files
// strip — silently, since there is nothing to see when a row is missing. bash is the
// deliberate exception: it mutates, but it names no path (its arguments are a shell command).
func TestEveryMutatingPathTakingBuiltinIsParsed(t *testing.T) {
	seen := 0
	for _, tl := range harness.BuiltinTools() {
		if !tl.Mutates || !declaresProperty(t, tl.Def, "path") {
			continue
		}
		seen++
		if file, edits := toolEdits(tl.Def.Name, `{"path":"p"}`); file == "" || len(edits) == 0 {
			t.Fatalf("builtin %q mutates and takes a path, but toolEdits ignores it: "+
				"add a case to fileedits.go or the changed-files strip will never show it", tl.Def.Name)
		}
	}
	if seen != 2 {
		t.Fatalf("found %d mutating path-taking builtins, want 2 (write, edit) — "+
			"the guard above only proves something while it actually matches", seen)
	}
	// Negative control: bash mutates and must NOT be caught by the rule this guard applies.
	for _, tl := range harness.BuiltinTools() {
		if tl.Def.Name == "bash" && (!tl.Mutates || declaresProperty(t, tl.Def, "path")) {
			t.Fatalf("bash def changed (Mutates=%v, path param=%v) — re-examine the rule above",
				tl.Mutates, declaresProperty(t, tl.Def, "path"))
		}
	}
}

func declaresProperty(t *testing.T, def harness.ToolDef, name string) bool {
	t.Helper()
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(def.Parameters, &schema); err != nil {
		t.Fatalf("tool %q has unparsable Parameters: %v", def.Name, err)
	}
	_, ok := schema.Properties[name]
	return ok
}
