package mcpx

import (
	"encoding/json"
	"strings"
	"testing"
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

// A write whose future start time the answer leaves out is reported as not kept. A past time (the
// Control Plane drops it by design), one that does not parse (it refuses it) and an existing note
// say nothing about the Control Plane.
func TestMemoWriteResultStartAfter(t *testing.T) {
	isErr := func(args, answer string) bool {
		return strings.Contains(string(memoWriteResult(json.RawMessage(`1`), json.RawMessage(args), answer)), `"isError":true`)
	}
	future := `{"kind":"text","startAfter":"2099-12-04T09:00:00+09:00"}`
	if !isErr(future, `{"id":"m1"}`) {
		t.Error("an answer without startAfter was not reported")
	}
	if isErr(future, `{"id":"m1","startAfter":"2099-12-04T00:00:00.000000Z"}`) {
		t.Error("an answer with startAfter was reported")
	}
	if isErr(future, `{"id":"m0","existing":true}`) {
		t.Error("an existing note was reported")
	}
	if isErr(`{"kind":"text","startAfter":"2020-01-01T00:00:00Z"}`, `{"id":"m1"}`) {
		t.Error("a past time was reported as not kept")
	}
	if isErr(`{"kind":"text","startAfter":"2099-12-04 09:00"}`, `{"id":"m1"}`) {
		t.Error("a time without an offset was reported as not kept")
	}
	if isErr(`{"kind":"text","startAfter":""}`, `{"id":"m1"}`) {
		t.Error("a clear was reported as not kept")
	}
}
