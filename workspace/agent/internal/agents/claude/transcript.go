package claude

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// claude jsonl transcript の読み出しと解析（docs/23 残① Wave F: 旧 package main
// session_io.go の jsonl 読み出し + session_transcript.go の claude パーサ）。
// /messages・/output の HTTP ハンドラ（ウィンドウ処理・ページング・internal/status
// の pending 合成）は package main の session_transcript.go / session_io.go に残り、
// ここの exported 関数を呼ぶ。共有の turn/part モデルは internal/transcript。

// jsonlByMtime returns sid's conversation logs newest-first. claude can leave more
// than one <sid>.jsonl under projects/* (a cwd change, a CLAUDE_CONFIG_DIR switch,
// or a stale log from an earlier run all produce siblings under different project
// dirs). glob order is lexical, so paths[0] can be an OLD file that never grows —
// the chat then freezes on stale content. The live log is the most recently written
// one, so we sort by mtime and read that.
func jsonlByMtime(sid string) []string {
	paths := jsonlPaths(sid)
	sort.SliceStable(paths, func(i, j int) bool {
		return jsonlMtime(paths[i]).After(jsonlMtime(paths[j]))
	})
	return paths
}

// jsonlMtime は package main の同名ヘルパの複製（極小 stat のため共有せず重複を
// 許容 — main 側は generic /messages でも使う）。
func jsonlMtime(p string) time.Time {
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// TranscriptRead reads the session's live jsonl as raw lines — one JSON event per
// line, the line count being the cursor — and returns the chosen path plus every
// matching path (for the /messages diagnostics).
//
// It prefers the NEWEST log that actually holds a conversation. A session commonly
// has sibling <sid>.jsonl files: the real transcript, plus stubs (a Remote Control
// "bridge-session", a lone summary) that can carry a NEWER mtime — while a workflow
// runs, a bridge stub may be touched more recently than the main log. Reading a stub
// would show an empty chat, so we skip stubs and fall back to the newest file only
// when none has real turns yet.
func TranscriptRead(sid string) (lines [][]byte, path string, matched []string) {
	matched = jsonlByMtime(sid)
	if len(matched) == 0 {
		return nil, "", nil
	}
	var fallback [][]byte
	fallbackPath := matched[0]
	for i, p := range matched {
		ls := readJSONLLines(p)
		if i == 0 {
			fallback = ls
		}
		if HasConversation(ls) {
			return ls, p, matched
		}
	}
	return fallback, fallbackPath, matched
}

// lastLineWhere returns the LAST line of p that ok accepts, scanning backwards. It reads
// the tail window first and only falls back to the whole file when the window holds no
// accepted line, so the polled callers (freshness / 中断検知) do not re-read a multi-MB
// transcript every tick just to look at its end.
func lastLineWhere(p string, ok func([]byte) bool) ([]byte, bool) {
	for _, lines := range tailThenWhole(p) {
		for i := len(lines) - 1; i >= 0; i-- {
			if ok(lines[i]) {
				return lines[i], true
			}
		}
	}
	return nil, false
}

// tailThenWhole yields the file's lines twice over: the last transcriptTailWindow bytes
// (with the half-line the window cut off dropped), then — only if the file is bigger —
// the whole thing.
func tailThenWhole(p string) [][][]byte {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	windows := []int64{transcriptTailWindow}
	if fi.Size() > transcriptTailWindow {
		windows = append(windows, fi.Size())
	}
	var out [][][]byte
	for _, w := range windows {
		off := fi.Size() - w
		truncated := off > 0
		if off < 0 {
			off = 0
		}
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return out
		}
		buf, err := io.ReadAll(f)
		if err != nil {
			return out
		}
		lines := bytes.Split(buf, []byte("\n"))
		if truncated && len(lines) > 0 {
			lines = lines[1:]
		}
		out = append(out, lines)
	}
	return out
}

// readJSONLLines reads a jsonl file into its non-empty raw lines, dropping a trailing
// line that is still being written.
//
// claude appends the transcript in 4 KiB chunks (measured 2026-08-03: a live log's size
// advanced 4096 bytes at a time), so a read can land INSIDE a line longer than that and
// see it half-written. Counting that fragment as a line is not a cosmetic parse failure:
// /messages hands the client `len(lines)` as its cursor, the parser drops the unreadable
// fragment, and every later window starts AFTER it — so that turn is never delivered
// again and silently vanishes from the mirror (a lost user prompt then also leaves its
// optimistic echo stuck at 「反映待ち」 forever). A complete line always ends in "\n", so
// cut anything past the last one and let the next poll read the line once it's whole.
func readJSONLLines(p string) [][]byte {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	i := bytes.LastIndexByte(b, '\n')
	if i < 0 {
		return nil // nothing but a partially-written first line
	}
	var out [][]byte
	for _, ln := range strings.Split(string(b[:i+1]), "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, []byte(ln))
		}
	}
	return out
}

// HasConversation reports whether the lines include a real user/assistant turn
// (not just bookkeeping) — the per-file form of JSONLResumable, so we can skip stubs.
func HasConversation(lines [][]byte) bool {
	for _, ln := range lines {
		if bytesContains(ln, `"type":"user"`) || bytesContains(ln, `"type":"assistant"`) {
			return true
		}
	}
	return false
}

// TranscriptLines is the lines-only view (the /output MCP poll doesn't need the
// source path); both share the newest-file selection above.
func TranscriptLines(sid string) [][]byte {
	lines, _, _ := TranscriptRead(sid)
	return lines
}

// AssistantText extracts the concatenated text blocks from an assistant event
// line (skips user/tool/bookkeeping lines).
func AssistantText(line []byte) string {
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Type != "assistant" {
		return ""
	}
	var sb strings.Builder
	for _, c := range ev.Message.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

// CollectTurns builds the displayable turns from lines[lo:hi] (a window into the
// transcript — the whole file, a tail window, a backward page, or a live increment; see
// docs/decisions/0009). It resolves each answered AskUserQuestion/ExitPlanMode's chosen
// answer from its tool_result for display (the currently-pending one is surfaced
// separately). Each turn keeps its ABSOLUTE line index as idx (stable across windows, so
// React keys and ordering hold when pages are prepended). Capped at 1 MiB of newest text.
//
// NOTE: window-local answer resolution is only complete when the window holds BOTH the
// question's tool_use AND its tool_result. That is NOT guaranteed: claude writes an
// AskUserQuestion/ExitPlanMode/Agent tool_use at ASK time and its tool_result lands later
// (an answer can be minutes and — live — many polls away), so a live increment or a page
// boundary can split them, leaving the question turn here with an empty Answer. The
// Console reconciles that from the whole-transcript CollectInteractionAnswers map, which
// the /messages handler sends alongside these turns and patches on by qid.
func CollectTurns(lines [][]byte, lo, hi int) []transcript.Turn {
	if lo < 0 {
		lo = 0
	}
	if hi > len(lines) {
		hi = len(lines)
	}
	// Best-effort window-local resolution: fills the Answer when the tool_result is in the
	// same window. When it isn't (an ask/answer split across increments or a page boundary),
	// the Answer stays empty here and the Console patches it from CollectInteractionAnswers.
	answers := collectAnswers(lines[lo:hi])
	turns := []transcript.Turn{}
	budget := 0
	// Walk newest→oldest so the 1 MiB cap keeps the LATEST turns (the old oldest-first cap
	// could drop the newest of a huge transcript); reverse to chronological before return.
	for i := hi - 1; i >= lo; i-- {
		t, ok := parseTurn(lines[i], i) // i is the absolute line index (stable across windows)
		if !ok {
			continue // tool results, summaries, bridge/meta bookkeeping
		}
		for pi := range t.Parts {
			if (t.Parts[pi].Kind == "question" || t.Parts[pi].Kind == "plan") && t.Parts[pi].QID != "" {
				a := answers[t.Parts[pi].QID]
				t.Parts[pi].Answer = a.Text
				if t.Parts[pi].Kind == "question" {
					t.Parts[pi].Declined = a.Declined
				}
			}
			if t.Parts[pi].Kind == "delegation" && t.Parts[pi].QID != "" {
				if result := answers[t.Parts[pi].QID].Text; result != "" {
					t.Parts[pi].Output = transcript.CapOutput(result)
					// Only foreground delegations retain QID; their tool_result is final.
					t.Parts[pi].Status = "completed"
				}
			}
		}
		turns = append(turns, t)
		if budget += len(t.Text); budget > 1<<20 { // cap a single response at 1 MiB (newest kept)
			break
		}
	}
	for l, r := 0, len(turns)-1; l < r; l, r = l+1, r-1 {
		turns[l], turns[r] = turns[r], turns[l]
	}
	return turns
}

// parseTurn builds a transcript.Turn from a transcript line. ok is false for lines that
// carry nothing displayable: tool_result-only user turns, summaries, the Remote
// Control bridge-session line, and meta entries (isMeta).
func parseTurn(line []byte, idx int) (transcript.Turn, bool) {
	var ev struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		// UUID is the line's own id in claude's uuid/parentUuid DAG. It is the fork
		// anchor (docs/55): claude's own --fork-session rewrites only sessionId and
		// leaves uuid/parentUuid untouched (実測), so a message keeps the same uuid
		// across branches — it is a durable handle, unlike the line index.
		UUID             string `json:"uuid"`
		IsMeta           bool   `json:"isMeta"`
		IsSidechain      bool   `json:"isSidechain"`
		IsCompactSummary bool   `json:"isCompactSummary"`
		GitBranch        string `json:"gitBranch"`
		Cwd              string `json:"cwd"`
		// 合成 API エラーレコードの3点（errors.go）。abort.go が中断判定に使うのと
		// 同じフィールドだが、あちらは「ターンが落ちて終わったか」、ここは「画面に
		// どう出すか」— 用途が違うので読み手も別で持つ。
		IsAPIError     bool   `json:"isApiErrorMessage"`
		APIErrorStatus int    `json:"apiErrorStatus"`
		Error          string `json:"error"`
		Attachment     struct {
			Type   string `json:"type"`
			Prompt string `json:"prompt"`
			Origin struct {
				Kind string `json:"kind"`
			} `json:"origin"`
		} `json:"attachment"`
		Message struct {
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
		return transcript.Turn{}, false
	}
	if ev.IsMeta {
		return transcript.Turn{}, false
	}
	// A prompt sent INTO a running turn (steering) is never logged as a user line —
	// claude (≥2.1.207 observed) records only an attachment/queued_command event when it
	// injects the queued text. Surface it as the user turn it is, or the mirror never
	// shows mid-run prompts. Origin is checked so a non-human queued command (none seen
	// yet, but the field exists) doesn't masquerade as the user.
	if ev.Type == "attachment" {
		a := ev.Attachment
		txt := strings.TrimSpace(a.Prompt)
		if a.Type != "queued_command" || txt == "" || (a.Origin.Kind != "" && a.Origin.Kind != "human") {
			return transcript.Turn{}, false
		}
		// AnchorID は**付けない**（docs/55）。この行は type:"attachment" で、分岐点の検査
		// （forkat.go の cutIndex）は type:"user" の行しか受け付けないため、uuid を渡すと
		// 「ここから分岐」の導線が出るのに必ず 400（エージェントの発言からは分岐できません）
		// になる。割り込みはターンの途中（tool_use と tool_result の間）に注入されるので、
		// その手前で切る分岐はそもそも成立しない — 導線を出さないのが正しい答え。
		return transcript.Turn{
			Role: "user", Parts: []transcript.Part{{Kind: "text", Text: txt}}, Text: txt,
			Idx: idx, TS: ev.Timestamp, Sidechain: ev.IsSidechain, Branch: ev.GitBranch, Cwd: ev.Cwd,
		}, true
	}
	if ev.Type != "user" && ev.Type != "assistant" {
		return transcript.Turn{}, false
	}
	var parts []transcript.Part
	var text string
	if ev.Type == "assistant" && ev.IsAPIError {
		// 失敗したターンは回答ではない。text part にすると普通の回答と同じ吹き出しで
		// 出てしまうので、専用の error part にする（errors.go）。Text は他エージェント
		// と同じ `[error] …` の平坦形 — コピー・get_session_output・チャットブリッジは
		// これを読む。
		e := apiError{msg: contentText(ev.Message.Content), kind: ev.Error, status: ev.APIErrorStatus}
		parts, text = []transcript.Part{e.part()}, e.summary()
	} else if ev.Type == "assistant" {
		parts, text = assistantParts(ev.Message.Content)
	} else if t := contentText(ev.Message.Content); t != "" {
		parts, text = []transcript.Part{{Kind: "text", Text: t}}, t
	}
	if len(parts) == 0 {
		return transcript.Turn{}, false
	}
	t := transcript.Turn{
		Role: ev.Type, Parts: parts, Text: text, Idx: idx, TS: ev.Timestamp,
		Sidechain: ev.IsSidechain, Branch: ev.GitBranch, Cwd: ev.Cwd,
		Compact: ev.IsCompactSummary, AnchorID: ev.UUID,
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
func assistantParts(raw json.RawMessage) (parts []transcript.Part, text string) {
	if len(raw) == 0 {
		return nil, ""
	}
	if raw[0] != '[' {
		if s := contentText(raw); s != "" {
			return []transcript.Part{{Kind: "text", Text: s}}, s
		}
		return nil, ""
	}
	var blocks []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		Name  string          `json:"name"`
		ID    string          `json:"id"`
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
			parts = append(parts, transcript.Part{Kind: "text", Text: b.Text})
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(b.Text)
		case "tool_use":
			// Agent (Task in older Claude releases) delegates work to a sidechain.
			// Surface the delegation itself as a compact card; the child's raw
			// isSidechain turns are hidden by the Console's normal timeline.
			if b.Name == "Agent" || b.Name == "Task" {
				var in struct {
					Description     string `json:"description"`
					Prompt          string `json:"prompt"`
					SubagentType    string `json:"subagent_type"`
					Model           string `json:"model"`
					RunInBackground bool   `json:"run_in_background"`
				}
				if json.Unmarshal(b.Input, &in) == nil && (in.Description != "" || in.Prompt != "") {
					qid := b.ID
					if in.RunInBackground {
						// Claude returns a launch acknowledgement immediately, not the final
						// report. Do not mislabel that acknowledgement as completion.
						qid = ""
					}
					parts = append(parts, transcript.Part{
						Kind: "delegation", Tool: b.Name, Info: strings.TrimSpace(in.Description),
						Prompt: strings.TrimSpace(in.Prompt), AgentType: in.SubagentType,
						Model: in.Model, Status: "requested", QID: qid,
					})
					continue
				}
			}
			// AskUserQuestion becomes an answerable question block, not a faint trace.
			if b.Name == "AskUserQuestion" {
				if qs := parseQuestions(b.Input); len(qs) > 0 {
					parts = append(parts, transcript.Part{Kind: "question", Tool: b.Name, Questions: qs, QID: b.ID})
					continue
				}
			}
			// ExitPlanMode carries the plan Markdown — a plan block, openable in a pane.
			if b.Name == "ExitPlanMode" {
				var pin struct {
					Plan string `json:"plan"`
				}
				if json.Unmarshal(b.Input, &pin) == nil && pin.Plan != "" {
					parts = append(parts, transcript.Part{Kind: "plan", Tool: b.Name, Plan: pin.Plan, QID: b.ID})
					continue
				}
			}
			// SendUserFile surfaces one or more files the agent wants shown — a file
			// panel, each entry openable in its own pane. Paths (raw here — absolute or
			// cwd-relative) are resolved to browse-root-relative by the HTTP handler,
			// which knows the browse root and the turn's cwd.
			if b.Name == "SendUserFile" {
				var fin struct {
					Files   []string `json:"files"`
					Caption string   `json:"caption"`
				}
				if json.Unmarshal(b.Input, &fin) == nil {
					var files []string
					for _, f := range fin.Files {
						if f = strings.TrimSpace(f); f != "" {
							files = append(files, f)
						}
					}
					if len(files) > 0 {
						parts = append(parts, transcript.Part{Kind: "userfile", Tool: b.Name, Files: files, Caption: strings.TrimSpace(fin.Caption)})
						continue
					}
				}
			}
			part := transcript.Part{Kind: "tool", Tool: b.Name, Info: toolInfo(b.Name, b.Input)}
			if f, es := toolEdits(b.Name, b.Input); len(es) > 0 {
				part.File, part.Edits = f, es
			}
			parts = append(parts, part)
		}
	}
	return parts, strings.TrimSpace(sb.String())
}

// InteractionAnswer is one interaction tool's resolved tool_result: the text (a picked
// label, free text, or a delegation's capped output) plus whether it was a DECLINE —
// claude's own "The user doesn't want to proceed… wants to clarify these questions" /
// "(No answer provided)" rejection boilerplate (an Escape/interrupt out of the
// AskUserQuestion modal, e.g. docs/dev/92 §6's preview free-text bug) — rather than a
// genuine answer. Declined is only ever set for kind=question: ExitPlanMode already has
// its own text-heuristic outcome classification (planDecision.ts isRejected), and a
// delegation's tool_result is its output, not an answer to decline.
type InteractionAnswer struct {
	Text     string `json:"text"`
	Declined bool   `json:"declined,omitempty"`
}

// isDeclinedAnswer recognizes claude's AskUserQuestion decline boilerplate — an Escape
// out of the modal, surfaced as an is_error tool_result whose text is the "wants to
// clarify" template with "(No answer provided)" for every question. Matched on content
// (not is_error alone) because other tools also return is_error for unrelated reasons.
func isDeclinedAnswer(text string, isErr bool) bool {
	return isErr && strings.Contains(text, "(No answer provided)")
}

// collectAnswers maps each tool_use id to the text of its tool_result — used to show
// which option an answered AskUserQuestion resolved to. Best-effort: the answer text
// is whatever text the tool_result carried (a selected label, or a free-text reply).
func collectAnswers(lines [][]byte) map[string]InteractionAnswer {
	out := map[string]InteractionAnswer{}
	for _, ln := range lines {
		var ev struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(ln, &ev) != nil || len(ev.Message.Content) == 0 || ev.Message.Content[0] != '[' {
			continue
		}
		var blocks []struct {
			Type      string          `json:"type"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal(ev.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_result" && b.ToolUseID != "" {
				if t := contentText(b.Content); t != "" {
					out[b.ToolUseID] = InteractionAnswer{Text: t, Declined: isDeclinedAnswer(t, b.IsError)}
				}
			}
		}
	}
	return out
}

// CollectInteractionAnswers maps the tool_use id of each INTERACTION tool
// (AskUserQuestion, ExitPlanMode, and foreground Agent/Task delegations) to the text of
// its tool_result, scanning the WHOLE transcript (like CollectTasks/CollectQueued).
//
// CollectTurns resolves answers only within the emitted window, which breaks for these
// tools specifically: claude writes their tool_use at ASK time and the tool_result lands
// later — in a live increment or backward page that no longer re-emits the question/plan/
// delegation turn (the Console's append-only merge then keeps it forever unanswered). The
// Console patches the answer onto the already-held turn by qid using this map. Only these
// tools are included (not every tool_result) so the payload stays small — a Bash/Read
// round-trip resolves within its own turn and never needs a late patch. Delegation outputs
// are capped like CollectTurns; question/plan answers are small and kept whole.
func CollectInteractionAnswers(lines [][]byte) map[string]InteractionAnswer {
	interactive := map[string]bool{} // qid the Console may need to patch later
	delegation := map[string]bool{}  // subset whose value is a (capped) delegation output
	question := map[string]bool{}    // subset that can be Declined (AskUserQuestion only)
	out := map[string]InteractionAnswer{}
	for _, ln := range lines {
		var ev struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(ln, &ev) != nil || len(ev.Message.Content) == 0 || ev.Message.Content[0] != '[' {
			continue
		}
		var blocks []struct {
			Type      string          `json:"type"`
			Name      string          `json:"name"`
			ID        string          `json:"id"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Input     json.RawMessage `json:"input"`
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal(ev.Message.Content, &blocks) != nil {
			continue
		}
		// tool_use precedes its tool_result in the transcript, so a single in-order pass
		// registers the interaction qid before its answer line is reached.
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				switch b.Name {
				case "AskUserQuestion", "ExitPlanMode":
					if b.ID != "" {
						interactive[b.ID] = true
						if b.Name == "AskUserQuestion" {
							question[b.ID] = true
						}
					}
				case "Agent", "Task":
					// Only foreground delegations get a final tool_result (a background one
					// returns just a launch ack); mirror assistantParts' QID gating.
					var in struct {
						RunInBackground bool `json:"run_in_background"`
					}
					if b.ID != "" && json.Unmarshal(b.Input, &in) == nil && !in.RunInBackground {
						interactive[b.ID] = true
						delegation[b.ID] = true
					}
				}
			case "tool_result":
				if b.ToolUseID != "" && interactive[b.ToolUseID] {
					if t := contentText(b.Content); t != "" {
						if delegation[b.ToolUseID] {
							t = transcript.CapOutput(t)
						}
						out[b.ToolUseID] = InteractionAnswer{
							Text:     t,
							Declined: question[b.ToolUseID] && isDeclinedAnswer(t, b.IsError),
						}
					}
				}
			}
		}
	}
	return out
}

// CollectTasks reconstructs the current ToDo list from the transcript. TaskCreate adds
// a task (single, or a batch via tasks[]) with a sequential id matching claude's
// "Task #N" numbering; TaskUpdate merges status/subject/activeForm onto an existing id.
// TaskStop (a background-agent stop, a hash id in a different space) and TaskList/TaskGet
// (reads) don't change the list and are ignored. Returns tasks in creation order.
func CollectTasks(lines [][]byte) []transcript.Task {
	order := []string{}
	m := map[string]*transcript.Task{}
	next := 1
	for _, ln := range lines {
		// Cheap prefilter so this stays a light full-scan (kept whole-transcript for an
		// accurate ToDo list even when turns are windowed — see docs/decisions/0009).
		if !bytesContains(ln, "TaskCreate") && !bytesContains(ln, "TaskUpdate") {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(ln, &ev) != nil || ev.Type != "assistant" {
			continue
		}
		if len(ev.Message.Content) == 0 || ev.Message.Content[0] != '[' {
			continue
		}
		var blocks []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(ev.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type != "tool_use" {
				continue
			}
			switch b.Name {
			case "TaskCreate":
				for _, tc := range parseTaskCreate(b.Input) {
					id := strconv.Itoa(next)
					next++
					tc.ID = id
					if tc.Status == "" {
						tc.Status = "pending"
					}
					cp := tc
					m[id] = &cp
					order = append(order, id)
				}
			case "TaskUpdate":
				applyTaskUpdate(m, b.Input)
			}
		}
	}
	out := make([]transcript.Task, 0, len(order))
	for _, id := range order {
		if it, ok := m[id]; ok {
			out = append(out, *it)
		}
	}
	return out
}

// CollectQueued reconstructs the prompts currently sitting in claude's mid-run queue
// (typed while a turn runs, not yet injected) from the transcript's queue-operation
// events: "enqueue" adds the content, any other content-carrying op (remove — the only
// one observed) drops its first match, and a content-less op clears the queue. A real
// (non-meta, non-tool_result) user prompt line also clears it: a fresh human turn can
// only start once the previous run's queue was consumed or discarded, so anything still
// tracked at that point is a stale leftover (e.g. a run killed mid-queue), not a live
// queue. Returns queued prompts in enqueue order; the caller gates on run state.
func CollectQueued(lines [][]byte) []string {
	var queue []string
	for _, ln := range lines {
		if bytesContains(ln, `"queue-operation"`) {
			var ev struct {
				Type      string `json:"type"`
				Operation string `json:"operation"`
				Content   string `json:"content"`
			}
			if json.Unmarshal(ln, &ev) != nil || ev.Type != "queue-operation" {
				continue
			}
			switch {
			case ev.Operation == "enqueue" && ev.Content != "":
				queue = append(queue, ev.Content)
			case ev.Content != "":
				for i, q := range queue {
					if q == ev.Content {
						queue = append(queue[:i], queue[i+1:]...)
						break
					}
				}
			default:
				queue = nil
			}
			continue
		}
		// Cheap prefilter for the clearing rule; parseTurn does the real classification
		// (drops meta lines and tool_result-only turns).
		if len(queue) > 0 && bytesContains(ln, `"type":"user"`) && !bytesContains(ln, `"tool_result"`) {
			if t, ok := parseTurn(ln, 0); ok && t.Role == "user" {
				queue = nil
			}
		}
	}
	return queue
}

// parseTaskCreate returns the tasks one TaskCreate call adds — normally one (the subject
// is on the input itself), or several when it carries a tasks[] batch.
func parseTaskCreate(input json.RawMessage) []transcript.Task {
	var in struct {
		Subject    string `json:"subject"`
		ActiveForm string `json:"activeForm"`
		Tasks      []struct {
			Subject    string `json:"subject"`
			ActiveForm string `json:"activeForm"`
		} `json:"tasks"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	if len(in.Tasks) > 0 {
		out := make([]transcript.Task, 0, len(in.Tasks))
		for _, t := range in.Tasks {
			if t.Subject != "" {
				out = append(out, transcript.Task{Subject: t.Subject, Active: t.ActiveForm})
			}
		}
		return out
	}
	if in.Subject == "" {
		return nil
	}
	return []transcript.Task{{Subject: in.Subject, Active: in.ActiveForm}}
}

// applyTaskUpdate merges a TaskUpdate's non-empty fields onto the referenced task.
func applyTaskUpdate(m map[string]*transcript.Task, input json.RawMessage) {
	var in struct {
		TaskID     string `json:"taskId"`
		Status     string `json:"status"`
		Subject    string `json:"subject"`
		ActiveForm string `json:"activeForm"`
	}
	if json.Unmarshal(input, &in) != nil || in.TaskID == "" {
		return
	}
	it, ok := m[in.TaskID]
	if !ok {
		return
	}
	if in.Status != "" {
		it.Status = in.Status
	}
	if in.Subject != "" {
		it.Subject = in.Subject
	}
	if in.ActiveForm != "" {
		it.Active = in.ActiveForm
	}
}

// bytesContains is strings.Contains for a []byte without allocating a string.
func bytesContains(b []byte, sub string) bool {
	return strings.Contains(string(b), sub)
}

// parseQuestions pulls the AskUserQuestion tool input into transcript.Questions. Returns
// nil when the input doesn't carry a questions array (falls back to a tool trace).
func parseQuestions(input json.RawMessage) []transcript.Question {
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

// toolEdits extracts the before/after content of an edit-family tool so the Console can
// render a diff pane. Returns the target file and one entry per edit; non-edit tools
// (or malformed input) return nil, so the tool part stays a plain trace.
func toolEdits(name string, input json.RawMessage) (string, []transcript.Edit) {
	if len(input) == 0 {
		return "", nil
	}
	switch name {
	case "Edit":
		var in struct {
			FilePath  string `json:"file_path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		}
		if json.Unmarshal(input, &in) != nil || in.FilePath == "" {
			return "", nil
		}
		return in.FilePath, []transcript.Edit{{Old: transcript.CapEdit(in.OldString), New: transcript.CapEdit(in.NewString)}}
	case "Write":
		var in struct {
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
		}
		if json.Unmarshal(input, &in) != nil || in.FilePath == "" {
			return "", nil
		}
		return in.FilePath, []transcript.Edit{{Old: "", New: transcript.CapEdit(in.Content)}}
	case "MultiEdit":
		var in struct {
			FilePath string `json:"file_path"`
			Edits    []struct {
				OldString string `json:"old_string"`
				NewString string `json:"new_string"`
			} `json:"edits"`
		}
		if json.Unmarshal(input, &in) != nil || in.FilePath == "" {
			return "", nil
		}
		var es []transcript.Edit
		for _, e := range in.Edits {
			es = append(es, transcript.Edit{Old: transcript.CapEdit(e.OldString), New: transcript.CapEdit(e.NewString)})
		}
		return in.FilePath, es
	case "NotebookEdit":
		var in struct {
			NotebookPath string `json:"notebook_path"`
			NewSource    string `json:"new_source"`
		}
		if json.Unmarshal(input, &in) != nil || in.NotebookPath == "" {
			return "", nil
		}
		return in.NotebookPath, []transcript.Edit{{Old: "", New: transcript.CapEdit(in.NewSource)}}
	}
	return "", nil
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
