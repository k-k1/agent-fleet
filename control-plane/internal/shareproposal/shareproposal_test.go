package shareproposal

import "testing"

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
