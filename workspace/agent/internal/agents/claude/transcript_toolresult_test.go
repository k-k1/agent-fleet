package claude

import "testing"

func TestHasToolResult(t *testing.T) {
	use := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_A","name":"Bash","input":{}}]}}`)
	res := []byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_A","content":"denied"}]}}`)
	other := []byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_AB","content":"ok"}]}}`)
	if HasToolResult([][]byte{use}, "toolu_A") {
		t.Error("the tool_use alone counted as a result")
	}
	if HasToolResult([][]byte{use, other}, "toolu_A") {
		t.Error("another call's result (an id with the same prefix) counted")
	}
	if !HasToolResult([][]byte{use, res}, "toolu_A") {
		t.Error("the call's result was missed")
	}
	if HasToolResult([][]byte{use, res}, "") {
		t.Error("an empty id matched")
	}
}
