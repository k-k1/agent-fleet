package memengine

import (
	"errors"
	"strings"
	"testing"
)

var shown = ClassifyRequest{
	Observation: "obs",
	Memories:    []Snippet{{Name: "a", Description: "da", Body: "ba"}, {Name: "b", Description: "db", Body: "bb"}},
}

func TestParseClassify(t *testing.T) {
	cases := []struct {
		name, in string
		wantOp   Operation
		bad      bool
	}{
		{"add", `{"operation":"add","targets":[],"reason":"r"}`, OpAdd, false},
		{"no-op", `{"operation":"no-op","targets":[],"reason":"r"}`, OpNoop, false},
		{"patch one", `{"operation":"patch","targets":["a"],"reason":"r"}`, OpPatch, false},
		{"supersede one", `{"operation":"supersede","targets":["b"],"reason":"r"}`, OpSupersede, false},
		{"merge two", `{"operation":"merge","targets":["a","b"],"reason":"r"}`, OpMerge, false},
		{"merge of an observation only", `{"operation":"merge","targets":[],"reason":"r"}`, OpMerge, false},
		{"unknown operation", `{"operation":"delete","targets":[],"reason":"r"}`, "", true},
		{"upper case operation", `{"operation":"Patch","targets":["a"],"reason":"r"}`, "", true},
		{"patch without target", `{"operation":"patch","targets":[],"reason":"r"}`, "", true},
		{"patch with two targets", `{"operation":"patch","targets":["a","b"],"reason":"r"}`, "", true},
		{"add with a target", `{"operation":"add","targets":["a"],"reason":"r"}`, "", true},
		{"target never shown", `{"operation":"patch","targets":["zzz"],"reason":"r"}`, "", true},
		{"target twice", `{"operation":"merge","targets":["a","a"],"reason":"r"}`, "", true},
		{"extra field", `{"operation":"add","targets":[],"reason":"r","confidence":1}`, "", true},
		{"not json", `I think add`, "", true},
		{"trailing data", `{"operation":"add","targets":[],"reason":"r"} {}`, "", true},
		{"operation of the wrong type", `{"operation":1,"targets":[],"reason":"r"}`, "", true},
	}
	for _, c := range cases {
		got, err := ParseClassify(c.in, shown)
		if c.bad {
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: want ErrInvalid, got %v (%v)", c.name, err, got)
			}
			continue
		}
		if err != nil || got.Operation != c.wantOp {
			t.Errorf("%s: got %v, %v", c.name, got, err)
		}
	}
}

func TestEscalation(t *testing.T) {
	want := map[Operation]bool{OpAdd: false, OpPatch: false, OpNoop: false, OpSupersede: true, OpMerge: true}
	for op, w := range want {
		if op.Escalates() != w {
			t.Errorf("%s.Escalates() = %v, want %v", op, !w, w)
		}
	}
	if OpNoop.IsChange() || !OpAdd.IsChange() {
		t.Error("only no-op is not a change")
	}
}

func TestClassifyPromptBoundsTheSnippets(t *testing.T) {
	long := strings.Repeat("あ", SnippetRunes*3)
	p := ClassifyRequest{Observation: "OBS-TEXT", Memories: []Snippet{{Name: "n1", Description: "d1", Body: long}}}.Prompt()
	if !strings.Contains(p, "OBS-TEXT") || !strings.Contains(p, "n1") {
		t.Fatalf("prompt lacks the observation or the memory name:\n%s", p)
	}
	if got := strings.Count(p, "あ"); got != SnippetRunes {
		t.Errorf("snippet carries %d runes, want exactly %d", got, SnippetRunes)
	}
	if !strings.Contains(p, ClassifySchema) {
		t.Error("prompt does not carry the fixed schema")
	}
	if !strings.Contains((ClassifyRequest{Observation: "o"}).Prompt(), "(none)") {
		t.Error("a request without memories should say so")
	}
}

// The small tier leaned to no-op on findings about a note (stale fact, retired
// term, dangling link) and, with no notes shown, on plain lessons. The prompt
// has to keep saying how to tell those apart; the measured effect is in the
// gold-set report, this only stops the guidance from being edited away.
func TestPromptKeepsThePatchVersusNoopGuidance(t *testing.T) {
	p := ClassifyRequest{Observation: "o"}.Prompt()
	for _, want := range []string{
		"a finding about the notes themselves",
		"a link to a note that does not exist",
		"a small fix is still a patch",
		"A link that points nowhere is a defect of the note that holds it",
		"absence of a note to fix is not a reason for no-op",
		"the flagged text is correct as written",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the classify prompt lost %q", want)
		}
	}
	if strings.Index(p, "- patch:") > strings.Index(p, "- add:") {
		t.Error("patch is listed before add on purpose: the order is part of what was measured")
	}
}
