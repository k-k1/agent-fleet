package main

import "testing"

func TestJSONLHasConversation(t *testing.T) {
	toLines := func(ss ...string) [][]byte {
		out := make([][]byte, 0, len(ss))
		for _, s := range ss {
			out = append(out, []byte(s))
		}
		return out
	}
	cases := []struct {
		name  string
		lines [][]byte
		want  bool
	}{
		{"empty", nil, false},
		{"bridge stub only", toLines(`{"type":"bridge-session"}`, `{"type":"summary","summary":"x"}`), false},
		{"has user turn", toLines(`{"type":"summary"}`, `{"type":"user","message":{"content":"hi"}}`), true},
		{"has assistant turn", toLines(`{"type":"assistant","message":{"content":[]}}`), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := jsonlHasConversation(c.lines); got != c.want {
				t.Fatalf("jsonlHasConversation = %v, want %v", got, c.want)
			}
		})
	}
}
