package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// Structured transcript for the Console chat view. Where /output (session_io.go)
// flattens a claude session's assistant text for the MCP drive poll, /messages keeps
// turn boundaries and adds what a readable chat needs: the user's own prompts, each
// turn's timestamp, and — as ordered "parts" — the tool_use activity interleaved
// with the assistant's text, so the Console can faintly show what claude was doing
// (Read/Bash/Edit …) between paragraphs. Both read the same jsonl (cursor = line #).
//
// The shared turn/part model itself lives in internal/transcript (docs/23 P1-W5),
// so the claude/codex/opencode parsers are compiler-bound to one output vocabulary.
// claude の jsonl 解析（CollectTurns/CollectTasks ほか）は internal/agents/claude
// へ移設（docs/23 残① Wave F）; ここにはウィンドウ処理・ページング・internal/status
// の pending 合成を行う HTTP ハンドラだけが残る。

// forkPreviewCursor is an out-of-range line cursor handed out while a fork previews its
// source transcript, so the first poll that reads the fork's own (shorter) jsonl trips
// the reset branch and swaps cleanly. Far above any real transcript length.
const forkPreviewCursor = 1 << 30

// jsonlMtime は internal/agents/claude の同名ヘルパの複製（極小 stat のため共有せず
// 重複を許容 — generic /messages はエージェント種別を問わず使う）。
func jsonlMtime(p string) time.Time {
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

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
	alive := sessionAlive(meta)
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
	lines, jpath, jmatched := claude.TranscriptRead(sid)
	// A just-forked session's OWN jsonl isn't materialized until claude finishes copying
	// the source conversation (buildProgram runs --fork-session on first launch).
	// Until then, show the source's history (identical up to the fork point) instead of an
	// empty chat. Served as a reset with an out-of-range cursor, so the first poll that
	// sees the fork's own jsonl trips the `since > len(lines)` reset below and swaps to it.
	forkPreview := false
	if meta.ForkFrom != "" && !claude.HasConversation(lines) {
		if srcLines, srcPath, srcMatched := claude.TranscriptRead(meta.ForkFrom); claude.HasConversation(srcLines) {
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
	case forkPreview && since == forkPreviewCursor:
		// The client's cursor is already parked at the sentinel, i.e. it holds the
		// preview from a prior tick — don't re-send the whole source every poll
		// (status/pending below still refresh). The first poll that sees the fork's
		// own jsonl leaves preview mode and trips the reset case below.
		lo, hi = 0, 0
		cursor = forkPreviewCursor
	case forkPreview:
		// First preview send: the whole source history (content is treated as stable);
		// park the cursor past any real line count so the next non-preview poll resets
		// onto the fork.
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
		// file). Tell the client to reload from scratch — but hand it a TAIL window,
		// not the whole file (a compacted transcript can still be ~1MiB); firstLine
		// lets it page older history in on scroll, same as the initial open.
		hi = len(lines)
		lo = max(0, hi-clampWindowLimit(r.URL.Query().Get("limit")))
		firstLine = lo
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
	turns := claude.CollectTurns(lines, lo, hi)
	resolveUserFiles(turns)       // SendUserFile paths → browse-root-relative, per each turn's cwd
	tagInjectedTurns(name, turns) // Source=operator/discord/… on injected user turns (docs/30 ②, docs/37 P2a)
	mt := jsonlMtime(jpath)       // hoisted: also feeds the title-suggestion idle check below
	if autoTitleSuggestEnabled() && meta.Title == "" && meta.SuggestedTitle == "" &&
		!meta.SuggestedTitleDismissed && titleGenReady(name) {
		// Full-transcript parse — expensive, so only reached once the cheap field/backoff
		// checks above pass. Once Title/SuggestedTitle/Dismissed settle, this line never
		// runs again for this session (see docs/decisions/0009 on why the windowed
		// `turns` above can't be reused here: it's an incremental slice after the first poll).
		maybeSuggestTitle(name, claude.CollectTurns(lines, 0, len(lines)), time.Since(mt))
	}
	// Whole-transcript answers for AskUserQuestion/ExitPlanMode/Agent, keyed by tool_use
	// id. claude writes an interaction tool_use at ASK time; its answer can land in a later
	// poll whose window no longer re-emits that turn, so the windowed `turns` above may
	// carry it unanswered. The Console patches the answer onto the already-held turn by qid.
	answers := claude.CollectInteractionAnswers(lines)
	// Pending question/plan/permission — built aside (not straight into resp) because the
	// de-duplication below needs to know what was surfaced, and may withdraw it.
	pending := map[string]any{}
	surfacePendingPayloads(pending, sid, state)
	// 畳まれたときに画面に出ていたもの（docs/75）。保留が無いときだけ載る。
	surfaceCarried(pending, sid)
	// The pending question/plan above is ALSO in the transcript already (ask-time tool_use),
	// so the same card would render twice. Drop the duplicate and hold the cursor short of
	// its line, so it comes back — decided — once it resolves.
	turns, hold := hidePendingInteraction(turns, pending, answers)
	// forkPreview の cursor は行番号ではなく、次の poll で fork 自身の jsonl へ乗り換える
	// ための番兵。行番号に落とすと乗り換えそのものが壊れるので、そこだけは触らない。
	if !forkPreview && hold >= 0 && hold < cursor {
		cursor = hold
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
	if len(answers) > 0 {
		resp["answers"] = answers
	}
	for k, v := range pending { // pendingQuestions / pendingText / pendingPlan / pendingPermission
		resp[k] = v
	}
	// Current mode label (Plan / Bypass / Default / …), only while alive — read from the
	// status line so it reflects a terminal-side Shift+Tab too. The jsonl mode is a stale
	// per-prompt snapshot with a different vocabulary, so it's not used as a fallback.
	if alive {
		if m := paneMode(session.KindClaude, session.TmuxName(name)); m != "" {
			resp["mode"] = m
		}
	}
	// Current ToDo list (reconstructed from Task tool calls) so the chat can show progress.
	if tasks := claude.CollectTasks(lines); len(tasks) > 0 {
		resp["tasks"] = tasks
	}
	// Files this session edited (docs/68). Whole-transcript like the ToDo list above —
	// the turns sent alongside are a window, so anything derived from them would
	// undercount. jsonl lines are immutable once written, so all of them are foldable.
	var head []byte
	if len(lines) > 0 {
		head = lines[0]
	}
	if files := sessionFileTouches(name, jpath, fileAggHead(head), len(lines), len(lines),
		func(from, to int) []transcript.FileEdit { return claude.CollectFileEdits(lines[:to], from) },
	); len(files) > 0 {
		resp["files"] = files
	}
	// Prompts queued into the running turn (typed mid-run, not yet injected) so the
	// mirror can badge them キュー済み like the terminal does. The queue only exists
	// while a turn runs, so gate on working — this also hides stale leftovers from a
	// run that died with items still queued.
	if alive && state == "working" {
		if q := claude.CollectQueued(lines); len(q) > 0 {
			resp["queuedPrompts"] = q
		}
	}
	// Surface terminal-only states (startup resume menu / auto-compaction) the chat
	// can't otherwise see, so the Console can prompt the user or show a 圧縮中 badge.
	if alive {
		if ts, prog := sessionTerminalState(name); ts != "" {
			resp["terminalState"] = ts
			if prog != nil {
				resp["compactProgress"] = prog
			}
		}
		// Idle by hook but background work may still be running; surface it so the chat
		// header shows "入力待ち · BG実行中". BackgroundBusy catches run_in_background worker
		// processes under the pane; SubagentBusy catches in-process background subagents /
		// Workflow agents via transcript freshness; BackgroundShellBusy catches a Monitor /
		// sleep- or I/O-bound background shell (S state) that slips past both. Only computed
		// when not already working (the chip prefers 進行中 then), keeping the scans off the
		// hot path during turns.
		if state == "idle" || state == "" {
			resp["backgroundBusy"] = claude.BackgroundBusy(name) || claude.SubagentBusy(sid) || claude.BackgroundShellBusy(name)
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
		// The transcript shrank below the cursor (a resumed/replaced session): tell the
		// client to reload — with a tail window (not the whole conversation), like the
		// claude path; firstLine lets it page older history in on scroll.
		hi = total
		lo = max(0, hi-clampWindowLimit(r.URL.Query().Get("limit")))
		firstLine = lo
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
	// OpenCode (and other store-backed agents) can add parts to an existing
	// assistant message: reasoning/tool parts arrive before the final text, but
	// the message count — our cursor — does not change. Re-send the mutable tail
	// when the client is already caught up so it can replace its prior version.
	// Without this, a pane that stays open keeps the reasoning trace forever and
	// only sees the final answer after reopening (which starts again at since=0).
	if update := genericMutableTail(all, since); update != nil {
		turns = update
	}
	// userfile parts exist here too (codex imagegen's generated file) — map their
	// paths browse-root-relative so the Console's 共有ファイル panel can open them.
	resolveUserFiles(turns)
	tagInjectedTurns(meta.Name, turns) // Source=operator/discord/… on injected user turns (docs/30 ②, docs/37 P2a)
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
	// Files this session edited (docs/68), from the same whole parse. The LAST turn is
	// held back from the fold: these agents keep appending parts to an already-counted
	// message (genericMutableTail above), so folding it into the cache would double-count
	// its edits — it is re-folded into a copy on every poll instead.
	if total > 0 {
		head := all[0].TS + "|" + all[0].AnchorID + "|" + strconv.Itoa(all[0].Idx)
		if files := sessionFileTouches(meta.Name, path, head, total, total-1,
			func(from, to int) []transcript.FileEdit {
				var out []transcript.FileEdit
				for i := from; i < to; i++ {
					out = append(out, transcript.FileEditsInTurn(all[i])...)
				}
				return out
			},
		); len(files) > 0 {
			resp["files"] = files
		}
	}
	// A currently-awaiting agent question (codex request_user_input / opencode question
	// tool), surfaced interactively like claude's AskUserQuestion — only while the session
	// is live (a stopped session can't be answered).
	if alive && len(td.Pending) > 0 {
		resp["pendingQuestions"] = td.Pending
	}
	// Prompts queued into the running turn (typed mid-run, not yet injected as a user
	// message) so the mirror can badge them キュー済み — same gate as claude's path: the
	// queue only means anything while a turn runs, and this hides stale leftovers.
	if alive && state == "working" && len(td.Queued) > 0 {
		resp["queuedPrompts"] = td.Queued
	}
	// Compaction in flight (opencode session.time_compacting): reuse the chat's claude
	// 圧縮中 block (spinner-only — opencode reports no progress percentage).
	if alive && td.Compacting {
		resp["terminalState"] = "compacting"
	}
	// Session-level context fill for agents with no per-turn token usage in their
	// transcript (agy): the ContextBar's fallback source. Cached agent-side; the
	// call is non-blocking (a stale reading triggers a background refresh).
	if cr, ok := agentOf(meta.Kind).(agents.ContextReporter); ok {
		if c := cr.ContextFill(meta); c != nil {
			resp["context"] = c
		}
	}
	// codex's startup update menu is terminal-only (no transcript event) and eats
	// keystrokes — and its "Update now" exits the process, killing the tmux session.
	// Surface it so the mirror blocks the composer and offers the skip choices,
	// the same treatment as claude's startup resume menu.
	if alive && meta.Kind == session.KindCodex {
		if ts := codexTerminalState(meta.Name); ts != "" {
			resp["terminalState"] = ts
		}
	}
	// Current mode (plan / normal) so the Console shows the plan indicator and toggle.
	// Read it ONLY from the live terminal — a stopped session isn't "in plan mode", and
	// the rollout's per-turn mode is a stale snapshot (the last turn's), which made a
	// stopped codex show 計画モードON. When not alive, or the composer isn't drawn yet,
	// report no mode (the Console shows the default, normal).
	// managed（docs/27 §10.2-5）: pane が無いので TranscriptData.Mode（driver 設定 ＝
	// 次 turn が使う値、無ければ db の最後の turn の agent）からの射影で供給する。
	if alive {
		if meta.DriverKind() == session.DriverManaged {
			switch td.Mode {
			case "plan":
				resp["mode"] = "Plan"
			case "normal":
				if meta.Kind == session.KindOpencode {
					resp["mode"] = "Build" // opencode の非 plan agent 名（Console の defaultModeLabel と同語彙）
				} else {
					resp["mode"] = "Default"
				}
			}
		} else if pm := paneMode(meta.Kind, session.TmuxName(meta.Name)); pm != "" {
			// Codex 0.145.0 no longer prints "Plan mode" in the footer. paneMode still
			// returns Default as the composer-readiness signal; mirror-driven /plan and
			// BTab changes are persisted in meta by rememberCodexTUIMode.
			if meta.Kind == session.KindCodex && pm == "Default" && meta.Mode == "plan" {
				resp["mode"] = "Plan"
			} else {
				resp["mode"] = pm
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// genericMutableTail returns the last assistant turn when a client has consumed
// every turn. The turn's stable index identifies it as a replacement, not an append.
func genericMutableTail(all []transcript.Turn, since int) []transcript.Turn {
	if since != len(all) || len(all) == 0 || all[len(all)-1].Role != "assistant" {
		return nil
	}
	return []transcript.Turn{all[len(all)-1]}
}

// capTurnsNewest bounds a returned window to ~1 MiB of text, keeping the NEWEST turns
// (walk from the end, keep until the budget is spent). Mirrors CollectTurns' cap so a
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

// resolveUserFiles rewrites each SendUserFile part's paths (raw from the transcript —
// absolute or cwd-relative) into browse-root-relative paths the Console can open via
// api/fs/file. Resolution uses the part's own turn cwd (recorded on the jsonl line).
// A path that lands outside the browse root (e.g. a /tmp scratchpad) is left untouched:
// it still shows in the panel, but opening it will honestly report "読み込めません".
func resolveUserFiles(turns []transcript.Turn) {
	root := browseRoot()
	for ti := range turns {
		cwd := turns[ti].Cwd
		for pi := range turns[ti].Parts {
			p := &turns[ti].Parts[pi]
			if p.Kind != "userfile" {
				continue
			}
			for fi, f := range p.Files {
				p.Files[fi] = toBrowseRel(f, cwd, root)
			}
		}
	}
}

// toBrowseRel maps a SendUserFile path to a browse-root-relative path. A relative path
// is first joined onto the turn's cwd; an absolute path within root becomes root-relative
// (forward-slashed, the form the Console's fs API and FileView expect). Anything that
// can't be placed under root is returned unchanged.
func toBrowseRel(p, cwd, root string) string {
	if !filepath.IsAbs(p) {
		if cwd == "" {
			return p // no cwd to anchor a relative path — leave as-is
		}
		p = filepath.Join(cwd, p)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p // outside the browse root — not openable, but still listed
	}
	return filepath.ToSlash(rel)
}

// surfacePendingPayloads adds any currently-pending AskUserQuestion / ExitPlanMode /
// permission to resp, from the status hook's captured payloads. The transcript alone
// can't drive these: it carries the tool_use (written at ASK time — see CollectTurns)
// but nothing that says the modal is still up, and a permission prompt never reaches
// the jsonl at all. The tool_use it DOES carry is the duplicate hidePendingInteraction
// removes.
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
	//
	// Same precedence as status.EffectiveModal, which applies it on the WRITE paths
	// (/plan-respond, the {prompt} guard) and on the state chips (WireLive / driveState).
	// Display and decision must agree on which modal is up — when only this side applied
	// it, the Console showed a plan card whose 送信 the Agent refused as「許可の判断待ち」,
	// and an AUQ カードの隣で state チップだけが「許可待ち」を名乗った。
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

// surfaceCarried adds the CARRIED interaction (docs/75 §75.6) — what was on screen when
// the session was folded away, which the resumed CLI no longer knows about.
//
// pending-* とは**別のキー**で出す。同じキーに相乗りさせると、Console 側は「今モーダルが
// 出ている（キー列で答える）」と「もう出ていない（文章で答える）」を alive の有無で
// 見分けることになり、その見分けは一度も実装されたことがない（保留カードは今も alive を
// 見ていない）。別キーなら、キーを撃つコードが持ち越しに到達し得ない。
//
// 保留が生きているあいだは持ち越しを出さない: 昇格は畳むときにしか起きないので普通は
// 同時に存在しないが、halt 直後に再開して新しい質問が出た、のような順序では両方揃いうる。
// そのときに正しいのは常に「今出ているモーダル」。
func surfaceCarried(resp map[string]any, sid string) {
	if _, hasQ := resp["pendingQuestions"]; hasQ {
		return
	}
	if _, hasP := resp["pendingPlan"]; hasP {
		return
	}
	if _, hasPerm := resp["pendingPermission"]; hasPerm {
		return
	}
	c, ok := status.ReadCarried(sid)
	if !ok {
		return
	}
	out := map[string]any{"kind": c.Kind, "capturedAt": c.CapturedAt, "reason": c.Reason}
	switch c.Kind {
	case "question":
		out["questions"] = c.Questions
	case "plan":
		out["plan"] = c.Plan
	case "permission":
		out["permission"] = c.Permission
	}
	if c.Text != "" {
		out["text"] = c.Text
	}
	resp["carried"] = out
}

// hidePendingInteraction removes the transcript part that DUPLICATES a still-pending
// AskUserQuestion / ExitPlanMode payload, and reports the line index the cursor must be
// held at (-1 = nothing hidden).
//
// WHY: claude writes an interaction's tool_use at ASK time, so while the modal is up the
// very same question/plan exists twice in one response — as a part of the newest turn,
// and as the pending payload surfacePendingPayloads surfaces for the actionable card the
// Console renders at the bottom. The mirror drew both (実測 2026-08-19), and the inline
// copy additionally badged 決定済み, because "it reached the transcript" used to mean
// "it was already answered". One decision must be shown once, in one place: the pending
// card, which is the only one that can be acted on.
//
// WHY hold the cursor instead of just dropping the part: the client only ever asks for
// lines AFTER its cursor, so a line dropped from the response it advances past is gone
// from that client's history for good (until a full reload). Holding the cursor short of
// that line leaves it unconsumed — the poll after the decision delivers it again, this
// time with its tool_result, and the Console renders it as the decided card. Turns are
// merged by idx (mergeTurns), so re-delivery replaces rather than duplicates.
//
// Matching is by CONTENT: the hook payload carries no tool_use id (it is captured in
// PreToolUse, before one is written). The LAST match wins — a rejected plan is often
// refined and re-presented with identical Markdown, and only the newest presentation can
// be the pending one. A match that is already ANSWERED means the payload is stale (a hook
// missed its clear), and then it's the ghost card that goes, never the history.
func hidePendingInteraction(turns []transcript.Turn, pending map[string]any, answers map[string]claude.InteractionAnswer) ([]transcript.Turn, int) {
	hold := -1
	if plan, _ := pending["pendingPlan"].(string); strings.TrimSpace(plan) != "" {
		turns, hold, _ = hideDuplicatePart(turns, hold, answers,
			func(p transcript.Part) bool {
				return p.Kind == "plan" && strings.TrimSpace(p.Plan) == strings.TrimSpace(plan)
			},
			func() { delete(pending, "pendingPlan") })
	}
	if raw, _ := pending["pendingQuestions"].(json.RawMessage); len(raw) > 0 {
		// The payload is the tool_input.questions array — the exact JSON parseQuestions
		// reads into the part, so the parsed forms compare equal when they are the same ask.
		var want []transcript.Question
		if json.Unmarshal(raw, &want) == nil && len(want) > 0 {
			var hidden bool
			turns, hold, hidden = hideDuplicatePart(turns, hold, answers,
				func(p transcript.Part) bool {
					return p.Kind == "question" && reflect.DeepEqual(p.Questions, want)
				},
				func() { delete(pending, "pendingQuestions"); delete(pending, "pendingText") })
			if hidden {
				// pendingText is the prose the assistant streamed just before the question.
				// It exists because that prose was believed to reach the transcript only
				// after the answer — but the question's tool_use is here, and claude writes
				// the prose message BEFORE it (実測: 別行の assistant メッセージが先に出る),
				// so the transcript already shows it. Sending it again would print the same
				// paragraphs twice, inline and inside the card.
				delete(pending, "pendingText")
			}
		}
	}
	return turns, hold
}

// hideDuplicatePart is one pass of hidePendingInteraction: find the last part `match`
// accepts, and either strip it (still unanswered — the pending card owns it) or, when it
// already has an answer, withdraw the stale pending payload via dropPending. Returns the
// turns, the lowest line index the cursor must not pass yet, and whether a part was
// actually stripped.
func hideDuplicatePart(turns []transcript.Turn, hold int, answers map[string]claude.InteractionAnswer, match func(transcript.Part) bool, dropPending func()) ([]transcript.Turn, int, bool) {
	ti, pi := -1, -1
	for i := range turns {
		for j, p := range turns[i].Parts {
			if match(p) {
				ti, pi = i, j
			}
		}
	}
	if ti < 0 {
		return turns, hold, false // outside this window (a backward page) — nothing to hide here
	}
	if qid := turns[ti].Parts[pi].QID; qid != "" {
		if _, answered := answers[qid]; answered {
			dropPending()
			return turns, hold, false
		}
	}
	parts := make([]transcript.Part, 0, len(turns[ti].Parts)-1)
	parts = append(parts, turns[ti].Parts[:pi]...)
	parts = append(parts, turns[ti].Parts[pi+1:]...)
	idx := turns[ti].Idx
	if len(parts) == 0 {
		// The tool_use was the whole message (the common shape): drop the turn, or the
		// Console renders an empty bubble where the card used to be.
		turns = append(turns[:ti:ti], turns[ti+1:]...)
	} else {
		t := turns[ti]
		t.Parts = parts
		turns = append(turns[:ti:ti], append([]transcript.Turn{t}, turns[ti+1:]...)...)
	}
	if hold < 0 || idx < hold {
		hold = idx
	}
	return turns, hold, true
}
