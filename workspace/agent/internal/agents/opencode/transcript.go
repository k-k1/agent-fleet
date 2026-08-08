package opencode

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	_ "modernc.org/sqlite" // pure-Go SQLite driver (registers "sqlite"), as in the CP
)

// opencode chat transcript. opencode keeps its conversation in a SQLite database
// (~/.local/share/opencode/opencode.db, WAL mode) rather than a jsonl file: a `message`
// row per turn (role + tokens + model in a JSON `data` blob) and `part` rows per message
// (text / reasoning / tool / patch, ordered by time_created). We already capture this
// slot's opencode session id (sids, "ses_…", written by the bundled plugin), so
// we read that session's messages+parts read-only and normalize them into the SAME
// transcript.Turn model the Console chat consumes for claude/codex. The generic /messages
// handler windows the result (there's no line cursor — the store isn't append-only text).
//
// Shapes (verified against a real opencode.db):
//   message.data = {role:"user"|"assistant", modelID, tokens:{input,output,cache:{read,write}}, time:{created}, path:{cwd}}
//   part.data    = {type:"text"|"reasoning"|"tool"|"patch"|"step-start"|"step-finish", ...}
//     text/reasoning: {text}          tool: {tool, state:{input:{command|file_path|…}}}
//     patch: {files:[...]}            step-*: framing, dropped

// dbPath is the shared SQLite store the opencode TUI sessions write.
func dbPath() string {
	return filepath.Join(paths.HomeDir(), ".local", "share", "opencode", "opencode.db")
}

// openRO opens the store read-only (WAL auto-detected; busy_timeout so a
// concurrent opencode write can't wedge us). ok=false when the db is absent/unopenable.
func openRO() (*sql.DB, bool) {
	p := dbPath()
	if _, err := os.Stat(p); err != nil {
		return nil, false
	}
	db, err := sql.Open("sqlite", "file:"+p+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, false
	}
	return db, true
}

// activeSession resolves the slot's CURRENT opencode conversation. Per-slot truth
// first: the id the bundled plugin captured for THIS slot (it re-records on every
// event, so runtime session switches track too — claude/codex と同じスロット単位の
// 対応付け). When no mapping exists (plugin not firing), fall back to the store —
// but ONLY to a root conversation created AFTER the slot itself, i.e. one this slot
// must have opened. The previous unbounded by-dir lookup made a brand-new session
// hijack (and resume) the dir's most recent OLD conversation — 実例: 新規スロット
// szyyh2f が ~/repos/temp の9日前の会話を --session で引き継いでしまった。
func activeSession(db *sql.DB, m session.Meta) string {
	id, _ := activeSessionErr(db, m)
	return id
}

// activeSessionErr is activeSession keeping the store error apart from "no conversation
// yet" (sql.ErrNoRows → "", nil). Only LiveState needs the distinction: a real query
// error means opencode's store contract moved under us (see LiveState), not that the
// slot is idle. Other callers take activeSession and treat both as "no conversation".
func activeSessionErr(db *sql.DB, m session.Meta) (string, error) {
	if id := sids.Read(session.UUID(m.Dir, m.Name)); id != "" && sessionExists(db, id) {
		return id, nil
	}
	var id string
	err := db.QueryRow(
		`SELECT s.id FROM session s JOIN message msg ON msg.session_id = s.id
		 WHERE s.directory = ? AND (s.parent_id IS NULL OR s.parent_id = '')
		   AND s.time_created >= ?
		 GROUP BY s.id ORDER BY MAX(msg.time_created) DESC LIMIT 1`, m.Dir, metaCreatedMs(m),
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil // no conversation this slot opened — a normal, empty result
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// sessionExists guards a stale plugin mapping (a deleted/imported-away session id)
// from shadowing the store-derived fallback.
func sessionExists(db *sql.DB, id string) bool {
	var n int
	return db.QueryRow(`SELECT count(*) FROM session WHERE id = ?`, id).Scan(&n) == nil && n > 0
}

// metaCreatedMs is the slot's creation time as epoch millis (opencode's time unit).
// A missing/unparsable CreatedAt yields 0 — the permissive pre-fix behavior — so a
// legacy meta without the field keeps its conversation rather than losing it.
func metaCreatedMs(m session.Meta) int64 {
	if m.CreatedAt == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, m.CreatedAt)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// LiveState derives opencode's working/idle/question state from the store (robust —
// the status plugin's events are unreliable): a turn is in flight when the active
// session's newest message isn't a completed assistant reply, and an in-flight turn
// whose question tool is running is "question" — the same state claude raises, so the
// sessions list chip (質問あり) and desktop notifications light up for opencode too.
// Returns "" when the state can't be derived (caller falls back to the plugin status
// store): the db can't be read, OR the store answered but not in the shape we parse.
//
// Why "" and not "idle" on a store error: opencode's schema is its own private,
// unversioned contract and it does migrate (the store carries ~38 applied migrations,
// and a v2 session_message store already exists alongside the v1 message/part one this
// reads). Returning "idle" on a broken read would assert the *strongest* claim — the
// turn is done — from the *least* information, and it fails silently: a schema change
// would flip a live turn's state to 入力待ち with no stop button, which is exactly the
// claude false-idle bug (there, a TUI footer string moved). It can't self-heal either,
// because a non-empty "idle" short-circuits driveState before the reverse-heal, and
// claude's spinner probe doesn't match opencode's footer anyway. "" degrades to the
// plugin status instead of guessing. Verified: renaming `message` turns a live
// "working" into "" (fallback), not a silent "idle".
func LiveState(m session.Meta) string {
	db, ok := openRO()
	if !ok {
		return ""
	}
	defer db.Close()
	ses, err := activeSessionErr(db, m)
	if err != nil {
		return "" // store contract moved — unknown, not idle
	}
	if ses == "" {
		return "idle" // no conversation yet — sitting at the composer
	}
	var data []byte
	switch err := db.QueryRow(`SELECT data FROM message WHERE session_id = ? ORDER BY time_created DESC LIMIT 1`, ses).Scan(&data); {
	case errors.Is(err, sql.ErrNoRows):
		return "idle" // a conversation with no messages yet — genuinely at the composer
	case err != nil:
		return "" // store contract moved — unknown, not idle
	}
	var md struct {
		Role string `json:"role"`
		Time struct {
			Completed int64 `json:"completed"`
		} `json:"time"`
	}
	if json.Unmarshal(data, &md) != nil {
		return "" // message payload isn't the shape we parse — unknown, not idle
	}
	if md.Role == "assistant" && md.Time.Completed > 0 {
		return "idle"
	}
	if len(pending(db, ses)) > 0 {
		return "question" // the in-flight turn is waiting on the user's answer
	}
	return "working" // an in-flight assistant turn, or a user message awaiting a reply
}

// readTranscript reads the slot's current opencode conversation as normalized
// chat turns plus the db path (diagnostics). ok is always true; no conversation yet
// yields nil turns (an empty chat).
func readTranscript(m session.Meta) (agents.TranscriptData, bool) {
	db, ok := openRO()
	if !ok {
		return agents.TranscriptData{}, true
	}
	defer db.Close()
	path := dbPath()
	ses := activeSession(db, m)
	if ses == "" {
		td := agents.TranscriptData{Path: path}
		managedEnrich(m, &td)
		return td, true
	}
	td := agents.TranscriptData{
		Turns:      readSession(db, ses),
		Path:       path,
		Tasks:      tasks(db, ses),
		Pending:    pending(db, ses),
		Mode:       mode(db, ses),
		Queued:     queued(db, ses),
		Compacting: compacting(db, ses),
	}
	// managed セッション（docs/27 P2）: driver の runtime 状態を合流 — pending 質問へ
	// Interaction id（/respond の宛先）、driver 内キューを キュー済み へ。
	managedEnrich(m, &td)
	return td, true
}

// queued returns the prompts sitting in opencode's mid-run input queue — typed while a
// turn runs, recorded as session_input rows, and not yet promoted to a real user
// message (promoted_seq is set on promotion; consumed rows are cleaned up). Surfaced as
// the mirror's キュー済み badge, like claude's queue-operation reconstruction. Ordered by
// arrival. The generic handler gates on the working state, so stale leftovers from a
// killed run stay hidden.
func queued(db *sql.DB, ses string) []string {
	rows, err := db.Query(
		`SELECT prompt FROM session_input WHERE session_id = ? AND promoted_seq IS NULL ORDER BY time_created, id`, ses,
	)
	if err != nil {
		return nil // older store without session_input — no queue to show
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) != nil {
			continue
		}
		if t := strings.TrimSpace(promptText(p)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// promptText decodes a session_input prompt column into displayable text. opencode
// stores it JSON-encoded; the shapes seen/anticipated are a bare string, an object
// with a text field, or a parts array whose text parts carry the typed message.
// Anything undecodable falls back to the raw column so the queue never shows blank.
func promptText(raw string) string {
	b := []byte(strings.TrimSpace(raw))
	if len(b) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		return s
	}
	type textPart struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	join := func(parts []textPart) string {
		var sb strings.Builder
		for _, p := range parts {
			if (p.Type == "" || p.Type == "text") && strings.TrimSpace(p.Text) != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(p.Text)
			}
		}
		return sb.String()
	}
	var arr []textPart
	if json.Unmarshal(b, &arr) == nil {
		if t := join(arr); t != "" {
			return t
		}
	}
	var obj struct {
		Text  string     `json:"text"`
		Parts []textPart `json:"parts"`
	}
	if json.Unmarshal(b, &obj) == nil {
		if obj.Text != "" {
			return obj.Text
		}
		if t := join(obj.Parts); t != "" {
			return t
		}
	}
	return raw
}

// compacting reports whether opencode is compacting this session's conversation right
// now — session.time_compacting is set while a compaction runs and cleared after
// (opencode's own status derives "compacting" from exactly this field).
func compacting(db *sql.DB, ses string) bool {
	var v sql.NullInt64
	if db.QueryRow(`SELECT time_compacting FROM session WHERE id = ?`, ses).Scan(&v) != nil {
		return false
	}
	return v.Valid && v.Int64 > 0
}

// mode reports the session's current agent/mode normalized to "plan" | "normal".
// opencode's "plan" agent is its plan mode; anything else (build, …) is normal. Read from
// the newest message's agent field (falling back to mode).
func mode(db *sql.DB, ses string) string {
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

// pending returns the questions of a currently-running `question` tool part in
// this session (opencode is awaiting the user's answer), or nil. Same shape as claude's
// AskUserQuestion so the Console renders it interactively.
func pending(db *sql.DB, ses string) []transcript.Question {
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
	return questions(p.State.Input)
}

// questions parses a question tool's state.input into transcript.Questions (identical
// schema to claude's AskUserQuestion: questions[]{question,header,multiSelect,options}).
func questions(input json.RawMessage) []transcript.Question {
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

// answer pulls the chosen answer text out of a completed question tool's output
// ("User has answered your questions: \"Q\"=\"A\". …") for the answered block's display.
func answer(output string) string {
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

// tasks reads this session's ToDo list from opencode's `todo` table (direct
// columns: content/status/priority/position) so the chat shows the same checklist claude
// gets. Returns nil when the table is empty for the session.
func tasks(db *sql.DB, ses string) []transcript.Task {
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

// sessionResumable reports whether resuming this session with `opencode
// --session <id>` is safe — i.e. its last turn is COMPLETE. opencode continues an
// incomplete/interrupted assistant turn on resume (re-running the pending work, e.g. an
// interrupted Explore subagent), which is never what the user wants after a Stop→Start.
// A message is complete when its data.time.completed is set (opencode leaves it null
// while a turn is in flight / was interrupted). No captured session, no db, or an empty
// session all count as resumable (nothing to re-run). Best-effort: on any read error we
// default to true so a transient hiccup doesn't needlessly drop the conversation.
func sessionResumable(ses string) bool {
	if ses == "" {
		return true
	}
	db, ok := openRO()
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

// readSession loads one session's messages (ordered) and their parts, building a
// transcript.Turn per displayable message. Subagent activity (child sessions with
// parent_id = ses, spawned by the task tool) is interleaved by creation time and
// flagged Sidechain, so the chat shows it like claude's subagent turns. A message with
// no displayable part (a pure reasoning/step frame) is dropped. Best-effort: a query
// error yields the turns gathered so far rather than failing the whole poll.
//
// Ordering note: rows are stamped time_created at insert, so the merged parent+child
// sequence is append-only across polls — each turn's ordinal (Idx, the render key and
// paging cursor unit) stays stable as new messages arrive.
func readSession(db *sql.DB, ses string) []transcript.Turn {
	sessions := append([]string{ses}, childSessions(db, ses)...)
	ph := strings.TrimSuffix(strings.Repeat("?,", len(sessions)), ",")
	args := make([]any, len(sessions))
	for i, s := range sessions {
		args[i] = s
	}
	rows, err := db.Query(
		`SELECT id, session_id, data FROM message WHERE session_id IN (`+ph+`) ORDER BY time_created, id`, args...,
	)
	if err != nil {
		return nil
	}
	type msgRow struct {
		id   string
		ses  string
		data []byte
	}
	var msgs []msgRow
	for rows.Next() {
		var mr msgRow
		if rows.Scan(&mr.id, &mr.ses, &mr.data) == nil {
			msgs = append(msgs, mr)
		}
	}
	rows.Close()

	turns := make([]transcript.Turn, 0, len(msgs))
	for i, mr := range msgs {
		t, ok := parseMessage(db, mr.id, mr.data, i)
		if ok {
			t.Sidechain = mr.ses != ses
			turns = append(turns, t)
		}
	}
	return turns
}

// childSessions lists the subagent sessions spawned under ses (one level — the task
// tool's children). Best-effort: any error just means no sidechain turns.
func childSessions(db *sql.DB, ses string) []string {
	rows, err := db.Query(`SELECT id FROM session WHERE parent_id = ? ORDER BY time_created, id`, ses)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && id != "" {
			out = append(out, id)
		}
	}
	return out
}

// parseMessage builds a transcript.Turn from one message row and its parts. idx is the
// message ordinal (a stable render key + the unit the generic windower pages over).
func parseMessage(db *sql.DB, msgID string, data []byte, idx int) (transcript.Turn, bool) {
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
			Created   int64 `json:"created"`
			Completed int64 `json:"completed"` // assistant only; absent while the turn runs
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
	parts, text := readParts(db, msgID)
	// A failed turn (errors.go) carries its reason in the message row, not in a part,
	// and typically has NO parts at all — so the error part has to be appended BEFORE
	// the empty-turn drop below, or the turn (and the only explanation the user gets)
	// vanishes from the mirror while the session quietly reads idle.
	if e, ok := decodeMessageError(data); ok {
		parts = append(parts, e.part())
		// Text is the flattened form (copy button, get_session_output, chat bridge):
		// the operator must see the failure there too, not just in the rendered block.
		if text != "" {
			text += "\n\n"
		}
		text += e.summary()
	}
	if len(parts) == 0 {
		return transcript.Turn{}, false
	}
	t := transcript.Turn{
		Role: md.Role, Parts: parts, Text: text, Idx: idx,
		Cwd: md.Path.Cwd,
		// opencode's own message id IS the fork anchor: POST /session/{id}/fork takes a
		// messageID and its copy loop stops at the first message whose id sorts >= that
		// value (docs/55 §55.2), so the anchor travels to the server untranslated.
		AnchorID: msgID,
	}
	if md.Time.Created > 0 {
		// opencode stores epoch milliseconds.
		t.TS = time.UnixMilli(md.Time.Created).UTC().Format(time.RFC3339)
	}
	if md.Role == "assistant" {
		// One opencode message IS one whole turn (text + every tool part), so created is
		// the turn's START. completed lands when the turn ends — that is what the mirror's
		// footer wants. Still running (completed == 0): leave EndTS empty and let the
		// Console fall back to created.
		if md.Time.Completed > 0 {
			t.EndTS = time.UnixMilli(md.Time.Completed).UTC().Format(time.RFC3339)
		}
		t.Model = md.ModelID
		t.Effort = md.Variant
		t.InTok, t.OutTok = md.Tokens.Input, md.Tokens.Output
		t.CacheRead, t.CacheCreate = md.Tokens.Cache.Read, md.Tokens.Cache.Write
	}
	return t, true
}

// readParts reads a message's parts in order and maps them onto transcript.Parts: text →
// rendered Markdown, tool/patch → a faint trace, reasoning/step framing → dropped.
// Returns the parts and the concatenated text (for copy).
func readParts(db *sql.DB, msgID string) ([]transcript.Part, string) {
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
			// question (pending), so it's skipped here.
			if name == "question" {
				if p.State.Status == "completed" {
					if qs := questions(p.State.Input); len(qs) > 0 {
						parts = append(parts, transcript.Part{Kind: "question", Tool: "question", Questions: qs, Answer: answer(p.State.Output)})
					}
				}
				continue
			}
			if name == "task" {
				var in struct {
					Description  string `json:"description"`
					Prompt       string `json:"prompt"`
					SubagentType string `json:"subagent_type"`
					Model        string `json:"model"`
				}
				if json.Unmarshal(p.State.Input, &in) == nil {
					status := strings.ToLower(strings.TrimSpace(p.State.Status))
					switch status {
					case "pending", "queued":
						status = "requested"
					case "in_progress":
						status = "running"
					case "error", "cancelled":
						status = "failed"
					case "completed", "running":
					default:
						status = "requested"
					}
					parts = append(parts, transcript.Part{
						Kind: "delegation", Tool: name, Info: strings.TrimSpace(in.Description),
						Prompt: strings.TrimSpace(in.Prompt), AgentType: in.SubagentType,
						Model: in.Model, Status: status, Output: transcript.CapOutput(p.State.Output),
					})
					continue
				}
			}
			part := transcript.Part{Kind: "tool", Tool: name, Info: toolInfo(p.State.Input), Output: transcript.CapOutput(p.State.Output)}
			// Edit-family tools carry before/after so the trace opens as a diff pane.
			if f, es := toolEdits(name, p.State.Input); len(es) > 0 {
				part.File, part.Edits = f, es
			}
			parts = append(parts, part)
		case "patch":
			// A committed edit; opencode records only the file list + hash, so it's a
			// trace (no before/after to open as a diff).
			parts = append(parts, transcript.Part{Kind: "tool", Tool: "patch", Info: transcript.Clip(strings.Join(p.Files, ", "))})
		}
		// step-start / step-finish are framing — dropped.
	}
	return parts, strings.TrimSpace(sb.String())
}

// toolInfo renders a short one-line summary of a tool call's input (command /
// path / pattern), reusing transcript.Clip for trimming.
func toolInfo(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	// description last: it's the task (subagent) tool's summary, but a more specific
	// field (command/path/pattern) should win when both exist.
	for _, k := range []string{"command", "file_path", "filePath", "path", "pattern", "query", "url", "description"} {
		if v, ok := m[k].(string); ok && v != "" {
			return transcript.Clip(v)
		}
	}
	return ""
}

// toolEdits extracts before/after content for opencode's edit-family tools so
// the Console can open a diff pane: `write` is all-added (Old=""), `edit` carries the
// old/new strings. Other tools return nil (they stay a plain trace).
func toolEdits(name string, input json.RawMessage) (string, []transcript.Edit) {
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
		return in.FilePath, []transcript.Edit{{Old: "", New: transcript.CapEdit(in.Content)}}
	case "edit":
		var in struct {
			FilePath  string `json:"filePath"`
			OldString string `json:"oldString"`
			NewString string `json:"newString"`
		}
		if json.Unmarshal(input, &in) != nil || in.FilePath == "" {
			return "", nil
		}
		return in.FilePath, []transcript.Edit{{Old: transcript.CapEdit(in.OldString), New: transcript.CapEdit(in.NewString)}}
	}
	return "", nil
}
