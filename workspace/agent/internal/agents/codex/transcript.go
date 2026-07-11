package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// Codex chat transcript. Unlike claude (a single <sid>.jsonl we read directly),
// codex writes a "rollout" JSONL under ~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-
// <session_id>.jsonl, one JSON event per line. We already capture codex's own
// session_id (sids, from its status hook), so we locate that slot's rollout by
// id and normalize its events into the SAME transcript.Turn/transcript.Part model the Console chat
// consumes for claude. The rollout is append-only JSONL, so — like claude — the line
// order is chronological; here we parse the whole file into ordered turns and let the
// generic /messages windower page over them (see handleGenericMessages).
//
// Event shape (codex 0.14x, verified against real rollouts):
//   {"timestamp","type":"session_meta","payload":{cwd,git:{branch},...}}   — head
//   {"type":"response_item","payload":{"type":"message","role":"user"|"assistant"|
//        "developer","content":[{"type":"input_text"|"output_text","text":...}]}}
//   {"type":"response_item","payload":{"type":"function_call",name,arguments,...}}
//   {"type":"response_item","payload":{"type":"function_call_output"|"reasoning",...}}
//   {"type":"event_msg","payload":{"type":"token_count","info":{...usage...}}}
// developer messages are system instructions (permissions/skills/collaboration mode)
// — noise, dropped. function_call becomes a faint tool trace on the assistant side
// (the Console merges it into the adjacent assistant block, like claude's tool_use).

// jsonlMtime は package main の同名ヘルパの複製（極小のため共有せず重複を許容）。
func jsonlMtime(p string) time.Time {
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// rolloutPath returns the rollout jsonl for a codex session id, or "" if none is
// found yet (the file only exists once codex has started a conversation). The layout is
// ~/.codex/sessions/<Y>/<M>/<D>/rollout-<ts>-<id>.jsonl; we glob the date levels.
func rolloutPath(codexID string) string {
	if codexID == "" {
		return ""
	}
	root := filepath.Join(paths.HomeDir(), ".codex", "sessions")
	// Y/M/D are three glob levels; the filename ends with the session id.
	matches, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "rollout-*"+codexID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	// Normally exactly one; if several matched (id collision is astronomically unlikely),
	// prefer the newest so a resumed session's latest rollout wins.
	best, bestMod := matches[0], jsonlMtime(matches[0])
	for _, m := range matches[1:] {
		if mt := jsonlMtime(m); mt.After(bestMod) {
			best, bestMod = m, mt
		}
	}
	return best
}

// wrapperTags marks a codex user message that is really injected context, not a
// human prompt (environment snapshot, instructions, mode banner). A user turn whose
// text is entirely such a wrapper is dropped from the chat.
var wrapperTags = []string{
	"<environment_context>",
	"<user_instructions>",
	"<permissions instructions>",
	"<collaboration_mode>",
	"<skills_instructions>",
	// codex injects each AGENTS.md (our workspace-notes.md, plus project AGENTS.md) as a
	// user message headed "# AGENTS.md instructions" and wrapped in <INSTRUCTIONS>…; both
	// are injected context, not a human prompt.
	"# AGENTS.md instructions",
	"<INSTRUCTIONS>",
}

// parseRollout normalizes a codex rollout's lines into ordered transcript.Turns. Each
// turn keeps its ABSOLUTE line index as Idx (a stable React key, and the unit the
// generic windower pages over). session_meta seeds the cwd/branch shown as a context
// line; token_count events attach usage to the preceding assistant turn so the chat's
// context gauge works the same as claude's.
func parseRollout(lines [][]byte) ([]transcript.Turn, []transcript.Task) {
	turns, tasks, _, _ := parseRolloutFull(lines)
	return turns, tasks
}

// parseRolloutFull is parseRollout plus the currently-pending question
// (request_user_input awaiting an answer), split out so readTranscript can surface
// it while the two-value form stays convenient for tests.
func parseRolloutFull(lines [][]byte) ([]transcript.Turn, []transcript.Task, []transcript.Question, string) {
	var turns []transcript.Turn
	var tasks []transcript.Task
	var cwd, branch, model, effort, mode string
	lastAssistant := -1           // index of the most recent assistant turn (for usage)
	callTurn := map[string]int{}  // function_call call_id -> its tool/question turn index
	answered := map[string]bool{} // call_ids whose function_call_output arrived
	askCalls := []string{}        // request_user_input call_ids, in order (for pending)
	for i, ln := range lines {
		var ev struct {
			Type      string          `json:"type"`
			Timestamp string          `json:"timestamp"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(ln, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "session_meta":
			cwd, branch = metaContext(ev.Payload)
		case "turn_context":
			// Precedes each turn; carries the model (e.g. "gpt-5.5") and reasoning effort
			// in effect. effort is often null (default) — kept only when codex records one.
			if m, e := turnModel(ev.Payload); m != "" {
				model = m
				effort = e
			}
			// collaboration_mode.mode is "default" | "plan"; normalize to normal/plan.
			if cm := turnMode(ev.Payload); cm != "" {
				mode = cm
			}
		case "compacted":
			// codex compacted its history (auto or /compact) and wrote the replacement
			// summary; shown as claude's collapsible 圧縮されました block.
			turns = append(turns, transcript.Turn{
				Role: "user", Compact: true, Text: compactedText(ev.Payload),
				Idx: i, TS: ev.Timestamp, Cwd: cwd, Branch: branch,
			})
		case "response_item":
			t, callID, ok := parseResponseItem(ev.Payload, ev.Timestamp, i, cwd, branch)
			if ok {
				if t.Role == "assistant" {
					t.Model = model
					t.Effort = effort
				}
				turns = append(turns, t)
				if t.Role == "assistant" {
					lastAssistant = len(turns) - 1
				}
				if callID != "" {
					callTurn[callID] = len(turns) - 1
					if len(t.Parts) > 0 && t.Parts[0].Kind == "question" {
						askCalls = append(askCalls, callID)
					}
				}
				continue
			}
			// Not a displayable turn on its own: a plan update (feeds the ToDo list) or a
			// tool output (attached to its call's trace, or as a question's answer).
			if pt := parsePlan(ev.Payload); pt != nil {
				tasks = pt // update_plan resends the whole list
			}
			if id, out := parseCallOutput(ev.Payload); id != "" {
				answered[id] = true
				if ti, okk := callTurn[id]; okk && len(turns[ti].Parts) > 0 && out != "" {
					if turns[ti].Parts[0].Kind == "question" {
						turns[ti].Parts[0].Answer = out
					} else {
						turns[ti].Parts[0].Output = out
					}
				}
			}
		case "event_msg":
			if in, out, read, win, ok := tokenUsage(ev.Payload); ok && lastAssistant >= 0 {
				turns[lastAssistant].InTok = in
				turns[lastAssistant].OutTok = out
				turns[lastAssistant].CacheRead = read
				if win > 0 {
					turns[lastAssistant].CtxWindow = win
				}
			}
			// context_compacted marks a compaction when no "compacted" line was written
			// (version-dependent). Skip it when the previous turn already is the compact
			// block from that line, so one compaction never renders twice.
			if isContextCompacted(ev.Payload) &&
				(len(turns) == 0 || !turns[len(turns)-1].Compact) {
				turns = append(turns, transcript.Turn{
					Role: "user", Compact: true,
					Idx: i, TS: ev.Timestamp, Cwd: cwd, Branch: branch,
				})
			}
		}
	}
	// Pending question = the last request_user_input still awaiting an answer. Its
	// function_call is already in the rollout, so drop that turn from the transcript
	// (it's surfaced interactively as pending instead) to avoid showing it twice — once
	// answered it stays in the transcript as a normal answered question block.
	var pending []transcript.Question
	for i := len(askCalls) - 1; i >= 0; i-- {
		id := askCalls[i]
		if answered[id] {
			continue
		}
		if ti, ok := callTurn[id]; ok && len(turns[ti].Parts) > 0 {
			pending = turns[ti].Parts[0].Questions
			turns = append(turns[:ti], turns[ti+1:]...)
		}
		break
	}
	return turns, tasks, pending, mode
}

// turnMode reads the collaboration mode from a turn_context payload and normalizes
// it: "plan" → "plan", anything else (default) → "normal". "" when absent.
func turnMode(payload json.RawMessage) string {
	var p struct {
		CollaborationMode struct {
			Mode string `json:"mode"`
		} `json:"collaboration_mode"`
	}
	if json.Unmarshal(payload, &p) != nil || p.CollaborationMode.Mode == "" {
		return ""
	}
	if p.CollaborationMode.Mode == "plan" {
		return "plan"
	}
	return "normal"
}

// parseQuestions parses request_user_input's arguments into transcript.Questions. codex's
// schema mirrors AskUserQuestion (questions[]{question,header,options}); a flatter
// {question,options} form is also accepted. Returns nil when neither is present.
func parseQuestions(payload json.RawMessage) []transcript.Question {
	var p struct {
		Arguments string `json:"arguments"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Arguments == "" {
		return nil
	}
	var multi struct {
		Questions []transcript.Question `json:"questions"`
	}
	if json.Unmarshal([]byte(p.Arguments), &multi) == nil && len(multi.Questions) > 0 {
		return multi.Questions
	}
	var one transcript.Question
	if json.Unmarshal([]byte(p.Arguments), &one) == nil && (one.Question != "" || len(one.Options) > 0) {
		return []transcript.Question{one}
	}
	return nil
}

// metaContext pulls the working dir and git branch from a session_meta payload.
func metaContext(payload json.RawMessage) (cwd, branch string) {
	var m struct {
		Cwd string `json:"cwd"`
		Git struct {
			Branch string `json:"branch"`
		} `json:"git"`
	}
	if json.Unmarshal(payload, &m) != nil {
		return "", ""
	}
	return m.Cwd, m.Git.Branch
}

// turnModel pulls the model name and reasoning effort from a turn_context payload.
// effort lives under collaboration_mode.settings.reasoning_effort (or a top-level
// reasoning_effort); it is commonly null (the model default), in which case "" is
// returned and no effort is shown.
func turnModel(payload json.RawMessage) (model, effort string) {
	var m struct {
		Model             string `json:"model"`
		ReasoningEffort   string `json:"reasoning_effort"`
		CollaborationMode struct {
			Settings struct {
				ReasoningEffort string `json:"reasoning_effort"`
			} `json:"settings"`
		} `json:"collaboration_mode"`
	}
	if json.Unmarshal(payload, &m) != nil {
		return "", ""
	}
	effort = m.ReasoningEffort
	if effort == "" {
		effort = m.CollaborationMode.Settings.ReasoningEffort
	}
	return m.Model, effort
}

// parseResponseItem turns one response_item payload into a transcript.Turn, returning the
// function_call's call_id (when it is one) so its later output can be attached. ok is
// false for non-displayable items (developer/system messages, tool outputs, an
// injected-context user turn); those are handled by the caller (plan / call output).
func parseResponseItem(payload json.RawMessage, ts string, idx int, cwd, branch string) (transcript.Turn, string, bool) {
	var p struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Name    string `json:"name"`
		CallID  string `json:"call_id"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return transcript.Turn{}, "", false
	}
	switch p.Type {
	case "custom_tool_call":
		// A freeform tool call — in practice apply_patch (codex's edit tool, invoked with
		// the raw patch envelope as `input`). Parsed into per-file before/after parts so
		// the trace opens as a diff pane, like claude's Edit/Write. Other custom tools
		// fall back to a plain trace. Output attaches when custom_tool_call_output lands.
		var ct struct {
			Name   string `json:"name"`
			CallID string `json:"call_id"`
			Input  string `json:"input"`
		}
		if json.Unmarshal(payload, &ct) != nil {
			return transcript.Turn{}, "", false
		}
		name := ct.Name
		if name == "" {
			name = "tool"
		}
		parts := patchParts(name, ct.Input)
		if len(parts) == 0 {
			parts = []transcript.Part{{Kind: "tool", Tool: name, Info: transcript.Clip(ct.Input)}}
		}
		return transcript.Turn{
			Role: "assistant", Parts: parts,
			Idx: idx, TS: ts, Cwd: cwd, Branch: branch,
		}, ct.CallID, true
	case "message":
		if p.Role != "user" && p.Role != "assistant" {
			return transcript.Turn{}, "", false // developer/system instructions — noise
		}
		var sb strings.Builder
		for _, c := range p.Content {
			if c.Type == "input_text" || c.Type == "output_text" || c.Type == "text" {
				if c.Text != "" {
					sb.WriteString(c.Text)
				}
			}
		}
		text := strings.TrimSpace(sb.String())
		if text == "" {
			return transcript.Turn{}, "", false
		}
		if p.Role == "user" && isWrapper(text) {
			return transcript.Turn{}, "", false // an injected-context user turn, not a prompt
		}
		return transcript.Turn{
			Role: p.Role, Parts: []transcript.Part{{Kind: "text", Text: text}}, Text: text,
			Idx: idx, TS: ts, Cwd: cwd, Branch: branch,
		}, "", true
	case "reasoning":
		// codex's chain-of-thought summary — shown as a collapsible thinking block (like
		// claude's thinking). Encrypted-only reasoning (no summary text) is skipped.
		if txt := reasoningText(payload); txt != "" {
			return transcript.Turn{
				Role: "assistant", Parts: []transcript.Part{{Kind: "thinking", Text: txt}}, Text: "",
				Idx: idx, TS: ts, Cwd: cwd, Branch: branch,
			}, "", true
		}
		return transcript.Turn{}, "", false
	case "function_call":
		// A tool call: a faint trace on the assistant side (the Console merges it into the
		// adjacent assistant block); its output is attached when the matching
		// function_call_output arrives. update_plan is not a trace — it feeds the ToDo list.
		if p.Name == "update_plan" {
			return transcript.Turn{}, "", false
		}
		// request_user_input is codex's AskUserQuestion: render as a question block, and
		// (when still unanswered) surface as the pending question. call_id lets its answer
		// attach when the output arrives.
		if p.Name == "request_user_input" {
			if qs := parseQuestions(payload); len(qs) > 0 {
				return transcript.Turn{
					Role: "assistant", Parts: []transcript.Part{{Kind: "question", Tool: "request_user_input", Questions: qs}},
					Idx: idx, TS: ts, Cwd: cwd, Branch: branch,
				}, p.CallID, true
			}
		}
		// apply_patch can also arrive as a function_call whose arguments carry the patch
		// envelope as {"input": …} — same diff-pane treatment as the custom_tool_call form.
		if p.Name == "apply_patch" {
			if in := applyPatchInput(payload); in != "" {
				if parts := patchParts(p.Name, in); len(parts) > 0 {
					return transcript.Turn{
						Role: "assistant", Parts: parts,
						Idx: idx, TS: ts, Cwd: cwd, Branch: branch,
					}, p.CallID, true
				}
			}
		}
		name := p.Name
		if name == "" {
			name = "tool"
		}
		return transcript.Turn{
			Role: "assistant", Parts: []transcript.Part{{Kind: "tool", Tool: name, Info: toolInfo(payload)}},
			Idx: idx, TS: ts, Cwd: cwd, Branch: branch,
		}, p.CallID, true
	}
	return transcript.Turn{}, "", false
}

// reasoningText extracts the human-readable reasoning from a reasoning payload:
// the summary_text blocks (codex's shown chain-of-thought), falling back to content
// text. Returns "" when the reasoning is encrypted-only.
func reasoningText(payload json.RawMessage) string {
	var p struct {
		Summary []struct {
			Text string `json:"text"`
		} `json:"summary"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return ""
	}
	var sb strings.Builder
	for _, s := range p.Summary {
		if s.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(s.Text)
		}
	}
	if sb.Len() == 0 {
		for _, c := range p.Content {
			if c.Text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n\n")
				}
				sb.WriteString(c.Text)
			}
		}
	}
	return strings.TrimSpace(sb.String())
}

// parseCallOutput returns a function_call_output's / custom_tool_call_output's call_id
// and its (truncated) output text, or "","" for other payloads. The output is codex's
// tool result; the JSON shape varies (string, or {output:...}) so we best-effort stringify.
func parseCallOutput(payload json.RawMessage) (callID, output string) {
	var p struct {
		Type   string          `json:"type"`
		CallID string          `json:"call_id"`
		Output json.RawMessage `json:"output"`
	}
	if json.Unmarshal(payload, &p) != nil ||
		(p.Type != "function_call_output" && p.Type != "custom_tool_call_output") {
		return "", ""
	}
	out := ""
	if len(p.Output) > 0 {
		if p.Output[0] == '"' {
			_ = json.Unmarshal(p.Output, &out)
		} else {
			// {output:"...", ...} or a structured result — pull a text field if present.
			var m map[string]any
			if json.Unmarshal(p.Output, &m) == nil {
				if s, ok := m["output"].(string); ok {
					out = s
				} else if s, ok := m["content"].(string); ok {
					out = s
				}
			}
			if out == "" {
				out = string(p.Output)
			}
		}
	}
	return p.CallID, transcript.CapOutput(out)
}

// parsePlan turns an update_plan function_call into the current ToDo list. codex
// resends the whole plan each call (like claude's TodoWrite), so the latest wins.
// Returns nil for non-update_plan payloads.
func parsePlan(payload json.RawMessage) []transcript.Task {
	var p struct {
		Type string `json:"type"`
		Name string `json:"name"`
		Args string `json:"arguments"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Type != "function_call" || p.Name != "update_plan" {
		return nil
	}
	var in struct {
		Plan []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"plan"`
	}
	if json.Unmarshal([]byte(p.Args), &in) != nil || len(in.Plan) == 0 {
		return nil
	}
	out := make([]transcript.Task, 0, len(in.Plan))
	for i, s := range in.Plan {
		st := s.Status
		if st == "" {
			st = "pending"
		}
		out = append(out, transcript.Task{ID: strconv.Itoa(i + 1), Subject: s.Step, Status: st})
	}
	return out
}

// isWrapper reports whether a codex user message is entirely an injected-context
// wrapper (environment/instructions/mode) rather than a human prompt.
func isWrapper(text string) bool {
	s := strings.TrimSpace(text)
	for _, tag := range wrapperTags {
		if strings.HasPrefix(s, tag) {
			return true
		}
	}
	return false
}

// toolInfo renders a short one-line summary of a function_call for the trace: the
// shell command for a command exec, else the first recognizable string argument.
func toolInfo(payload json.RawMessage) string {
	var p struct {
		Arguments string `json:"arguments"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Arguments == "" {
		return ""
	}
	// arguments is a JSON string; try to pull a command / path out of it.
	var args map[string]any
	if json.Unmarshal([]byte(p.Arguments), &args) == nil {
		for _, k := range []string{"command", "cmd", "file_path", "path", "query", "url", "description"} {
			if v, ok := args[k].(string); ok && v != "" {
				return transcript.Clip(v)
			}
		}
		// command is sometimes an array of argv tokens (codex exec_command).
		if v, ok := args["command"].([]any); ok && len(v) > 0 {
			parts := make([]string, 0, len(v))
			for _, e := range v {
				if s, ok := e.(string); ok {
					parts = append(parts, s)
				}
			}
			return transcript.Clip(strings.Join(parts, " "))
		}
	}
	return transcript.Clip(p.Arguments)
}

// applyPatchInput pulls the patch envelope out of a function_call apply_patch's
// arguments ({"input": "*** Begin Patch…"}). "" when the shape doesn't match.
func applyPatchInput(payload json.RawMessage) string {
	var p struct {
		Arguments string `json:"arguments"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Arguments == "" {
		return ""
	}
	var args struct {
		Input string `json:"input"`
	}
	if json.Unmarshal([]byte(p.Arguments), &args) != nil {
		return ""
	}
	return args.Input
}

// patchParts parses an apply_patch envelope ("*** Begin Patch" … "*** End Patch")
// into one tool part per touched file, each carrying before/after Edits so the
// Console opens it as a diff pane (claude's Edit/Write treatment). The before/after
// are reconstructed from the hunks: context+removed lines vs context+added lines —
// an approximation (context may be partial), but a faithful view of what changed.
// Returns nil when the input isn't a patch envelope (caller falls back to a trace).
func patchParts(tool, input string) []transcript.Part {
	if !strings.Contains(input, "*** Begin Patch") {
		return nil
	}
	var parts []transcript.Part
	var file, verb string
	var oldB, newB strings.Builder
	flush := func() {
		if file == "" {
			return
		}
		info := file
		if verb == "delete" {
			info = "delete " + file
		}
		p := transcript.Part{Kind: "tool", Tool: tool, Info: transcript.Clip(info), File: file}
		if verb != "delete" {
			p.Edits = []transcript.Edit{{Old: transcript.CapEdit(strings.TrimRight(oldB.String(), "\n")),
				New: transcript.CapEdit(strings.TrimRight(newB.String(), "\n"))}}
		}
		parts = append(parts, p)
		file, verb = "", ""
		oldB.Reset()
		newB.Reset()
	}
	for _, ln := range strings.Split(input, "\n") {
		switch {
		case strings.HasPrefix(ln, "*** Add File: "):
			flush()
			file, verb = strings.TrimSpace(strings.TrimPrefix(ln, "*** Add File: ")), "add"
		case strings.HasPrefix(ln, "*** Update File: "):
			flush()
			file, verb = strings.TrimSpace(strings.TrimPrefix(ln, "*** Update File: ")), "update"
		case strings.HasPrefix(ln, "*** Delete File: "):
			flush()
			file, verb = strings.TrimSpace(strings.TrimPrefix(ln, "*** Delete File: ")), "delete"
		case strings.HasPrefix(ln, "*** Move to: "):
			// Rename: show the destination in the info line, keep diffing under the source.
			if file != "" {
				file = file + " → " + strings.TrimSpace(strings.TrimPrefix(ln, "*** Move to: "))
			}
		case strings.HasPrefix(ln, "***"): // Begin/End Patch or other directives — framing
		case file == "":
			// Preamble outside any file section — ignore.
		case strings.HasPrefix(ln, "+"):
			newB.WriteString(ln[1:])
			newB.WriteString("\n")
		case strings.HasPrefix(ln, "-"):
			oldB.WriteString(ln[1:])
			oldB.WriteString("\n")
		case strings.HasPrefix(ln, "@@"):
			// Hunk separator — keep both sides aligned with a blank spacer between hunks.
			if oldB.Len() > 0 || newB.Len() > 0 {
				oldB.WriteString("\n")
				newB.WriteString("\n")
			}
		default:
			// Context line (leading space or bare) — present on both sides.
			t := strings.TrimPrefix(ln, " ")
			oldB.WriteString(t)
			oldB.WriteString("\n")
			newB.WriteString(t)
			newB.WriteString("\n")
		}
	}
	flush()
	return parts
}

// compactedText extracts the display text of a "compacted" rollout item: the summary
// message (older shape {message}) or the replacement history's text content (newer
// shape {replacement_history:[…]}), capped for display.
func compactedText(payload json.RawMessage) string {
	var p struct {
		Message            string `json:"message"`
		ReplacementHistory []struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"replacement_history"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return ""
	}
	if p.Message != "" {
		return transcript.CapOutput(p.Message)
	}
	var sb strings.Builder
	for _, m := range p.ReplacementHistory {
		for _, c := range m.Content {
			if strings.TrimSpace(c.Text) == "" || isWrapper(c.Text) {
				continue // injected wrappers re-appear in the replacement history — noise
			}
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(c.Text)
		}
	}
	return transcript.CapOutput(sb.String())
}

// isContextCompacted reports an event_msg payload of type context_compacted
// ("Conversation history was compacted", auto or manual).
func isContextCompacted(payload json.RawMessage) bool {
	var p struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(payload, &p) == nil && p.Type == "context_compacted"
}

// tokenUsage extracts the fresh-input / output / cached-read token counts from a
// token_count event_msg, mapped onto claude's usage semantics (fresh input excludes
// the cached read, which is surfaced separately). ok is false for other event_msgs.
func tokenUsage(payload json.RawMessage) (in, out, read, window int, ok bool) {
	var p struct {
		Type string `json:"type"`
		Info struct {
			Last struct {
				InputTokens  int `json:"input_tokens"`
				CachedInput  int `json:"cached_input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"last_token_usage"`
			ModelContextWindow int `json:"model_context_window"`
		} `json:"info"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Type != "token_count" {
		return 0, 0, 0, 0, false
	}
	fresh := p.Info.Last.InputTokens - p.Info.Last.CachedInput
	if fresh < 0 {
		fresh = 0
	}
	return fresh, p.Info.Last.OutputTokens, p.Info.Last.CachedInput, p.Info.ModelContextWindow, true
}

// HasPendingQuestion reports whether the slot's rollout currently ends in an
// unanswered request_user_input — codex is sitting on its question dialog. Used by
// WireLive to surface the "question" state (the 質問あり chip + notification) that
// codex's injected hooks can't report (no notification hook fires for it). Light
// tail probe: only the last chunk of the rollout is scanned, so it stays cheap on
// the sessions-list poll even for a long conversation.
func HasPendingQuestion(m session.Meta) bool {
	path := rolloutPath(sids.Read(session.UUID(m.Dir, m.Name)))
	if path == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	const tail = 256 << 10
	off := int64(0)
	if fi, err := f.Stat(); err == nil && fi.Size() > tail {
		off = fi.Size() - tail
	}
	b := make([]byte, tail)
	n, _ := f.ReadAt(b, off)
	b = b[:n]
	lines := strings.Split(string(b), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // drop the first, likely partial, line of a mid-file read
	}
	pendingCalls := map[string]bool{}
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Payload struct {
				Type   string `json:"type"`
				Name   string `json:"name"`
				CallID string `json:"call_id"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(ln), &ev) != nil || ev.Type != "response_item" {
			continue
		}
		switch ev.Payload.Type {
		case "function_call":
			if ev.Payload.Name == "request_user_input" && ev.Payload.CallID != "" {
				pendingCalls[ev.Payload.CallID] = true
			}
		case "function_call_output", "custom_tool_call_output":
			delete(pendingCalls, ev.Payload.CallID)
		}
	}
	return len(pendingCalls) > 0
}

// readTranscript reads a codex session's normalized chat turns plus the rollout
// path (for diagnostics). ok is always true (codex supports generic transcript); an
// absent rollout (no conversation yet) yields nil turns, which the chat shows as empty.
func readTranscript(m session.Meta) (agents.TranscriptData, bool) {
	slot := session.UUID(m.Dir, m.Name)
	cxid := sids.Read(slot)
	path := rolloutPath(cxid)
	if path == "" {
		return agents.TranscriptData{}, true
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return agents.TranscriptData{Path: path}, true
	}
	var lines [][]byte
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, []byte(ln))
		}
	}
	turns, tasks, pending, mode := parseRolloutFull(lines)
	return agents.TranscriptData{Turns: turns, Path: path, Tasks: tasks, Pending: pending, Mode: mode}, true
}
