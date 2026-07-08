package main

// tmux まわりのセッション操作: 起動/再生成、生存一覧、ディレクトリ配下の判定、
// pane の種別/プロンプト状態の推定。session.go からの機械的分割（docs/23 P1-W4）。

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// startSessionTmux launches the detached tmux session for m. For claude it injects
// the OAuth token and builds the resume/new program (buildSessionProgram picks
// --resume once a jsonl exists); for shell it runs a login bash.
func startSessionTmux(m sessionMeta, ssmForce bool) error {
	// The kind decides the pane program and launch dir; the agent builds both.
	plan, err := agentOf(m.Kind).buildLaunch(m, launchOpts{ssmForce: ssmForce})
	if err != nil {
		return err
	}
	// Inject the current toolchain selection (JAVA_HOME / node / TZ) so a Console
	// change applies to this freshly-launched session without a Stop→Start. tmux
	// runs the pane command via /bin/sh -c, so the export prefix takes effect.
	program := toolchainShellPrefix() + plan.program
	args := []string{"new-session", "-d", "-s", tmuxName(m.Name), "-c", plan.cwd, program}
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

// ensureSessionTmux (re)creates the tmux session from its recorded meta when it is
// not currently alive — used on attach so a clicked-but-exited session relaunches
// claude rather than the default shell. Reports whether a meta was found.
func ensureSessionTmux(name string, ssmForce bool) bool {
	if tmuxHasSession(tmuxName(name)) {
		return true
	}
	m, ok := readSessionMeta(name)
	if !ok {
		return false
	}
	_ = startSessionTmux(m, ssmForce)
	return true
}

// liveSessionNames returns the set of currently-running claude_* tmux session
// slugs. A missing tmux server / no sessions yields an error, which we treat as
// "none live". Shared by the session list and the branch-switch guard so both
// agree on what "running" means.
func liveSessionNames() map[string]bool {
	live := map[string]bool{}
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return live
	}
	for _, tn := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if tn == "" || !strings.HasPrefix(tn, tmuxPrefix) {
			continue
		}
		live[strings.TrimPrefix(tn, tmuxPrefix)] = true
	}
	return live
}

// liveSessionsInDir returns the display names of running sessions whose cwd is at
// or under dir. Switching branches in dir would swap the working tree beneath
// these processes mid-flight (vanished/rewritten files, stale diffs, edits landing
// on the wrong branch) — the "大惨事" this guards against — so callers refuse the
// operation while this is non-empty. Only LIVE sessions count: a stopped session
// has no process to corrupt (branch drift for those is handled elsewhere). Archived
// sessions are ignored. A subdir cwd still counts because checkout rewrites the
// whole working tree, not just the repo root.
func liveSessionsInDir(dir string) []string {
	return sessionsInDir(listSessionMetas(), liveSessionNames(), dir)
}

// sessionsInDir is the pure core of liveSessionsInDir (tmux/fs kept out so it is
// testable): from metas + the live set, the display names of running, non-archived
// sessions whose cwd equals dir or sits strictly beneath it. The trailing
// PathSeparator on the prefix test is load-bearing — it keeps "/r/foo" from matching
// a sibling "/r/foobar".
func sessionsInDir(metas []sessionMeta, live map[string]bool, dir string) []string {
	var names []string
	for _, m := range metas {
		if m.Archived || !live[m.Name] {
			continue
		}
		if m.Dir == dir || strings.HasPrefix(m.Dir, dir+string(os.PathSeparator)) {
			names = append(names, sessionDisplay(m))
		}
	}
	sort.Strings(names)
	return names
}

// worktreeHasSessions reports whether ANY session meta (live, stopped, or archived)
// still has its cwd at or under dir. Auto-pruning a worktree checks this first so a
// working copy that a stopped/archived session could still resume or restore into is
// never removed out from under it.
func worktreeHasSessions(dir string) bool {
	for _, m := range listSessionMetas() {
		if m.Dir == dir || strings.HasPrefix(m.Dir, dir+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// paneKind sniffs a session's kind from its tmux pane start command. This is a LAST
// RESORT, used ONLY for orphan sessions that have no meta (handleListSessions); a
// session with a meta always takes the recorded meta.Kind and never reaches here.
// The match is a fragile substring test: a claude/shell pane that merely RAN
// `opencode`/`codex` as a command (or a wrapper whose path contains one of these
// words) would misclassify. Kept because an orphan has no other signal, and the only
// cost is a wrong badge on a session that already lost its meta. Defaults to shell.
func paneKind(name string) string {
	out, err := exec.Command("tmux", "list-panes", "-t", exactT(tmuxName(name)), "-F", "#{pane_start_command}").Output()
	if err != nil {
		return kindShell
	}
	s := string(out)
	switch {
	case strings.Contains(s, "opencode"):
		return kindOpencode
	case strings.Contains(s, "codex"):
		return kindCodex
	case strings.Contains(s, "claude"):
		return kindClaude
	default:
		return kindShell
	}
}

// exactT returns a tmux target that matches NAME exactly. Without the leading '=',
// tmux's -t resolution prefix-matches, so a target like "claude_agent-fleet" would
// match an unrelated "claude_agent-fleet-sh" — wrongly reporting "already running"
// (blocking session creation) or killing the sibling on stop/archive/recreate.
func exactT(tn string) string { return "=" + tn }

func tmuxHasSession(tn string) bool {
	return exec.Command("tmux", "has-session", "-t", exactT(tn)).Run() == nil
}

// sessionAtIdlePrompt reports whether a claude pane is sitting at its ready input
// prompt — used to self-heal a stale status cache (a killed+resumed session, or a
// rejected permission / abandoned question, where no resolving hook fired). The
// mode-cycle footer ("shift+tab to cycle" / "? for shortcuts") shows only at the ready
// prompt; a busy spinner ("esc to interrupt") or any modal ("Enter to select", "Esc to
// cancel", "Do you want to", …) means NOT idle. Best-effort TUI read.
func sessionAtIdlePrompt(name string) bool {
	// capture-pane needs a PANE target; the "=name" exact-SESSION form fails with
	// "can't find pane" (same reason send-keys resolves a %N pane id first).
	pane := sessionPaneID(tmuxName(name))
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
