package main

import "testing"

// A synthetic codex rollout exercising the event shapes we normalize: session_meta
// (context), a dropped developer message, a wrapper-only user turn (dropped), a real
// user prompt, an assistant reply, a function_call trace, and a token_count that must
// attach usage to the preceding assistant turn.
func codexRolloutLines() [][]byte {
	return [][]byte{
		[]byte(`{"timestamp":"2026-06-29T00:00:00Z","type":"session_meta","payload":{"cwd":"/home/dev/repos/x","git":{"branch":"main"}}}`),
		[]byte(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>..."}]}}`),
		[]byte(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>cwd=/x</environment_context>"}]}}`),
		[]byte(`{"timestamp":"2026-06-29T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello codex"}]}}`),
		[]byte(`{"timestamp":"2026-06-29T00:00:02Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"ls\",\"-la\"]}"}}`),
		[]byte(`{"timestamp":"2026-06-29T00:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi there"}]}}`),
		[]byte(`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000,"cached_input_tokens":800,"output_tokens":42}}}}`),
	}
}

func TestCodexParseRollout(t *testing.T) {
	turns := codexParseRollout(codexRolloutLines())
	// developer + wrapper user are dropped; user prompt, assistant, function_call remain.
	if len(turns) != 3 {
		t.Fatalf("want 3 turns, got %d: %+v", len(turns), turns)
	}

	if turns[0].Role != "user" || turns[0].Text != "hello codex" {
		t.Fatalf("turn0 = %+v, want user 'hello codex'", turns[0])
	}
	if turns[0].Cwd != "/home/dev/repos/x" || turns[0].Branch != "main" {
		t.Fatalf("turn0 context = cwd %q branch %q, want session_meta values", turns[0].Cwd, turns[0].Branch)
	}
	// Absolute line index preserved (the user prompt is line 3).
	if turns[0].Idx != 3 {
		t.Fatalf("turn0 idx = %d, want 3 (absolute line index)", turns[0].Idx)
	}

	// The tool call (function_call) is a faint assistant-side trace, emitted before the
	// final answer; the Console merges it into the adjacent assistant block.
	if turns[1].Role != "assistant" || len(turns[1].Parts) != 1 || turns[1].Parts[0].Kind != "tool" {
		t.Fatalf("turn1 = %+v, want assistant tool part", turns[1])
	}
	if turns[1].Parts[0].Tool != "shell" || turns[1].Parts[0].Info != "ls -la" {
		t.Fatalf("turn1 tool = %q info %q, want shell / 'ls -la'", turns[1].Parts[0].Tool, turns[1].Parts[0].Info)
	}

	if turns[2].Role != "assistant" || turns[2].Text != "hi there" {
		t.Fatalf("turn2 = %+v, want assistant 'hi there'", turns[2])
	}
	// token_count attaches to the most recent assistant turn (the final answer): fresh =
	// input - cached.
	if turns[2].InTok != 200 || turns[2].OutTok != 42 || turns[2].CacheRead != 800 {
		t.Fatalf("turn2 usage = in %d out %d read %d, want 200/42/800",
			turns[2].InTok, turns[2].OutTok, turns[2].CacheRead)
	}
}

func TestCodexIsWrapper(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"<environment_context>x", true},
		{"<user_instructions>do", true},
		{"  <skills_instructions>...", true},
		{"hello codex", false},
		{"please read <environment_context>", false}, // tag not at start = a real prompt
	}
	for _, c := range cases {
		if got := codexIsWrapper(c.text); got != c.want {
			t.Errorf("codexIsWrapper(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestCodexParseRolloutEmpty(t *testing.T) {
	if turns := codexParseRollout(nil); len(turns) != 0 {
		t.Fatalf("empty rollout -> %d turns, want 0", len(turns))
	}
	// A rollout with only bookkeeping/system lines yields no displayable turns.
	only := [][]byte{
		[]byte(`{"type":"session_meta","payload":{"cwd":"/x"}}`),
		[]byte(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"sys"}]}}`),
		[]byte(`{"type":"event_msg","payload":{"type":"task_started"}}`),
	}
	if turns := codexParseRollout(only); len(turns) != 0 {
		t.Fatalf("system-only rollout -> %d turns, want 0", len(turns))
	}
}
