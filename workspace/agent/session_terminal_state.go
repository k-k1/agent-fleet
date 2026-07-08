package main

import (
	"os/exec"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// sessionTerminalState detects claude terminal-only states that the chat view can't
// otherwise see — they aren't transcript events or AskUserQuestion tool calls — by
// capturing the pane and matching the CLI's on-screen text. Returns:
//
//	"resume"     — parked at the startup "Resume from summary / Resume full session /
//	               Don't ask me again" menu (a chat user who pressed 再開して続ける is
//	               stuck here; keystrokes go to the menu, not a prompt).
//	"compacting" — auto-compaction (context compression) is running.
//	""           — none detected.
//
// Best-effort: the wording is claude-CLI-specific, so a version bump may need the
// match strings updated.
func sessionTerminalState(name string) string {
	pane := tmuxx.SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return ""
	}
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return ""
	}
	s := string(out)
	switch {
	case strings.Contains(s, "Resume from summary") || strings.Contains(s, "Resume full session"):
		return "resume"
	case strings.Contains(s, "Compacting"):
		return "compacting"
	default:
		return ""
	}
}
