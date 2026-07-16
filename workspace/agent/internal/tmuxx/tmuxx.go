// Package tmuxx は tmux プロービングの純粋プリミティブ（存在確認・pane 解決・
// pane キャプチャ・生存一覧・pane 種別推定）。package main の session_tmux.go /
// session_io.go からの抽出（docs/23 残① Wave A）。tmux コマンド実行だけを持ち、
// オーケストレーション（起動・メタ・ツールチェーン）は main に残す。依存は
// tmuxx→session の一方向のみ。
package tmuxx

import (
	"os/exec"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func HasSession(tn string) bool {
	return exec.Command("tmux", "has-session", "-t", session.ExactTarget(tn)).Run() == nil
}

// SessionPaneID returns the active pane id (e.g. "%0") of a session's current
// window, or "" if none. Uses the "=" exact target for list-panes (a target-
// SESSION context, where "=" is honored), then returns the active pane.
func SessionPaneID(tn string) string {
	out, err := exec.Command("tmux", "list-panes", "-t", session.ExactTarget(tn), "-F", "#{pane_active} #{pane_id}").Output()
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
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", pane).Output()
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
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
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
	out, err := exec.Command("tmux", "list-panes", "-t", session.ExactTarget(session.TmuxName(name)), "-F", "#{pane_start_command}").Output()
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
// rejected permission / abandoned question, where no resolving hook fired). The
// mode-cycle footer ("shift+tab to cycle" / "? for shortcuts") shows only at the ready
// prompt; a busy spinner ("esc to interrupt") or any modal ("Enter to select", "Esc to
// cancel", "Do you want to", …) means NOT idle. Best-effort TUI read.
func AtIdlePrompt(name string) bool {
	// capture-pane needs a PANE target; the "=name" exact-SESSION form fails with
	// "can't find pane" (same reason send-keys resolves a %N pane id first).
	pane := SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return false
	}
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return false
	}
	s := string(out)
	for _, busy := range []string{
		"esc to interrupt", "Enter to select", "Esc to cancel", "to approve",
		"Do you want to", "Would you like to proceed", "Ready to submit",
	} {
		if strings.Contains(s, busy) {
			return false
		}
	}
	return strings.Contains(s, "shift+tab to cycle") || strings.Contains(s, "? for shortcuts")
}

// IsBusy reports whether a claude pane is actively running a turn — its footer shows
// the "esc to interrupt" affordance, which appears only while a turn is in flight (a
// thinking spinner or a running tool) and never at the ready prompt. It's the positive
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
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "esc to interrupt")
}
