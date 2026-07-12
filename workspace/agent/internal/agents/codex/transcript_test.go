package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// A synthetic codex rollout exercising the event shapes we normalize: session_meta
// (context), a dropped developer message, a wrapper-only user turn (dropped), a real
// user prompt, an assistant reply, a function_call trace, and a token_count that must
// attach usage to the preceding assistant turn.
func codexRolloutLines() [][]byte {
	return [][]byte{
		[]byte(`{"timestamp":"2026-06-29T00:00:00Z","type":"session_meta","payload":{"cwd":"/home/dev/repos/x","git":{"branch":"main"}}}`),
		[]byte(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>..."}]}}`),
		[]byte(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>cwd=/x</environment_context>"}]}}`),
		[]byte(`{"type":"turn_context","payload":{"model":"gpt-5.5","cwd":"/home/dev/repos/x","collaboration_mode":{"settings":{"reasoning_effort":"high"}}}}`),
		[]byte(`{"timestamp":"2026-06-29T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello codex"}]}}`),
		[]byte(`{"timestamp":"2026-06-29T00:00:02Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"ls\",\"-la\"]}"}}`),
		[]byte(`{"timestamp":"2026-06-29T00:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi there"}]}}`),
		[]byte(`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000,"cached_input_tokens":800,"output_tokens":42},"model_context_window":258400}}}`),
	}
}

func TestCodexParseRollout(t *testing.T) {
	turns, _ := parseRollout(codexRolloutLines())
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
	// Absolute line index preserved (the user prompt is line 4).
	if turns[0].Idx != 4 {
		t.Fatalf("turn0 idx = %d, want 4 (absolute line index)", turns[0].Idx)
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
	// Model + reasoning effort from turn_context are attached to assistant turns.
	if turns[2].Model != "gpt-5.5" {
		t.Fatalf("turn2 model = %q, want gpt-5.5", turns[2].Model)
	}
	if turns[2].Effort != "high" {
		t.Fatalf("turn2 effort = %q, want high", turns[2].Effort)
	}
	// token_count attaches to the most recent assistant turn (the final answer): fresh =
	// input - cached.
	if turns[2].InTok != 200 || turns[2].OutTok != 42 || turns[2].CacheRead != 800 {
		t.Fatalf("turn2 usage = in %d out %d read %d, want 200/42/800",
			turns[2].InTok, turns[2].OutTok, turns[2].CacheRead)
	}
	// Real context-window size from token_count feeds the accurate gauge.
	if turns[2].CtxWindow != 258400 {
		t.Fatalf("turn2 ctxWindow = %d, want 258400", turns[2].CtxWindow)
	}
}

func TestCodexReasoningPlanOutput(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"session_meta","payload":{"cwd":"/x"}}`),
		[]byte(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]}}`),
		[]byte(`{"type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"let me think"}]}}`),
		[]byte(`{"type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"c1","arguments":"{\"command\":\"ls\"}"}}`),
		[]byte(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"file1\nfile2"}}`),
		[]byte(`{"type":"response_item","payload":{"type":"function_call","name":"update_plan","arguments":"{\"plan\":[{\"step\":\"read\",\"status\":\"completed\"},{\"step\":\"write\",\"status\":\"in_progress\"}]}"}}`),
		[]byte(`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`),
	}
	turns, tasks := parseRollout(lines)

	// Turns: user, thinking, tool(shell), assistant. update_plan is NOT a turn.
	if len(turns) != 4 {
		t.Fatalf("want 4 turns, got %d: %+v", len(turns), turns)
	}
	if turns[1].Parts[0].Kind != "thinking" || turns[1].Parts[0].Text != "let me think" {
		t.Fatalf("turn1 = %+v, want thinking 'let me think'", turns[1].Parts[0])
	}
	if turns[2].Parts[0].Kind != "tool" || turns[2].Parts[0].Output != "file1\nfile2" {
		t.Fatalf("turn2 = %+v, want tool with output attached", turns[2].Parts[0])
	}

	// update_plan reconstructs the ToDo list.
	if len(tasks) != 2 || tasks[0].Subject != "read" || tasks[0].Status != "completed" ||
		tasks[1].Subject != "write" || tasks[1].Status != "in_progress" {
		t.Fatalf("tasks = %+v, want [read/completed, write/in_progress]", tasks)
	}
}

func TestCodexQuestion(t *testing.T) {
	// An answered request_user_input, then a pending one (no output yet).
	answered := [][]byte{
		[]byte(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]}}`),
		[]byte(`{"type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"q1","arguments":"{\"questions\":[{\"header\":\"H\",\"question\":\"pick?\",\"options\":[{\"label\":\"A\"},{\"label\":\"B\"}]}]}"}}`),
		[]byte(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"q1","output":"B"}}`),
		[]byte(`{"type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"q2","arguments":"{\"questions\":[{\"question\":\"next?\",\"options\":[{\"label\":\"X\"}]}]}"}}`),
	}
	turns, _, pending, _ := parseRolloutFull(answered)

	// The answered question renders as a question part with its answer.
	var q *transcript.Part
	for i := range turns {
		if len(turns[i].Parts) > 0 && turns[i].Parts[0].Kind == "question" && turns[i].Parts[0].Questions[0].Question == "pick?" {
			q = &turns[i].Parts[0]
		}
	}
	if q == nil || q.Answer != "B" {
		t.Fatalf("answered question not found or wrong answer: %+v", q)
	}
	// The unanswered request_user_input is the pending question.
	if len(pending) != 1 || pending[0].Question != "next?" {
		t.Fatalf("pending = %+v, want the 'next?' question", pending)
	}
}

func TestCodexMode(t *testing.T) {
	plan := [][]byte{
		[]byte(`{"type":"turn_context","payload":{"model":"gpt-5.5","collaboration_mode":{"mode":"plan"}}}`),
		[]byte(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`),
	}
	if _, _, _, mode := parseRolloutFull(plan); mode != "plan" {
		t.Fatalf("plan mode = %q, want plan", mode)
	}
	def := [][]byte{
		[]byte(`{"type":"turn_context","payload":{"model":"gpt-5.5","collaboration_mode":{"mode":"default"}}}`),
		[]byte(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`),
	}
	if _, _, _, mode := parseRolloutFull(def); mode != "normal" {
		t.Fatalf("default mode = %q, want normal", mode)
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
		{"# AGENTS.md instructions\n\n<INSTRUCTIONS>\n# Workspace Guide", true},
		{"<INSTRUCTIONS>foo", true},
		{"hello codex", false},
		{"please read <environment_context>", false}, // tag not at start = a real prompt
	}
	for _, c := range cases {
		if got := isWrapper(c.text); got != c.want {
			t.Errorf("isWrapper(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestCodexParseRolloutEmpty(t *testing.T) {
	if turns, _ := parseRollout(nil); len(turns) != 0 {
		t.Fatalf("empty rollout -> %d turns, want 0", len(turns))
	}
	// A rollout with only bookkeeping/system lines yields no displayable turns.
	only := [][]byte{
		[]byte(`{"type":"session_meta","payload":{"cwd":"/x"}}`),
		[]byte(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"sys"}]}}`),
		[]byte(`{"type":"event_msg","payload":{"type":"task_started"}}`),
	}
	if turns, _ := parseRollout(only); len(turns) != 0 {
		t.Fatalf("system-only rollout -> %d turns, want 0", len(turns))
	}
}

func TestCodexApplyPatch(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: docs/new.md\n+# hi\n+body\n*** Update File: main.go\n@@ func x\n ctx\n-old line\n+new line\n*** Delete File: gone.txt\n*** End Patch\n"
	lines := [][]byte{
		[]byte(`{"timestamp":"2026-06-29T00:00:00Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"c1","input":` + string(mustJSON(patch)) + `}}`),
		[]byte(`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":"Success. Updated the following files:\nA docs/new.md"}}`),
	}
	turns, _ := parseRollout(lines)
	if len(turns) != 1 {
		t.Fatalf("want 1 turn, got %d: %+v", len(turns), turns)
	}
	ps := turns[0].Parts
	if len(ps) != 3 {
		t.Fatalf("want 3 parts (add/update/delete), got %d: %+v", len(ps), ps)
	}
	if ps[0].File != "docs/new.md" || len(ps[0].Edits) != 1 || ps[0].Edits[0].Old != "" || ps[0].Edits[0].New != "# hi\nbody" {
		t.Fatalf("add part = %+v", ps[0])
	}
	if ps[1].File != "main.go" || len(ps[1].Edits) != 1 ||
		ps[1].Edits[0].Old != "ctx\nold line" || ps[1].Edits[0].New != "ctx\nnew line" {
		t.Fatalf("update part = %+v", ps[1])
	}
	if ps[2].Info != "delete gone.txt" || len(ps[2].Edits) != 0 {
		t.Fatalf("delete part = %+v", ps[2])
	}
	// The custom_tool_call_output attaches to the call's first part.
	if !strings.HasPrefix(ps[0].Output, "Success.") {
		t.Fatalf("output not attached: %+v", ps[0])
	}
}

func TestCodexApplyPatchFunctionCall(t *testing.T) {
	// apply_patch can also arrive as a function_call with {"input": patch} arguments.
	patch := "*** Begin Patch\n*** Update File: a.txt\n-x\n+y\n*** End Patch"
	args := string(mustJSON(`{"input":` + string(mustJSON(patch)) + `}`))
	lines := [][]byte{
		[]byte(`{"type":"response_item","payload":{"type":"function_call","name":"apply_patch","call_id":"c2","arguments":` + args + `}}`),
	}
	turns, _ := parseRollout(lines)
	if len(turns) != 1 || len(turns[0].Parts) != 1 {
		t.Fatalf("turns = %+v", turns)
	}
	p := turns[0].Parts[0]
	if p.File != "a.txt" || len(p.Edits) != 1 || p.Edits[0].Old != "x" || p.Edits[0].New != "y" {
		t.Fatalf("part = %+v", p)
	}
}

func TestCodexCompacted(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"timestamp":"2026-06-29T00:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`),
		[]byte(`{"timestamp":"2026-06-29T00:00:01Z","type":"compacted","payload":{"message":"summary of earlier context"}}`),
		[]byte(`{"type":"event_msg","payload":{"type":"context_compacted"}}`), // same compaction's event — deduped
	}
	turns, _ := parseRollout(lines)
	if len(turns) != 2 {
		t.Fatalf("want 2 turns (user + ONE compact block), got %d: %+v", len(turns), turns)
	}
	c := turns[1]
	if !c.Compact || c.Text != "summary of earlier context" {
		t.Fatalf("compact turn = %+v", c)
	}
	// An event_msg WITHOUT a preceding compacted line still yields a marker.
	only := [][]byte{
		[]byte(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`),
		[]byte(`{"type":"event_msg","payload":{"type":"context_compacted"}}`),
	}
	turns, _ = parseRollout(only)
	if len(turns) != 2 || !turns[1].Compact {
		t.Fatalf("event-only compaction -> %+v", turns)
	}
}

func TestCodexCompactedReplacementHistory(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"compacted","payload":{"replacement_history":[{"role":"user","content":[{"type":"input_text","text":"<user_instructions>x</user_instructions>"}]},{"role":"user","content":[{"type":"input_text","text":"the summary"}]}]}}`),
	}
	turns, _ := parseRollout(lines)
	if len(turns) != 1 || !turns[0].Compact || turns[0].Text != "the summary" {
		t.Fatalf("turns = %+v, want one compact turn with 'the summary' (wrapper dropped)", turns)
	}
}

func TestCodexAnswerText(t *testing.T) {
	cases := []struct{ in, want string }{
		// single-question select: the label, not the raw envelope
		{`{"answers":{"ask_user_test":{"answers":["質問だけ確認 (Recommended)"]}}}`, "質問だけ確認 (Recommended)"},
		// multi-select within one question: joined labels
		{`{"answers":{"q":{"answers":["AWS","セルフホスト"]}}}`, "AWS, セルフホスト"},
		// multiple questions: flattened, keys sorted for a stable order
		{`{"answers":{"b":{"answers":["Two"]},"a":{"answers":["One"]}}}`, "One, Two"},
		// unknown shape falls back to the raw output (answer never lost)
		{`plain free text`, "plain free text"},
		{`{"answers":{}}`, `{"answers":{}}`},
	}
	for _, c := range cases {
		if got := answerText(c.in); got != c.want {
			t.Errorf("answerText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCodexQuestionAnswerFromRollout checks the end-to-end path: a request_user_input
// call + its envelope output resolve to the clean chosen label on the question part.
func TestCodexQuestionAnswerFromRollout(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"response_item","payload":{"type":"function_call","call_id":"c1","name":"request_user_input","arguments":"{\"questions\":[{\"header\":\"H\",\"question\":\"Q?\",\"options\":[{\"label\":\"Yes\"},{\"label\":\"No\"}]}]}"}}`),
		[]byte(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"{\"answers\":{\"q\":{\"answers\":[\"Yes\"]}}}"}}`),
	}
	turns, _ := parseRollout(lines)
	var got string
	for _, tn := range turns {
		for _, p := range tn.Parts {
			if p.Kind == "question" {
				got = p.Answer
			}
		}
	}
	if got != "Yes" {
		t.Fatalf("question answer = %q, want %q", got, "Yes")
	}
}

func mustJSON(v string) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
