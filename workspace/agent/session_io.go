package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// opencodeStatusAgentRe pulls the current agent from opencode's composer status line
// ("Plan auto · <model> …" / "Build auto · …"), the ground-truth mode the TUI shows.
var opencodeStatusAgentRe = regexp.MustCompile(`([A-Za-z][\w-]*) +auto +·`)

// codexFooterEffortRe pulls the reasoning effort from codex's composer footer
// "<model> <effort> · <cwd>" (the word right before " · " then the cwd path).
var codexFooterEffortRe = regexp.MustCompile(`([a-z]+) +· +[~/]`)

// pane キャプチャ/解決の tmux プリミティブ（capturePane / sessionPaneID）は
// internal/tmuxx へ移設（docs/23 残① Wave A）。

// paneTail returns the last n non-empty lines of s (the TUI's status/composer footer
// region), so mode detection matches the STATUS LINE — not conversation text that merely
// mentions "plan mode" (which false-positived claude's indicator).
func paneTail(s string, n int) string {
	lines := strings.Split(s, "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append(out, lines[i])
		}
	}
	return strings.Join(out, "\n")
}

// paneMode reads the session's CURRENT permission/collaboration mode straight from the
// terminal (what the TUI displays), so it reflects toggles made in the terminal too — not
// just via the chat. Returns the DISPLAY label the Console shows in the composer's mode
// chip: "Plan" (plan mode, the special one) or the agent's non-plan mode name — claude
// "Bypass"/"Accept Edits", codex "Default", opencode "Build" (or another agent's name).
// "" = unknown (composer not drawn / stopped) → the Console shows no mode. Matching is
// confined to the pane's tail (status line region) so conversation content can't spoof it.
func paneMode(kind, tn string) string {
	s := tmuxx.CapturePane(tn)
	if s == "" {
		return ""
	}
	switch kind {
	case session.KindClaude:
		// claude's status line (last line) shows the active mode: "⏸ plan mode on
		// (shift+tab to cycle)" vs "⏵⏵ bypass permissions on …" / "accept edits on …".
		t := paneTail(s, 3)
		if strings.Contains(t, "plan mode on") {
			return "Plan"
		}
		if strings.Contains(t, "accept edits on") {
			return "Accept Edits"
		}
		if strings.Contains(t, "bypass permissions on") {
			return "Bypass"
		}
		if strings.Contains(t, "shift+tab to cycle") {
			return "Default"
		}
	case session.KindOpencode:
		// The composer status line ("<Agent> auto · …") sits a few lines above the very
		// bottom (above the border + token/commands footer). The agent name IS the mode.
		if m := opencodeStatusAgentRe.FindStringSubmatch(paneTail(s, 8)); m != nil {
			return titleFirst(m[1]) // "Plan" / "Build" / …
		}
	case session.KindCodex:
		// codex's composer footer is "<model> <effort> · <cwd>  Plan mode [(shift+tab to
		// cycle)]" — "Plan mode" appears ONLY in plan mode (Default shows no label). The
		// "(shift+tab to cycle)" suffix is truncated on a narrow pane, so DON'T require it.
		// Check the FOOTER line itself (identified by the effort regex) so the history line
		// "… for Plan mode." can't spoof the detection. No footer line → composer not drawn.
		for _, line := range strings.Split(paneTail(s, 3), "\n") {
			if codexFooterEffortRe.MatchString(line) {
				if strings.Contains(line, "Plan mode") {
					return "Plan"
				}
				return "Default"
			}
		}
	}
	return ""
}

// titleFirst upper-cases the first rune ("plan" → "Plan", "build" → "Build").
func titleFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Programmatic session I/O for the MCP drive tools (docs/decisions/0006, P3-6 E).
// A user's own Claude (via the CP /mcp endpoint) drives the claude sessions in
// their Workspace: send a prompt, poll status, read the reply. Built on the
// existing primitives — tmux send-keys, the session-status hooks (working|idle|
// question), and the claude jsonl transcript — so this is the only new Agent code.

// handleSessionInput (POST /sessions/{name}/input {prompt}) types a prompt into a
// session and submits it. Returns immediately; the caller polls /status then
// /output for the reply.
func handleSessionInput(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	// Body is one of:
	//   {prompt}      type text + Enter (the composer / a single-select answer).
	//   {keys:[...]}  send named keys — drive the AskUserQuestion modal by navigation
	//                 (Down/Space/Enter/Right).
	//   {seq:[...]}   an ORDERED mix of named keys and literal text ({k}|{t}) — answer a
	//                 question via its "Type something" free-text row: move down to it,
	//                 type, Enter. This is what plain {keys} can't do.
	var body struct {
		Prompt string   `json:"prompt"`
		Keys   []string `json:"keys"`
		Seq    []struct {
			K string `json:"k"` // a whitelisted named key
			T string `json:"t"` // literal text (send-keys -l)
		} `json:"seq"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	if len(body.Keys) == 0 && len(body.Seq) == 0 && strings.TrimSpace(body.Prompt) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "empty_prompt", "prompt, keys or seq is required")
		return
	}
	tn := session.TmuxName(name)
	if !tmuxx.HasSession(tn) {
		httpx.WriteErr(w, http.StatusConflict, "not_running", "session is not running; start it first")
		return
	}
	// Resolve the active pane id. send-keys takes a target-PANE, where tmux's "="
	// exact-session prefix is read literally ("can't find pane: =claude_x"); a
	// globally-unique pane id (%N) is unambiguous and avoids tmux's prefix matching.
	pane := tmuxx.SessionPaneID(tn)
	if pane == "" {
		httpx.WriteErr(w, http.StatusInternalServerError, "no_pane", "could not resolve session pane")
		return
	}
	if len(body.Keys) > 0 {
		keys := body.Keys
		// opencode quirk: a plain Escape (the chat 停止 button) interrupts from the main
		// view, but while opencode's SUBAGENT DETAIL view is up Escape only navigates it —
		// you must step out to the parent (Up) first. Detect that view by its nav footer
		// and prepend Up so the stop button works regardless of which view is showing.
		if len(keys) == 1 && keys[0] == "Escape" && opencodeInSubagentView(tn) {
			keys = []string{"Up", "Escape"}
		}
		// Named-key navigation. Send one at a time with a small gap so the TUI can
		// re-render between keys (e.g. after Enter advances to the next question page).
		for _, k := range keys {
			if !allowedKey(k) {
				httpx.WriteErr(w, http.StatusBadRequest, "bad_key", "unsupported key: "+k)
				return
			}
		}
		for i, k := range keys {
			if out, err := exec.Command("tmux", "send-keys", "-t", pane, k).CombinedOutput(); err != nil {
				httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", string(out))
				return
			}
			if i < len(keys)-1 {
				time.Sleep(90 * time.Millisecond)
			}
		}
		// Only a submit (a key sequence containing Enter — answering a question) starts a
		// turn; pure navigation / mode-cycle (BTab, Tab) / stop (Escape) must NOT mark the
		// session working, or codex sticks on 進行中 after a plan-mode toggle (no Stop hook
		// fires to clear it).
		for _, k := range keys {
			if k == "Enter" {
				markSessionWorking(name)
				break
			}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": name})
		return
	}
	if len(body.Seq) > 0 {
		// Validate up-front so a bad step doesn't half-drive the modal: each step is
		// either a whitelisted named key or literal text.
		for _, s := range body.Seq {
			if s.K != "" && !allowedKey(s.K) {
				httpx.WriteErr(w, http.StatusBadRequest, "bad_key", "unsupported key: "+s.K)
				return
			}
			if s.K == "" && s.T == "" {
				httpx.WriteErr(w, http.StatusBadRequest, "bad_seq", "each seq step needs k or t")
				return
			}
		}
		working := false
		for i, s := range body.Seq {
			var cmd *exec.Cmd
			if s.K != "" {
				cmd = exec.Command("tmux", "send-keys", "-t", pane, s.K)
				if s.K == "Enter" {
					working = true // a submit (answering the question) starts a turn
				}
			} else {
				// -l: literal, so the answer text is typed verbatim (no key-name interp).
				cmd = exec.Command("tmux", "send-keys", "-t", pane, "-l", s.T)
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", string(out))
				return
			}
			if i < len(body.Seq)-1 {
				time.Sleep(90 * time.Millisecond) // let the TUI re-render between steps
			}
		}
		if working {
			markSessionWorking(name)
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": name})
		return
	}
	// While an AskUserQuestion is awaiting an answer, the TUI modal IGNORES typed text
	// on option rows and the trailing Enter confirms the highlighted FIRST option — a
	// {prompt} here silently answers the wrong choice (v2.1.204 実測, docs/dev/92).
	// Reject it for every {prompt} sender (Console composer, MCP drive tools); answers
	// must go through {keys}/{seq}, which stay allowed above.
	if questionPending(name) {
		httpx.WriteErr(w, http.StatusConflict, "question_pending",
			"a question is awaiting an answer; answer it via keys/seq (the question card), not free text")
		return
	}
	// Send the prompt literally (-l: no key-name interpretation), then Enter to submit.
	if out, err := exec.Command("tmux", "send-keys", "-t", pane, "-l", body.Prompt).CombinedOutput(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", string(out))
		return
	}
	// Pause before Enter so the TUI finishes ingesting the pasted text first. codex's
	// (and opencode's) input widget can drop an Enter that arrives while it's still
	// consuming the paste — the symptom being a prompt that sits in the composer until
	// the user presses Enter again in the terminal. claude tolerates back-to-back, but a
	// short beat is harmless there too.
	time.Sleep(inputSubmitDelay(name))
	if out, err := exec.Command("tmux", "send-keys", "-t", pane, "Enter").CombinedOutput(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", string(out))
		return
	}
	// A slash command (/plan, /model, …) isn't a turn — don't optimistically mark the
	// session working, or codex sticks on 進行中 (no Stop hook fires to clear it). Real
	// prompts still mark working so the chip reacts before the agent's own hook.
	if !slashCmdRe.MatchString(strings.TrimSpace(body.Prompt)) {
		markSessionWorking(name)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": name})
}

// slashCmdRe matches a single-token slash command like "/plan" or "/model foo" (but not a
// path such as /home/dev/x, which has a second slash).
var slashCmdRe = regexp.MustCompile(`^/[A-Za-z][\w-]*(\s|$)`)

// typeLineAndSubmit types a literal line into the session's pane and submits it —
// the same type-then-Enter primitive as handleSessionInput's {prompt} path, but
// as a plain Go call (no HTTP round trip) for server-side orchestration (e.g.
// disconnectRemoteControl's /remote-control, deliverInitialPrompt's launch task).
func typeLineAndSubmit(name, pane, text string) error {
	if out, err := exec.Command("tmux", "send-keys", "-t", pane, "-l", text).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	time.Sleep(inputSubmitDelay(name))
	if out, err := exec.Command("tmux", "send-keys", "-t", pane, "Enter").CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

// deliverInitialPrompt types a launch task into a freshly created session once its
// agent CLI has booted, then submits it. It is the SERVER-SIDE counterpart of the
// Console's sendPromptWhenAlive (open.ts): handleCreateSession fires it in a goroutine
// when a create carries initial_prompt, so an orchestrator (フリート・オペレーター /
// the create_session MCP tool) can spawn a session AND hand it the first task in a
// single call, without a live Console mirror to auto-send.
//
// Best-effort and silent: if the session never becomes typable within the window we
// give up (the session still exists; the user can paste the task manually) — a failed
// hand-off must never wedge the created session.
func deliverInitialPrompt(name, prompt string) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}
	tn := session.TmuxName(name)
	// Wait for tmux + a resolvable pane id (the agent process is up). Cap ~30s to match
	// the Console's give-up budget, polling on the same cadence.
	var pane string
	for i := 0; i < 60; i++ {
		if tmuxx.HasSession(tn) {
			if pane = tmuxx.SessionPaneID(tn); pane != "" {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if pane == "" {
		return
	}
	// Alive ≠ ready to type: text sent into the boot screen is simply eaten (verified
	// live with a cold opencode — a fixed 2.5s beat lost the prompt). Wait until the CLI
	// has actually drawn its composer, using the same readiness signal the Console's
	// launch seed uses: paneMode reads the status/footer line that claude/codex/opencode
	// draw only once ready. Cap the wait, then fall back to the old fixed beat.
	kind := session.KindClaude
	if m, ok := session.ReadMeta(name); ok {
		kind = m.Kind
	}
	ready := false
	for i := 0; i < 60; i++ {
		if paneMode(kind, tn) != "" {
			ready = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ready {
		time.Sleep(2500 * time.Millisecond) // composer never detected — best-effort send anyway
	}
	if typeLineAndSubmit(name, pane, prompt) != nil {
		return
	}
	// A freshly booted CLI can coalesce the paste and swallow the Enter that arrives
	// inside the paste window (the Console's seedSubmit nudges for the same reason).
	// A second Enter after the window closes is a no-op if the first already submitted.
	time.Sleep(900 * time.Millisecond)
	_ = exec.Command("tmux", "send-keys", "-t", pane, "Enter").Run()
	// A real task starts a turn — mark working so the chip reacts before the agent's hook.
	markSessionWorking(name)
}

// disconnectRemoteControl best-effort disconnects an active claude.ai Remote
// Control bridge right before handleHaltSession kills the tmux pane.
//
// Why: claude.ai's shown session name is fixed at RC-connect time; it is not
// re-read from --name on a later relaunch. Verified by hand: stopping a session
// while RC is still connected and resuming it later keeps showing the stale
// name — but disconnecting RC BEFORE stopping means the next resume's
// remote-control-at-startup autoconnect performs a genuinely fresh connection,
// which picks up whatever title is current at THAT time. So this only needs to
// run once, right before a halt, not after every title change or after resume.
//
// There is no non-interactive "off" — verified by hand, `/remote-control` with
// an argument is only accepted while disconnected (it reconnects under that
// name); while connected it always opens a 3-item menu (Disconnect this
// session / Show QR code / Continue) with the cursor defaulting to "Continue".
// One Down wraps to "Disconnect this session" — confirmed by hand.
//
// Best-effort and silent: a stop the user explicitly asked for must never be
// blocked or delayed by a failure here, and this only ever runs immediately
// before that kill — never while the session might still be in active use.
func disconnectRemoteControl(name string, m session.Meta) {
	if m.Kind != session.KindClaude || claude.RemoteSessionURL(session.UUID(m.Dir, m.Name)) == "" {
		return // not a claude session, or RC has never been used here
	}
	pane := tmuxx.SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return
	}
	if typeLineAndSubmit(name, pane, "/remote-control") != nil {
		return
	}
	time.Sleep(300 * time.Millisecond) // let the menu render
	_ = exec.Command("tmux", "send-keys", "-t", pane, "Down").Run()
	time.Sleep(90 * time.Millisecond)
	_ = exec.Command("tmux", "send-keys", "-t", pane, "Enter").Run()
	time.Sleep(300 * time.Millisecond) // let the disconnect actually land before the pane is killed
}

// inputSubmitDelay is how long to wait between typing a prompt and sending Enter, per
// kind. codex/opencode need a beat so their input widget doesn't drop the Enter mid-
// paste (a prompt left un-submitted in the composer); claude submits reliably back-to-
// back so it gets a token pause. Tunable via AGENT_INPUT_SUBMIT_DELAY_MS.
func inputSubmitDelay(name string) time.Duration {
	ms := 200
	if meta, ok := session.ReadMeta(name); ok && meta.Kind == session.KindClaude {
		ms = 20
	}
	if v := os.Getenv("AGENT_INPUT_SUBMIT_DELAY_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 5000 {
			ms = n
		}
	}
	return time.Duration(ms) * time.Millisecond
}

// opencodeInSubagentView reports whether the pane is currently showing opencode's
// subagent DETAIL view, identified by its navigation footer ("Parent … Prev … Next").
// In that view Escape only navigates, so the stop button must step out (Up) first.
// Best-effort: a capture failure returns false (treat as the normal view → plain Escape).
func opencodeInSubagentView(tn string) bool {
	s := tmuxx.CapturePane(tn)
	return strings.Contains(s, "Parent") && strings.Contains(s, "Next")
}

// questionPending reports whether the session is blocked on an AskUserQuestion
// (status "question": written by the PreToolUse hook, cleared when the question's
// own lifecycle moves on — answered→working / Stop→idle). Unknown sessions or a
// missing status file read as not-pending.
func questionPending(name string) bool {
	meta, ok := session.ReadMeta(name)
	if !ok {
		return false
	}
	st, ok := status.Read(session.UUID(meta.Dir, name))
	return ok && st.State == "question"
}

// markSessionWorking optimistically marks the session working so a poll immediately
// after a send doesn't read a stale idle before claude's UserPromptSubmit hook fires.
func markSessionWorking(name string) {
	meta, ok := session.ReadMeta(name)
	if !ok {
		return
	}
	status.Persist(session.UUID(meta.Dir, name), "working")
}

// allowedKey is the whitelist of tmux key names the Console may send to drive a TUI
// (the AskUserQuestion modal): navigation + confirm, nothing that could run a command.
func allowedKey(k string) bool {
	switch k {
	case "Up", "Down", "Left", "Right", "Enter", "Space", "Escape", "Tab", "BTab", "BSpace", "Home", "End":
		return true
	}
	return false
}

// handleSessionStatus (GET /sessions/{name}/status) returns the session's live
// state for the drive poll loop: working | idle | question, plus alive.
func handleSessionStatus(w http.ResponseWriter, r *http.Request) {
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
	state := driveState(meta, alive, true)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"name": name, "kind": meta.Kind, "alive": alive, "status": state})
}

// handleSessionOutput (GET /sessions/{name}/output?since=<cursor>) returns the
// session's assistant text appended since the cursor, plus a new cursor and the
// current status. Phase 1: claude only (its jsonl transcript). cursor is a line
// index into the transcript.
func handleSessionOutput(w http.ResponseWriter, r *http.Request) {
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
	// /output opts out of the idle-heal (heal=false) to preserve its historical behavior.
	state := driveState(meta, alive, false)
	if !agentOf(meta.Kind).Caps().CanTranscript {
		httpx.WriteErr(w, http.StatusBadRequest, "unsupported_kind", "output is available for transcript-capable sessions only")
		return
	}
	since := 0
	if v := r.URL.Query().Get("since"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			since = n
		}
	}
	// codex/opencode: their stores aren't claude's jsonl — build the flattened assistant
	// output from the generic Transcript() turns instead (cursor = turn count), so the
	// drive tools (MCP get_session_output) work for every transcript-capable kind.
	if meta.Kind != session.KindClaude {
		td, _ := agentOf(meta.Kind).Transcript(meta)
		var gb strings.Builder
		for i := since; i < len(td.Turns); i++ {
			t := td.Turns[i]
			if t.Role == "assistant" && t.Text != "" {
				if gb.Len() > 0 {
					gb.WriteString("\n\n")
				}
				gb.WriteString(t.Text)
			}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"name": name, "output": gb.String(), "cursor": len(td.Turns),
			"status": state, "alive": alive,
		})
		return
	}
	sid := session.UUID(meta.Dir, name)
	lines := claude.TranscriptLines(sid)
	var sb strings.Builder
	for i := since; i < len(lines); i++ {
		if t := claude.AssistantText(lines[i]); t != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(t)
		}
		if sb.Len() > 1<<20 { // cap at 1 MiB
			break
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"name": name, "output": sb.String(), "cursor": len(lines),
		"status": state, "alive": alive,
	})
}

// claude jsonl の読み出し（jsonlByMtime / transcriptRead / jsonlHasConversation /
// transcriptLines / assistantText）は internal/agents/claude の transcript.go へ
// 移設（docs/23 残① Wave F）。
