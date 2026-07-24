package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
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
	case session.KindAgy:
		// agy's composer footer (v1.1.4 実測): left "? for shortcuts" (idle) or
		// "esc to cancel" (generating), right "<model>" — "plan · <model>" in plan
		// mode. The footer only exists once the composer is drawn (the "Signing
		// in..." boot screen has none, and text typed into it is eaten), so this
		// doubles as the launch-seed readiness signal, like the other kinds.
		for _, line := range strings.Split(paneTail(s, 3), "\n") {
			if strings.Contains(line, "? for shortcuts") || strings.Contains(line, "esc to cancel") {
				if strings.Contains(line, "plan · ") {
					return "Plan"
				}
				return "Default"
			}
		}
	case session.KindCopilot:
		// copilot's composer footer (v1.0.73 実測): idle "/ commands · ? help · tab
		// next tab", draft "@ files · # issues", working "◎ Working esc interrupt" —
		// right edge shows the model ("Auto → gpt-5-mini"). The footer exists only
		// once the composer is drawn (the trust dialog / boot screen has none), so
		// this doubles as the launch-seed readiness signal. Mode has no footer
		// marker; plan mode is chosen at launch (--mode plan)＝meta が真実なので
		// here we only distinguish drawn/not-drawn and report the non-plan label.
		for _, line := range strings.Split(paneTail(s, 3), "\n") {
			if strings.Contains(line, "/ commands") || strings.Contains(line, "@ files") ||
				strings.Contains(line, "Working") {
				return "Default"
			}
		}
	case session.KindCursor:
		// cursor's composer footer (v2026.07.20 実測): idle placeholder "→ Add a
		// follow-up" / "→ Plan, search, build anything"、下部にモデル名（"Auto"）と
		// cwd、working は "Running … ctrl+c to stop"。フッタはコンポーザ描画後にのみ
		// 出る（--trust 起動で trust ダイアログは出ないが、ブート直後は無い）ので、
		// これが launch-seed の readiness 信号を兼ねる。plan は起動時固定（--plan）＝
		// meta が真実なので、ここでは描画済み/未描画のみ判定し非 plan ラベルを返す。
		for _, line := range strings.Split(paneTail(s, 4), "\n") {
			if strings.Contains(line, "Add a follow-up") ||
				strings.Contains(line, "Plan, search, build anything") ||
				strings.Contains(line, "ctrl+c to stop") || strings.Contains(line, "Running") {
				return "Default"
			}
		}
	case session.KindKiro:
		// kiro's composer footer (2.14.1 実測): idle placeholder "ask a question or
		// describe a task ↵"、その上の行にモデル/コンテキスト "kiro_default · auto ·
		// ◔ n%   <cwd>"、working は "Kiro is working · Type to steer · Ctrl+S to queue"。
		// フッタはコンポーザ描画後にのみ出る（chat.disableTrustAllConfirmation 固定で
		// 危険モード確認ダイアログは出ないが、ブート直後は無い）ので、これが launch-seed
		// の readiness 信号を兼ねる。plan は起動時固定（--trust-all-tools を外す）＝meta が
		// 真実なので、ここでは描画済み/未描画のみ判定し非 plan ラベルを返す。
		for _, line := range strings.Split(paneTail(s, 4), "\n") {
			if strings.Contains(line, "ask a question or describe a task") ||
				strings.Contains(line, "Kiro is working") || strings.Contains(line, "requires approval") {
				return "Default"
			}
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
		// ReportTo (docs/30) arms a one-shot session report to this conversation once
		// the prompt's turn reaches an awaiting-input state. Sent by the af_write MCP's
		// send_to_session, which carries its own conversation id (--conv).
		ReportTo string `json:"report_to"`
		// Confirm (docs/38 配達検証) makes the {prompt} path block until the prompt
		// provably started a turn, self-heal once, and answer 502 delivery_unconfirmed
		// otherwise. Set by unattended senders (the CP scheduler's reuse send) whose
		// prompt is always a real task; NOT for turnless UI slash commands (/model …).
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	if len(body.Keys) == 0 && len(body.Seq) == 0 && strings.TrimSpace(body.Prompt) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "empty_prompt", "prompt, keys or seq is required")
		return
	}
	// managed セッションの {prompt} は tmux ペインを持たない（app-server 経由）ので、
	// tmux 存在チェックより先に ThreadHandle.Send へ回す。send_to_session（MCP の
	// af_write ツール）など /input を直叩きする呼び出し元は tui/managed を意識しない
	// ため、ここで分岐しないと生きているセッションにも常に not_running が返っていた
	// （/turn の handleManagedTurn と同じ理由で、docs/27 で /turn は分岐済みだったが
	// /input 側の分岐が漏れていた）。{keys}/{seq}（生 TUI 駆動）は tui 専用のまま。
	if len(body.Keys) == 0 && len(body.Seq) == 0 {
		if meta, ok := session.ReadMeta(name); ok && meta.DriverKind() == session.DriverManaged {
			handleManagedInputPrompt(w, meta, body.Prompt, body.ReportTo)
			return
		}
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
		if err := sendNamedKeys(pane, keys); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
			return
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
				cmd = tmuxx.Cmd("send-keys", "-t", pane, s.K)
				if s.K == "Enter" {
					working = true // a submit (answering the question) starts a turn
				}
			} else {
				// -l: literal, so the answer text is typed verbatim (no key-name interp).
				cmd = tmuxx.Cmd("send-keys", "-t", pane, "-l", s.T)
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
	// 配達検証の基線はタイプ前に取る（confirm 時のみ）。meta が読めない session は
	// 検証不能として従来意味論のまま通す。
	var confirmMeta session.Meta
	var confirmBase deliverySnapshot
	if body.Confirm {
		if m, ok := session.ReadMeta(name); ok {
			confirmMeta = m
			confirmBase = deliveryBaseline(m)
		}
	}
	if !submitPromptTUI(w, name, pane, body.Prompt) {
		return
	}
	if body.Confirm && confirmMeta.Name != "" {
		if err := confirmPromptDelivery(confirmMeta, pane, body.Prompt, confirmBase); err != nil {
			// 未確認は成功と偽らない: 呼び出し元（CP スケジューラ / operator MCP）が
			// error として記録・通知し、偽 fired を作らない（docs/38 配達検証）。
			httpx.WriteErr(w, http.StatusBadGateway, "delivery_unconfirmed", err.Error())
			return
		}
	}
	// Each delivered instruction re-arms exactly one report (docs/30 の指示1件=報告1回)
	// and — being operator-originated (report_to present) — is remembered so the mirror
	// can badge the resulting user turn (docs/30 ②).
	if body.ReportTo != "" {
		armSessionReport(name, body.ReportTo)
		recordOperatorInjection(name, body.Prompt)
	} else {
		// Genuine Console-typed input (not an operator/MCP injection): mirror it into
		// the session's Discord thread so the thread reflects both directions (docs/37
		// Fix ②). Best-effort + async — never blocks or fails the input.
		go bridge.MirrorUserInput(name, body.Prompt)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": name})
}

// handleManagedInputPrompt is /input's {prompt} counterpart to handleManagedTurn's
// start op (session_turn.go) — same ThreadHandle.Send delivery, but keeps /input's
// report_to contract (armSessionReport / recordOperatorInjection) that /turn doesn't
// carry, so send_to_session's docs/30 auto-report keeps working for managed sessions.
func handleManagedInputPrompt(w http.ResponseWriter, meta session.Meta, prompt, reportTo string) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "empty_prompt", "prompt, keys or seq is required")
		return
	}
	if questionPending(meta.Name) {
		httpx.WriteErr(w, http.StatusConflict, "question_pending",
			"a question is awaiting an answer; answer it via the question card, not free text")
		return
	}
	d, ok := driverOf(meta)
	if !ok {
		httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
			"managed driver はこの kind ではまだ利用できません")
		return
	}
	h, err := d.Resume(meta)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "runtime_failed", err.Error())
		return
	}
	if err := h.Send(agents.TurnInput{Prompt: prompt}); err != nil {
		if errors.Is(err, agents.ErrQuestionPending) {
			httpx.WriteErr(w, http.StatusConflict, "question_pending",
				"a question is awaiting an answer; answer it via the question card, not free text")
			return
		}
		httpx.WriteErr(w, http.StatusBadGateway, "runtime_failed", err.Error())
		return
	}
	markSessionWorking(meta.Name)
	if reportTo != "" {
		armSessionReport(meta.Name, reportTo)
		recordOperatorInjection(meta.Name, prompt)
	} else {
		go bridge.MirrorUserInput(meta.Name, prompt) // docs/37 Fix ②: Console-input mirror
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": meta.Name})
}

// submitPromptTUI is the shared {prompt}→TUI delivery behind /input's {prompt} path
// and /turn's tui start/steer route. Guards, types + submits, and marks working;
// on failure the HTTP error is already written and false is returned.
func submitPromptTUI(w http.ResponseWriter, name, pane, prompt string) bool {
	// While an AskUserQuestion is awaiting an answer, the TUI modal IGNORES typed text
	// on option rows and the trailing Enter confirms the highlighted FIRST option — a
	// prompt here silently answers the wrong choice (v2.1.204 実測, docs/dev/92).
	// Reject it for every prompt sender (Console composer, MCP drive tools); answers
	// must go through {keys}/{seq} (tui) or /respond (managed).
	if questionPending(name) {
		httpx.WriteErr(w, http.StatusConflict, "question_pending",
			"a question is awaiting an answer; answer it via the question card, not free text")
		return false
	}
	// agy: the "Signing in..." boot screen eats typed text entirely (docs/32) — a
	// send_to_session right after create (no initial_prompt) would vanish. Its
	// composer footer is persistent once drawn ("? for shortcuts" idle / "esc to
	// cancel" working), so an empty paneMode here means still booting: wait briefly.
	// Cap ~15s (slow sign-in) then proceed best-effort; a pending question/permission
	// was already rejected above, so this can't stall on a widget.
	// copilot: 同型 — trust 事前追記済みでもタブ UI の描画までコンポーザは無く、
	// ブート画面に送った文字は無音で消える（8780956 の教訓）。フッタが readiness。
	if meta, ok := session.ReadMeta(name); ok &&
		(meta.Kind == session.KindAgy || meta.Kind == session.KindCopilot || meta.Kind == session.KindCursor ||
			meta.Kind == session.KindKiro) {
		tn := session.TmuxName(name)
		for i := 0; i < 30 && paneMode(meta.Kind, tn) == ""; i++ {
			time.Sleep(500 * time.Millisecond)
		}
	}
	// Use the same type-and-submit primitive as server-side initial prompts. In
	// particular, Codex/OpenCode need bracketed paste so a long prompt's trailing Enter
	// is not swallowed while their input widget is still consuming the paste.
	if err := typeLineAndSubmit(name, pane, prompt); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return false
	}
	// A slash command (/plan, /model, …) isn't a turn — don't optimistically mark the
	// session working, or codex sticks on 進行中 (no Stop hook fires to clear it). Real
	// prompts still mark working so the chip reacts before the agent's own hook.
	if !slashCmdRe.MatchString(strings.TrimSpace(prompt)) {
		markSessionWorking(name)
	}
	return true
}

// slashCmdRe matches a single-token slash command like "/plan" or "/model foo" (but not a
// path such as /home/dev/x, which has a second slash).
var slashCmdRe = regexp.MustCompile(`^/[A-Za-z][\w-]*(\s|$)`)

// typeLineAndSubmit types a literal line into the session's pane and submits it —
// the same type-then-Enter primitive as handleSessionInput's {prompt} path, but
// as a plain Go call (no HTTP round trip) for server-side orchestration (e.g.
// disconnectRemoteControl's /remote-control, deliverInitialPrompt's launch task).
func typeLineAndSubmit(name, pane, text string) error {
	if err := typePromptText(name, pane, text); err != nil {
		return err
	}
	time.Sleep(inputSubmitDelay(name))
	if out, err := tmuxx.Cmd("send-keys", "-t", pane, "Enter").CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

// typePromptText puts text in an agent's composer. Codex and OpenCode treat a fast
// stream of literal key events as an in-progress paste and may consume a following
// Enter as part of that paste. tmux paste-buffer -p uses the terminal's bracketed-paste
// protocol, which gives the TUI an explicit end-of-paste marker before Enter arrives.
// Claude does not need this workaround and retains the established literal-key path.
func typePromptText(name, pane, text string) error {
	kind := session.KindClaude
	if meta, ok := session.ReadMeta(name); ok {
		kind = meta.Kind
	}
	// copilot も bracketed paste が必要: 実測（v1.0.73）で literal keys と同じ
	// send-keys 内の Enter がペースト折り畳みに食われ、プロンプトが確定しない。
	if kind != session.KindCodex && kind != session.KindOpencode && kind != session.KindCopilot && kind != session.KindCursor && kind != session.KindKiro {
		if out, err := tmuxx.Cmd("send-keys", "-t", pane, "-l", text).CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		return nil
	}

	buffer := fmt.Sprintf("af-prompt-%s-%d", name, time.Now().UnixNano())
	load := tmuxx.Cmd("load-buffer", "-b", buffer, "-")
	load.Stdin = strings.NewReader(text)
	if out, err := load.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	// -p enables bracketed paste when the target TUI requested it; -d removes the
	// one-shot buffer even after a successful paste.
	if out, err := tmuxx.Cmd("paste-buffer", "-p", "-d", "-b", buffer, "-t", pane).CombinedOutput(); err != nil {
		_ = tmuxx.Cmd("delete-buffer", "-b", buffer).Run()
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
	meta, metaOK := session.ReadMeta(name)
	kind := session.KindClaude
	if metaOK {
		kind = meta.Kind
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
	var base deliverySnapshot
	if metaOK {
		base = deliveryBaseline(meta)
	}
	if typeLineAndSubmit(name, pane, prompt) != nil {
		return
	}
	// A freshly booted CLI can coalesce the paste and swallow the Enter that arrives
	// inside the paste window (the Console's seedSubmit nudges for the same reason).
	// A second Enter after the window closes is a no-op if the first already submitted.
	time.Sleep(900 * time.Millisecond)
	_ = tmuxx.Cmd("send-keys", "-t", pane, "Enter").Run()
	// A real task starts a turn — mark working so the chip reacts before the agent's hook.
	markSessionWorking(name)
	// 配達検証（docs/38）: 打鍵と nudge の後、ターンが実際に始まった証拠を確認し、
	// 出なければ自己修復を 1 巡試す。この経路は create 応答から切り離された goroutine
	// なので HTTP では失敗を返せない — 最終的に未確認ならログに残す（reuse 送信側の
	// confirm と違い、ここは best-effort のまま）。
	if metaOK && base != nil {
		if err := confirmPromptDelivery(meta, pane, prompt, base); err != nil {
			log.Printf("initial prompt delivery UNCONFIRMED for %s: %v", name, err)
		}
	}
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
	_ = tmuxx.Cmd("send-keys", "-t", pane, "Down").Run()
	time.Sleep(90 * time.Millisecond)
	_ = tmuxx.Cmd("send-keys", "-t", pane, "Enter").Run()
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

// sendNamedKeys sends named tmux keys to pane one at a time, with a small gap so
// the TUI can re-render between keys (e.g. after Enter advances to the next
// question page). Shared by the /input {keys} path and /turn's tui interrupt.
func sendNamedKeys(pane string, keys []string) error {
	for i, k := range keys {
		if out, err := tmuxx.Cmd("send-keys", "-t", pane, k).CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		if i < len(keys)-1 {
			time.Sleep(90 * time.Millisecond)
		}
	}
	return nil
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
	// agy has no status hooks — its pending interactive prompt is detected via the
	// conversation-DB probe instead (pending.go). Both states must reject free text:
	// question AND permission menus treat a typed line + Enter as confirming the
	// highlighted row (docs/32 実測 — 許可メニューでは「1. Yes」確定 = 無言の承認事故)。
	if meta.Kind == session.KindAgy {
		st, _ := agy.Probe(meta)
		return st != ""
	}
	// copilot: hooks 無し — events.jsonl の未完了 permission.requested が保留の
	// 唯一のソース（tui の許可メニュー / managed の Interaction 双方を同じ形で拾う）。
	if meta.Kind == session.KindCopilot {
		return copilot.LiveState(meta) == "question"
	}
	// kiro: hooks 無し — "question"（承認待ち「shell requires approval」）は driveState が
	// 返すだけで status ストアに書かれないため、下の汎用フォールバック（status.Read）は
	// kiro では常に false。TUI 経路で承認パネル中に自由文を送ると素通しになる穴なので、
	// TUI 文字列を直接読んでガードする（copilot 同型。managed は ErrQuestionPending で
	// 別途ガード済み）。
	if meta.Kind == session.KindKiro {
		return kiro.LiveState(meta) == "question"
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
	alive := sessionAlive(meta)
	state := driveState(meta, alive, true)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"name": name, "kind": meta.Kind, "alive": alive, "status": state,
		"ready": sessionInputReady(meta, alive),
	})
}

// sessionInputReady is stricter than liveness: a newly resumed terminal session can
// have a pane while its CLI is still drawing the boot screen, where typed prompts are
// lost. Managed sessions can accept Send as soon as their handle is alive; raw shells
// are immediately typable. Agent TUIs must expose their composer footer and must not
// be blocked on a startup menu.
func sessionInputReady(meta session.Meta, alive bool) bool {
	if !alive {
		return false
	}
	if meta.DriverKind() == session.DriverManaged || meta.Kind == session.KindShell {
		return true
	}
	if meta.Kind == session.KindSSM {
		pane := tmuxx.SessionPaneID(session.TmuxName(meta.Name))
		if pane == "" {
			return false
		}
		out, err := tmuxx.Cmd("capture-pane", "-p", "-S", "-", "-t", pane).Output()
		return err == nil && strings.Contains(string(out), "Starting session with SessionId:")
	}
	if meta.Kind == session.KindClaude {
		if state, _ := sessionTerminalState(meta.Name); state != "" {
			return false
		}
	}
	if meta.Kind == session.KindCodex && codexTerminalState(meta.Name) != "" {
		return false
	}
	return paneMode(meta.Kind, session.TmuxName(meta.Name)) != ""
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
	alive := sessionAlive(meta)
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
