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
	"unicode/utf8"

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
		return claudeModeLabel(paneTail(s, 3))
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
		// codex's composer footer is "<model> <effort> · <cwd>". Through 0.144 it
		// appended "Plan mode [(shift+tab to cycle)]" in plan mode; 0.145 removed that
		// textual marker. Keep accepting it for pinned/older versions, while the bare
		// footer remains the composer-readiness signal and mirror-driven mode is persisted
		// in meta (rememberCodexTUIMode). Never inspect history text here: a stale
		// "… for Plan mode." line would spoof the current state.
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

// claudeModeLabel maps claude's status-line mode strip to the chip label.
//
// 実測（claude 2.1.241・2026-08-24。左が --permission-mode の値）:
//
//	(無指定＝既定) / manual  "⏸ manual mode on · ← for agents"
//	auto                     "⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
//	acceptEdits              "⏵⏵ accept edits on (shift+tab to cycle) · …"
//	bypassPermissions        "⏵⏵ bypass permissions on (shift+tab to cycle) · …"
//	dontAsk                  "⏵⏵ don't ask on (shift+tab to cycle) · …"
//	plan                     "⏸ plan mode on (shift+tab to cycle) · …"
//
// ★**manual だけ "(shift+tab to cycle)" を出さない**（tmuxx の modeFooterRe が 2.1.212 で
// 同じことを踏んでいる）。そして manual は「権限確認あり」で起動したセッションが落ちる先
// （docs/76）そのものなので、旧実装の「4 つの名前 ＋ shift+tab の合言葉」では**空文字**に
// なる。paneMode の空文字は「コンポーザ未描画」を意味し、launch-seed の readiness ゲート
// （submitPromptTUI / 初回プロンプト配達）が 30 秒待ってから best-effort に落ちる。
//
// なので名前を並べるだけで終わらせず、**フッタ帯そのもの**（tmuxx.ClaudeModeFooter）を
// 最後の砦に置く: 名前が増えても改名されても「描画済み・モード名は Default 扱い」に倒れ、
// 配達だけは止まらない。
func claudeModeLabel(tail string) string {
	switch {
	case strings.Contains(tail, "plan mode on"):
		return "Plan"
	case strings.Contains(tail, "accept edits on"):
		return "Accept Edits"
	case strings.Contains(tail, "bypass permissions on"):
		return "Bypass"
	case strings.Contains(tail, "auto mode on"):
		return "Auto"
	case strings.Contains(tail, "manual mode on"):
		return "Manual"
	case strings.Contains(tail, "don't ask on"):
		return "Don't ask"
	case tmuxx.ClaudeModeFooter(tail), strings.Contains(tail, "shift+tab to cycle"):
		// 未知のモード名、または古いビルドの合言葉だけが残っている場合。
		return "Default"
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
		// WhenReady defers a {prompt} to the launch-task delivery loop
		// (deliverInitialPrompt): wait for tmux + the CLI's own composer, type, nudge
		// Enter once the paste window closed, verify. Answers 202 immediately — the
		// caller does not wait for the boot. It is the create call's `initial_prompt`
		// for the one case that cannot use it: the Console's 作業を始める uploads pasted
		// attachments TO the session, so the prompt text only becomes final after the
		// session exists. Plain {prompt} only, and not combined with report_to /
		// peer_from / confirm (those own their own delivery semantics).
		WhenReady bool `json:"when_ready"`
		// Source attributes an injection's origin for the mirror badge (docs/38):
		// "schedule" / "schedule-manual" from the CP scheduler; anything else alongside a
		// report_to (incl. empty — the operator MCP) records as "operator". Whitelisted
		// server-side. A schedule origin is remembered WITHOUT report_to as well: 完了報告
		// OFF のスケジュールは report_to を持たないが、投入されたことに変わりはない。
		Source string `json:"source"`
		// PeerFrom marks this as a session-to-session message and names the SENDING
		// session (docs/58 / ADR 0041). It is not a badge string the caller picks: the
		// server validates it, refuses report_to alongside it, builds the envelope
		// itself and applies the peer rate limit. See session_peer.go for why those
		// invariants live server-side.
		PeerFrom string `json:"peer_from"`
		// PeerIntent is the message's kind (request / question / answer / notice) and is
		// REQUIRED alongside peer_from. The server derives the reply policy from it and
		// puts both in the envelope (docs/58 §58.14) — the sender picks what it is, never
		// what the receiver owes back.
		PeerIntent string `json:"peer_intent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	if len(body.Keys) == 0 && len(body.Seq) == 0 && strings.TrimSpace(body.Prompt) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "empty_prompt", "prompt, keys or seq is required")
		return
	}
	// Each key/seq step is paced (~90ms gaps for TUI re-render), so an unbounded list
	// would let one request occupy the handler for minutes — cap it. Real drivers
	// (question modals, navigation) use a handful of steps.
	const maxInputSteps = 256
	if len(body.Keys) > maxInputSteps || len(body.Seq) > maxInputSteps {
		httpx.WriteErr(w, http.StatusBadRequest, "too_many_steps",
			fmt.Sprintf("keys/seq must have at most %d elements", maxInputSteps))
		return
	}
	// peer 送信（docs/58 / ADR 0041）。ここで全ての不変条件を満たしてから通常の投入経路へ
	// 合流させる。managed/tui の分岐より前に置くのは自己申告1行と同じ理由で、片方の経路
	// だけ素通しになるのを防ぐため。
	if body.PeerFrom == "" && strings.TrimSpace(body.PeerIntent) != "" {
		// peer 以外の投入に種別だけ載せても封筒は付かない。黙って無視すると呼び出し元は
		// 「返信規律を伝えた」つもりのまま素の投入をしてしまうので、ここで落とす。
		httpx.WriteErr(w, http.StatusBadRequest, "peer_intent_without_from",
			"peer_intent は peer_from と一緒にのみ指定できます")
		return
	}
	if body.PeerFrom != "" {
		// arm 非干渉は構造で担保する: 両方が載った要求は通さない。呼び出し元の実装ミスで
		// peer メッセージが指示台帳に載ると、リコンサイラが「利用者の新指示」と誤認する
		// （ADR 0041 決定4／docs/51 の早期 settle 事故と同型）。
		if body.ReportTo != "" {
			httpx.WriteErr(w, http.StatusBadRequest, "peer_with_report_to",
				"peer_from と report_to は同時に指定できません（peer メッセージは指示台帳に載せません）")
			return
		}
		if len(body.Keys) > 0 || len(body.Seq) > 0 {
			httpx.WriteErr(w, http.StatusBadRequest, "peer_needs_prompt",
				"peer_from は {prompt} 経路でのみ使えます")
			return
		}
		if err := peerValidateMessage(body.Prompt); err != nil {
			writePeerErr(w, err)
			return
		}
		if _, err := peerPolicy(body.PeerFrom, name); err != nil {
			writePeerErr(w, err)
			return
		}
		reply, err := peerResolveIntent(body.PeerIntent)
		if err != nil {
			writePeerErr(w, err)
			return
		}
		if err := peerRate.allow(body.PeerFrom, name, strings.TrimSpace(body.Prompt), time.Now()); err != nil {
			writePeerErr(w, err)
			return
		}
		// 封筒はサーバが付ける（呼び出し元に組ませない＝付け忘れも名乗り詐称も起きない）。
		body.Prompt = peerEnvelope(body.PeerFrom, strings.TrimSpace(body.PeerIntent), reply, body.Prompt)
		// 無人経路なので配達検証は必須。打鍵 200 で「送れた」と返すと、送信側モデルは
		// 伝わった前提で先へ進んでしまう。
		body.Confirm = true
	}
	// docs/51 Phase 3 §自己申告ファストパス: 報告義務を負う指示（report_to 付き）にだけ
	// 「終わったら af_report を呼べ」を1行足す。managed/tui の分岐より前に置くのは、
	// どちらの経路でも同じ1行が乗るようにするため（分岐の後だと片方で漏れる）。
	if body.ReportTo != "" {
		if m, ok := session.ReadMeta(name); ok {
			body.Prompt = withSelfReportHint(body.Prompt, m)
		}
	}
	// managed セッションの {prompt} は tmux ペインを持たない（app-server 経由）ので、
	// tmux 存在チェックより先に ThreadHandle.Send へ回す。send_to_session（MCP の
	// af_write ツール）など /input を直叩きする呼び出し元は tui/managed を意識しない
	// ため、ここで分岐しないと生きているセッションにも常に not_running が返っていた
	// （/turn の handleManagedTurn と同じ理由で、docs/27 で /turn は分岐済みだったが
	// /input 側の分岐が漏れていた）。{keys}/{seq}（生 TUI 駆動）は tui 専用のまま。
	if len(body.Keys) == 0 && len(body.Seq) == 0 {
		if meta, ok := session.ReadMeta(name); ok && meta.DriverKind() == session.DriverManaged {
			handleManagedInputPrompt(w, meta, body.Prompt, body.ReportTo, body.Source, body.PeerFrom)
			return
		}
	}
	// when_ready（起動直後のセッションへの最初の指示）: ここから下の「今すぐ打つ」経路には
	// 載せない。tmux もペインもまだ無いことがあり（not_running で弾かれる）、有っても CLI が
	// composer を描く前の打鍵は起動画面ごと食われる — 待ち・二度目 Enter・配達確認を持つ
	// deliverInitialPrompt に渡し、202 で返す。managed は boot 画面が無く上の分岐で即送信
	// 済みなので、ここへは tui だけが来る。
	if body.WhenReady {
		if len(body.Keys) > 0 || len(body.Seq) > 0 {
			httpx.WriteErr(w, http.StatusBadRequest, "when_ready_needs_prompt",
				"when_ready は {prompt} 経路でのみ使えます")
			return
		}
		if body.ReportTo != "" || body.PeerFrom != "" || body.Confirm {
			httpx.WriteErr(w, http.StatusBadRequest, "when_ready_conflict",
				"when_ready は report_to / peer_from / confirm とは併用できません")
			return
		}
		// 存在しないセッション名は待たせず落とす — deliverInitialPrompt は tmux が
		// 現れるまで黙って待つので、typo は 30 秒後に無言で消えるだけになる。
		if _, ok := session.ReadMeta(name); !ok {
			httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session")
			return
		}
		go deliverInitialPrompt(name, body.Prompt)
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"queued": name})
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
		if !allowViewNavKeys(name, keys) {
			writeViewNavErr(w)
			return
		}
		if err := sendNamedKeys(pane, keys); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
			return
		}
		rememberCodexTUIMode(name, "", keys)
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
		var seqKeys []string
		for _, s := range body.Seq {
			if s.K != "" && !allowedKey(s.K) {
				httpx.WriteErr(w, http.StatusBadRequest, "bad_key", "unsupported key: "+s.K)
				return
			}
			if s.K == "" && s.T == "" {
				httpx.WriteErr(w, http.StatusBadRequest, "bad_seq", "each seq step needs k or t")
				return
			}
			seqKeys = append(seqKeys, s.K)
		}
		if !allowViewNavKeys(name, seqKeys) {
			writeViewNavErr(w)
			return
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
	// 利用上限メニュー（/rate-limit-options）が出ているペインは、打った文字がそのまま
	// **メニューの選択操作**になる。上のエージェント表示と同じ誤配達クラスなので、ここでは
	// 弾く。ここで（LeaveAgentsView のように）その場で復帰させないのは、解除の可否が
	// 画面の状態に依るから: 既定が 1（リセットまで待つ）に立っているときだけ押してよく、
	// 人がカーソルを 2（管理者へ増枠を依頼）に動かしていたら触ってはいけない。復帰は
	// 専用ループ（rate_limit_resume.go・docs/47 §4-4）がその判定込みで受け持ち、リセット
	// 時刻の自動再開まで面倒を見る。この送信は「今は届かない」ことを呼び出し元
	// （ミラー・オペレーター・定時実行）へ返して終わる。
	if m, ok := session.ReadMeta(name); ok && normalizeKind(m.Kind) == session.KindClaude &&
		tmuxx.AtRateLimitModal(name) {
		httpx.WriteErr(w, http.StatusConflict, "rate_limit_modal",
			"session is parked on claude's usage-limit menu; typed text would become a menu "+
				"selection instead of a prompt. Choose an option in the pane (wait for the reset, "+
				"or ask for more usage) and send again.")
		return
	}
	// claude のペインの入力欄が**セッション自身の会話以外**に紐づいていると、打った文字は
	// メイン会話に届かない: レールでエージェントが選ばれていればそのエージェントへの割り込みに
	// なり、agents ホーム画面なら「新しいセッションを作る」入力欄になる。どちらもペイン上は
	// 自分の文字が見えるので送信側は成功したと思い込み、メイン転写にもミラーにも残らない
	// （実測 2026-07-30 sannme2、制御プローブで再現 — internal/tmuxx/testdata/footers）。
	//
	// 戻せるなら戻す（復帰手順は実測済み）。戻せなかったときだけ 409 で突き返す — 黙って
	// 誤配達するより、届かなかったことを呼び出し元に伝える方がよい。
	if m, ok := session.ReadMeta(name); ok && normalizeKind(m.Kind) == session.KindClaude &&
		tmuxx.AgentsViewActive(name) {
		if !tmuxx.LeaveAgentsView(name) {
			httpx.WriteErr(w, http.StatusConflict, "agents_view",
				"session pane's input box is bound to a background agent / the agents screen, "+
					"not the session's conversation; typing there does not reach the session "+
					"(and could not be returned automatically — a draft may be open in the composer). "+
					"Return the pane to the main conversation and send again.")
			return
		}
		log.Printf("input: %s のペインがエージェント表示だったのでメイン会話へ戻した", name)
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
	// ミラーのバッジ用の由来は**打つ前に**記録する（recordInjection のコメント参照）。
	// 配達の後ろに置くと、転写に user 行が現れてから記録が済むまでの隙間にポーリングが
	// 当たったターンは、由来なしのまま利用者の画面に固定される。peer は配達確認を必ず
	// 通る＝その隙間が確実に開く経路なので、ここが唯一の正しい位置になる。
	if src := badgeOriginOf(body.PeerFrom, body.ReportTo, body.Source); src != "" {
		recordInjection(name, body.Prompt, src)
	}
	if !submitPromptTUI(w, name, pane, body.Prompt) {
		return
	}
	rememberCodexTUIMode(name, body.Prompt, nil)
	if body.Confirm && confirmMeta.Name != "" {
		if err := confirmPromptDelivery(confirmMeta, pane, body.Prompt, confirmBase); err != nil {
			// 未確認は成功と偽らない: 呼び出し元（CP スケジューラ / operator MCP）が
			// error として記録・通知し、偽 fired を作らない（docs/38 配達検証）。
			httpx.WriteErr(w, http.StatusBadGateway, "delivery_unconfirmed", err.Error())
			return
		}
	}
	// 利用上限の自動再開は、予約時刻ではなく配達確認が通ったここを「再開した」の
	// 真実源にする。重複防止と内部プロンプトの照合は rate_limit_resume.go が担う。
	notifyRateLimitResumeDelivered(name, body.Prompt, body.Source, time.Now())
	// Each delivered instruction ADDS one ledger row (docs/51 Phase 2 — 指示1件=報告1回。
	// 追加であって上書きではないので、キュー投入で先行指示が潰れない)。carrying report_to
	// (operator / scheduler) それ自体は、ミラーが user ターンにバッジを付けるための由来
	// としても覚える (docs/30 ② / docs/38 バッジ).
	switch {
	case body.PeerFrom != "":
		// peer は台帳に載せない（arm 非干渉 — ADR 0041 決定4）。覚えるのはミラーの
		// バッジ用の由来だけで、それは配達の前に済ませてある。Discord への転記もしない:
		// あれは「利用者が Console で打った入力」をスレッドへ反映するためのもので、
		// peer はどちらでもない。
	case body.ReportTo != "":
		addInstruction(name, body.ReportTo, injectionSource(body.Source))
	case scheduleInjectionSource(body.Source) != "":
		// 完了報告 OFF のスケジュール投入（report_to が空なので上の枝に入らない）。台帳へは
		// 載せない — 報告先そのものが無い。由来だけを覚えるのは peer と同じ扱いで、これも
		// 配達の前に済ませてある。Discord へ転記しないのも意図的: あのミラーは「利用者が
		// Console で打った入力」をスレッドへ反映するためのもので、定時実行の投入はそれ
		// ではない。
	default:
		// Genuine Console-typed input (not an operator/MCP injection): mirror it into
		// the session's Discord thread so the thread reflects both directions (docs/37
		// Fix ②). Best-effort + async — never blocks or fails the input.
		go bridge.MirrorUserInput(name, body.Prompt)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": name})
}

// writePeerErr maps a peer rejection (session_peer.go) onto its HTTP status. 4xx across
// the board: every one of them is "the caller may not send this", not a server fault.
// The rate/duplicate pair gets 429 so a sender can tell "slow down" from "malformed".
func writePeerErr(w http.ResponseWriter, err error) {
	rej, ok := err.(*peerRejection)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "peer_rejected", err.Error())
		return
	}
	status := http.StatusBadRequest
	switch rej.Code {
	case "peer_rate_limited", "peer_duplicate":
		status = http.StatusTooManyRequests
	case "peer_from_forbidden", "peer_target_forbidden":
		status = http.StatusForbidden
	case "peer_from_unknown", "peer_target_unknown":
		status = http.StatusNotFound
	}
	httpx.WriteErr(w, status, rej.Code, rej.Msg)
}

// handleManagedInputPrompt is /input's {prompt} counterpart to handleManagedTurn's
// start op (session_turn.go) — same ThreadHandle.Send delivery, but keeps /input's
// report_to contract (addInstruction / recordOperatorInjection) that /turn doesn't
// carry, so send_to_session's docs/30 auto-report keeps working for managed sessions.
func handleManagedInputPrompt(w http.ResponseWriter, meta session.Meta, prompt, reportTo, source, peerFrom string) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "empty_prompt", "prompt, keys or seq is required")
		return
	}
	if st := promptBlocker(meta.Name); st != "" {
		writeBlockedErr(w, st)
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
	// TUI 側と同じ理由で、由来は送る前に記録する（recordInjection のコメント）。managed は
	// Send が返った時点でストアに user ターンが載っているので、後ろに置くと隙間は tmux 経路
	// より短いだけで同じ形で開く。
	if src := badgeOriginOf(peerFrom, reportTo, source); src != "" {
		recordInjection(meta.Name, prompt, src)
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
	switch {
	case peerFrom != "":
		// 台帳には載せない（ADR 0041 決定4）。由来は上で記録済み。
	case reportTo != "":
		addInstruction(meta.Name, reportTo, injectionSource(source))
	case scheduleInjectionSource(source) != "":
		// 報告 OFF の定時実行（TUI 側と同じ）— 台帳も Discord も無し。
	default:
		go bridge.MirrorUserInput(meta.Name, prompt) // docs/37 Fix ②: Console-input mirror
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": meta.Name})
}

// submitPromptTUI is the shared {prompt}→TUI delivery behind /input's {prompt} path
// and /turn's tui start/steer route. Guards, types + submits, and marks working;
// on failure the HTTP error is already written and false is returned.
func submitPromptTUI(w http.ResponseWriter, name, pane, prompt string) bool {
	// While ANY interaction is pending (question / plan approval / permission), the TUI
	// is showing a modal that swallows typed text and lets the trailing Enter confirm
	// its highlighted row — a wrong answer, a silent plan approval, or a silent 許可.
	// Reject it for every prompt sender (Console composer, SendSelectionModal, memo /
	// scheduler injections, MCP drive tools); decisions must go through {keys}/{seq}
	// (tui), /plan-respond (plan) or /respond (managed). See promptBlocker.
	if st := promptBlocker(name); st != "" {
		writeBlockedErr(w, st)
		return false
	}
	// agy: the "Signing in..." boot screen eats typed text entirely (docs/32) — a
	// send_to_session right after create (no initial_prompt) would vanish. Its
	// composer footer is persistent once drawn ("? for shortcuts" idle / "esc to
	// cancel" working), so an empty paneMode here means still booting: wait briefly.
	// Cap ~15s (slow sign-in) then proceed best-effort; a pending question/permission
	// was already rejected above, so this can't stall on a widget.
	// copilot: 同型 — trust 事前追記済みでもタブ UI の描画までコンポーザは無く、
	// ブート画面に送った文字は無音で消える（0b0a07f の教訓）。フッタが readiness。
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

// rememberCodexTUIMode persists mirror-driven TUI mode changes. Codex 0.145.0
// removed the "Plan mode" label from the composer footer, so pane scraping can
// still prove readiness but cannot distinguish Default from Plan. The mirror owns
// these two inputs (/plan to enter, BTab to leave/toggle), making meta the reliable
// desired-next-turn state. A user toggling directly in the terminal remains
// invisible on 0.145+ because the upstream TUI exposes no textual state signal.
func rememberCodexTUIMode(name, prompt string, keys []string) {
	meta, ok := session.ReadMeta(name)
	if !ok || meta.Kind != session.KindCodex || meta.DriverKind() == session.DriverManaged {
		return
	}
	mode := ""
	switch {
	case strings.TrimSpace(prompt) == "/plan":
		mode = "plan"
	case len(keys) == 1 && keys[0] == "BTab":
		if meta.Mode == "plan" {
			mode = "normal"
		} else {
			mode = "plan"
		}
	}
	if mode != "" && meta.Mode != mode {
		meta.Mode = mode
		session.WriteMeta(meta)
	}
}

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
	if metaOK && base.logs != nil {
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

// promptFreeStates are the ONLY states in which a session may be handed free text:
// idle (the composer owns the keystrokes — a new turn) and working (steering the
// running turn, which every sender from the Console composer to the scheduler relies
// on). Everything else is a MODAL the TUI drives by row selection.
var promptFreeStates = map[string]bool{"idle": true, "working": true}

// promptBlocker returns the interactive state that must be decided before free text
// may be delivered, or "" when the composer is free. Unknown sessions and an
// unreadable state read as free (delivery then fails on its own terms).
//
// WHITELIST, not a denylist. In every non-free state the TUI is showing a menu whose
// highlighted row a typed line + Enter CONFIRMS — the text is swallowed and the Enter
// decides for the user:
//   - question:   the modal ignores typed text and Enter picks the FIRST option
//     (v2.1.204 実測, docs/dev/92) — a silent wrong answer.
//   - plan:       Enter confirms the first row of the ExitPlanMode dialog, which is
//     always an approval — a prompt sent here SILENTLY APPROVES the plan. Decide it
//     from the plan card / plan-respond instead.
//   - permission: Enter confirms 許可 — a silent tool approval (docs/32 §agy に同型の実測)。
//
// A denylist (the previous `state == "question"` check) let plan and permission
// through for claude even though the same accident applies, and any state added
// upstream would silently join them. Anything not on the whitelist therefore blocks.
func promptBlocker(name string) string {
	meta, ok := session.ReadMeta(name)
	if !ok {
		return ""
	}
	// agy has no status hooks — its pending interactive prompt is detected via the
	// conversation-DB probe instead (pending.go). It reports ONLY the blocking states
	// ("question" / "permission"), so an empty probe means "nothing pending".
	if meta.Kind == session.KindAgy {
		st, _ := agy.Probe(meta)
		return st
	}
	// copilot: hooks 無し — events.jsonl の未完了 permission.requested が保留の
	// 唯一のソース（tui の許可メニュー / managed の Interaction 双方を同じ形で拾う）。
	if meta.Kind == session.KindCopilot {
		return blockingState(copilot.LiveState(meta))
	}
	// kiro: hooks 無し — "question"（承認待ち「shell requires approval」）は driveState が
	// 返すだけで status ストアに書かれないため、下の汎用フォールバック（status.Read）は
	// kiro では常に false。TUI 経路で承認パネル中に自由文を送ると素通しになる穴なので、
	// TUI 文字列を直接読んでガードする（copilot 同型。managed は ErrQuestionPending で
	// 別途ガード済み）。
	if meta.Kind == session.KindKiro {
		return blockingState(kiro.LiveState(meta))
	}
	// claude: 資格情報が切れているなら、どのモーダルより先にこれで断る（docs/47 §4-8）。
	// TUI は文字を受け取り Enter も通るのに**ターンが 1 つも始まらない**ので、送信側から
	// 見ると成功に見え、ミラーにはプロンプトが 反映待ち のまま残る（利用者報告 2026-08-14）。
	// 断ることで初めて「認証切れです」と言える。キー操作（{keys}/{seq}）は塞がない —
	// ペインで /login を踏むのは正当な回復手段。
	if normalizeKind(meta.Kind) == session.KindClaude && claude.AuthExpired() {
		return agents.StateAuth
	}
	sid := session.UUID(meta.Dir, name)
	st, ok := status.Read(sid)
	if !ok {
		return ""
	}
	// effectiveModal: a captured question/plan payload wins over the "permission" the
	// tool's own permission_prompt wrote, so the refusal names the modal that is really
	// on screen (plan_pending, not permission_pending) and the operator is pointed at
	// the surface that can actually decide it.
	return blockingState(effectiveModal(sid, st.State))
}

// blockingState maps a live state to "" (free) or the state itself (blocking). An
// empty state is "no opinion" from a kind probe that couldn't tell — free, matching
// driveState's own fallthrough.
func blockingState(state string) string {
	if state == "" || promptFreeStates[state] {
		return ""
	}
	return state
}

// blockedErrCode / blockedErrMessage give each blocking state its own wire code and
// hint. "question_pending" keeps its exact spelling — the Console (err.<code> i18n),
// the CP scheduler and the MCP drive tools all classify on it. New codes need their
// own err.<code> entry in both locale catalogs (docs/28 / ADR0016).
func blockedErrCode(state string) string {
	switch state {
	case "question":
		return "question_pending"
	case "plan":
		return "plan_pending"
	case "permission":
		return "permission_pending"
	case agents.StateAuth:
		return "auth_expired"
	}
	return "interaction_pending"
}

func blockedErrMessage(state string) string {
	switch state {
	case "question":
		return "a question is awaiting an answer; answer it via the question card, not free text"
	case "plan":
		return "a plan is awaiting approval; decide it from the plan card (typed text would be swallowed by the dialog and the Enter would approve it)"
	case "permission":
		return "a permission prompt is awaiting a decision; answer it from the permission card (typed text would be swallowed by the menu and the Enter would allow it)"
	case agents.StateAuth:
		return "the claude login for this workspace has expired; re-authenticate from 設定 > エージェント (a prompt sent now would be accepted by the TUI but never start a turn)"
	}
	return "the session is showing an interactive prompt (" + state + "); answer it from the Console, not free text"
}

// writeBlockedErr answers a free-text send that a pending interaction refuses.
func writeBlockedErr(w http.ResponseWriter, state string) {
	httpx.WriteErr(w, http.StatusConflict, blockedErrCode(state), blockedErrMessage(state))
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
// viewNavKeys are the keys claude's TUI reads as VIEW navigation when no dialog is up:
// ← leaves the conversation for the agents home screen (実測 claude 2.1.220 — the footer
// advertises it as "← for agents"), whose composer reads "describe a task for a new
// session" and CREATES A NEW SESSION when submitted. Neither that screen nor the
// rail-bound agent view delivers to the session, so a stray ← turns every later injection
// into a misdelivery — the 2026-07-30 shape. Inside a dialog the same key is ordinary
// navigation, so the guard only refuses when there is no dialog to drive.
var viewNavKeys = map[string]bool{"Left": true}

// allowViewNavKeys reports whether this key batch may be sent as-is. It only touches tmux
// when the batch actually contains a view-navigation key, so the common answer path
// (Down/Space/Enter) costs nothing.
func allowViewNavKeys(name string, keys []string) bool {
	nav := false
	for _, k := range keys {
		if viewNavKeys[k] {
			nav = true
			break
		}
	}
	if !nav {
		return true
	}
	if m, ok := session.ReadMeta(name); !ok || normalizeKind(m.Kind) != session.KindClaude {
		return true // 他 kind に ← の意味は無い（この罠は claude の TUI 固有）
	}
	return tmuxx.ModalActive(name) // ダイアログがあるならナビゲーションとして正当
}

func writeViewNavErr(w http.ResponseWriter) {
	httpx.WriteErr(w, http.StatusConflict, "view_nav_key",
		"a bare Left arrow leaves a claude pane's conversation for its agents screen "+
			"(footer: ← for agents), where later input creates a new session or steers an agent "+
			"instead of reaching this one; it is only accepted while a dialog is open")
}

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
	resp := map[string]any{
		"name": name, "kind": meta.Kind, "alive": alive, "status": state,
		"ready": sessionInputReady(meta, alive),
	}
	// A pending AskUserQuestion / ExitPlanMode plan rides along (claude: the
	// hook-captured payloads) so the operator can relay them to the user and act via
	// /answer-question // /plan-respond without scraping the terminal.
	if meta.Kind == session.KindClaude {
		sid := session.UUID(meta.Dir, name)
		if raw, ok := status.ReadPendingQuestion(sid); ok && len(raw) > 0 {
			resp["questions"] = json.RawMessage(raw)
		}
		if plan, ok := status.ReadPendingPlan(sid); ok && plan != "" {
			resp["plan"] = plan
		}
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
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
	// tail=<bytes>: 出力の**末尾**だけを返す（クリップ時は省略マーカーを前置し
	// clipped=true）。呼び手は LLM（MCP get_session_output）— ツール結果は会話の
	// コンテキストに蓄積するので、上限なしの全文は以降の全ターンを高くする。
	tail := 0
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			tail = n
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
		out, clipped := clipOutputTail(gb.String(), tail)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"name": name, "output": out, "cursor": len(td.Turns),
			"status": state, "alive": alive, "clipped": clipped,
		})
		return
	}
	sid := session.UUID(meta.Dir, name)
	lines := claude.TranscriptLines(sid)
	var sb strings.Builder
	cursor := len(lines)
	for i := since; i < len(lines); i++ {
		if t := claude.AssistantText(lines[i]); t != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(t)
		}
		// tail 指定時はページ分割しない（末尾が欲しいのに 1 MiB 毎の前方ページを
		// 歩かせては本末転倒）。TranscriptLines が既に全行をメモリに持っているので、
		// 全量を組み立ててもメモリのオーダーは変わらない。
		if tail <= 0 && sb.Len() > 1<<20 { // cap at 1 MiB — the next poll resumes at i+1
			cursor = i + 1
			break
		}
	}
	out, clipped := clipOutputTail(sb.String(), tail)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"name": name, "output": out, "cursor": cursor,
		"status": state, "alive": alive, "clipped": clipped,
	})
}

// sessionOutputClipNote is prepended to a tail-clipped output. 読み手はオペレーター
// （LLM）なので、切れている事実と全文の在処を本文で伝える。
const sessionOutputClipNote = "【先頭を省略】出力が長いため末尾のみを表示しています（全文は Console のミラーで確認できます）。\n\n"

// clipOutputTail keeps the LAST max bytes of s (rune-safe: 切断点はルーン境界まで
// 前進させる), prepending sessionOutputClipNote when it actually clipped.
// max <= 0 means no clipping.
func clipOutputTail(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	cut := len(s) - max
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return sessionOutputClipNote + s[cut:], true
}

// claude jsonl の読み出し（jsonlByMtime / transcriptRead / jsonlHasConversation /
// transcriptLines / assistantText）は internal/agents/claude の transcript.go へ
// 移設（docs/23 残① Wave F）。
