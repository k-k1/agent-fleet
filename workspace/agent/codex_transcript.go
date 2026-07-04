package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Codex chat transcript. Unlike claude (a single <sid>.jsonl we read directly),
// codex writes a "rollout" JSONL under ~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-
// <session_id>.jsonl, one JSON event per line. We already capture codex's own
// session_id (codexSids, from its status hook), so we locate that slot's rollout by
// id and normalize its events into the SAME chatTurn/chatPart model the Console chat
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

// codexRolloutPath returns the rollout jsonl for a codex session id, or "" if none is
// found yet (the file only exists once codex has started a conversation). The layout is
// ~/.codex/sessions/<Y>/<M>/<D>/rollout-<ts>-<id>.jsonl; we glob the date levels.
func codexRolloutPath(codexID string) string {
	if codexID == "" {
		return ""
	}
	root := filepath.Join(homeDir(), ".codex", "sessions")
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

// codexWrapperTags marks a codex user message that is really injected context, not a
// human prompt (environment snapshot, instructions, mode banner). A user turn whose
// text is entirely such a wrapper is dropped from the chat.
var codexWrapperTags = []string{
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

// codexParseRollout normalizes a codex rollout's lines into ordered chatTurns. Each
// turn keeps its ABSOLUTE line index as Idx (a stable React key, and the unit the
// generic windower pages over). session_meta seeds the cwd/branch shown as a context
// line; token_count events attach usage to the preceding assistant turn so the chat's
// context gauge works the same as claude's.
func codexParseRollout(lines [][]byte) []chatTurn {
	var turns []chatTurn
	var cwd, branch string
	lastAssistant := -1 // index into turns of the most recent assistant turn (for usage)
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
			cwd, branch = codexMetaContext(ev.Payload)
		case "response_item":
			t, ok := codexParseResponseItem(ev.Payload, ev.Timestamp, i, cwd, branch)
			if !ok {
				continue
			}
			turns = append(turns, t)
			if t.Role == "assistant" {
				lastAssistant = len(turns) - 1
			}
		case "event_msg":
			if in, out, read, ok := codexTokenUsage(ev.Payload); ok && lastAssistant >= 0 {
				turns[lastAssistant].InTok = in
				turns[lastAssistant].OutTok = out
				turns[lastAssistant].CacheRead = read
			}
		}
	}
	return turns
}

// codexMetaContext pulls the working dir and git branch from a session_meta payload.
func codexMetaContext(payload json.RawMessage) (cwd, branch string) {
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

// codexParseResponseItem turns one response_item payload into a chatTurn. Returns
// ok=false for anything non-displayable (developer/system messages, tool outputs,
// reasoning, or a user turn that is only injected context).
func codexParseResponseItem(payload json.RawMessage, ts string, idx int, cwd, branch string) (chatTurn, bool) {
	var p struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Name    string `json:"name"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return chatTurn{}, false
	}
	switch p.Type {
	case "message":
		if p.Role != "user" && p.Role != "assistant" {
			return chatTurn{}, false // developer/system instructions — noise
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
			return chatTurn{}, false
		}
		if p.Role == "user" && codexIsWrapper(text) {
			return chatTurn{}, false // an injected-context user turn, not a prompt
		}
		return chatTurn{
			Role: p.Role, Parts: []chatPart{{Kind: "text", Text: text}}, Text: text,
			Idx: idx, TS: ts, Cwd: cwd, Branch: branch,
		}, true
	case "function_call":
		// A tool call: a faint trace on the assistant side (the Console merges it into
		// the adjacent assistant block). function_call_output/reasoning are dropped.
		name := p.Name
		if name == "" {
			name = "tool"
		}
		return chatTurn{
			Role: "assistant", Parts: []chatPart{{Kind: "tool", Tool: name, Info: codexToolInfo(payload)}},
			Idx: idx, TS: ts, Cwd: cwd, Branch: branch,
		}, true
	}
	return chatTurn{}, false
}

// codexIsWrapper reports whether a codex user message is entirely an injected-context
// wrapper (environment/instructions/mode) rather than a human prompt.
func codexIsWrapper(text string) bool {
	s := strings.TrimSpace(text)
	for _, tag := range codexWrapperTags {
		if strings.HasPrefix(s, tag) {
			return true
		}
	}
	return false
}

// codexToolInfo renders a short one-line summary of a function_call for the trace: the
// shell command for a command exec, else the first recognizable string argument.
func codexToolInfo(payload json.RawMessage) string {
	var p struct {
		Arguments string `json:"arguments"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Arguments == "" {
		return ""
	}
	// arguments is a JSON string; try to pull a command / path out of it.
	var args map[string]any
	if json.Unmarshal([]byte(p.Arguments), &args) == nil {
		for _, k := range []string{"command", "cmd", "file_path", "path", "query"} {
			if v, ok := args[k].(string); ok && v != "" {
				return codexClip(v)
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
			return codexClip(strings.Join(parts, " "))
		}
	}
	return codexClip(p.Arguments)
}

func codexClip(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	return s
}

// codexTokenUsage extracts the fresh-input / output / cached-read token counts from a
// token_count event_msg, mapped onto claude's usage semantics (fresh input excludes
// the cached read, which is surfaced separately). ok is false for other event_msgs.
func codexTokenUsage(payload json.RawMessage) (in, out, read int, ok bool) {
	var p struct {
		Type string `json:"type"`
		Info struct {
			Last struct {
				InputTokens  int `json:"input_tokens"`
				CachedInput  int `json:"cached_input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"last_token_usage"`
		} `json:"info"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Type != "token_count" {
		return 0, 0, 0, false
	}
	fresh := p.Info.Last.InputTokens - p.Info.Last.CachedInput
	if fresh < 0 {
		fresh = 0
	}
	return fresh, p.Info.Last.OutputTokens, p.Info.Last.CachedInput, true
}

// readCodexTranscript reads a codex session's normalized chat turns plus the rollout
// path (for diagnostics). ok is always true (codex supports generic transcript); an
// absent rollout (no conversation yet) yields nil turns, which the chat shows as empty.
func readCodexTranscript(m sessionMeta) (turns []chatTurn, path string, ok bool) {
	slot := sessionUUID(m.Dir, m.Name)
	cxid := codexSids.read(slot)
	path = codexRolloutPath(cxid)
	if path == "" {
		return nil, "", true
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, path, true
	}
	var lines [][]byte
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, []byte(ln))
		}
	}
	return codexParseRollout(lines), path, true
}
