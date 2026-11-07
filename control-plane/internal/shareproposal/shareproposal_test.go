package shareproposal

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	ok := []struct{ action, in, want string }{
		{"turn", `{"op":"start","prompt":"hi"}`, `{"op":"start","prompt":"hi"}`},
		{"respond", `{"id":"i","decision":"allow","scope":"turn"}`, `{"id":"i","decision":"allow","scope":"turn"}`},
		{"answer-question", `{"choices":[1,2]}`, `{"choices":[1,2]}`},
		{"plan-respond", `{"decision":"reject","feedback":"no"}`, `{"decision":"reject","feedback":"no"}`},
	}
	for _, c := range ok {
		got, err := Normalize(c.action, []byte(c.in))
		if err != nil || string(got) != c.want {
			t.Errorf("%s %s: got %s, %v", c.action, c.in, got, err)
		}
	}
	bad := []struct{ action, in string }{
		{"turn", `{"op":"start","prompt":"hi","attachments":["/home/dev/.ssh/id"]}`},
		{"turn", `{"op":"start","prompt":"hi","attachments":[]}`},
		{"turn", `{"op":"start","prompt":"hi","discard_queue":true}`},
		{"turn", `{"op":"interrupt","prompt":"hi"}`},
		{"turn", `{"op":"start","prompt":"  "}`},
		{"turn", `[]`},
		{"respond", `{"id":"i","decision":"allow","scope":"forever"}`},
		{"plan-respond", `{"decision":"approve","expect_plan":"p"}`},
		{"answer-question", `{"choices":[]}`},
	}
	for _, c := range bad {
		if got, err := Normalize(c.action, []byte(c.in)); err == nil {
			t.Errorf("%s %s accepted as %s", c.action, c.in, got)
		}
	}
}

func TestNormalizeBoundsTheCanonicalSize(t *testing.T) {
	// "<" is one byte on the wire in and six after HTML escaping; the canonical form must not grow.
	in := `{"op":"start","prompt":"` + strings.Repeat("<", 20000) + `"}`
	got, err := Normalize("turn", []byte(in))
	if err != nil || len(got) > len(in) {
		t.Fatalf("len=%d in=%d err=%v", len(got), len(in), err)
	}
	// Invalid UTF-8 is replaced by a six-byte escape, so the canonical form can grow: it is
	// checked again after normalising.
	big := `{"op":"start","prompt":"` + strings.Repeat("\xff", 30000) + `"}`
	if _, err := Normalize("turn", []byte(big)); err == nil {
		t.Fatal("a payload that grows past the limit when normalised must be refused")
	}
}
