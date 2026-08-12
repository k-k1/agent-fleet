package codex

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestCodexDelegationPart(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"response_item","payload":{"type":"function_call","name":"spawn_agent","call_id":"s1","arguments":"{\"task_name\":\"audit_ui\",\"message\":\"Inspect the mirror UI\",\"fork_turns\":\"all\"}"}}`),
		[]byte(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"s1","output":"agent started"}}`),
	}
	turns, _ := parseRollout(lines)
	if len(turns) != 1 || len(turns[0].Parts) != 1 {
		t.Fatalf("turns = %+v, want one delegation turn", turns)
	}
	p := turns[0].Parts[0]
	if p.Kind != "delegation" || p.Tool != "spawn_agent" || p.Info != "audit_ui" ||
		p.AgentType != "audit_ui" || p.Prompt != "Inspect the mirror UI" || p.Status != "requested" {
		t.Fatalf("delegation = %+v", p)
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

func TestCodexCleanUserTextKeepsPromptAfterStartupContext(t *testing.T) {
	raw := `<recommended_plugins>
Here is a list of plugins that are available but not installed.
</recommended_plugins>
# AGENTS.md instructions for /home/dev/repos/x
<INSTRUCTIONS>
# Workspace Guide
internal policy
</INSTRUCTIONS>
<environment_context>
  <cwd>/home/dev/repos/x</cwd>
  <shell>bash</shell>
</environment_context>
メモからセッションへ送れない <image name=[Image #1] path="/home/dev/.cache/agent-fleet/pasted/a/paste-1.png">`
	want := `メモからセッションへ送れない <image name=[Image #1] path="/home/dev/.cache/agent-fleet/pasted/a/paste-1.png">`
	if got := cleanUserText(raw); got != want {
		t.Fatalf("cleanUserText = %q, want %q", got, want)
	}

	item := wrapItem(t, map[string]any{
		"type": "message", "role": "user",
		"content": []map[string]string{{"type": "input_text", "text": raw}},
	})
	turns, _ := parseRollout([][]byte{item})
	if len(turns) != 1 || turns[0].Text != want {
		t.Fatalf("turns = %+v, want one clean user prompt", turns)
	}
}

// TestCodexImageOnlyUserTurnNotDropped covers a caption-less paste-and-send: the
// managed turn/start input has no "text" item at all (buildInput only adds one when
// the prompt is non-empty), so a user message whose content is purely an attachment
// (no input_text) must still produce a turn. Previously it was silently dropped
// (text == "" was treated as "not a real prompt" for every content shape), which
// left the Console's optimistic 反映待ち echo with no real user turn to reconcile
// against — stuck until a page reload wiped the client's in-memory echo state.
func TestCodexImageOnlyUserTurnNotDropped(t *testing.T) {
	item := wrapItem(t, map[string]any{
		"type": "message", "role": "user",
		"content": []map[string]string{{"type": "input_image", "image_url": "data:image/png;base64,AAAA"}},
	})
	turns, _ := parseRollout([][]byte{item})
	if len(turns) != 1 || turns[0].Role != "user" {
		t.Fatalf("turns = %+v, want one (possibly empty-text) user turn", turns)
	}
}

// TestCodexImageOnlyUserTurnKeepsPath covers the case where Codex does preserve the
// uploaded path on the echoed attachment item: it should be folded into the turn's
// text so the existing pasted-path thumbnail/echo matching (pastedImages.ts
// PASTE_PATH_RE) still recognizes it.
func TestCodexImageOnlyUserTurnKeepsPath(t *testing.T) {
	path := "/home/dev/.cache/agent-fleet/pasted/a/paste-1.png"
	item := wrapItem(t, map[string]any{
		"type": "message", "role": "user",
		"content": []map[string]string{{"type": "localImage", "path": path}},
	})
	turns, _ := parseRollout([][]byte{item})
	if len(turns) != 1 || !strings.Contains(turns[0].Text, path) {
		t.Fatalf("turns = %+v, want one user turn whose text contains %q", turns, path)
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

func TestLatestRolloutLifecycle(t *testing.T) {
	lines := []string{
		`{"timestamp":"2026-07-14T18:00:00Z","type":"event_msg","payload":{"type":"task_complete"}}`,
		`{"timestamp":"2026-07-14T18:01:00Z","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"type":"response_item","payload":{"type":"message"}}`,
	}
	state, at := latestRolloutLifecycle(lines)
	if state != "task_started" || !at.Equal(time.Date(2026, 7, 14, 18, 1, 0, 0, time.UTC)) {
		t.Fatalf("lifecycle = %q %v, want latest task_started", state, at)
	}

	lines = append(lines, `{"timestamp":"2026-07-14T18:02:00.123Z","type":"event_msg","payload":{"type":"task_complete"}}`)
	state, at = latestRolloutLifecycle(lines)
	if state != "task_complete" || at.Nanosecond() != 123000000 {
		t.Fatalf("lifecycle = %q %v, want fractional task_complete", state, at)
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
	cases := []struct {
		in        string
		questions []transcript.Question
		want      string
	}{
		// single-question select: the label, not the raw envelope
		{`{"answers":{"ask_user_test":{"answers":["質問だけ確認 (Recommended)"]}}}`, nil, "質問だけ確認 (Recommended)"},
		// multi-select within one question: joined labels
		{`{"answers":{"q":{"answers":["AWS","セルフホスト"]}}}`, nil, "AWS, セルフホスト"},
		// multiple questions with no ids to match against (older/synthetic rollout):
		// flattened, keys sorted for a stable order
		{`{"answers":{"b":{"answers":["Two"]},"a":{"answers":["One"]}}}`, nil, "One, Two"},
		// multiple questions WITH ids: anchored per-question prose, not flattened —
		// this is what the Console's per-question parser (questionAnswers.ts) needs to
		// attribute each answer to its own card instead of every card showing every
		// question's combined picks.
		{
			`{"answers":{"b":{"answers":["Two"]},"a":{"answers":["One"]}}}`,
			[]transcript.Question{{ID: "a", Question: "Q1?"}, {ID: "b", Question: "Q2?"}},
			`"Q1?"="One", "Q2?"="Two"`,
		},
		// ids present but one doesn't match the answers map: falls back to flattened
		// rather than dropping the mismatched question's answer.
		{
			`{"answers":{"a":{"answers":["One"]},"b":{"answers":["Two"]}}}`,
			[]transcript.Question{{ID: "a", Question: "Q1?"}, {ID: "missing", Question: "Q2?"}},
			"One, Two",
		},
		// unknown shape falls back to the raw output (answer never lost)
		{`plain free text`, nil, "plain free text"},
		{`{"answers":{}}`, nil, `{"answers":{}}`},
	}
	for _, c := range cases {
		if got := answerText(c.in, c.questions); got != c.want {
			t.Errorf("answerText(%q, %+v) = %q, want %q", c.in, c.questions, got, c.want)
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

// TestCodexQuestionAnswerFromRolloutMultiQuestion is the multi-question counterpart of
// TestCodexQuestionAnswerFromRollout: one request_user_input call asking several
// questions must resolve to an answer per question, each attributed to its own id —
// not one flattened string that (via the Console's byAnchors fallback) shows every
// question's combined picks on every card. Shape taken from a real rollout (実測
// 2026-08-12): each questions[].id reappears verbatim as the answers map's key.
func TestCodexQuestionAnswerFromRolloutMultiQuestion(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"response_item","payload":{"type":"function_call","call_id":"c1","name":"request_user_input","arguments":"{\"questions\":[{\"id\":\"switch_behavior\",\"question\":\"切替時どうする？\",\"options\":[{\"label\":\"方式ごとに保持\"}]},{\"id\":\"tab_capacity\",\"question\":\"上限は？\",\"options\":[{\"label\":\"全体24件まで\"}]}]}"}}`),
		[]byte(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"{\"answers\":{\"switch_behavior\":{\"answers\":[\"方式ごとに保持\"]},\"tab_capacity\":{\"answers\":[\"全体24件まで\"]}}}"}}`),
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
	want := `"切替時どうする？"="方式ごとに保持", "上限は？"="全体24件まで"`
	if got != want {
		t.Fatalf("question answer = %q, want %q", got, want)
	}
}

func TestCodexExecScriptParts(t *testing.T) {
	// codex 0.144+ "exec" tool: a JS snippet that applies a patch and runs a command.
	js := `const patch = "*** Begin Patch\n*** Update File: /p/README.md\n@@\n old line\n+new line\n*** End Patch";
const a = await tools.apply_patch(patch);
const r = await tools.exec_command({cmd:"ls -la && echo done","workdir":"/p","yield_time_ms":10000}); text(r.output)`
	parts := execScriptParts(js)
	if len(parts) < 2 {
		t.Fatalf("execScriptParts = %+v, want an exec_command trace + a diff part", parts)
	}
	if parts[0].Kind != "tool" || parts[0].Tool != "exec_command" || parts[0].Info != "ls -la && echo done" {
		t.Errorf("parts[0] = %+v, want exec_command trace with the shell command", parts[0])
	}
	var diff *transcript.Part
	for i := range parts {
		if parts[i].Tool == "apply_patch" || parts[i].File != "" {
			diff = &parts[i]
		}
	}
	if diff == nil || diff.File == "" || len(diff.Edits) == 0 {
		t.Fatalf("no apply_patch diff part with File+Edits in %+v", parts)
	}
	if !strings.Contains(diff.File, "README.md") {
		t.Errorf("diff.File = %q, want the patched path", diff.File)
	}
}

func TestCodexExecScriptCommandOnly(t *testing.T) {
	js := `const r = await tools.exec_command({cmd:"rtk git status --short","workdir":"/p"}); text(r.output)`
	parts := execScriptParts(js)
	if len(parts) != 1 || parts[0].Tool != "exec_command" || parts[0].Info != "rtk git status --short" {
		t.Fatalf("execScriptParts = %+v, want a single exec_command trace", parts)
	}
	if isExecScript(`*** Begin Patch\n…`) {
		t.Errorf("a bare patch envelope must not be treated as a JS exec script")
	}
}

// TestCodexCustomToolCallExec runs the end-to-end path for codex 0.144+: a custom_tool_call
// name=exec + its array-shaped output resolve to a clean command trace + diff with output.
func TestCodexCustomToolCallExec(t *testing.T) {
	input := `const r = await tools.exec_command({cmd:"echo hi","workdir":"/p"}); text(r.output)`
	call := map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "c1", "input": input}
	// output is codex 0.144's [{type:input_text,text}] array
	outBlocks := []map[string]string{{"type": "input_text", "text": "Script completed\nOutput:\n"}, {"type": "input_text", "text": "hi\n"}}
	callOut := map[string]any{"type": "custom_tool_call_output", "call_id": "c1", "output": outBlocks}
	lines := [][]byte{
		wrapItem(t, call),
		wrapItem(t, callOut),
	}
	turns, _ := parseRollout(lines)
	var got *transcript.Part
	for i := range turns {
		for j := range turns[i].Parts {
			if turns[i].Parts[j].Tool == "exec_command" {
				got = &turns[i].Parts[j]
			}
		}
	}
	if got == nil {
		t.Fatalf("no exec_command part in %+v", turns)
	}
	if got.Info != "echo hi" {
		t.Errorf("info = %q, want the shell command", got.Info)
	}
	if !strings.Contains(got.Output, "hi") || strings.Contains(got.Output, "input_text") {
		t.Errorf("output = %q, want the concatenated command text, not the raw array", got.Output)
	}
}

func TestCodexExecScriptImageGen(t *testing.T) {
	// The imagegen skill's built-in call: prompt as a backtick template literal.
	js := "const r = await tools.image_gen__imagegen({prompt: `Use case: logo\nPrimary request: red circle`});\nimage(r.image_url);"
	parts := execScriptParts(js)
	if len(parts) != 1 || parts[0].Tool != "image_gen" {
		t.Fatalf("execScriptParts = %+v, want a single image_gen trace", parts)
	}
	if !strings.Contains(parts[0].Info, "red circle") {
		t.Errorf("info = %q, want the generation prompt", parts[0].Info)
	}
}

func TestCodexExecScriptViewImage(t *testing.T) {
	js := `const r = await tools.view_image({path:"/home/u/assets/red.png",detail:"original"});
image(r.image_url);`
	parts := execScriptParts(js)
	if len(parts) != 1 || parts[0].Tool != "view_image" || parts[0].Info != "/home/u/assets/red.png" {
		t.Fatalf("execScriptParts = %+v, want a single view_image trace with the path", parts)
	}
}

// TestCodexImageGenUserFile runs the end-to-end imagegen shape from a real 0.144.1
// rollout: the exec(image_gen) call, then the wait call whose output announces the
// saved file and re-embeds the image as base64 noise. The saved path must surface as
// a userfile part (the Console's 共有ファイル panel) and the noise must not leak into
// the wait trace's output.
func TestCodexImageGenUserFile(t *testing.T) {
	genPath := "/home/u/.codex/generated_images/abc/exec-123.png"
	lines := [][]byte{
		wrapItem(t, map[string]any{
			"type": "custom_tool_call", "name": "exec", "call_id": "c1",
			"input": "const r = await tools.image_gen__imagegen({prompt: `red circle`});",
		}),
		wrapItem(t, map[string]any{
			"type": "custom_tool_call_output", "call_id": "c1",
			"output": []map[string]string{{"type": "input_text", "text": "Script running with cell ID 1\n"}},
		}),
		wrapItem(t, map[string]any{
			"type": "function_call", "name": "wait", "call_id": "c2",
			"arguments": `{"cell_id":"1"}`,
		}),
		wrapItem(t, map[string]any{
			"type": "function_call_output", "call_id": "c2",
			"output": []map[string]string{
				{"type": "input_text", "text": "Script completed\nOutput:\n"},
				{"type": "input_image", "image_url": "data:image/png;base64,AAAA"},
				{"type": "input_text", "text": "Generated images are saved to /home/u/.codex/generated_images/abc as " + genPath + " by default.\n"},
				{"type": "input_text", "text": `{"image_url":"data:image/png;base64,AAAA","output_hint":"Generated images are saved to /home/u/.codex/generated_images/abc as ` + genPath + ` by default."}`},
			},
		}),
	}
	turns, _ := parseRollout(lines)
	var gen, wait, files *transcript.Part
	for i := range turns {
		for j := range turns[i].Parts {
			switch {
			case turns[i].Parts[j].Tool == "image_gen":
				gen = &turns[i].Parts[j]
			case turns[i].Parts[j].Tool == "wait":
				wait = &turns[i].Parts[j]
			case turns[i].Parts[j].Kind == "userfile":
				files = &turns[i].Parts[j]
			}
		}
	}
	if gen == nil || gen.Info != "red circle" {
		t.Fatalf("no image_gen trace with the prompt in %+v", turns)
	}
	if wait == nil || !strings.Contains(wait.Output, "Generated images are saved") {
		t.Fatalf("no wait trace carrying the completion notice in %+v", turns)
	}
	if strings.Contains(wait.Output, "data:image") {
		t.Errorf("wait output leaks base64 noise: %q", wait.Output)
	}
	if files == nil || len(files.Files) != 1 || files.Files[0] != genPath {
		t.Fatalf("userfile part = %+v, want exactly [%s]", files, genPath)
	}
}

// TestCodexParseCallOutputViewImage exercises parseCallOutput's 4th return value
// (inlineImages) directly: a view_image-shaped output — an input_image block with no
// "Generated images are saved" text anywhere — must report genImages == nil (no
// already-servable path exists) and inlineImages carrying the raw data URL.
func TestCodexParseCallOutputViewImage(t *testing.T) {
	payload := []byte(`{"type":"custom_tool_call_output","call_id":"call_view1","output":[` +
		`{"type":"input_text","text":"Script completed\nWall time 0.1 seconds\nOutput:\n"},` +
		`{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}` +
		`]}`)
	id, out, gen, imgs := parseCallOutput(payload)
	if id != "call_view1" {
		t.Errorf("callID = %q, want call_view1", id)
	}
	if !strings.Contains(out, "Script completed") || strings.Contains(out, "data:image") {
		t.Errorf("output = %q, want the text block only, no base64 leak", out)
	}
	if gen != nil {
		t.Errorf("genImages = %v, want nil (no announced path)", gen)
	}
	if len(imgs) != 1 || imgs[0] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Errorf("inlineImages = %v, want the one data URL", imgs)
	}
}

// TestCodexViewImageStashedFromRollout is the parseRollout-level counterpart: a
// real-shaped view_image call+output must leave the tool part carrying
// ViewImageCallID/ViewImageData (not yet persisted — parseRollout is pure, only
// readTranscript's persistViewImages call performs I/O).
func TestCodexViewImageStashedFromRollout(t *testing.T) {
	lines := [][]byte{
		wrapItem(t, map[string]any{
			"type": "custom_tool_call", "name": "exec", "call_id": "call_view1",
			"input": `const r = await tools.view_image({path:"/tmp/shot.png",detail:"high"});
image(r.image_url);`,
		}),
		wrapItem(t, map[string]any{
			"type": "custom_tool_call_output", "call_id": "call_view1",
			"output": []map[string]string{
				{"type": "input_text", "text": "Script completed\nWall time 0.1 seconds\nOutput:\n"},
				{"type": "input_image", "image_url": "data:image/png;base64,iVBORw0KGgo="},
			},
		}),
	}
	turns, _ := parseRollout(lines)
	var p *transcript.Part
	for i := range turns {
		for j := range turns[i].Parts {
			if turns[i].Parts[j].Tool == "view_image" {
				p = &turns[i].Parts[j]
			}
		}
	}
	if p == nil {
		t.Fatalf("no view_image trace in %+v", turns)
	}
	if p.ViewImageCallID != "call_view1" {
		t.Errorf("ViewImageCallID = %q, want call_view1", p.ViewImageCallID)
	}
	if len(p.ViewImageData) != 1 || p.ViewImageData[0] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Errorf("ViewImageData = %v, want the one data URL", p.ViewImageData)
	}
}

// TestCodexPersistViewImagesTo covers the actual I/O step: decoding the stashed data
// URL to a servable file, appending a sibling userfile part, and — since
// readTranscript re-parses the whole rollout on every /messages poll — staying
// idempotent (and still re-emitting the userfile part, not just on the first poll)
// across a second independent parse of the same rollout. dir is t.TempDir(), never
// the real home (this repo's tests must not touch real config/state paths).
func TestCodexPersistViewImagesTo(t *testing.T) {
	want := []byte("PNGDATA")
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(want)
	lines := [][]byte{
		wrapItem(t, map[string]any{
			"type": "custom_tool_call", "name": "exec", "call_id": "call_view1",
			"input": `const r = await tools.view_image({path:"/tmp/shot.png",detail:"high"});
image(r.image_url);`,
		}),
		wrapItem(t, map[string]any{
			"type": "custom_tool_call_output", "call_id": "call_view1",
			"output": []map[string]string{
				{"type": "input_text", "text": "Script completed\nOutput:\n"},
				{"type": "input_image", "image_url": dataURL},
			},
		}),
	}
	dir := t.TempDir()
	wantPath := filepath.Join(dir, "call_view1-0.png")

	checkPersisted := func(turns []transcript.Turn) {
		t.Helper()
		var files *transcript.Part
		for i := range turns {
			for j := range turns[i].Parts {
				if turns[i].Parts[j].Kind == "userfile" {
					files = &turns[i].Parts[j]
				}
			}
		}
		if files == nil || len(files.Files) != 1 || files.Files[0] != wantPath {
			t.Fatalf("userfile part = %+v, want exactly [%s]", files, wantPath)
		}
		got, err := os.ReadFile(wantPath)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", wantPath, err)
		}
		if string(got) != string(want) {
			t.Errorf("persisted bytes = %q, want %q", got, want)
		}
	}

	turns, _ := parseRollout(lines)
	persistViewImagesTo(turns, dir)
	checkPersisted(turns)
	for i := range turns {
		for j := range turns[i].Parts {
			if len(turns[i].Parts[j].ViewImageData) != 0 || turns[i].Parts[j].ViewImageCallID != "" {
				t.Errorf("part %+v: ViewImageData/CallID should be cleared after persisting", turns[i].Parts[j])
			}
		}
	}

	// Simulate a second, independent /messages poll re-parsing the same rollout: a
	// fresh turns slice with the data URL freshly stashed again. The file already
	// exists on disk (idempotent skip), but the userfile part must still be emitted —
	// not just on the very first poll.
	turns2, _ := parseRollout(lines)
	persistViewImagesTo(turns2, dir)
	checkPersisted(turns2)
}

// wrapItem marshals a response_item payload line for parseRollout.
func wrapItem(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "response_item", "payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustJSON(v string) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
