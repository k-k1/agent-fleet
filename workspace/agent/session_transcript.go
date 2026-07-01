package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Structured transcript for the Console chat view. Where /output (session_io.go)
// flattens a claude session's assistant text for the MCP drive poll, /messages keeps
// turn boundaries and adds what a readable chat needs: the user's own prompts, each
// turn's timestamp, and — as ordered "parts" — the tool_use activity interleaved
// with the assistant's text, so the Console can faintly show what claude was doing
// (Read/Bash/Edit …) between paragraphs. Both read the same jsonl (cursor = line #).

// chatPart is one ordered piece of a turn: rendered text, or a faint tool trace.
type chatPart struct {
	Kind string `json:"kind"`           // "text" | "tool"
	Text string `json:"text,omitempty"` // kind=text: Markdown
	Tool string `json:"tool,omitempty"` // kind=tool: tool name (Bash, Read, …)
	Info string `json:"info,omitempty"` // kind=tool: short arg summary (command/path)
}

// chatTurn is one displayable conversation turn.
type chatTurn struct {
	Role      string     `json:"role"`                // "user" | "assistant"
	Parts     []chatPart `json:"parts"`               // ordered text/tool pieces
	Text      string     `json:"text"`                // concatenated text only (for copy / fallback)
	Model     string     `json:"model,omitempty"`     // assistant only: the model that answered
	Sidechain bool       `json:"sidechain,omitempty"` // true = a subagent (Task) sidechain turn
	Branch    string     `json:"branch,omitempty"`    // git branch at the time of the turn
	Cwd       string     `json:"cwd,omitempty"`       // working dir at the time of the turn
	// Token usage (assistant only), per event; the Console sums output across a turn's
	// events and takes the last event's input/cache as the context size.
	InTok       int    `json:"inTok,omitempty"`
	OutTok      int    `json:"outTok,omitempty"`
	CacheRead   int    `json:"cacheRead,omitempty"`
	CacheCreate int    `json:"cacheCreate,omitempty"`
	TS          string `json:"ts"`  // RFC3339 from the transcript line, "" if absent
	Idx         int    `json:"idx"` // transcript line index — a stable render key
}

// handleSessionMessages (GET /sessions/{name}/messages?since=<cursor>) returns the
// turns appended since the cursor, plus a new cursor and the live status. claude
// only (its jsonl transcript). cursor is a line index into that transcript.
func handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	meta, ok := readSessionMeta(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	alive := tmuxHasSession(tmuxName(name))
	state := "stopped"
	if alive {
		state = "idle"
		if st, ok := readSessionStatus(sessionUUID(meta.Dir, name)); ok {
			state = st.State
		}
	}
	if meta.Kind != "claude" {
		writeErr(w, http.StatusBadRequest, "unsupported_kind", "messages are available for claude sessions only")
		return
	}
	since := 0
	if v := r.URL.Query().Get("since"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			since = n
		}
	}
	sid := sessionUUID(meta.Dir, name)
	lines := transcriptLines(sid)
	turns := []chatTurn{}
	budget := 0
	for i := since; i < len(lines); i++ {
		t, ok := parseTurn(lines[i], i)
		if !ok {
			continue // tool results, summaries, bridge/meta bookkeeping
		}
		turns = append(turns, t)
		if budget += len(t.Text); budget > 1<<20 { // cap a single response at 1 MiB
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": name, "messages": turns, "cursor": len(lines),
		"status": state, "alive": alive,
	})
}

// parseTurn builds a chatTurn from a transcript line. ok is false for lines that
// carry nothing displayable: tool_result-only user turns, summaries, the Remote
// Control bridge-session line, and meta entries (isMeta).
func parseTurn(line []byte, idx int) (chatTurn, bool) {
	var ev struct {
		Type        string `json:"type"`
		Timestamp   string `json:"timestamp"`
		IsMeta      bool   `json:"isMeta"`
		IsSidechain bool   `json:"isSidechain"`
		GitBranch   string `json:"gitBranch"`
		Cwd         string `json:"cwd"`
		Message     struct {
			Model   string          `json:"model"`
			Content json.RawMessage `json:"content"`
			Usage   struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return chatTurn{}, false
	}
	if ev.IsMeta || (ev.Type != "user" && ev.Type != "assistant") {
		return chatTurn{}, false
	}
	var parts []chatPart
	var text string
	if ev.Type == "assistant" {
		parts, text = assistantParts(ev.Message.Content)
	} else if t := contentText(ev.Message.Content); t != "" {
		parts, text = []chatPart{{Kind: "text", Text: t}}, t
	}
	if len(parts) == 0 {
		return chatTurn{}, false
	}
	t := chatTurn{
		Role: ev.Type, Parts: parts, Text: text, Idx: idx, TS: ev.Timestamp,
		Sidechain: ev.IsSidechain, Branch: ev.GitBranch, Cwd: ev.Cwd,
	}
	if ev.Type == "assistant" {
		u := ev.Message.Usage
		t.Model = ev.Message.Model
		t.InTok, t.OutTok = u.InputTokens, u.OutputTokens
		t.CacheRead, t.CacheCreate = u.CacheReadInputTokens, u.CacheCreationInputTokens
	}
	return t, true
}

// assistantParts walks an assistant message's content blocks in order, emitting a
// text part per text block and a tool part per tool_use (thinking/other are skipped).
// It also returns the concatenated text (for copy). content is normally an array of
// blocks; a bare-string form is handled as a single text part.
func assistantParts(raw json.RawMessage) (parts []chatPart, text string) {
	if len(raw) == 0 {
		return nil, ""
	}
	if raw[0] != '[' {
		if s := contentText(raw); s != "" {
			return []chatPart{{Kind: "text", Text: s}}, s
		}
		return nil, ""
	}
	var blocks []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return nil, ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			parts = append(parts, chatPart{Kind: "text", Text: b.Text})
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(b.Text)
		case "tool_use":
			parts = append(parts, chatPart{Kind: "tool", Tool: b.Name, Info: toolInfo(b.Name, b.Input)})
		}
	}
	return parts, strings.TrimSpace(sb.String())
}

// toolInfo renders a short, single-line summary of a tool_use's input — the piece a
// human would recognize (the command, the file, the pattern). Best-effort; unknown
// tools fall back to the first recognizable string field.
func toolInfo(name string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	var s string
	switch name {
	case "Bash":
		s = pick("command")
	case "Read", "Write", "Edit", "NotebookEdit":
		s = pick("file_path", "notebook_path", "path")
	case "Grep", "Glob":
		s = pick("pattern")
	case "Task":
		s = pick("description")
	case "WebFetch":
		s = pick("url")
	case "WebSearch":
		s = pick("query")
	default:
		s = pick("file_path", "path", "command", "pattern", "query", "description", "url")
	}
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] // first line only
	}
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	return s
}

// contentText pulls the human text out of a message's content, which claude encodes
// either as a plain string (simple user turns) or an array of typed blocks. Only
// text blocks count; tool_use / tool_result / thinking / image blocks are skipped,
// so a turn that is purely a tool round-trip yields "".
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' { // plain-string content
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return strings.TrimSpace(s)
		}
		return ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return strings.TrimSpace(sb.String())
}
