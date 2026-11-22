package memotidy

import (
	"strings"
	"testing"
)

func item(id string, g string) Item {
	return Item{ID: id, Group: Group{Provenance: g, Producer: "p", ProducerOrigin: "user"}, Mergeable: true, Bytes: 10}
}

func req(ops ...Op) Request {
	return Request{
		Self:         "self",
		Items:        []Item{item("a", "member"), item("b", "member"), item("c", "member"), item("d", "workspace"), {ID: "f", Group: Group{Provenance: "member"}, Bytes: 5}, {ID: "m", Mergeable: true, Bytes: 4, Moved: true}},
		Destinations: []Destination{{ID: "q1"}, {ID: "q2", OpenRefs: map[string]bool{"gh\x00o/r#1": true}}},
		Ops:          ops,
	}
}

func code(r *Refusal) string {
	if r == nil {
		return ""
	}
	return r.Code
}

func TestValidateTable(t *testing.T) {
	long := strings.Repeat("x", MaxTextBytes+1)
	many := make([]Op, MaxOperations+1)
	for i := range many {
		many[i] = Op{Op: OpKeep, ID: "a"}
	}
	ids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = string(rune('a' + i))
		}
		return out
	}
	// A request with many same-group items, for the source count bounds.
	wide := Request{Self: "self"}
	for i := 0; i < MaxMergeSources+1; i++ {
		wide.Items = append(wide.Items, item(string(rune('a'+i)), "member"))
	}
	refReq := func(ops ...Op) Request {
		r := req(ops...)
		r.Items = append(r.Items, Item{ID: "r1", Ref: "gh\x00o/r#1", Bytes: 3}, Item{ID: "r2", Ref: "gh\x00o/r#2", Bytes: 3}, Item{ID: "r3", Ref: "gh\x00o/r#2", Bytes: 3})
		return r
	}
	cases := []struct {
		name string
		r    Request
		want string
	}{
		{"nothing is valid and keeps all", req(), ""},
		{"merge", req(Op{Op: OpMerge, IDs: []string{"a", "b"}, Text: "ab"}), ""},
		{"split", req(Op{Op: OpSplit, ID: "a", Texts: []string{"1", "2"}}), ""},
		{"move", req(Op{Op: OpMove, ID: "a", To: "q1"}), ""},
		{"keep", req(Op{Op: OpKeep, ID: "a"}), ""},
		{"too many operations", Request{Self: "s", Items: []Item{item("a", "member")}, Ops: many}, CodeTooManyOps},
		{"unknown id in keep", req(Op{Op: OpKeep, ID: "zz"}), CodeUnknownID},
		{"unknown id in a merge", req(Op{Op: OpMerge, IDs: []string{"a", "zz"}, Text: "x"}), CodeUnknownID},
		{"an id in two operations", req(Op{Op: OpKeep, ID: "a"}, Op{Op: OpMove, ID: "a", To: "q1"}), CodeDuplicateID},
		{"an id twice in one merge", req(Op{Op: OpMerge, IDs: []string{"a", "a"}, Text: "x"}), CodeDuplicateID},
		{"merge of one", req(Op{Op: OpMerge, IDs: []string{"a"}, Text: "x"}), CodeMergeSources},
		{"merge of eleven", Request{Self: "s", Items: wide.Items, Ops: []Op{{Op: OpMerge, IDs: ids(MaxMergeSources + 1), Text: "x"}}}, CodeMergeSources},
		{"merge of ten", Request{Self: "s", Items: wide.Items, Ops: []Op{{Op: OpMerge, IDs: ids(MaxMergeSources), Text: "x"}}}, ""},
		{"merge of a file note", req(Op{Op: OpMerge, IDs: []string{"a", "f"}, Text: "x"}), CodeNotMergeable},
		{"merge across provenance", req(Op{Op: OpMerge, IDs: []string{"a", "d"}, Text: "x"}), CodeMixedGroup},
		{"merge across producers", Request{Self: "s", Items: []Item{{ID: "a", Group: Group{Provenance: "member", Producer: "p1"}, Mergeable: true}, {ID: "b", Group: Group{Provenance: "member", Producer: "p2"}, Mergeable: true}},
			Ops: []Op{{Op: OpMerge, IDs: []string{"a", "b"}, Text: "x"}}}, CodeMixedGroup},
		{"merge across producer origins", Request{Self: "s", Items: []Item{{ID: "a", Group: Group{Provenance: "workspace", Producer: "p", ProducerOrigin: "user"}, Mergeable: true}, {ID: "b", Group: Group{Provenance: "workspace", Producer: "p"}, Mergeable: true}},
			Ops: []Op{{Op: OpMerge, IDs: []string{"a", "b"}, Text: "x"}}}, CodeMixedGroup},
		{"merge with an empty text", req(Op{Op: OpMerge, IDs: []string{"a", "b"}, Text: "  \n"}), CodeBadText},
		{"merge text over the bound", req(Op{Op: OpMerge, IDs: []string{"a", "b"}, Text: long}), CodeTextTooLong},
		{"text that is not UTF-8", req(Op{Op: OpMerge, IDs: []string{"a", "b"}, Text: "a\xffb"}), CodeBadText},
		{"text with a NUL", req(Op{Op: OpMerge, IDs: []string{"a", "b"}, Text: "a\x00b"}), CodeBadText},
		{"text that opens an envelope", req(Op{Op: OpMerge, IDs: []string{"a", "b"}, Text: "  [agent-fleet:peer from=x]"}), CodeEnvelope},
		{"an envelope that is not at the start", req(Op{Op: OpMerge, IDs: []string{"a", "b"}, Text: "see [agent-fleet:x]"}), ""},
		{"merge grows past the slack", req(Op{Op: OpMerge, IDs: []string{"a", "b"}, Text: strings.Repeat("x", 20+GrowthSlackBytes+1)}), CodeGrowth},
		{"merge grows to the slack", req(Op{Op: OpMerge, IDs: []string{"a", "b"}, Text: strings.Repeat("x", 20+GrowthSlackBytes)}), ""},
		{"split into one", req(Op{Op: OpSplit, ID: "a", Texts: []string{"x"}}), CodeSplitParts},
		{"split into six", req(Op{Op: OpSplit, ID: "a", Texts: []string{"1", "2", "3", "4", "5", "6"}}), CodeSplitParts},
		{"split into five", req(Op{Op: OpSplit, ID: "a", Texts: []string{"1", "2", "3", "4", "5"}}), ""},
		{"split of a file note", req(Op{Op: OpSplit, ID: "f", Texts: []string{"1", "2"}}), CodeNotMergeable},
		{"split with a bad part", req(Op{Op: OpSplit, ID: "a", Texts: []string{"1", ""}}), CodeBadText},
		{"split grows past the slack", req(Op{Op: OpSplit, ID: "a", Texts: []string{strings.Repeat("x", 600), strings.Repeat("y", 600)}}), CodeGrowth},
		{"move to a destination not offered", req(Op{Op: OpMove, ID: "a", To: "q9"}), CodeBadDestination},
		{"move to the queue itself", req(Op{Op: OpMove, ID: "a", To: "self"}), CodeMoveSelf},
		{"move of a message a run moved", req(Op{Op: OpMove, ID: "m", To: "q1"}), CodeAlreadyMoved},
		{"move into a queue that holds the reference", refReq(Op{Op: OpMove, ID: "r1", To: "q2"}), CodeDuplicateRef},
		{"move of another reference", refReq(Op{Op: OpMove, ID: "r2", To: "q2"}), ""},
		{"two moves of one reference into one queue", refReq(Op{Op: OpMove, ID: "r2", To: "q1"}, Op{Op: OpMove, ID: "r3", To: "q1"}), CodeDuplicateRef},
		{"two moves of one reference into two queues", refReq(Op{Op: OpMove, ID: "r2", To: "q1"}, Op{Op: OpMove, ID: "r3", To: "q2"}), ""},
		{"an operation of no kind", req(Op{Op: "rewrite", ID: "a", Text: "x"}), CodeBadShape},
		{"a merge with a move's field", req(Op{Op: OpMerge, IDs: []string{"a", "b"}, Text: "x", To: "q1"}), CodeBadShape},
		{"a split with a text", req(Op{Op: OpSplit, ID: "a", Text: "x", Texts: []string{"1", "2"}}), CodeBadShape},
		{"a move without a destination", req(Op{Op: OpMove, ID: "a"}), CodeBadShape},
		{"a keep with a text", req(Op{Op: OpKeep, ID: "a", Text: "x"}), CodeBadShape},
		{"one bad operation drops the rest", req(Op{Op: OpKeep, ID: "a"}, Op{Op: OpMerge, IDs: []string{"b", "d"}, Text: "x"}), CodeMixedGroup},
	}
	for _, c := range cases {
		if got := code(Validate(c.r)); got != c.want {
			t.Errorf("%s: refusal %q, want %q", c.name, got, c.want)
		}
	}
}

func TestParseIsStrict(t *testing.T) {
	ok := `{"operations":[{"op":"merge","ids":["a","b"],"text":"x"},{"op":"keep","id":"c"}]}`
	ops, r := Parse([]byte(ok))
	if r != nil || len(ops) != 2 || ops[0].Op != OpMerge || len(ops[0].IDs) != 2 {
		t.Fatalf("Parse(%s) = %+v, %v", ok, ops, r)
	}
	if ops, r = Parse([]byte(`{"operations":[]}`)); r != nil || ops == nil || len(ops) != 0 {
		t.Fatalf("an empty list: %+v %v", ops, r)
	}
	for _, bad := range []string{
		``, `[]`, `{}`, `{"operations":null}`, `{"operations":[{"op":"keep","id":"a","extra":1}]}`,
		`{"operations":[]} {"operations":[]}`, `{"operations":[]} x`, `{"operations":[],"note":"hi"}`, `{"operations":[{"op":"keep","id":1}]}`,
	} {
		if _, r := Parse([]byte(bad)); r == nil || r.Code != CodeBadShape {
			t.Errorf("Parse(%q) accepted or answered %v", bad, r)
		}
	}
}

// A message that has or is a prerequisite is only kept: a merge, a split or a move of it refuses
// the whole reply, and a keep of it passes.
func TestValidateKeepOnly(t *testing.T) {
	r := req()
	r.Items = append(r.Items, Item{ID: "k", Group: Group{Provenance: "member", Producer: "p", ProducerOrigin: "user"}, Mergeable: true, Bytes: 10, KeepOnly: true})
	for name, op := range map[string]Op{
		"merge": {Op: OpMerge, IDs: []string{"a", "k"}, Text: "a and k"},
		"split": {Op: OpSplit, ID: "k", Texts: []string{"one", "two"}},
		"move":  {Op: OpMove, ID: "k", To: "q1"},
	} {
		rr := r
		rr.Ops = []Op{op}
		if got := code(Validate(rr)); got != CodeKeepOnly {
			t.Fatalf("%s of a keep-only message: %q, want %q", name, got, CodeKeepOnly)
		}
	}
	r.Ops = []Op{{Op: OpKeep, ID: "k"}, {Op: OpMerge, IDs: []string{"a", "b"}, Text: "a and b"}}
	if got := code(Validate(r)); got != "" {
		t.Fatalf("keep of a keep-only message: %q", got)
	}
}
