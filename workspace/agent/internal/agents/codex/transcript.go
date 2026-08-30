package codex

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// wrapperPairs are injected context blocks that Codex may record with role=user.
// Newer app-server versions can concatenate several of these blocks and the actual
// prompt into ONE response_item, so parseResponseItem strips leading complete blocks
// instead of dropping the whole item merely because it starts with one.
var wrapperPairs = [][2]string{
	{"<recommended_plugins>", "</recommended_plugins>"},
	{"<environment_context>", "</environment_context>"},
	{"<user_instructions>", "</user_instructions>"},
	{"<permissions instructions>", "</permissions instructions>"},
	{"<collaboration_mode>", "</collaboration_mode>"},
	{"<skills_instructions>", "</skills_instructions>"},
	{"<INSTRUCTIONS>", "</INSTRUCTIONS>"},
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
	// The rollout turn currently open (task_started … task_complete). Every displayable
	// turn inside it carries this id as its fork anchor (docs/log/55): `thread/fork`'s
	// lastTurnId speaks in these, not in line numbers. Empty until the first
	// task_started — the preamble (injected instructions) belongs to no turn and must
	// not be branchable.
	curTurn := ""
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
			// turn_context also carries turn_id, and it can appear WITHOUT a task_started
			// (settings applied between turns), so it is the fallback anchor source — but
			// never the primary: it repeats more often than turns do (実測: 19 turn_context
			// vs 15 task_started in one rollout).
			if id := payloadTurnID(ev.Payload); id != "" && curTurn == "" {
				curTurn = id
			}
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
				t.AnchorID = curTurn
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
			if id, out, gen, imgs := parseCallOutput(ev.Payload); id != "" {
				answered[id] = true
				if ti, okk := callTurn[id]; okk && len(turns[ti].Parts) > 0 && out != "" {
					if turns[ti].Parts[0].Kind == "question" {
						turns[ti].Parts[0].Answer = answerText(out, turns[ti].Parts[0].Questions)
					} else {
						turns[ti].Parts[0].Output = out
					}
					// A generated image (imagegen): surface its saved file as a userfile
					// part — the same 共有ファイル panel claude's SendUserFile gets, so the
					// user can open the image from the chat instead of digging the path out
					// of a tool trace.
					if len(gen) > 0 {
						turns[ti].Parts = append(turns[ti].Parts, transcript.Part{Kind: "userfile", Files: gen})
					} else if len(imgs) > 0 {
						// view_image: the screenshot only exists as inline base64 here (no
						// servable path was announced) — stash it for readTranscript to
						// decode and persist to a servable file (see persistViewImages).
						turns[ti].Parts[0].ViewImageCallID = id
						turns[ti].Parts[0].ViewImageData = imgs
					}
				}
			}
		case "event_msg":
			// task_started opens the turn every following item belongs to (実測: it
			// precedes the user prompt's response_item), so the anchor is set here and
			// stays until the next one.
			if id := taskStartedTurnID(ev.Payload); id != "" {
				curTurn = id
			}
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
		Type      string `json:"type"`
		Role      string `json:"role"`
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Arguments string `json:"arguments"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Path string `json:"path"` // localImage echo, when Codex preserves it verbatim
		} `json:"content"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return transcript.Turn{}, "", false
	}
	switch p.Type {
	case "custom_tool_call":
		// A freeform tool call. Two shapes seen:
		//   - codex ≤0.143: name=apply_patch, `input` = the raw patch envelope.
		//   - codex 0.144+: name=exec, `input` = a JS snippet driving tools.exec_command /
		//     tools.apply_patch (unified "exec" tool). We destructure the JS to recover the
		//     command + patch so the trace still shows a clean command and opens a diff pane.
		// Either way it's parsed into per-file before/after parts (diff pane, like claude's
		// Edit/Write) when a patch is present, else a plain trace. Output attaches when
		// custom_tool_call_output lands.
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
		var parts []transcript.Part
		if isExecScript(ct.Input) {
			parts = execScriptParts(ct.Input)
		} else {
			parts = patchParts(name, ct.Input)
		}
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
		// hasAttachment: a user message can be an image with no caption at all (the
		// composer's ＋/paste with the text box left empty) — turn/start's localImage
		// item then has no accompanying "text" content, so the loop below never writes
		// anything to sb. That must not make this look like a droppable non-prompt: the
		// pending 反映待ち echo (pendingEcho.ts) can only resolve against a real user
		// turn, and one that never lands (because we dropped it here) leaves it stuck
		// forever — recoverable only by a page reload that wipes the client's in-memory
		// echo state, not by anything server-side. If Codex preserves the path, fold it
		// into the text too so the existing pasted-path thumbnail/echo matching
		// (pastedImages.ts PASTE_PATH_RE) still recognizes it.
		hasAttachment := false
		for _, c := range p.Content {
			switch {
			case c.Type == "input_text" || c.Type == "output_text" || c.Type == "text":
				if c.Text != "" {
					sb.WriteString(c.Text)
				}
			case c.Type != "":
				hasAttachment = true
				if c.Path != "" {
					if sb.Len() > 0 {
						sb.WriteString(" ")
					}
					sb.WriteString(c.Path)
				}
			}
		}
		text := strings.TrimSpace(sb.String())
		if text == "" && !(p.Role == "user" && hasAttachment) {
			return transcript.Turn{}, "", false
		}
		if p.Role == "user" {
			text = cleanUserText(text)
			if text == "" && !hasAttachment {
				return transcript.Turn{}, "", false // injected context only, not a prompt
			}
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
		// Codex collaboration tools launch or re-task a child agent. They are a
		// user-relevant orchestration event, not an ordinary low-level tool trace.
		spawn := p.Name == "spawn_agent" || strings.HasSuffix(p.Name, "__spawn_agent") || strings.HasSuffix(p.Name, ".spawn_agent")
		followup := p.Name == "followup_task" || strings.HasSuffix(p.Name, "__followup_task") || strings.HasSuffix(p.Name, ".followup_task")
		if spawn || followup {
			var args struct {
				TaskName string `json:"task_name"`
				Message  string `json:"message"`
				Target   string `json:"target"`
			}
			if json.Unmarshal([]byte(p.Arguments), &args) == nil {
				label := strings.TrimSpace(args.TaskName)
				if label == "" {
					label = strings.TrimSpace(args.Target)
				}
				return transcript.Turn{
					Role: "assistant", Parts: []transcript.Part{{
						Kind: "delegation", Tool: p.Name, Info: label,
						Prompt: strings.TrimSpace(args.Message), AgentType: label, Status: "requested",
					}},
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

// parseCallOutput returns a function_call_output's / custom_tool_call_output's call_id,
// its (truncated) output text, and any generated-image paths announced in it, or
// "","",nil for other payloads. The output is codex's tool result; the JSON shape
// varies so we best-effort stringify:
//   - a bare string
//   - {output|content: "…"}
//   - codex 0.144+: an array of {type:"input_text",text:"…"} blocks (unified exec) — we
//     concatenate their text. An image_gen result also carries the raw image as an
//     input_image data URL plus a text block that re-embeds it as JSON — pure noise for
//     a text trace, so those are dropped (the image itself reaches the user as the
//     userfile part synthesized from genImages).
//
// parseCallOutput additionally returns any inline "data:image/...;base64,..." URLs
// found in an array-shaped output (codex's view_image tool_result embeds the
// screenshot this way — no path text, unlike image_gen, whose result also carries
// this shape but ANNOUNCES an already-servable path separately, which genImagePaths
// extracts). The caller only uses inlineImages when genImages is empty, so
// image_gen's existing behavior is unaffected by this also being populated for it.
func parseCallOutput(payload json.RawMessage) (callID, output string, genImages []string, inlineImages []string) {
	var p struct {
		Type   string          `json:"type"`
		CallID string          `json:"call_id"`
		Output json.RawMessage `json:"output"`
	}
	if json.Unmarshal(payload, &p) != nil ||
		(p.Type != "function_call_output" && p.Type != "custom_tool_call_output") {
		return "", "", nil, nil
	}
	out := ""
	var imgs []string
	if len(p.Output) > 0 {
		switch p.Output[0] {
		case '"':
			_ = json.Unmarshal(p.Output, &out)
		case '[':
			var blocks []struct {
				Text     string `json:"text"`
				ImageURL string `json:"image_url"`
			}
			if json.Unmarshal(p.Output, &blocks) == nil {
				var sb strings.Builder
				for _, b := range blocks {
					if b.ImageURL != "" {
						imgs = append(imgs, b.ImageURL)
					}
					t := strings.TrimSpace(b.Text)
					if strings.HasPrefix(t, `{"image_url":"data:image`) || strings.HasPrefix(t, "data:image") {
						continue // base64 image re-embedded as text — noise
					}
					sb.WriteString(b.Text)
				}
				out = sb.String()
			}
			if out == "" {
				out = string(p.Output)
			}
		default:
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
	return p.CallID, transcript.CapOutput(out), genImagePaths(out), imgs
}

// genImageRe matches the imagegen harness's completion notice ("Generated images are
// saved to <dir> as <path> by default."), the only place the saved file's concrete
// path appears in the rollout (image_generation_end carries the bytes but no path).
var genImageRe = regexp.MustCompile(`Generated images are saved to \S+ as (\S+?\.(?:png|jpe?g|webp|gif))`)

// genImagePaths extracts the generated-image file paths announced in a tool output,
// deduped in order of appearance.
func genImagePaths(out string) []string {
	var paths []string
	seen := map[string]bool{}
	for _, m := range genImageRe.FindAllStringSubmatch(out, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			paths = append(paths, m[1])
		}
	}
	return paths
}

// maxViewImageEncodedLen bounds the base64 payload persistViewImagesTo will decode
// (~28MB encoded ≈ 20MB decoded) — generous for a screenshot, guards against a
// malformed or runaway inline payload turning into an oversized allocation/file.
const maxViewImageEncodedLen = 28 << 20

// decodeDataURL splits a "data:<mime>;base64,<payload>" URL into its mime type and
// decoded bytes. ok is false for anything else (codex is only known to emit base64
// PNG data URLs for view_image, but this stays defensive rather than assuming).
func decodeDataURL(u string) (mime string, data []byte, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(u, prefix) {
		return "", nil, false
	}
	comma := strings.IndexByte(u, ',')
	if comma < 0 {
		return "", nil, false
	}
	header := u[len(prefix):comma]
	if !strings.HasSuffix(header, ";base64") {
		return "", nil, false
	}
	if len(u)-(comma+1) > maxViewImageEncodedLen {
		return "", nil, false
	}
	data, err := base64.StdEncoding.DecodeString(u[comma+1:])
	if err != nil {
		return "", nil, false
	}
	return strings.TrimSuffix(header, ";base64"), data, true
}

// extForDataURLMime maps a data URL's mime type to a file extension. Codex's
// screenshots are PNG in every observed case; the default keeps an unrecognized
// mime openable rather than dropping the image.
func extForDataURLMime(mime string) string {
	switch mime {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

// sanitizeCallID reduces a call_id to a safe filename fragment. codex's call_ids are
// already alnum+underscore (e.g. "call_YHcw5Pm6sBMucKvcvz4iErte"), but this stays
// defensive against a future format change rather than trusting it blindly.
func sanitizeCallID(id string) string {
	if id == "" {
		return "call"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// viewImagePersisted caches destination paths already confirmed on disk this
// process's lifetime, so readTranscript's repeated re-parses of the same rollout
// (one per /messages poll, across several independent call sites — see
// persistViewImages) skip even the os.Stat check after the first success. It only
// gates the WRITE — every call still returns the path so the sibling userfile Part
// keeps being emitted on every read, not just the first.
var viewImagePersisted sync.Map // dest path -> struct{}

// persistViewImages decodes and writes any pending view_image inline data
// (stashed on Part.ViewImageData by parseRolloutFull, see the parseCallOutput call
// site) to a servable per-session directory, then appends a sibling kind=userfile
// Part so the mirror's existing UserFileBlock/FileThumb renders it inline — the same
// treatment codex's own image_gen results already get via genImagePaths, just with
// agent-fleet doing the file write since codex never wrote view_image's screenshot
// anywhere servable itself (see workspace/agent/session_paste.go's pastedDir(sid)
// for the sibling convention this mirrors: homeDir()/.cache/agent-fleet/<kind>/<sid>).
func persistViewImages(turns []transcript.Turn, sid string) {
	persistViewImagesTo(turns, filepath.Join(paths.HomeDir(), ".cache", "agent-fleet", "codex-view-image", sid))
}

// persistViewImagesTo is persistViewImages with an explicit target directory, so
// tests can point it at t.TempDir() instead of the real home. Errors are always
// swallowed per-file: a write failure (disk full, permissions) must never surface
// to readTranscript's caller — this is best-effort image persistence, not a
// required part of reading the transcript.
func persistViewImagesTo(turns []transcript.Turn, dir string) {
	dirMade := false
	for ti := range turns {
		var files []string
		for pi := range turns[ti].Parts {
			p := &turns[ti].Parts[pi]
			if len(p.ViewImageData) == 0 {
				continue
			}
			for i, u := range p.ViewImageData {
				mime, data, ok := decodeDataURL(u)
				if !ok {
					continue
				}
				dest := filepath.Join(dir, sanitizeCallID(p.ViewImageCallID)+"-"+strconv.Itoa(i)+extForDataURLMime(mime))
				if _, done := viewImagePersisted.Load(dest); !done {
					if _, err := os.Stat(dest); err != nil {
						if !dirMade {
							if os.MkdirAll(dir, 0o700) != nil {
								continue
							}
							dirMade = true
						}
						if writeFileAtomic(dir, dest, data) != nil {
							continue
						}
					}
					viewImagePersisted.Store(dest, struct{}{})
				}
				files = append(files, dest)
			}
			p.ViewImageData = nil
			p.ViewImageCallID = ""
		}
		if len(files) > 0 {
			turns[ti].Parts = append(turns[ti].Parts, transcript.Part{Kind: "userfile", Files: files})
		}
	}
}

// writeFileAtomic writes data to dest via a temp file + rename in dir, mirroring
// workspace/agent/session_paste.go's savePastedImageTo so a reader never observes a
// partially-written file.
func writeFileAtomic(dir, dest string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".view-image-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return werr
	}
	if cerr != nil {
		_ = os.Remove(tmp.Name())
		return cerr
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// answerText renders a request_user_input function_call_output into the chosen answer
// label(s) for display. codex wraps the reply as
// {"answers":{"<questionId>":{"answers":["label",…]}}}. For a single question that
// flattened "label, label" form is enough — matching claude's clean prose so the
// mirror's QuestionBlock highlights the picked options instead of dumping raw JSON.
//
// For MULTIPLE questions in one call, flattening loses which label belongs to which
// question: every card ends up showing every question's combined picks (reported
// 2026-08-12 — a 3-question AUQ where each card's "自由入力" box showed the OTHER two
// questions' selections). codex's per-question "id" (request_user_input's
// questions[].id) reappears verbatim as the answers map's key (実測 against a live
// rollout: {"answers":{"switch_behavior":{...},"new_open_behavior":{...},...}} keyed
// by the same ids the request's questions[] carried), so when `questions` carries
// those ids we render the same anchored `"<question>"="<answer>"[, ...]` prose claude's
// AskUserQuestion tool_result uses — the Console's per-question parser
// (console/src/features/mirror/questionAnswers.ts byAnchors) already splits that form
// per question. Falls back to the old flattened form (ids absent/mismatched, or a
// shape we don't recognize) so the answer is never lost, just possibly ambiguous as
// before.
func answerText(out string, questions []transcript.Question) string {
	var env struct {
		Answers map[string]struct {
			Answers []string `json:"answers"`
		} `json:"answers"`
	}
	if json.Unmarshal([]byte(out), &env) != nil || len(env.Answers) == 0 {
		return out
	}
	label := func(k string) string {
		var vals []string
		for _, s := range env.Answers[k].Answers {
			if s = strings.TrimSpace(s); s != "" {
				vals = append(vals, s)
			}
		}
		return strings.Join(vals, ", ")
	}
	if len(questions) > 1 {
		pairs := make([]string, 0, len(questions))
		for _, q := range questions {
			a, ok := env.Answers[q.ID]
			if q.ID == "" || !ok || len(a.Answers) == 0 {
				pairs = nil
				break
			}
			pairs = append(pairs, `"`+q.Question+`"="`+label(q.ID)+`"`)
		}
		if len(pairs) == len(questions) {
			return strings.Join(pairs, ", ")
		}
	}
	keys := make([]string, 0, len(env.Answers))
	for k := range env.Answers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var labels []string
	for _, k := range keys {
		if s := label(k); s != "" {
			labels = append(labels, s)
		}
	}
	if len(labels) == 0 {
		return out
	}
	return strings.Join(labels, ", ")
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

// cleanUserText removes only LEADING injected context blocks from a Codex user
// response_item and returns the real prompt that follows. It deliberately does not
// search inside ordinary text, so a human discussing one of these tags is untouched.
func cleanUserText(text string) string {
	s := strings.TrimSpace(text)
	for s != "" {
		removed := false
		for _, pair := range wrapperPairs {
			if !strings.HasPrefix(s, pair[0]) {
				continue
			}
			end := strings.Index(s[len(pair[0]):], pair[1])
			if end < 0 {
				return "" // legacy/truncated wrapper-only item
			}
			s = strings.TrimSpace(s[len(pair[0])+end+len(pair[1]):])
			removed = true
			break
		}
		if removed {
			continue
		}
		// AGENTS.md is preceded by a human-readable heading, then a single
		// <INSTRUCTIONS> wrapper containing the workspace + project policies.
		if strings.HasPrefix(s, "# AGENTS.md instructions") {
			open := strings.Index(s, "<INSTRUCTIONS>")
			if open < 0 {
				return ""
			}
			body := s[open+len("<INSTRUCTIONS>"):]
			end := strings.Index(body, "</INSTRUCTIONS>")
			if end < 0 {
				return ""
			}
			s = strings.TrimSpace(body[end+len("</INSTRUCTIONS>"):])
			continue
		}
		break
	}
	return s
}

// isWrapper is retained as the narrow predicate used by compacted-history parsing.
func isWrapper(text string) bool {
	return strings.TrimSpace(text) != "" && cleanUserText(text) == ""
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

// codex 0.144+ packs its unified "exec" custom tool as a JS snippet, e.g.
//
//	const patch = "*** Begin Patch\n*** Update File: /p\n@@\n a\n+b\n*** End Patch";
//	const a = await tools.apply_patch(patch);
//	const r = await tools.exec_command({cmd:"ls -la","workdir":"/p",…}); text(r.output)
//
// jsCmdRe pulls the shell command out of exec_command({cmd:"…"}); the patch is found by
// scanning string literals for the "Begin Patch" marker (see extractExecScript).
// jsPromptTickRe/jsPromptStrRe pull the image_gen prompt (a backtick template literal
// in observed rollouts, double-quoted as a fallback); jsPathRe the view_image path.
var (
	jsCmdRe        = regexp.MustCompile(`\bcmd\s*:\s*"((?:[^"\\]|\\.)*)"`)
	jsStrRe        = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	jsPromptTickRe = regexp.MustCompile("\\bprompt\\s*:\\s*`([^`]*)`")
	jsPromptStrRe  = regexp.MustCompile(`\bprompt\s*:\s*"((?:[^"\\]|\\.)*)"`)
	jsPathRe       = regexp.MustCompile(`\bpath\s*:\s*"((?:[^"\\]|\\.)*)"`)
)

// isExecScript reports whether a custom_tool_call input is the codex 0.144+ JS "exec"
// snippet (drives tools.exec_command / tools.apply_patch / tools.image_gen__* /
// tools.view_image) rather than a bare patch envelope.
func isExecScript(input string) bool {
	return strings.Contains(input, "await tools.") ||
		strings.Contains(input, "tools.exec_command") || strings.Contains(input, "tools.apply_patch")
}

// execScriptParts destructures a codex 0.144+ JS "exec" snippet into display parts: the
// shell command as a tool trace (Output attaches here — it's Parts[0]), an image_gen /
// view_image trace with its prompt / path as the info, and, when the snippet applies a
// patch, the per-file diff parts after it. Returns nil when nothing is recoverable, so
// the caller can fall back to a raw trace.
func execScriptParts(input string) []transcript.Part {
	cmd, patch := extractExecScript(input)
	var parts []transcript.Part
	if cmd != "" {
		parts = append(parts, transcript.Part{Kind: "tool", Tool: "exec_command", Info: transcript.Clip(cmd)})
	}
	if strings.Contains(input, "tools.image_gen") {
		// The built-in image generation (imagegen skill). The saved file arrives later, in
		// the wait call's output (see the userfile synthesis in parseRolloutFull). The
		// prompt is a structured multi-line block; flatten it so Clip's one-line summary
		// shows more than its first field.
		info := ""
		if m := jsPromptTickRe.FindStringSubmatch(input); m != nil {
			info = m[1]
		} else if m := jsPromptStrRe.FindStringSubmatch(input); m != nil {
			info = unescapeJS(m[1])
		}
		info = strings.Join(strings.Fields(info), " ")
		parts = append(parts, transcript.Part{Kind: "tool", Tool: "image_gen", Info: transcript.Clip(info)})
	}
	if strings.Contains(input, "tools.view_image") {
		info := ""
		if m := jsPathRe.FindStringSubmatch(input); m != nil {
			info = unescapeJS(m[1])
		}
		parts = append(parts, transcript.Part{Kind: "tool", Tool: "view_image", Info: transcript.Clip(info)})
	}
	if patch != "" {
		parts = append(parts, patchParts("apply_patch", patch)...)
	}
	return parts
}

// extractExecScript recovers the exec_command shell command and the apply_patch envelope
// from a JS "exec" snippet. Both are JS double-quoted string literals whose escapes are
// JSON-compatible, so unescapeJS decodes them.
func extractExecScript(input string) (cmd, patch string) {
	if m := jsCmdRe.FindStringSubmatch(input); m != nil {
		cmd = unescapeJS(m[1])
	}
	for _, m := range jsStrRe.FindAllStringSubmatch(input, -1) {
		if strings.Contains(m[1], "Begin Patch") {
			patch = unescapeJS(m[1])
			break
		}
	}
	return cmd, patch
}

// unescapeJS decodes a JS double-quoted string literal body (\n, \", \\, \uXXXX, …) by
// round-tripping it through a JSON string, whose escape grammar these payloads share.
// Falls back to the raw body when it isn't valid JSON.
func unescapeJS(body string) string {
	var s string
	if json.Unmarshal([]byte(`"`+body+`"`), &s) == nil {
		return s
	}
	return body
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
		// A rename carries "<src> → <dst>" for the info line, which is not a path anything
		// can open. File is the coordinate (the destination — where the content lives now),
		// info keeps the arrow.
		target := file
		if i := strings.Index(target, " → "); i >= 0 {
			target = strings.TrimSpace(target[i+len(" → "):])
		}
		p := transcript.Part{Kind: "tool", Tool: tool, Info: transcript.Clip(info), File: target, Verb: verb}
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

// rolloutLines reads a rollout and returns its non-blank lines — the input every parser
// here takes. Shared by readTranscript and ResolveForkAt so the two never disagree about
// which lines exist.
func rolloutLines(path string) ([][]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lines [][]byte
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, []byte(ln))
		}
	}
	return lines, nil
}

// taskStartedTurnID returns the turn id an event_msg/task_started payload opens, or ""
// for any other event. This id is codex's own unit of "one exchange" and the currency of
// `thread/fork`'s lastTurnId (docs/log/55 §55.2).
func taskStartedTurnID(payload json.RawMessage) string {
	var p struct {
		Type   string `json:"type"`
		TurnID string `json:"turn_id"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Type != "task_started" {
		return ""
	}
	return p.TurnID
}

// payloadTurnID pulls turn_id off any payload that carries one (turn_context).
func payloadTurnID(payload json.RawMessage) string {
	var p struct {
		TurnID string `json:"turn_id"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return ""
	}
	return p.TurnID
}

// rolloutTurnIDs lists the rollout's turn ids in order, de-duplicated. ResolveForkAt
// walks it to translate an anchor ("branch before THIS turn") into codex's inclusive
// lastTurnId ("keep through THAT turn") — the previous entry.
func rolloutTurnIDs(lines [][]byte) []string {
	var out []string
	seen := map[string]bool{}
	for _, ln := range lines {
		var ev struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(ln, &ev) != nil {
			continue
		}
		var id string
		switch ev.Type {
		case "event_msg":
			id = taskStartedTurnID(ev.Payload)
		case "turn_context":
			id = payloadTurnID(ev.Payload)
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
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
func HasPendingQuestion(m session.Meta) bool { return PendingQuestionID(m) != "" }

// PendingQuestionID returns the stable request_user_input call id, used by the
// durable notification outbox to deduplicate a prompt even after its event is acked.
func PendingQuestionID(m session.Meta) string {
	path := rolloutPath(sids.Read(session.UUID(m.Dir, m.Name)))
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
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
	for id := range pendingCalls {
		return id
	}
	return ""
}

// readTranscript reads a codex session's normalized chat turns plus the rollout
// path (for diagnostics). ok is always true (codex supports generic transcript); an
// absent rollout (no conversation yet) yields nil turns, which the chat shows as empty.
func readTranscript(m session.Meta) (agents.TranscriptData, bool) {
	slot := session.UUID(m.Dir, m.Name)
	cxid := sids.Read(slot)
	compacting := isCompacting(m)
	path := rolloutPath(cxid)
	if path == "" {
		td := agents.TranscriptData{Compacting: compacting}
		managedEnrich(m, &td)
		return td, true
	}
	lines, err := rolloutLines(path)
	if err != nil {
		td := agents.TranscriptData{Path: path, Compacting: compacting}
		managedEnrich(m, &td)
		return td, true
	}
	turns, tasks, pending, mode := parseRolloutFull(lines)
	persistViewImages(turns, slot)
	td := agents.TranscriptData{Turns: turns, Path: path, Tasks: tasks, Pending: pending, Mode: mode, Compacting: compacting}
	managedEnrich(m, &td)
	return td, true
}

// rolloutCompletedAfter reports whether codex recorded completion of the current
// turn after the status store was optimistically moved to working. Stop hooks are
// the primary state source, but some codex versions occasionally leave that hook
// unfired even though the TUI has returned to its composer. The rollout lifecycle
// is an independent, append-only completion signal we can use to heal that stale
// working state. Requiring a timestamp at/after workingSince prevents the previous
// turn's task_complete from making a newly-submitted prompt look idle.
func rolloutCompletedAfter(m session.Meta, workingSince time.Time) bool {
	path := rolloutPath(sids.Read(session.UUID(m.Dir, m.Name)))
	if path == "" || workingSince.IsZero() {
		return false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	state, at := latestRolloutLifecycle(strings.Split(string(b), "\n"))
	return state == "task_complete" && !at.IsZero() && !at.Before(workingSince)
}

// latestRolloutLifecycle returns the final task_started/task_complete event. Codex
// writes both as event_msg payload types; malformed/truncated JSONL tail lines are
// ignored, as they are by the transcript parser.
func latestRolloutLifecycle(lines []string) (string, time.Time) {
	for i := len(lines) - 1; i >= 0; i-- {
		var ev struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   struct {
				Type string `json:"type"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(lines[i]), &ev) != nil || ev.Type != "event_msg" ||
			(ev.Payload.Type != "task_started" && ev.Payload.Type != "task_complete") {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, ev.Timestamp)
		return ev.Payload.Type, at
	}
	return "", time.Time{}
}
