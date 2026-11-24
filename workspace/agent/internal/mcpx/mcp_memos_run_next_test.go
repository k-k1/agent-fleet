package mcpx

import (
	"encoding/json"
	"strings"
	"testing"
)

// A create that asked for the run-next mark and got a note without it says so: an older Control
// Plane drops the field, and an existing note returned unchanged is not marked. An answer with the
// mark, and a call that asked for none, say nothing.
func TestMemoRunNextProblem(t *testing.T) {
	problem := func(args, answer string) string {
		return memoRunNextProblem(json.RawMessage(args), answer)
	}
	ask := `{"kind":"text","body":"x","runNext":true}`
	if got := problem(ask, `{"id":"m1","state":"held"}`); !strings.Contains(got, "does not support") {
		t.Errorf("an answer without the mark: %q", got)
	}
	if got := problem(ask, `{"id":"m0","existing":true}`); !strings.Contains(got, "Nothing was added") {
		t.Errorf("an existing note: %q", got)
	}
	if got := problem(ask, `{"id":"m1","runNext":true}`); got != "" {
		t.Errorf("an answer with the mark: %q", got)
	}
	for _, args := range []string{`{"kind":"text"}`, `{"kind":"text","runNext":false}`, `{"runNext":"yes"}`} {
		if got := problem(args, `{"id":"m1"}`); got != "" {
			t.Errorf("%s: %q", args, got)
		}
	}
}

// A create the Control Plane answered without the mark and without its prerequisites reports both:
// the missing mark never hides a warning about when the note may run. A note already there that a
// run has taken is not one to mark in the Console.
func TestMemoCreateResultReportsEveryProblem(t *testing.T) {
	res := string(memoCreateResult(json.RawMessage(`1`), json.RawMessage(`{"kind":"text","after":["m0"],"runNext":true}`), `{"id":"m1","state":"pending"}`))
	if !strings.Contains(res, `"isError":true`) || !strings.Contains(res, "prerequisites (after) were NOT kept") || !strings.Contains(res, "NOT marked to run next") {
		t.Fatalf("result %s", res)
	}
	if res := string(memoCreateResult(json.RawMessage(`1`), json.RawMessage(`{"kind":"text","runNext":true}`), `{"id":"m1","state":"held","runNext":true}`)); strings.Contains(res, `"isError":true`) {
		t.Fatalf("a kept mark was reported: %s", res)
	}
	for _, answer := range []string{`{"id":"m0","existing":true,"state":"claimed","deliveryId":"d1"}`, `{"id":"m0","existing":true,"state":"pending","deliveryId":"d1"}`} {
		got := memoRunNextProblem(json.RawMessage(`{"kind":"text","runNext":true}`), answer)
		if !strings.Contains(got, "already taken") || strings.Contains(got, "Console") {
			t.Errorf("%s: %q", answer, got)
		}
	}
	if got := memoRunNextProblem(json.RawMessage(`{"kind":"text","runNext":true}`), `{"id":"m0","existing":true,"state":"held"}`); !strings.Contains(got, "Console") {
		t.Errorf("a waiting existing note: %q", got)
	}
}
