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
