// Package tmuxx は tmux プロービングの純粋プリミティブ（存在確認・pane 解決・
// pane キャプチャ・生存一覧・pane 種別推定）。package main の session_tmux.go /
// session_io.go からの抽出（docs/23 残① Wave A）。tmux コマンド実行だけを持ち、
// オーケストレーション（起動・メタ・ツールチェーン）は main に残す。依存は
// tmuxx→session の一方向のみ。
package tmuxx

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Cmd is the single funnel for every tmux invocation in the agent (enforced by
// tmux_guard_test.go — exec tmux only through here). It scopes
// the invocation to this instance's tmux server: AF_TMUX_SOCKET=<name> maps to
// `tmux -L <name>`, which beats both the default socket and an inherited $TMUX —
// so a second agent instance (dev / in-container E2E) launched with it can NEVER
// reach the shared production server, even when started from inside one of its
// panes. Unset (production) it is plain `tmux` on the default socket.
//
// Background: a test instance's shutdown once ran `tmux kill-server` against the
// shared default socket and took down every live session in the workspace
// (docs/32 M1 E2E incident, 2026-07-20). Socket scoping here plus the
// owned-sessions-only shutdown (shutdown.go) are the two halves of the fix.
func Cmd(args ...string) *exec.Cmd {
	if s := os.Getenv("AF_TMUX_SOCKET"); s != "" {
		return exec.Command("tmux", append([]string{"-L", s}, args...)...)
	}
	return exec.Command("tmux", args...)
}

// spinnerRe matches claude's live working-spinner header — the "<glyph> <Gerund>…
// (<elapsed> · …)" line that ticks while a turn is in flight, e.g.
//
//	✢ Tempering… (6s · thinking with high effort)
//	✽ Perusing… (5m 42s · ↓ 17.8k tokens · thought for 3s)
//	✢ Adding regression tests… (13m 31s · ↓ 48.5k tokens)
//
// Only the gerund + "…" is truly dependable; this regex adds the parenthesised elapsed
// timer, which is dependable only while the pane is wide enough to hold it (see
// spinnerThinkingRe for the narrow-pane frame that has no timer). Everything else rotates:
//
//   - "esc to interrupt" is swapped out for a random Tip (hence the separate check in
//     spinnerActive, which still honours it on older builds);
//   - the "· ↓ <n> tokens" segment is NOT always there. It appears only once output
//     tokens have accrued, so a turn that is still thinking renders bare
//     "(6s · thinking with high effort)". Requiring "tokens" (as we did) therefore
//     false-idles the whole thinking phase — and a short turn start to finish;
//   - the timer is not even first inside the parens: while a hook runs, claude renders
//     "… (running stop hook · 6s · ↓ 279 tokens)" (found by the live contract probe,
//     tui_contract_test.go). Hence [^)\n]* before the timer.
//   - "shift+tab to cycle" stays visible mid-turn, so it can't tell busy from idle.
//
// The gerund is NOT a single word, and it is NOT even constrained in its characters: when
// a todo is in progress claude renders that item's activeForm as the spinner phrase, and
// an activeForm is arbitrary user-authored text. Real captures have shown it to be a
// multi-word phrase ("✢ Adding regression tests…"), Japanese ("· 検証ハーネスを作成中…",
// claude_sdfruv7), and parenthesised ("* nativeRuntime アダプタ実装 (AF_RUNTIME=native)…",
// claude_sx5m7yp). Each of those broke a narrower run in turn — [^\s(]* stopped at the
// first space, and [^(\n\x{2026}]* stopped at the "(" — so the run is now everything up to
// the ellipsis: [^\n\x{2026}]*. Assume nothing about the phrase's contents.
//
// The head is where the remaining discipline lives, and it is deliberately weak: one
// optional glyph, one optional space, then a letter or digit (\p{L}/\p{N}). An earlier
// head demanded [A-Z] or a non-ASCII char, which looked like it screened the phrase but in
// practice was satisfied by the *glyph* (✽ is non-ASCII) — so it went unexercised until a
// frame arrived with an ASCII glyph and a lower-case activeForm ("* nativeRuntime …"),
// which it then read idle. \p{L}\p{N} admits every activeForm we have seen while still
// doing the two jobs the head must do: a ≥2-space-indented transcript quote cannot match
// (^\S? ? absorbs at most one leading space, and the head is not a space), and a source
// line quoting a spinner inside a "//" comment cannot either (the head is not punctuation)
// — which matters because sessions asked to debug the TUI read their own pane.
//
// A slash command running as a todo renders ITS OWN NAME as the activeForm ("· /copyedit
// 02-noir A6… (20m 1s · almost done thinking with high effort)", real capture) — '/' is
// punctuation, so \p{L}\p{N} alone reads that idle for the whole command, same failure
// mode as the ASCII-glyph and CJK cases above. It can't simply join the \p{L}\p{N} class:
// a "//" comment's second slash sits in exactly the position a lone-glyph '/' would
// occupy once \S? backtracks to empty, so a bare-alternative class would match this file's
// own "// ..." examples. The second alternative below requires the glyph AND the space
// both present (\S, not \S?; a literal space, not " ?") before the '/' — a "//" comment
// has no space between its two slashes, so it can only ever feed the '/' to the empty-glyph
// path, which the mandatory-glyph alternative does not accept.
//
// It must not match the post-turn summary claude leaves in the transcript
// ("✻ Worked for 13m 53s", "✻ Sautéed for 5s · 1 shell still running"): those use a
// past-tense verb with no "…" and no parenthesised timer, so the ellipsis is what
// separates a live turn from a finished one. Anchored at line start (the spinner always
// renders at column 0, so a ≥2-space-indented transcript line that merely quotes a
// spinner — including this file's own examples, when a session is asked to debug the TUI
// — can't match). Best-effort; one captured frame.
// spinnerHead is the shared prefix of both spinner patterns: the head discipline above,
// the phrase run, the ellipsis, and the opening paren. What may follow inside the parens
// is what the two patterns disagree about.
const spinnerHead = `^(?:\S? ?[\p{L}\p{N}]|\S /)[^\n\x{2026}]*\x{2026} \(`

var spinnerRe = regexp.MustCompile(`(?m)` + spinnerHead + `[^)\n]*[0-9]+(?:h|m|s)\b`)

// spinnerThinkingRe matches the one live-turn frame that carries NO timer at all:
//
//	✳ Calculating… (almost done thinking with high effort)
//
// (real 60-column capture, claude_s36uuiv — the Console badged that session 入力待ち for
// the whole "almost done thinking" window of a 14-minute turn).
//
// The parenthesised segments are NOT a fixed set that merely gains and loses members with
// the turn's phase — they are laid out against the pane width, and the status phrase wins.
// claude 2.1.246 computes an available width from the columns and the gerund, fits the
// status phrase ("thinking" / "still thinking" / "thinking more" / "thinking some more" /
// "almost done thinking", each + the effort suffix) FIRST, and only then adds the elapsed
// timer if what is left still fits, and the token count after that. So on a narrow pane —
// 60 columns is what the Console gives a session on a phone — a long status phrase alone
// exceeds the budget and the timer is dropped, not the phrase. Requiring the timer (as
// spinnerRe does) therefore false-idles by pane width: the same turn reads busy in a wide
// pane and idle in a narrow one, which is why this survived every earlier fix.
//
// Keyed on "thinking" because that is the only timer-less content the layout can produce:
// every other status ("running tool for 5s", "ran tool for 5s", "thought for 3s") embeds a
// timer of its own and is already spinnerRe's business, and a token count is wider than
// the timer it would have to displace, so it can never be the sole survivor.
//
// Tightened with a ")" end-of-line anchor that spinnerRe does not need: without a timer to
// vouch for it, "…" + "(thinking" is weak enough that col-0 prose could reach it, and the
// cost of a false positive here is a session stuck badged 実行中 (the transcript line does
// not scroll away by itself). The real spinner always ends its line at the closing paren.
var spinnerThinkingRe = regexp.MustCompile(`(?m)` + spinnerHead + `[^)\n]*\bthinking\b[^)\n]*\)[ \t]*$`)

// spinnerActive reports whether the captured pane text shows a turn actively running —
// the classic "esc to interrupt" affordance, or the live spinner header with a timer
// (spinnerRe) or with the timer squeezed out by a narrow pane (spinnerThinkingRe).
func spinnerActive(s string) bool {
	return strings.Contains(s, "esc to interrupt") ||
		spinnerRe.MatchString(s) || spinnerThinkingRe.MatchString(s)
}

// modeFooterRe matches claude's permission-mode footer strip — the "⏸ manual mode on" /
// "⏵⏵ auto mode on" / "⏵⏵ accept edits on" / "⏸ plan mode on" / "⏵⏵ bypass permissions on"
// line drawn under the input box. It is the one part of that strip that is always
// rendered, so it — not the hint that trails it — is what tells us the TUI is sitting at
// its input box.
//
// The trailing hint is contextual and must NOT be relied on: claude 2.1.212 omits
// "(shift+tab to cycle)" entirely in the default (manual) mode, and swaps it for other
// segments when there is background work to report ("⏵⏵ bypass permissions on · 1 shell ·
// ← for agents"). "? for shortcuts", which used to stand in as the default-mode footer,
// is gone from that strip too. Keying idle off those hints (as we did) false-negatives on
// every default-mode session — the stale-status→idle self-heal then never fires and a
// session that is plainly at its prompt stays badged 実行中 with a 停止 bar.
//
// Anchored at line start so prose in the transcript that merely quotes a mode name can't
// match. Both symbols are matched: ⏵⏵ (U+23F5 ×2) for the go-ahead modes, ⏸ (U+23F8) for
// manual/plan.
var modeFooterRe = regexp.MustCompile(`(?m)^\s*(?:\x{23F5}\x{23F5}|\x{23F8}) .*\bon\b`)

// ClaudeModeFooter reports whether s carries claude's permission-mode footer strip,
// regardless of WHICH mode is named. paneMode（session_io.go）の最後の砦で、モード名が
// 増えた／改名されたときに「未描画」と読み違えないためにある — paneMode の空文字は
// launch-seed の readiness ゲートでもあるので、読み違えると初回プロンプトが 30 秒待たされる。
func ClaudeModeFooter(s string) bool { return modeFooterRe.MatchString(s) }

// atPromptFooter reports whether the capture shows claude's input-box footer — the
// permission-mode strip, or the older builds' mode-cycle / shortcuts hints (kept so a
// pinned-older CLI still reads correctly). Modals draw over the strip instead of under
// it: a permission or plan-approval dialog replaces the whole input box + footer, so a
// missing footer is itself a signal that a dialog is up.
func atPromptFooter(s string) bool {
	return modeFooterRe.MatchString(s) ||
		strings.Contains(s, "shift+tab to cycle") ||
		strings.Contains(s, "? for shortcuts")
}

func HasSession(tn string) bool {
	return Cmd("has-session", "-t", session.ExactTarget(tn)).Run() == nil
}

// SessionPaneID returns the active pane id (e.g. "%0") of a session's current
// window, or "" if none. Uses the "=" exact target for list-panes (a target-
// SESSION context, where "=" is honored), then returns the active pane.
func SessionPaneID(tn string) string {
	out, err := Cmd("list-panes", "-t", session.ExactTarget(tn), "-F", "#{pane_active} #{pane_id}").Output()
	if err != nil {
		return ""
	}
	first := ""
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(ln)
		if len(f) != 2 {
			continue
		}
		if first == "" {
			first = f[1]
		}
		if f[0] == "1" {
			return f[1]
		}
	}
	return first // fall back to the first pane if none flagged active
}

// CapturePane returns the session's visible pane text, targeting the active pane by its
// id. NOTE: capture-pane does NOT accept the "=<session>" exact-target syntax that
// send-keys/list-panes take ("can't find pane: =name") — that silently returned empty and
// broke all pane scraping — so we resolve the pane id via SessionPaneID first.
func CapturePane(tn string) string {
	pane := SessionPaneID(tn)
	if pane == "" {
		return ""
	}
	out, err := Cmd("capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// LiveSessionNames returns the set of currently-running claude_* tmux session
// slugs. A missing tmux server / no sessions yields an error, which we treat as
// "none live". Shared by the session list and the branch-switch guard so both
// agree on what "running" means.
func LiveSessionNames() map[string]bool {
	live := map[string]bool{}
	out, err := Cmd("list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return live
	}
	for _, tn := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if tn == "" || !strings.HasPrefix(tn, session.TmuxPrefix) {
			continue
		}
		live[strings.TrimPrefix(tn, session.TmuxPrefix)] = true
	}
	return live
}

// PaneKind sniffs a session's kind from its tmux pane start command. This is a LAST
// RESORT, used ONLY for orphan sessions that have no meta (handleListSessions); a
// session with a meta always takes the recorded meta.Kind and never reaches here.
// The match is a fragile substring test: a claude/shell pane that merely RAN
// `opencode`/`codex` as a command (or a wrapper whose path contains one of these
// words) would misclassify. Kept because an orphan has no other signal, and the only
// cost is a wrong badge on a session that already lost its meta. Defaults to shell.
func PaneKind(name string) string {
	out, err := Cmd("list-panes", "-t", session.ExactTarget(session.TmuxName(name)), "-F", "#{pane_start_command}").Output()
	if err != nil {
		return session.KindShell
	}
	s := string(out)
	switch {
	case strings.Contains(s, "opencode"):
		return session.KindOpencode
	case strings.Contains(s, "codex"):
		return session.KindCodex
	case strings.Contains(s, "claude"):
		return session.KindClaude
	default:
		return session.KindShell
	}
}

// AtIdlePrompt reports whether a claude pane is sitting at its ready input
// prompt — used to self-heal a stale status cache (a killed+resumed session, or a
// rejected permission / abandoned question, where no resolving hook fired). Idle means
// the input-box footer is drawn (atPromptFooter) with no live spinner (spinnerActive) and
// no modal over it ("Enter to select", "Esc to cancel", "Do you want to", …).
// Best-effort TUI read.
func AtIdlePrompt(name string) bool {
	// capture-pane needs a PANE target; the "=name" exact-SESSION form fails with
	// "can't find pane" (same reason send-keys resolves a %N pane id first).
	pane := SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return false
	}
	out, err := Cmd("capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return false
	}
	return atIdlePrompt(string(out))
}

// AgentsViewActive reports whether a claude pane's input box is bound to something
// OTHER than the session's own conversation. Typing there does not reach the session:
//
//   - agent selected in the rail — the input becomes a STEERING message for that
//     background agent, recorded in its own subagents/agent-*.jsonl as "The user sent a
//     new message while you were working:" and never in the session transcript;
//   - agents home screen (opened with ←) — the composer reads "describe a task for a new
//     session" and submitting there CREATES A NEW SESSION.
//
// 実測（制御プローブ・claude 2.1.220 / 2026-07-30, testdata/footers/agents_*）: レールで
// エージェントを選んだ状態でプロンプトを打つと、レール行に "1 queued" が付き、本文は
// エージェントの転写にだけ入って本体の会話には現れなかった。フリートで起きた誤配達
// （sannme2）と同じ形。ペイン上は自分の文字が見えるので、送信側からは成功に見える。
func AgentsViewActive(name string) bool {
	pane := SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return false
	}
	out, err := Cmd("capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return false
	}
	return agentsViewActive(string(out))
}

// railMainUnselectedRe matches the background-agent rail's "main" row when it is NOT the
// selected one. The rail is drawn under the composer as
//
//	  ◯ main
//	❯ ● general-purpose  Sleep then reply DONE   1m 18s · ↓ 16.6k tokens
//
// with the FILLED glyph (U+25CF) on the selected row and a hollow one (U+25EF) elsewhere;
// "❯" marks the cursor while the rail is being navigated. We key on the hollow glyph in
// front of "main" — hollow variants are enumerated so that ANY unknown glyph reads as "not
// in the agents view". That direction matters: a drift then costs us the guard (the old
// behaviour) instead of blocking every legitimate injection.
var railMainUnselectedRe = regexp.MustCompile(`(?m)^\s*(?:\x{276F} )?[\x{25EF}\x{25CB}\x{25CC}] main\s*$`)

// composerBorderRe matches the full-width rule claude draws under its input box. It is the
// structural anchor for reading the footer/rail region: everything after the LAST one
// belongs to the chrome, everything before it is transcript text. Anchoring here rather
// than on the mode-footer strip matters because that strip is REPLACED while the rail is
// being navigated ("↑/↓ to select · Enter to view") — the state the incident passed
// through — and on the agents home screen.
var composerBorderRe = regexp.MustCompile(`(?m)^\s*\x{2500}{8,}\s*$`)

// agentsViewActive is the pure decision over one captured frame.
func agentsViewActive(s string) bool {
	tail := afterLastComposerBorder(s)
	if tail == "" {
		return false // 入力欄が描かれていない（ダイアログ・起動中）— ここでの判断対象外
	}
	// The agents home screen's footer offers "enter to return" (to the conversation);
	// its composer creates a NEW session, so it is just as wrong a place to type into.
	if strings.Contains(tail, "enter to return") {
		return true
	}
	return railMainUnselectedRe.MatchString(tail)
}

// afterLastComposerBorder returns the chrome region: everything below the input box's
// bottom rule (footer / hint / agent rail). "" when the box is not drawn.
func afterLastComposerBorder(s string) string {
	loc := composerBorderRe.FindAllStringIndex(s, -1)
	if len(loc) == 0 {
		return ""
	}
	return s[loc[len(loc)-1][1]:]
}

// LeaveAgentsView tries to put a claude pane's input box back on the session's own
// conversation, and reports whether it ended up there. Both routes are 実測（制御プローブ・
// claude 2.1.220 / 2026-07-30）:
//
//   - rail に紐づいている: ↓ でレール選択が開き、カーソルは常に先頭行 (main) に立つ →
//     Enter で main の表示へ戻り → ↑ でレール選択を閉じる（先頭行での ↑ が抜ける操作）。
//   - agents ホーム画面: Esc で会話へ戻る（画面自身が "esc returns to it" と案内する）。
//
// 下書きが残っているときは何もしない: レール選択中の Enter は「選択を開く」であって
// 送信ではないと確認しているが、表示がドリフトしていた場合に人の書きかけを送信して
// しまう事故は取り返しがつかない。呼び出し側は false を「戻せなかった」として扱い、
// 送信を諦めればよい（黙って誤配達するよりは失敗する方がよい）。
func LeaveAgentsView(name string) bool {
	pane := SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return false
	}
	frame := CapturePane(session.TmuxName(name))
	if !agentsViewActive(frame) {
		return true
	}
	keys := []string{"Down", "Enter", "Up"}
	if strings.Contains(afterLastComposerBorder(frame), "enter to return") {
		keys = []string{"Escape"} // agents ホーム画面
	} else if !composerEmpty(frame) {
		return false
	}
	for _, k := range keys {
		if err := Cmd("send-keys", "-t", pane, k).Run(); err != nil {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
	return !agentsViewActive(CapturePane(session.TmuxName(name)))
}

// composerRuleRe matches EITHER of the input box's rules. The top one carries the title
// ("───── Sleep then reply DONE ──"), so it is not a pure run like the bottom one —
// composerEmpty needs both to bound the region, hence the looser form.
var composerRuleRe = regexp.MustCompile(`(?m)^\s*\x{2500}{4,}.*$`)

// composerEmpty reports whether the input box holds no draft: the composer region (between
// its two rules) is just the bare "❯" prompt.
func composerEmpty(s string) bool {
	loc := composerRuleRe.FindAllStringIndex(s, -1)
	if len(loc) < 2 {
		return false
	}
	region := s[loc[len(loc)-2][1]:loc[len(loc)-1][0]]
	for _, ln := range strings.Split(region, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || t == "\u276f" {
			continue
		}
		return false
	}
	return true
}

// modalMarkers are fragments of the dialogs that claude draws OVER the input box
// (permission prompt, plan approval, folder trust, AskUserQuestion). Matched against a
// width-wrapped capture, so only fragments that survive wrapping are reliable:
// "Would you like to proceed" wraps mid-phrase in an 80-col pane, where the plan dialog is
// caught by "to approve" instead. Mostly redundant now that idle requires the footer strip
// (a dialog replaces it), but kept as defence in depth.
var modalMarkers = []string{
	"Enter to select", "Esc to cancel", "to approve",
	"Do you want to", "Would you like to proceed", "Ready to submit",
}

// ModalActive reports whether a claude pane is showing one of its dialogs (permission,
// plan approval, folder trust, AskUserQuestion). Callers that drive the TUI with named
// keys use it to tell "there is a modal to navigate" from "the keys will hit the plain
// input box", where arrows mean something else entirely (← switches to the agents view).
func ModalActive(name string) bool {
	pane := SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return false
	}
	out, err := Cmd("capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return false
	}
	return modalActive(string(out))
}

func modalActive(s string) bool {
	for _, m := range modalMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// atIdlePrompt is the pure decision over one captured frame — split out from AtIdlePrompt
// (which supplies the frame) so the real judgement, not a reimplementation of it, can be
// replayed against the recorded panes in testdata/footers.
func atIdlePrompt(s string) bool {
	// A live turn's spinner means NOT idle — checked first because the footer strip stays
	// drawn mid-turn, so it alone does not imply the ready prompt.
	if spinnerActive(s) {
		return false
	}
	for _, m := range modalMarkers {
		if strings.Contains(s, m) {
			return false
		}
	}
	return atPromptFooter(s)
}

// rateLimitOptionRe matches the 利用上限メニュー's numbered option lines. claude draws it
// when a turn is cut off by a usage limit:
//
//	  What do you want to do?
//	❯ 1. Stop and wait for limit to reset
//	  2. Ask your admin for more usage
//	  Enter to confirm · Esc to cancel
//
// Keyed on the numbered option line (like resumeMenuRe), NOT on the banner
// "You've hit your session limit …" — that banner is transcript text and STAYS on screen
// after the menu is dismissed, so it would keep reporting a menu that is long gone (the
// trap isCodexUpdateMenu documents). Either option is accepted so a reworded first choice
// does not lose the whole detection, and each is truncated well short of the full sentence
// because the menu wraps in a narrow pane.
//
// 幅依存の脆さは modalMarkers と同じで、行頭アンカーにしているのは repo 自身の散文
// （このコメントを含む）が一致しないようにするため。
var rateLimitOptionRe = regexp.MustCompile(`(?m)^\s*(?:\x{276F} )?\d+\.\s+(?:Stop and wait for limit|Ask your admin for more usage)`)

// AtRateLimitModal reports whether a claude pane is parked on that menu.
//
// なぜ専用の検出が要るか: 上限でターンが切れると claude は Stop hook を鳴らさないので
// status は "working" のまま残る（docs/47）。それを直す唯一の経路は「ペインが待機プロンプト
// に戻っていたら HealIdle」だが、このメニューは "Esc to cancel"（modalMarkers）を必ず含み、
// 入力欄のモード表示フッタごと置き換わるので AtIdlePrompt は恒久的に false を返す。結果、
// セッションが永久に 進行中 に貼り付く（実測 2026-07-31・約16時間・claude 2.1.220）。
//
// Best-effort な TUI 読みなのは他の検出と同じ。取りこぼしたときの損害は「元の貼り付きに
// 戻る」だけで、誤検知しても HealIdle は転写末尾を見てから判断するので、実際には終わって
// いないターンを完了扱いにはしない。
func AtRateLimitModal(name string) bool {
	pane := SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return false
	}
	out, err := Cmd("capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return false
	}
	return atRateLimitModal(string(out))
}

// atRateLimitModal is the pure decision over one captured frame. Both markers are
// required: the option lines say WHICH menu, and the confirm footer says it is still up
// (claude echoes the chosen option into the transcript, so the option text alone can
// linger after the menu is gone — the same reason isCodexUpdateMenu needs two markers).
func atRateLimitModal(s string) bool {
	return strings.Contains(s, "Enter to confirm") && rateLimitOptionRe.MatchString(s)
}

// rateLimitDefaultRe matches the menu's FIRST option while it is the marked one
// ("❯ 1. Stop and wait for limit to reset"). DismissRateLimitModal requires it: Enter
// confirms whatever the cursor stands on, so pressing it blind would pick option 2
// ("Ask your admin for more usage" — a billing request) if a human had already moved
// down. 既定の位置に立っているときだけ押す。
var rateLimitDefaultRe = regexp.MustCompile(`(?m)^\s*\x{276F} 1\.\s+Stop and wait for limit`)

// DismissRateLimitModal confirms the usage-limit menu's DEFAULT option ("1. Stop and
// wait for limit to reset") and reports whether the menu is gone afterwards.
//
// なぜ自動で押してよいか: 選択肢は「リセットを待つ」か「管理者に上限引き上げを依頼する」
// で、課金判断を伴うのは後者だけ。前者は待つ＝何も買わない側で、しかもメニューを人が
// 消すまでセッションは何もできない（実測 約16時間の貼り付き・docs/47 §4-3）。よって
// 既定の 1 を選ぶ操作は「回復」であって「判断の代行」ではない。2 を選ばせたい利用者は
// メニューが出ている間に自分で選べる（この自動解除は 1 が選択された状態のときだけ動く）。
//
// LeaveAgentsView と同じ形: ペインを読んで前提を確かめ、キーを送り、結果をもう一度
// ペインから確かめる。送っただけで成功と見なさないのは、send-keys が 0 を返しても TUI が
// 受け取ったとは限らないため。
func DismissRateLimitModal(name string) bool {
	tn := session.TmuxName(name)
	pane := SessionPaneID(tn)
	if pane == "" {
		return false
	}
	frame := CapturePane(tn)
	if !atRateLimitModal(frame) {
		return true // もうメニューは出ていない
	}
	if !rateLimitDefaultRe.MatchString(frame) {
		return false // 選択が 1 から動いている — 人が選びかけているので触らない
	}
	if err := Cmd("send-keys", "-t", pane, "Enter").Run(); err != nil {
		return false
	}
	time.Sleep(400 * time.Millisecond) // 再描画を待つ（LeaveAgentsView と同じ間合い）
	return !atRateLimitModal(CapturePane(tn))
}

// PaneRead is one capture classified by every predicate the live-state code needs. The
// sessions list polls every few seconds for EVERY session, so asking each predicate
// separately would spend one capture-pane per predicate per session per tick; the pane is
// a single frame, so read it once and judge it three ways. OK=false means the pane could
// not be read (no session / capture failed) — every verdict is then false, which is what
// each individual predicate returns in that case too.
type PaneRead struct {
	Busy          bool // a turn is in flight (IsBusy)
	Idle          bool // sitting at the ready input box (AtIdlePrompt)
	RateLimitMenu bool // parked on the usage-limit menu (AtRateLimitModal)
	OK            bool
}

// ReadPane captures a session's pane once and returns all pane-derived verdicts.
func ReadPane(name string) PaneRead {
	pane := SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return PaneRead{}
	}
	out, err := Cmd("capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return PaneRead{}
	}
	s := string(out)
	return PaneRead{Busy: spinnerActive(s), Idle: atIdlePrompt(s), RateLimitMenu: atRateLimitModal(s), OK: true}
}

// IsBusy reports whether a claude pane is actively running a turn — its transcript shows
// the live spinner header (see spinnerActive), which ticks only while a turn is in flight
// (thinking or a running tool) and is replaced by a past-tense summary the moment the turn
// ends. It's the positive
// inverse of AtIdlePrompt, used to *reverse*-heal a status cache that reads idle while
// the pane is plainly working: the "working" status file can go missing (never written,
// or removed by the working→idle self-heal during a transient prompt frame) and then no
// mid-turn hook rewrites it — in bypass mode MessageDisplay/permtool don't touch status
// — so a busy session would wrongly badge 入力待ち with no stop button until the next
// Stop. Best-effort TUI read; false when the pane can't be read.
func IsBusy(name string) bool {
	pane := SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return false
	}
	out, err := Cmd("capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return false
	}
	return spinnerActive(string(out))
}
