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
// Only two parts of that line are dependable, and this regex uses exactly those: the
// gerund + "…", and the parenthesised elapsed timer. Everything else rotates:
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
// It must not match the post-turn summary claude leaves in the transcript
// ("✻ Worked for 13m 53s", "✻ Sautéed for 5s · 1 shell still running"): those use a
// past-tense verb with no "…" and no parenthesised timer, so the ellipsis is what
// separates a live turn from a finished one. Anchored at line start (the spinner always
// renders at column 0, so a ≥2-space-indented transcript line that merely quotes a
// spinner — including this file's own examples, when a session is asked to debug the TUI
// — can't match). Best-effort; one captured frame.
var spinnerRe = regexp.MustCompile(`(?m)^\S? ?[\p{L}\p{N}][^\n\x{2026}]*\x{2026} \([^)\n]*[0-9]+(?:h|m|s)\b`)

// spinnerActive reports whether the captured pane text shows a turn actively running —
// either the classic "esc to interrupt" affordance or the live spinner header (see
// spinnerRe).
func spinnerActive(s string) bool {
	return strings.Contains(s, "esc to interrupt") || spinnerRe.MatchString(s)
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
