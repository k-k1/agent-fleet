package chatx

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
	"strings"
	"testing"
)

// The result of `claude -p --output-format json` (a fixture abridged from claude-code 2.1.x,
// measured 2026-07): the last entry of usage.iterations is the final snapshot, and modelUsage
// carries the real window.
const claudeResultFixture = `{
  "type": "result", "subtype": "success", "is_error": false, "num_turns": 2,
  "result": "2", "session_id": "2abf80ca-0cfe-4ca5-a89d-17248cba29de",
  "usage": {
    "input_tokens": 18, "cache_creation_input_tokens": 11238, "cache_read_input_tokens": 34836,
    "output_tokens": 87,
    "iterations": [
      {"input_tokens": 9, "output_tokens": 40, "cache_read_input_tokens": 17418, "cache_creation_input_tokens": 11238, "type": "message"},
      {"input_tokens": 9, "output_tokens": 47, "cache_read_input_tokens": 28656, "cache_creation_input_tokens": 120, "type": "message"}
    ]
  },
  "modelUsage": {
    "claude-haiku-4-5-20251001": {"inputTokens": 18, "outputTokens": 87, "contextWindow": 200000, "maxOutputTokens": 32000}
  }
}`

func TestClaudeCtxObserveResultUsesLastIteration(t *testing.T) {
	var r claudeResult
	if err := json.Unmarshal([]byte(claudeResultFixture), &r); err != nil {
		t.Fatal(err)
	}
	tr := claudeCtx{model: "claude-sonnet-5"}
	tr.observeResult(r.Usage, r.ModelUsage)
	c := &ChatConversation{}
	tr.apply(c)
	if c.Context == nil {
		t.Fatal("no context captured")
	}
	// The last iteration (9 + 28656 + 120), not the top-level total.
	if got, want := c.Context.Tokens, 9+28656+120; got != want {
		t.Fatalf("tokens = %d, want %d (last iteration)", got, want)
	}
	if c.Context.Window != 200000 || c.Context.WindowSource != "recorded" {
		t.Fatalf("window = %d (%s), want 200000 (recorded)", c.Context.Window, c.Context.WindowSource)
	}
	if c.Context.Pct <= 0 || c.Context.Pct > 100 {
		t.Fatalf("pct out of range: %v", c.Context.Pct)
	}
}

func TestClaudeCtxStreamAssistantEvents(t *testing.T) {
	// stream-json: message.usage on an assistant event is a per-message snapshot. The last
	// event wins, and a result without iterations does not overwrite it.
	lines := []string{
		`{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","usage":{"input_tokens":9,"cache_creation_input_tokens":7064,"cache_read_input_tokens":21592,"output_tokens":5}}}`,
		`{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","usage":{"input_tokens":12,"cache_creation_input_tokens":100,"cache_read_input_tokens":28700,"output_tokens":9}}}`,
		`{"type":"result","result":"ok","usage":{"input_tokens":21,"cache_creation_input_tokens":7164,"cache_read_input_tokens":50292,"output_tokens":14},"modelUsage":{"claude-haiku-4-5-20251001":{"contextWindow":200000}}}`,
	}
	tr := claudeCtx{model: "claude-sonnet-5"}
	for _, ln := range lines {
		var sl streamLine
		if err := json.Unmarshal([]byte(ln), &sl); err != nil {
			t.Fatal(err)
		}
		switch sl.Type {
		case "assistant":
			tr.observeAssistant(sl.Message.Model, sl.Message.Usage)
		case "result":
			tr.observeResult(sl.Usage, sl.ModelUsage)
		}
	}
	c := &ChatConversation{}
	tr.apply(c)
	if got, want := c.Context.Tokens, 12+100+28700; got != want {
		t.Fatalf("tokens = %d, want %d (last assistant snapshot)", got, want)
	}
	if c.Context.Model != "claude-haiku-4-5-20251001" {
		t.Fatalf("model = %q, want the event-reported model", c.Context.Model)
	}
	if c.Context.Window != 200000 || c.Context.WindowSource != "recorded" {
		t.Fatalf("window = %d (%s), want 200000 (recorded)", c.Context.Window, c.Context.WindowSource)
	}
}

func TestParseCodexExecEventsUsage(t *testing.T) {
	out := strings.Join([]string{
		`{"type":"thread.started","thread_id":"th-1"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"2"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":14502,"cached_input_tokens":8960,"output_tokens":17}}`,
	}, "\n")
	reply, threadID, execErr, usage := parseCodexExecEvents([]byte(out))
	if reply != "2" || threadID != "th-1" || execErr != "" {
		t.Fatalf("parse = (%q, %q, %q)", reply, threadID, execErr)
	}
	if usage.InputTokens != 14502 || usage.CachedInputTokens != 8960 {
		t.Fatalf("usage = %+v", usage)
	}
	// input includes cached, so fresh is the difference and the total stays input.
	c := &ChatConversation{Agent: "codex", Model: "gpt-5.6-luna"}
	setChatContext(c, usage.InputTokens-usage.CachedInputTokens, usage.CachedInputTokens, 0, 0, chatCtxModelFor(c, "codex"))
	if c.Context.Tokens != 14502 || c.Context.Read != 8960 || c.Context.Fresh != 14502-8960 {
		t.Fatalf("context = %+v", c.Context)
	}
	if c.Context.Window != 272000 || c.Context.WindowSource != "estimated" {
		t.Fatalf("window = %d (%s), want 272000 (estimated, gpt-5)", c.Context.Window, c.Context.WindowSource)
	}
}

func TestParseOpencodeRunEventsUsage(t *testing.T) {
	out := strings.Join([]string{
		`{"type":"step_start","sessionID":"ses_1","part":{"id":"p1","type":"step-start"}}`,
		`{"type":"text","sessionID":"ses_1","part":{"id":"p2","type":"text","text":"2"}}`,
		`{"type":"step_finish","sessionID":"ses_1","part":{"id":"p3","type":"step-finish","tokens":{"total":11910,"input":11887,"output":2,"reasoning":21,"cache":{"write":30,"read":40}}}}`,
	}, "\n")
	reply, sesID, _, _, usage := parseOpencodeRunEvents([]byte(out))
	if reply != "2" || sesID != "ses_1" {
		t.Fatalf("parse = (%q, %q)", reply, sesID)
	}
	if usage.Input != 11887 || usage.Cache.Read != 40 || usage.Cache.Write != 30 {
		t.Fatalf("usage = %+v", usage)
	}
	// For the usage ledger (docs/log/46 §2): output comes from the same part.
	if usage.Output != 2 {
		t.Fatalf("output = %d, want 2", usage.Output)
	}
}

func TestSetChatContextKeepsPreviousOnEmpty(t *testing.T) {
	c := &ChatConversation{}
	setChatContext(c, 100, 200, 0, 0, "claude-sonnet-5")
	if c.Context == nil {
		t.Fatal("no context captured")
	}
	prev := c.Context
	// A turn with no usage (all zeros) must not erase the previous value.
	setChatContext(c, 0, 0, 0, 0, "claude-sonnet-5")
	if c.Context != prev {
		t.Fatal("empty usage overwrote the previous snapshot")
	}
}

func TestNoteContextPressureOncePerCrossing(t *testing.T) {
	withTempHome(t)
	c := &ChatConversation{ID: RandUUID(), Messages: []ChatMessage{}}

	notices := func() int {
		n := 0
		for _, m := range c.Messages {
			if m.Role == "notice" && strings.Contains(m.Content, "コンテキスト使用量") {
				n++
			}
		}
		return n
	}

	// Below the threshold: nothing is appended.
	setChatContext(c, 1000, 0, 0, 200000, "claude-sonnet-5")
	NoteContextPressure(c)
	if notices() != 0 || c.CtxWarned {
		t.Fatal("warned below the threshold")
	}
	// Over it: appended exactly once.
	setChatContext(c, 170000, 0, 0, 200000, "claude-sonnet-5")
	NoteContextPressure(c)
	NoteContextPressure(c)
	if notices() != 1 || !c.CtxWarned {
		t.Fatalf("notices = %d (CtxWarned=%v), want exactly 1", notices(), c.CtxWarned)
	}
	// Falling back under (compaction, say) resets the flag, so crossing again warns once more.
	setChatContext(c, 50000, 0, 0, 200000, "claude-sonnet-5")
	NoteContextPressure(c)
	if c.CtxWarned {
		t.Fatal("CtxWarned not reset after falling under the threshold")
	}
	setChatContext(c, 190000, 0, 0, 200000, "claude-sonnet-5")
	NoteContextPressure(c)
	if notices() != 2 {
		t.Fatalf("notices = %d, want 2 after a re-crossing", notices())
	}
}

func TestCtxPressureContent(t *testing.T) {
	u := &usagex.ContextUsage{Tokens: 170000, Window: 200000, Pct: 85}
	s := ctxPressureContent(u)
	for _, want := range []string{"85%", "170k", "200k", "新しいチャット"} {
		if !strings.Contains(s, want) {
			t.Fatalf("content %q missing %q", s, want)
		}
	}
}
