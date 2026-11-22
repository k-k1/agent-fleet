package mcpx

import (
	"encoding/json"
	"strings"
	"testing"
)

// A write whose run recipe the answer does not carry is reported: an older Control Plane drops the
// fields, and an existing note returned unchanged does not hold them. A clear and a call that asked
// for nothing say nothing, and other (non-string) arguments do not hide the check.
func TestMemoWriteResultRecipe(t *testing.T) {
	isErr := func(args, answer string) bool {
		return strings.Contains(string(memoWriteResult(json.RawMessage(`1`), json.RawMessage(args), answer)), `"isError":true`)
	}
	ask := `{"kind":"text","effort":"high","position":3,"after":[]}`
	if !isErr(ask, `{"id":"m1"}`) {
		t.Error("an answer without the effort was not reported")
	}
	if !isErr(ask, `{"id":"m1","effort":"low"}`) {
		t.Error("a different effort was not reported")
	}
	if !isErr(ask, `{"id":"m0","existing":true}`) {
		t.Error("an existing note returned unchanged was not reported")
	}
	if isErr(ask, `{"id":"m1","effort":"high"}`) {
		t.Error("an answer with the effort was reported")
	}
	if isErr(`{"kind":"text","effort":""}`, `{"id":"m1"}`) {
		t.Error("a clear was reported")
	}
	if isErr(`{"kind":"text"}`, `{"id":"m1"}`) {
		t.Error("a call that asked for no recipe was reported")
	}
}
