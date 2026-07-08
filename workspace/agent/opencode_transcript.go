package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	_ "modernc.org/sqlite" // pure-Go SQLite driver (registers "sqlite"), as in the CP
)

// opencode chat transcript. opencode keeps its conversation in a SQLite database
// (~/.local/share/opencode/opencode.db, WAL mode) rather than a jsonl file: a `message`
// row per turn (role + tokens + model in a JSON `data` blob) and `part` rows per message
// (text / reasoning / tool / patch, ordered by time_created). We already capture this
// slot's opencode session id (opencodeSids, "ses_…", written by the bundled plugin), so
// we read that session's messages+parts read-only and normalize them into the SAME
// transcript.Turn model the Console chat consumes for claude/codex. The generic /messages
// handler windows the result (there's no line cursor — the store isn't append-only text).
//
// Shapes (verified against a real opencode.db):
//   message.data = {role:"user"|"assistant", modelID, tokens:{input,output,cache:{read,write}}, time:{created}, path:{cwd}}
//   part.data    = {type:"text"|"reasoning"|"tool"|"patch"|"step-start"|"step-finish", ...}
//     text/reasoning: {text}          tool: {tool, state:{input:{command|file_path|…}}}
//     patch: {files:[...]}            step-*: framing, dropped

// opencodeDBPath is the shared SQLite store the opencode TUI sessions write.
func opencodeDBPath() string {
	return filepath.Join(homeDir(), ".local", "share", "opencode", "opencode.db")
}

// opencodeOpenRO opens the store read-only (WAL auto-detected; busy_timeout so a
// concurrent opencode write can't wedge us). ok=false when the db is absent/unopenable.
func opencodeOpenRO() (*sql.DB, bool) {
	p := opencodeDBPath()
	if _, err := os.Stat(p); err != nil {
		return nil, false
	}
	db, err := sql.Open("sqlite", "file:"+p+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, false
	}
	return db, true
}

// opencodeActiveSession resolves the slot's CURRENT opencode conversation from the store
// itself — the newest ROOT session (parent_id null, i.e. not a subagent child) in the
// slot's working dir, by most-recent message. This is plugin-independent: opencode can
// create/switch sessions at runtime, and its status plugin may not be firing, so we don't
// trust the captured sid alone — we read what opencode is actually using. Falls back to
// the captured sid only when the query can't run. (Caveat: two opencode slots in the SAME
// dir resolve to the same session — the pre-existing multi-slot-same-dir limitation.)
func opencodeActiveSession(db *sql.DB, m sessionMeta) string {
	var id string
	_ = db.QueryRow(
		`SELECT s.id FROM session s JOIN message msg ON msg.session_id = s.id
		 WHERE s.directory = ? AND (s.parent_id IS NULL OR s.parent_id = '')
		 GROUP BY s.id ORDER BY MAX(msg.time_created) DESC LIMIT 1`, m.Dir,
	).Scan(&id)
	if id == "" {
		return opencodeSids.read(sessionUUID(m.Dir, m.Name))
	}
	return id
}

// opencodeLiveState derives opencode's working/idle state from the store (robust — the
// status plugin's events are unreliable): a turn is in flight when the active session's
// newest message isn't a completed assistant reply. Returns "" when the db can't be read
// (caller falls back to the plugin status store).
func opencodeLiveState(m sessionMeta) string {
	db, ok := opencodeOpenRO()
	if !ok {
		return ""
	}
	defer db.Close()
	ses := opencodeActiveSession(db, m)
	if ses == "" {
		return "idle" // no conversation yet — sitting at the composer
	}
	var data []byte
	err := db.QueryRow(`SELECT data FROM message WHERE session_id = ? ORDER BY time_created DESC LIMIT 1`, ses).Scan(&data)
	if err != nil {
		return "idle"
	}
	var md struct {
		Role string `json:"role"`
		Time struct {
			Completed int64 `json:"completed"`
		} `json:"time"`
	}
	if json.Unmarshal(data, &md) != nil {
		return "idle"
	}
	if md.Role == "assistant" && md.Time.Completed > 0 {
		return "idle"
	}
	return "working" // an in-flight assistant turn, or a user message awaiting a reply
}

// readOpencodeTranscript reads the slot's current opencode conversation as normalized
// chat turns plus the db path (diagnostics). ok is always true; no conversation yet
// yields nil turns (an empty chat).
func readOpencodeTranscript(m sessionMeta) (transcriptData, bool) {
	db, ok := opencodeOpenRO()
	if !ok {
		return transcriptData{}, true
	}
	defer db.Close()
	path := opencodeDBPath()
	ses := opencodeActiveSession(db, m)
	if ses == "" {
		return transcriptData{path: path}, true
	}
	return transcriptData{
		turns:   opencodeReadSession(db, ses),
		path:    path,
		tasks:   opencodeTasks(db, ses),
		pending: opencodePending(db, ses),
		mode:    opencodeMode(db, ses),
	}, true
}

// opencodeMode reports the session's current agent/mode normalized to "plan" | "normal".
// opencode's "plan" agent is its plan mode; anything else (build, …) is normal. Read from
// the newest message's agent field (falling back to mode).
func opencodeMode(db *sql.DB, ses string) string {
	var data []byte
	if db.QueryRow(`SELECT data FROM message WHERE session_id = ? ORDER BY time_created DESC LIMIT 1`, ses).Scan(&data) != nil {
		return ""
	}
	var md struct {
		Agent string `json:"agent"`
		Mode  string `json:"mode"`
	}
	if json.Unmarshal(data, &md) != nil {
		return ""
	}
	v := md.Agent
	if v == "" {
		v = md.Mode
	}
	if v == "plan" {
		return "plan"
	}
	if v == "" {
		return ""
	}
	return "normal"
}

// opencodePending returns the questions of a currently-running `question` tool part in
// this session (opencode is awaiting the user's answer), or nil. Same shape as claude's
// AskUserQuestion so the Console renders it interactively.
func opencodePending(db *sql.DB, ses string) []transcript.Question {
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

// opencodeQuestions parses a question tool's state.input into transcript.Questions (identical
// schema to claude's AskUserQuestion: questions[]{question,header,multiSelect,options}).
func opencodeQuestions(input json.RawMessage) []transcript.Question {
	if len(input) == 0 {
		return nil
	}
	var in struct {
		Questions []transcript.Question `json:"questions"`
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
func opencodeTasks(db *sql.DB, ses string) []transcript.Task {
	rows, err := db.Query(`SELECT content, status FROM todo WHERE session_id = ? ORDER BY position`, ses)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []transcript.Task
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
		out = append(out, transcript.Task{ID: strconv.Itoa(i), Subject: content, Status: status})
	}
	return out
}

// opencodeSessionResumable reports whether resuming this session with `opencode
// --session <id>` is safe — i.e. its last turn is COMPLETE. opencode continues an
// incomplete/interrupted assistant turn on resume (re-running the pending work, e.g. an
// interrupted Explore subagent), which is never what the user wants after a Stop→Start.
// A message is complete when its data.time.completed is set (opencode leaves it null
// while a turn is in flight / was interrupted). No captured session, no db, or an empty
// session all count as resumable (nothing to re-run). Best-effort: on any read error we
// default to true so a transient hiccup doesn't needlessly drop the conversation.
func opencodeSessionResumable(ses string) bool {
	if ses == "" {
		return true
	}
	db, ok := opencodeOpenRO()
	if !ok {
		return true
	}
	defer db.Close()
	// Newest message for the session; is it a completed assistant turn?
	row := db.QueryRow(`SELECT data FROM message WHERE session_id = ? ORDER BY time_created DESC LIMIT 1`, ses)
	var data []byte
	if err := row.Scan(&data); err != nil {
		return true // no messages (fresh) or read error — nothing to re-run
	}
	var md struct {
		Role string `json:"role"`
		Time struct {
			Completed int64 `json:"completed"`
		} `json:"time"`
	}
	if json.Unmarshal(data, &md) != nil {
		return true
	}
	// A user message as the last row means the assistant never replied (interrupted
	// before responding); an assistant message with no completed time was interrupted
	// mid-turn. Either way, resuming would continue it — so it's not resumable.
	if md.Role == "assistant" && md.Time.Completed > 0 {
		return true
	}
	return false
}

// opencodeReadSession loads one session's messages (ordered) and their parts, building a
// transcript.Turn per displayable message. A message with no displayable part (a pure
// reasoning/step frame) is dropped. Best-effort: a query error yields the turns gathered
// so far rather than failing the whole poll.
func opencodeReadSession(db *sql.DB, ses string) []transcript.Turn {
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

	turns := make([]transcript.Turn, 0, len(msgs))
	for i, mr := range msgs {
		t, ok := opencodeParseMessage(db, mr.id, mr.data, i)
		if ok {
			turns = append(turns, t)
		}
	}
	return turns
}

// opencodeParseMessage builds a transcript.Turn from one message row and its parts. idx is the
// message ordinal (a stable render key + the unit the generic windower pages over).
func opencodeParseMessage(db *sql.DB, msgID string, data []byte, idx int) (transcript.Turn, bool) {
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
		return transcript.Turn{}, false
	}
	if md.Role != "user" && md.Role != "assistant" {
		return transcript.Turn{}, false // system / synthetic messages are not part of the chat
	}
	parts, text := opencodeParts(db, msgID)
	if len(parts) == 0 {
		return transcript.Turn{}, false
	}
	t := transcript.Turn{
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

// opencodeParts reads a message's parts in order and maps them onto transcript.Parts: text →
// rendered Markdown, tool/patch → a faint trace, reasoning/step framing → dropped.
// Returns the parts and the concatenated text (for copy).
func opencodeParts(db *sql.DB, msgID string) ([]transcript.Part, string) {
	rows, err := db.Query(`SELECT data FROM part WHERE message_id = ? ORDER BY time_created`, msgID)
	if err != nil {
		return nil, ""
	}
	defer rows.Close()
	var parts []transcript.Part
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
			parts = append(parts, transcript.Part{Kind: "text", Text: p.Text})
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(p.Text)
		case "reasoning":
			// opencode's chain-of-thought — a collapsible thinking block (like claude's).
			if strings.TrimSpace(p.Text) != "" {
				parts = append(parts, transcript.Part{Kind: "thinking", Text: p.Text})
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
						parts = append(parts, transcript.Part{Kind: "question", Tool: "question", Questions: qs, Answer: opencodeAnswer(p.State.Output)})
					}
				}
				continue
			}
			part := transcript.Part{Kind: "tool", Tool: name, Info: opencodeToolInfo(p.State.Input), Output: capOutput(p.State.Output)}
			// Edit-family tools carry before/after so the trace opens as a diff pane.
			if f, es := opencodeToolEdits(name, p.State.Input); len(es) > 0 {
				part.File, part.Edits = f, es
			}
			parts = append(parts, part)
		case "patch":
			// A committed edit; opencode records only the file list + hash, so it's a
			// trace (no before/after to open as a diff).
			parts = append(parts, transcript.Part{Kind: "tool", Tool: "patch", Info: codexClip(strings.Join(p.Files, ", "))})
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
func opencodeToolEdits(name string, input json.RawMessage) (string, []transcript.Edit) {
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
		return in.FilePath, []transcript.Edit{{Old: "", New: capEdit(in.Content)}}
	case "edit":
		var in struct {
			FilePath  string `json:"filePath"`
			OldString string `json:"oldString"`
			NewString string `json:"newString"`
		}
		if json.Unmarshal(input, &in) != nil || in.FilePath == "" {
			return "", nil
		}
		return in.FilePath, []transcript.Edit{{Old: capEdit(in.OldString), New: capEdit(in.NewString)}}
	}
	return "", nil
}
