package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Structured transcript for the Console chat view. Where /output (session_io.go)
// flattens a claude session's assistant text for the MCP drive poll, /messages keeps
// turn boundaries and adds what a readable chat needs: the user's own prompts and
// each turn's timestamp. Both read the same jsonl and use the line count as cursor.

// chatTurn is one displayable conversation turn.
type chatTurn struct {
	Role string `json:"role"` // "user" | "assistant"
	Text string `json:"text"` // the turn's Markdown text (tool blocks excluded)
	TS   string `json:"ts"`   // RFC3339 from the transcript line, "" if absent
	Idx  int    `json:"idx"`  // transcript line index — a stable render key
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
		role, text, ts := parseTurn(lines[i])
		if text == "" {
			continue // tool round-trips, summaries, bridge/meta bookkeeping
		}
		turns = append(turns, chatTurn{Role: role, Text: text, TS: ts, Idx: i})
		if budget += len(text); budget > 1<<20 { // cap a single response at 1 MiB
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": name, "messages": turns, "cursor": len(lines),
		"status": state, "alive": alive,
	})
}

// parseTurn extracts {role, text, timestamp} from a transcript line. text is "" for
// lines that carry no displayable message: tool_use/tool_result turns, summaries,
// the Remote Control bridge-session line, and meta entries (isMeta).
func parseTurn(line []byte) (role, text, ts string) {
	var ev struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		IsMeta    bool   `json:"isMeta"`
		Message   struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return "", "", ""
	}
	if ev.IsMeta || (ev.Type != "user" && ev.Type != "assistant") {
		return "", "", ""
	}
	return ev.Type, contentText(ev.Message.Content), ev.Timestamp
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
