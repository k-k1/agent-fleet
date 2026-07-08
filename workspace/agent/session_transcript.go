package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// Structured transcript for the Console chat view. Where /output (session_io.go)
// flattens a claude session's assistant text for the MCP drive poll, /messages keeps
// turn boundaries and adds what a readable chat needs: the user's own prompts, each
// turn's timestamp, and — as ordered "parts" — the tool_use activity interleaved
// with the assistant's text, so the Console can faintly show what claude was doing
// (Read/Bash/Edit …) between paragraphs. Both read the same jsonl (cursor = line #).

// The shared turn/part model itself lives in internal/transcript (docs/23 P1-W5),
// so the claude/codex/opencode parsers are compiler-bound to one output vocabulary.

// forkPreviewCursor is an out-of-range line cursor handed out while a fork previews its
// source transcript, so the first poll that reads the fork's own (shorter) jsonl trips
// the reset branch and swaps cleanly. Far above any real transcript length.
const forkPreviewCursor = 1 << 30

// handleSessionMessages (GET /sessions/{name}/messages?since=<cursor>) returns the
// turns appended since the cursor, plus a new cursor and the live status. claude
// only (its jsonl transcript). cursor is a line index into that transcript.
func handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	alive := tmuxx.HasSession(session.TmuxName(name))
	// heal=true: self-correct a stale waiting/working cache when the pane is back at
	// the ready prompt (rejected permission, abandoned question, killed+resumed).
	state := driveState(meta, alive, true)
	if !agentOf(meta.Kind).Caps().CanTranscript {
		httpx.WriteErr(w, http.StatusBadRequest, "unsupported_kind", "messages are available for transcript-capable sessions only")
		return
	}
	// Non-claude transcript agents (codex now, opencode later) don't have claude's
	// <sid>.jsonl; they normalize their native store into transcript.Turns via transcript(),
	// and the generic windower pages over those. claude keeps its own line-cursor path
	// below (battle-tested reset/stub/compaction handling — left untouched).
	if meta.Kind != session.KindClaude {
		handleGenericMessages(w, r, meta, alive, state)
		return
	}
	since := 0
	if v := r.URL.Query().Get("since"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			since = n
		}
	}
	sid := session.UUID(meta.Dir, name)
	lines, jpath, jmatched := transcriptRead(sid)
	// A just-forked session's OWN jsonl isn't materialized until claude finishes copying
	// the source conversation (buildSessionProgram runs --fork-session on first launch).
	// Until then, show the source's history (identical up to the fork point) instead of an
	// empty chat. Served as a reset with an out-of-range cursor, so the first poll that
	// sees the fork's own jsonl trips the `since > len(lines)` reset below and swaps to it.
	forkPreview := false
	if meta.ForkFrom != "" && !jsonlHasConversation(lines) {
		if srcLines, srcPath, srcMatched := transcriptRead(meta.ForkFrom); jsonlHasConversation(srcLines) {
			lines, jpath, jmatched = srcLines, srcPath, srcMatched
			forkPreview = true
		}
	}
	// Resolve which window [lo,hi) of lines to return and how the client treats it. cursor
	// is normally the new line count; firstLine (>=0) marks a windowed read whose oldest
	// line the client needs, to page further back. See docs/decisions/0009.
	reset := false
	cursor := len(lines)
	lo, hi := since, len(lines)
	firstLine := -1
	switch {
	case forkPreview:
		// Send the whole source preview each tick (content is stable); park the cursor
		// past any real line count so the next non-preview poll resets onto the fork.
		lo, hi = 0, len(lines)
		reset = true
		cursor = forkPreviewCursor
	case len(lines) == 0 && alive && since > 0:
		// A live session read as empty — almost always transient: a stub was briefly
		// the newest file, or the log was mid-write during a workflow's heavy
		// concurrent writes. Do NOT blank the chat: hold the client's cursor and turns
		// and send nothing this tick (it recovers on the next read).
		lo, hi = len(lines), len(lines)
		cursor = since
	case since > len(lines):
		// The live log is genuinely shorter than the cursor (compaction / a replaced
		// file). Restart from the top and tell the client to reload from scratch.
		lo, hi = 0, len(lines)
		reset = true
	default:
		// Windowed reads (P1): an initial tail window, or a backward page. With neither,
		// this is a live increment (since>0) or a legacy full load (since=0) — both are
		// [since, len), so old clients that send no window params are unchanged.
		limit := clampWindowLimit(r.URL.Query().Get("limit"))
		if bs := r.URL.Query().Get("before"); bs != "" {
			if b, err := strconv.Atoi(bs); err == nil && b > 0 {
				hi = min(b, len(lines))
				lo = max(0, hi-limit)
				firstLine = lo
			}
		} else if r.URL.Query().Get("tail") != "" && since == 0 {
			lo = max(0, len(lines)-limit)
			firstLine = lo
		}
	}
	turns := collectTurns(lines, lo, hi)
	mt := jsonlMtime(jpath) // hoisted: also feeds the title-suggestion idle check below
	if autoTitleSuggestEnabled() && meta.Title == "" && meta.SuggestedTitle == "" &&
		!meta.SuggestedTitleDismissed && titleGenReady(name) {
		// Full-transcript parse — expensive, so only reached once the cheap field/backoff
		// checks above pass. Once Title/SuggestedTitle/Dismissed settle, this line never
		// runs again for this session (see docs/decisions/0009 on why the windowed
		// `turns` above can't be reused here: it's an incremental slice after the first poll).
		maybeSuggestTitle(name, collectTurns(lines, 0, len(lines)), time.Since(mt))
	}
	resp := map[string]any{
		"name": name, "messages": turns, "cursor": cursor,
		"status": state, "alive": alive, "reset": reset,
		// Diagnostics: which jsonl we're reading, how long it is, when it last changed,
		// and how many <sid>.jsonl matched (>1 means siblings exist — the newest wins).
		// Lets us confirm from real data whether the file is found and growing, vs a
		// message merely queued in the TUI (uncommitted).
		"jsonlPath": jpath, "jsonlLines": len(lines),
		"jsonlMtime": mt.Format(time.RFC3339), "jsonlMatches": len(jmatched),
	}
	if meta.SuggestedTitle != "" && !meta.SuggestedTitleDismissed {
		resp["suggestedTitle"] = meta.SuggestedTitle
	}
	if firstLine >= 0 {
		// Windowed response: tell the client the oldest line it now holds and whether
		// there's more history above it to page in (?before=<firstLine>).
		resp["firstLine"] = firstLine
		resp["hasMore"] = firstLine > 0
	}
	surfacePendingPayloads(resp, sid, state)
	// Current mode label (Plan / Bypass / Default / …), only while alive — read from the
	// status line so it reflects a terminal-side Shift+Tab too. The jsonl mode is a stale
	// per-prompt snapshot with a different vocabulary, so it's not used as a fallback.
	if alive {
		if m := paneMode(session.KindClaude, session.TmuxName(name)); m != "" {
			resp["mode"] = m
		}
	}
	// Current ToDo list (reconstructed from Task tool calls) so the chat can show progress.
	if tasks := collectTasks(lines); len(tasks) > 0 {
		resp["tasks"] = tasks
	}
	// Surface terminal-only states (startup resume menu / auto-compaction) the chat
	// can't otherwise see, so the Console can prompt the user or show a 圧縮中 badge.
	if alive {
		if ts := sessionTerminalState(name); ts != "" {
			resp["terminalState"] = ts
		}
		// Idle by hook but a run_in_background task may still be running under the pane;
		// surface it so the chat header shows "入力待ち · BG実行中". Only computed when not
		// already working (the chip prefers 進行中 then), keeping the process scan off the
		// hot path during active turns.
		if state == "idle" || state == "" {
			resp["backgroundBusy"] = sessionBackgroundBusy(name)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// handleGenericMessages serves /messages for a non-claude transcript agent (codex,
// and later opencode). The agent hands back the whole conversation as normalized
// transcript.Turns (chronological, each carrying its absolute index); this windows them with
// the SAME semantics claude's line path uses — an initial tail window, backward
// ?before paging, and live since=<cursor> increments — so the Console chat is
// unchanged. The cursor here is a TURN count (not a jsonl line count), but the client
// treats it opaquely (reset / firstLine / hasMore drive it), so the two are compatible.
func handleGenericMessages(w http.ResponseWriter, r *http.Request, meta session.Meta, alive bool, state string) {
	td, _ := agentOf(meta.Kind).Transcript(meta)
	all, path := td.Turns, td.Path
	total := len(all)
	if autoTitleSuggestEnabled() && meta.Title == "" && meta.SuggestedTitle == "" &&
		!meta.SuggestedTitleDismissed && titleGenReady(meta.Name) {
		// `all` is already the full parse here (unlike claude's windowed path above), so
		// this is free — no extra transcript read.
		maybeSuggestTitle(meta.Name, all, time.Since(jsonlMtime(path)))
	}

	since := 0
	if v := r.URL.Query().Get("since"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			since = n
		}
	}

	reset := false
	cursor := total
	lo, hi := since, total
	firstLine := -1
	switch {
	case total == 0 && alive && since > 0:
		// Live but momentarily read empty (store mid-write / not yet materialized): hold
		// the client's cursor and turns rather than blanking the chat.
		lo, hi = 0, 0
		cursor = since
	case since > total:
		// The transcript shrank below the cursor (a resumed/replaced session): restart
		// from the top and tell the client to reload.
		lo, hi = 0, total
		reset = true
	default:
		limit := clampWindowLimit(r.URL.Query().Get("limit"))
		if bs := r.URL.Query().Get("before"); bs != "" {
			if b, err := strconv.Atoi(bs); err == nil && b > 0 {
				hi = min(b, total)
				lo = max(0, hi-limit)
				firstLine = lo
			}
		} else if r.URL.Query().Get("tail") != "" && since == 0 {
			lo = max(0, total-limit)
			firstLine = lo
		}
	}
	if lo < 0 {
		lo = 0
	}
	if hi > total {
		hi = total
	}
	turns := []transcript.Turn{}
	if lo < hi {
		turns = capTurnsNewest(all[lo:hi])
	}
	resp := map[string]any{
		"name": meta.Name, "messages": turns, "cursor": cursor,
		"status": state, "alive": alive, "reset": reset,
		"jsonlPath": path, "jsonlLines": total,
		"jsonlMtime": jsonlMtime(path).Format(time.RFC3339), "jsonlMatches": 1,
	}
	if meta.SuggestedTitle != "" && !meta.SuggestedTitleDismissed {
		resp["suggestedTitle"] = meta.SuggestedTitle
	}
	if firstLine >= 0 {
		resp["firstLine"] = firstLine
		resp["hasMore"] = firstLine > 0
	}
	// Current ToDo list (opencode todo table / codex update_plan), so the chat shows the
	// same progress checklist claude gets. Whole-transcript (not windowed), like claude's.
	if len(td.Tasks) > 0 {
		resp["tasks"] = td.Tasks
	}
	// A currently-awaiting agent question (codex request_user_input / opencode question
	// tool), surfaced interactively like claude's AskUserQuestion — only while the session
	// is live (a stopped session can't be answered).
	if alive && len(td.Pending) > 0 {
		resp["pendingQuestions"] = td.Pending
	}
	// Current mode (plan / normal) so the Console shows the plan indicator and toggle.
	// Read it ONLY from the live terminal — a stopped session isn't "in plan mode", and
	// the rollout's per-turn mode is a stale snapshot (the last turn's), which made a
	// stopped codex show 計画モードON. When not alive, or the composer isn't drawn yet,
	// report no mode (the Console shows the default, normal).
	if alive {
		if pm := paneMode(meta.Kind, session.TmuxName(meta.Name)); pm != "" {
			resp["mode"] = pm
		}
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// capTurnsNewest bounds a returned window to ~1 MiB of text, keeping the NEWEST turns
// (walk from the end, keep until the budget is spent). Mirrors collectTurns' cap so a
// huge single response can't bloat the payload.
func capTurnsNewest(turns []transcript.Turn) []transcript.Turn {
	budget := 0
	start := 0
	for i := len(turns) - 1; i >= 0; i-- {
		if budget += len(turns[i].Text); budget > 1<<20 {
			start = i
			break
		}
	}
	return turns[start:]
}

// collectTurns builds the displayable turns from lines[lo:hi] (a window into the
// transcript — the whole file, a tail window, a backward page, or a live increment; see
// docs/decisions/0009). A transcript AskUserQuestion/ExitPlanMode is always already
// answered (claude writes the tool_use only after the answer), so it resolves each one's
// chosen answer from its tool_result for display (the currently-pending one is surfaced
// separately). Each turn keeps its ABSOLUTE line index as idx (stable across windows, so
// React keys and ordering hold when pages are prepended). Capped at 1 MiB of newest text.
func collectTurns(lines [][]byte, lo, hi int) []transcript.Turn {
	if lo < 0 {
		lo = 0
	}
	if hi > len(lines) {
		hi = len(lines)
	}
	// Answers are resolved within the window: a question and its tool_result are adjacent
	// (claude writes the tool_use only after the answer), so window-local is enough.
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
				t.Parts[pi].Answer = answers[t.Parts[pi].QID]
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

// clampWindowLimit parses the window line-limit query param, with sane defaults/bounds.
func clampWindowLimit(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 400
	}
	if n < 50 {
		return 50
	}
	if n > 4000 {
		return 4000
	}
	return n
}

// surfacePendingPayloads adds any currently-pending AskUserQuestion / ExitPlanMode /
// permission to resp. These aren't in the transcript yet (claude writes the tool_use
// only after it's resolved), so they come from the status hook's captured payloads.
func surfacePendingPayloads(resp map[string]any, sid, state string) {
	// A captured question/plan payload takes precedence over the "permission" state.
	// AskUserQuestion / ExitPlanMode fire their OWN permission_prompt Notification
	// (state→"permission") between the tool's PreToolUse and PostToolUse, but the
	// terminal is showing that tool's selection / approval UI — NOT a generic tool
	// permission dialog. Surfacing pendingPermission here would make the Console show a
	// 許可/拒否 prompt whose keystrokes (Enter / Down Down Enter) mis-answer the question
	// menu underneath, skipping it. So whenever a question/plan is captured, surface it
	// and suppress the permission — the Console drives it with the correct keys. The
	// payload is cleared by its own lifecycle (PostToolUse→working, idle) once resolved.
	pq, hasQ := status.ReadPendingQuestion(sid)
	pp, hasP := status.ReadPendingPlan(sid)
	if state == "permission" && !hasQ && !hasP {
		// A genuine tool-permission prompt (Edit/Bash) with no question/plan behind it:
		// surface only it, because answer keystrokes must reach the permission dialog.
		if pm, ok := status.ReadPendingPermission(sid); ok {
			resp["pendingPermission"] = pm
		}
		return
	}
	if hasQ {
		resp["pendingQuestions"] = pq
		// The prose the assistant streamed just before the question (accumulated by the
		// MessageDisplay hook). Absent if MessageDisplay hasn't populated it by question
		// time — the pending card then renders without preceding context, as before.
		if txt, ok := status.ReadPendingText(sid); ok {
			if txt = strings.TrimSpace(txt); txt != "" {
				resp["pendingText"] = txt
			}
		}
	}
	if hasP {
		resp["pendingPlan"] = pp
	}
}

// parseTurn builds a transcript.Turn from a transcript line. ok is false for lines that
// carry nothing displayable: tool_result-only user turns, summaries, the Remote
// Control bridge-session line, and meta entries (isMeta).
func parseTurn(line []byte, idx int) (transcript.Turn, bool) {
	var ev struct {
		Type             string `json:"type"`
		Timestamp        string `json:"timestamp"`
		IsMeta           bool   `json:"isMeta"`
		IsSidechain      bool   `json:"isSidechain"`
		IsCompactSummary bool   `json:"isCompactSummary"`
		GitBranch        string `json:"gitBranch"`
		Cwd              string `json:"cwd"`
		Message          struct {
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
	if ev.IsMeta || (ev.Type != "user" && ev.Type != "assistant") {
		return transcript.Turn{}, false
	}
	var parts []transcript.Part
	var text string
	if ev.Type == "assistant" {
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
		Compact: ev.IsCompactSummary,
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
			part := transcript.Part{Kind: "tool", Tool: b.Name, Info: toolInfo(b.Name, b.Input)}
			if f, es := toolEdits(b.Name, b.Input); len(es) > 0 {
				part.File, part.Edits = f, es
			}
			parts = append(parts, part)
		}
	}
	return parts, strings.TrimSpace(sb.String())
}

// collectAnswers maps each tool_use id to the text of its tool_result — used to show
// which option an answered AskUserQuestion resolved to. Best-effort: the answer text
// is whatever text the tool_result carried (a selected label, or a free-text reply).
func collectAnswers(lines [][]byte) map[string]string {
	out := map[string]string{}
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
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal(ev.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_result" && b.ToolUseID != "" {
				if t := contentText(b.Content); t != "" {
					out[b.ToolUseID] = t
				}
			}
		}
	}
	return out
}

// collectTasks reconstructs the current ToDo list from the transcript. TaskCreate adds
// a task (single, or a batch via tasks[]) with a sequential id matching claude's
// "Task #N" numbering; TaskUpdate merges status/subject/activeForm onto an existing id.
// TaskStop (a background-agent stop, a hash id in a different space) and TaskList/TaskGet
// (reads) don't change the list and are ignored. Returns tasks in creation order.
func collectTasks(lines [][]byte) []transcript.Task {
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

// capEdit（editCap 上限つき切り詰め）は internal/transcript の transcript.CapEdit
// へ移設（docs/23 残① Wave D — opencode 縦割りと共有のため）。

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
