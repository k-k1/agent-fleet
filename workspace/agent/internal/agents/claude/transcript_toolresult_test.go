package claude

import (
	"testing"
	"time"
)

func TestToolResultAt(t *testing.T) {
	use := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_A","name":"Bash","input":{}}]}}`)
	res := []byte(`{"type":"user","timestamp":"2026-11-09T06:30:00.123Z","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_A","content":"denied"}]}}`)
	other := []byte(`{"type":"user","timestamp":"2026-11-09T06:30:00Z","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_AB","content":"ok"}]}}`)
	quoted := []byte(`{"type":"user","timestamp":"2026-11-09T06:30:00Z","message":{"content":"tool_result toolu_A"}}`)
	noClock := []byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_A","content":"denied"}]}}`)
	if _, ok := ToolResultAt([][]byte{use}, "toolu_A"); ok {
		t.Error("the tool_use alone counted as a result")
	}
	if _, ok := ToolResultAt([][]byte{use, other, quoted, []byte(`{"broken`)}, "toolu_A"); ok {
		t.Error("another call's result, a string content naming the id, or a broken line counted")
	}
	if _, ok := ToolResultAt([][]byte{use, noClock}, "toolu_A"); ok {
		t.Error("a result without a clock counted")
	}
	at, ok := ToolResultAt([][]byte{use, res}, "toolu_A")
	if want := time.Date(2026, 11, 9, 6, 30, 0, 123e6, time.UTC); !ok || !at.Equal(want) {
		t.Errorf("at=%v ok=%v, want %v", at, ok, want)
	}
	if _, ok := ToolResultAt([][]byte{use, res}, ""); ok {
		t.Error("an empty id matched")
	}
}
