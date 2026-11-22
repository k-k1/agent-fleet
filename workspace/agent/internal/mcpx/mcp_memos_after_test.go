package mcpx

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A write whose prerequisites the answer leaves out is reported as not kept, except when the
// answer is a note already in the queue (existing: true): nothing was written then, and the
// Control Plane is not one without prerequisites.
func TestMemoWriteResultAfter(t *testing.T) {
	args := json.RawMessage(`{"kind":"text","after":["p1"]}`)
	isErr := func(answer string) bool {
		return strings.Contains(string(memoWriteResult(json.RawMessage(`1`), args, answer)), `"isError":true`)
	}
	if !isErr(`{"id":"m1"}`) {
		t.Error("an answer without after was not reported")
	}
	if isErr(`{"id":"m1","after":["p1"]}`) {
		t.Error("an answer with after was reported")
	}
	if isErr(`{"id":"m0","existing":true}`) {
		t.Error("an existing note was reported as a Control Plane without prerequisites")
	}
}

// A write whose future start time is not on the note the answer returns is reported: a Control
// Plane that drops the field, a different time, or an existing note returned unchanged. A past time
// (the Control Plane clears it by design), one that does not parse (it refuses it), a clear, and a
// time that passed while the call was in flight say nothing.
func TestMemoWriteResultStartAfter(t *testing.T) {
	now := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	problem := func(args, answer string) string {
		return memoStartAfterProblem(json.RawMessage(args), answer, now)
	}
	future := `{"kind":"text","startAfter":"2026-12-04T09:00:00+09:00"}`
	if problem(future, `{"id":"m1"}`) == "" {
		t.Error("an answer without startAfter was not reported")
	}
	if p := problem(future, `{"id":"m1","startAfter":"2026-12-04T00:00:00.000000Z"}`); p != "" {
		t.Errorf("the same instant was reported: %s", p)
	}
	if !strings.Contains(problem(future, `{"id":"m1","startAfter":"2026-12-05T00:00:00.000000Z"}`), "different start time") {
		t.Error("a different time was not reported")
	}
	if !strings.Contains(problem(future, `{"id":"m0","existing":true}`), "unchanged") {
		t.Error("an existing note without the time was not reported")
	}
	if p := problem(future, `{"id":"m0","existing":true,"startAfter":"2026-12-04T00:00:00.000000Z"}`); p != "" {
		t.Errorf("an existing note with the same time was reported: %s", p)
	}
	for _, args := range []string{
		`{"kind":"text","startAfter":"2020-01-01T00:00:00Z"}`,
		`{"kind":"text","startAfter":"2026-12-04 09:00"}`,
		`{"kind":"text","startAfter":""}`,
		`{"kind":"text","startAfter":"2026-11-22T00:00:30Z"}`,
		`{"kind":"text"}`,
	} {
		if p := problem(args, `{"id":"m1"}`); p != "" {
			t.Errorf("%s was reported: %s", args, p)
		}
	}
	// The tool result itself is an error when there is a problem.
	if !strings.Contains(string(memoWriteResult(json.RawMessage(`1`), json.RawMessage(`{"startAfter":"2099-01-01T00:00:00Z"}`), `{"id":"m1"}`)), `"isError":true`) {
		t.Error("the tool result is not an error")
	}
}
