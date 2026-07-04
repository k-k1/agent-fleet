package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (registers "sqlite"), as in the CP
)

// opencode chat transcript. opencode keeps its conversation in a SQLite database
// (~/.local/share/opencode/opencode.db, WAL mode) rather than a jsonl file: a `message`
// row per turn (role + tokens + model in a JSON `data` blob) and `part` rows per message
// (text / reasoning / tool / patch, ordered by time_created). We already capture this
// slot's opencode session id (opencodeSids, "ses_…", written by the bundled plugin), so
// we read that session's messages+parts read-only and normalize them into the SAME
// chatTurn model the Console chat consumes for claude/codex. The generic /messages
// handler windows the result (there's no line cursor — the store isn't append-only text).
//
// Shapes (verified against a real opencode.db):
//   message.data = {role:"user"|"assistant", modelID, tokens:{input,output,cache:{read,write}}, time:{created}, path:{cwd}}
//   part.data    = {type:"text"|"reasoning"|"tool"|"patch"|"step-start"|"step-finish", ...}
//     text/reasoning: {text}          tool: {tool, state:{input:{command|file_path|…}}}
//     patch: {files:[...]}            step-*: framing, dropped

// readOpencodeTranscript reads an opencode session's normalized chat turns plus the db
// path (for diagnostics). ok is always true; an absent session id / db (no conversation
// yet) yields nil turns, shown as an empty chat.
func readOpencodeTranscript(m sessionMeta) (transcriptData, bool) {
	slot := sessionUUID(m.Dir, m.Name)
	ses := opencodeSids.read(slot)
	if ses == "" {
		return transcriptData{}, true
	}
	path := filepath.Join(homeDir(), ".local", "share", "opencode", "opencode.db")
	if _, err := os.Stat(path); err != nil {
		return transcriptData{}, true
	}
	// Read-only with a short busy timeout so a concurrent opencode write can't wedge the
	// poll. WAL is auto-detected from the db file (the Agent runs as the same user as
	// opencode, so the -wal/-shm are readable); we do NOT set journal_mode here — that
	// would be a write and fail on a mode=ro handle.
	dsn := "file:" + path + "?mode=ro&_pragma=busy_timeout(3000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return transcriptData{path: path}, true
	}
	defer db.Close()
	return transcriptData{
		turns:   opencodeReadSession(db, ses),
		path:    path,
		tasks:   opencodeTasks(db, ses),
		pending: opencodePending(db, ses),
	}, true
}

// opencodePending returns the questions of a currently-running `question` tool part in
// this session (opencode is awaiting the user's answer), or nil. Same shape as claude's
// AskUserQuestion so the Console renders it interactively.
func opencodePending(db *sql.DB, ses string) []chatQuestion {
	rows, err := db.Query(
		`SELECT data FROM part WHERE session_id = ? AND json_extract(data,'$.tool') = 'question' AND json_extract(data,'$.state.status') = 'running' ORDER BY time_created DESC LIMIT 1`,
		ses,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	if !rows.Next() {
		return nil
	}
	var pd []byte
	if rows.Scan(&pd) != nil {
		return nil
	}
	var p struct {
		State struct {
			Input json.RawMessage `json:"input"`
		} `json:"state"`
	}
	if json.Unmarshal(pd, &p) != nil {
		return nil
	}
	return opencodeQuestions(p.State.Input)
}

// opencodeQuestions parses a question tool's state.input into chatQuestions (identical
// schema to claude's AskUserQuestion: questions[]{question,header,multiSelect,options}).
func opencodeQuestions(input json.RawMessage) []chatQuestion {
	if len(input) == 0 {
		return nil
	}
	var in struct {
		Questions []chatQuestion `json:"questions"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	return in.Questions
}

// opencodeAnswer pulls the chosen answer text out of a completed question tool's output
// ("User has answered your questions: \"Q\"=\"A\". …") for the answered block's display.
func opencodeAnswer(output string) string {
	// Take the text inside the first ="…" pair (the selected label).
	i := strings.Index(output, `="`)
	if i < 0 {
		return ""
	}
	rest := output[i+2:]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return ""
}

// opencodeTasks reads this session's ToDo list from opencode's `todo` table (direct
// columns: content/status/priority/position) so the chat shows the same checklist claude
// gets. Returns nil when the table is empty for the session.
func opencodeTasks(db *sql.DB, ses string) []taskItem {
	rows, err := db.Query(`SELECT content, status FROM todo WHERE session_id = ? ORDER BY position`, ses)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []taskItem
	i := 0
	for rows.Next() {
		var content, status string
		if rows.Scan(&content, &status) != nil {
			continue
		}
		i++
		if status == "" {
			status = "pending"
		}
		out = append(out, taskItem{ID: strconv.Itoa(i), Subject: content, Status: status})
	}
	return out
}

// opencodeReadSession loads one session's messages (ordered) and their parts, building a
// chatTurn per displayable message. A message with no displayable part (a pure
// reasoning/step frame) is dropped. Best-effort: a query error yields the turns gathered
// so far rather than failing the whole poll.
func opencodeReadSession(db *sql.DB, ses string) []chatTurn {
	rows, err := db.Query(`SELECT id, data FROM message WHERE session_id = ? ORDER BY time_created`, ses)
	if err != nil {
		return nil
	}
	type msgRow struct {
		id   string
		data []byte
	}
	var msgs []msgRow
	for rows.Next() {
		var mr msgRow
		if rows.Scan(&mr.id, &mr.data) == nil {
			msgs = append(msgs, mr)
		}
	}
	rows.Close()

	turns := make([]chatTurn, 0, len(msgs))
	for i, mr := range msgs {
		t, ok := opencodeParseMessage(db, mr.id, mr.data, i)
		if ok {
			turns = append(turns, t)
		}
	}
	return turns
}

// opencodeParseMessage builds a chatTurn from one message row and its parts. idx is the
// message ordinal (a stable render key + the unit the generic windower pages over).
func opencodeParseMessage(db *sql.DB, msgID string, data []byte, idx int) (chatTurn, bool) {
	var md struct {
		Role    string `json:"role"`
		ModelID string `json:"modelID"`
		Variant string `json:"variant"` // opencode's reasoning effort/variant (e.g. "max")
		Tokens  struct {
			Input  int `json:"input"`
			Output int `json:"output"`
			Cache  struct {
				Read  int `json:"read"`
				Write int `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
		Time struct {
			Created int64 `json:"created"`
		} `json:"time"`
		Path struct {
			Cwd string `json:"cwd"`
		} `json:"path"`
	}
	if json.Unmarshal(data, &md) != nil {
		return chatTurn{}, false
	}
	if md.Role != "user" && md.Role != "assistant" {
		return chatTurn{}, false // system / synthetic messages are not part of the chat
	}
	parts, text := opencodeParts(db, msgID)
	if len(parts) == 0 {
		return chatTurn{}, false
	}
	t := chatTurn{
		Role: md.Role, Parts: parts, Text: text, Idx: idx,
		Cwd: md.Path.Cwd,
	}
	if md.Time.Created > 0 {
		// opencode stores epoch milliseconds.
		t.TS = time.UnixMilli(md.Time.Created).UTC().Format(time.RFC3339)
	}
	if md.Role == "assistant" {
		t.Model = md.ModelID
		t.Effort = md.Variant
		t.InTok, t.OutTok = md.Tokens.Input, md.Tokens.Output
		t.CacheRead, t.CacheCreate = md.Tokens.Cache.Read, md.Tokens.Cache.Write
	}
	return t, true
}

// opencodeParts reads a message's parts in order and maps them onto chatParts: text →
// rendered Markdown, tool/patch → a faint trace, reasoning/step framing → dropped.
// Returns the parts and the concatenated text (for copy).
func opencodeParts(db *sql.DB, msgID string) ([]chatPart, string) {
	rows, err := db.Query(`SELECT data FROM part WHERE message_id = ? ORDER BY time_created`, msgID)
	if err != nil {
		return nil, ""
	}
	defer rows.Close()
	var parts []chatPart
	var sb strings.Builder
	for rows.Next() {
		var pd []byte
		if rows.Scan(&pd) != nil {
			continue
		}
		var p struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Tool  string `json:"tool"`
			State struct {
				Status string          `json:"status"`
				Input  json.RawMessage `json:"input"`
				Output string          `json:"output"`
			} `json:"state"`
			Files []string `json:"files"`
		}
		if json.Unmarshal(pd, &p) != nil {
			continue
		}
		switch p.Type {
		case "text":
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			parts = append(parts, chatPart{Kind: "text", Text: p.Text})
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(p.Text)
		case "reasoning":
			// opencode's chain-of-thought — a collapsible thinking block (like claude's).
			if strings.TrimSpace(p.Text) != "" {
				parts = append(parts, chatPart{Kind: "thinking", Text: p.Text})
			}
		case "tool":
			name := p.Tool
			if name == "" {
				name = "tool"
			}
			// opencode's `question` tool is claude's AskUserQuestion: a completed one shows
			// as an answered question block; a running one is surfaced as the pending
			// question (opencodePending), so it's skipped here.
			if name == "question" {
				if p.State.Status == "completed" {
					if qs := opencodeQuestions(p.State.Input); len(qs) > 0 {
						parts = append(parts, chatPart{Kind: "question", Tool: "question", Questions: qs, Answer: opencodeAnswer(p.State.Output)})
					}
				}
				continue
			}
			part := chatPart{Kind: "tool", Tool: name, Info: opencodeToolInfo(p.State.Input), Output: capOutput(p.State.Output)}
			// Edit-family tools carry before/after so the trace opens as a diff pane.
			if f, es := opencodeToolEdits(name, p.State.Input); len(es) > 0 {
				part.File, part.Edits = f, es
			}
			parts = append(parts, part)
		case "patch":
			// A committed edit; opencode records only the file list + hash, so it's a
			// trace (no before/after to open as a diff).
			parts = append(parts, chatPart{Kind: "tool", Tool: "patch", Info: codexClip(strings.Join(p.Files, ", "))})
		}
		// step-start / step-finish are framing — dropped.
	}
	return parts, strings.TrimSpace(sb.String())
}

// opencodeToolInfo renders a short one-line summary of a tool call's input (command /
// path / pattern), reusing codexClip for trimming.
func opencodeToolInfo(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "filePath", "path", "pattern", "query", "url"} {
		if v, ok := m[k].(string); ok && v != "" {
			return codexClip(v)
		}
	}
	return ""
}

// opencodeToolEdits extracts before/after content for opencode's edit-family tools so
// the Console can open a diff pane: `write` is all-added (Old=""), `edit` carries the
// old/new strings. Other tools return nil (they stay a plain trace).
func opencodeToolEdits(name string, input json.RawMessage) (string, []chatEdit) {
	if len(input) == 0 {
		return "", nil
	}
	switch name {
	case "write":
		var in struct {
			FilePath string `json:"filePath"`
			Content  string `json:"content"`
		}
		if json.Unmarshal(input, &in) != nil || in.FilePath == "" {
			return "", nil
		}
		return in.FilePath, []chatEdit{{Old: "", New: capEdit(in.Content)}}
	case "edit":
		var in struct {
			FilePath  string `json:"filePath"`
			OldString string `json:"oldString"`
			NewString string `json:"newString"`
		}
		if json.Unmarshal(input, &in) != nil || in.FilePath == "" {
			return "", nil
		}
		return in.FilePath, []chatEdit{{Old: capEdit(in.OldString), New: capEdit(in.NewString)}}
	}
	return "", nil
}
