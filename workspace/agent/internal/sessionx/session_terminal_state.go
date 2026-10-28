package sessionx

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)



// sessionTerminalState detects claude terminal-only states that the chat view can't
// otherwise see — they aren't transcript events or AskUserQuestion tool calls — by
// capturing the pane and matching the CLI's on-screen text. Returns:
//
//	"resume"     — parked at the startup "Resume from summary / Resume full session /
//	               Don't ask me again" menu (a chat user who pressed "Resume and continue" is
//	               stuck here; keystrokes go to the menu, not a prompt).
//	"compacting" — auto-compaction (context compression) is running; the second return
//	               carries the parsed progress bar (nil for the other states).
//	""           — none detected.
//
// Best-effort: the wording is claude-CLI-specific, so a version bump may need the
// match strings updated.
func sessionTerminalState(name string) (string, *compactProgress) {
	pane := tmuxx.SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return "", nil
	}
	out, err := tmuxx.Cmd("capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return "", nil
	}
	return classifyClaudePane(string(out))
}


// codexTerminalState detects codex terminal-only states the chat can't otherwise see,
// the codex counterpart of sessionTerminalState. Returns:
//
//	"update" — parked at the startup update menu (1. Update now / 2. Skip / 3. Skip until
//	           next version). Keystrokes go to the menu, and "Update now" exits the process
//	           (the pane's tmux session dies with it), so the mirror must surface the choice
//	           instead of accepting a prompt.
//	""       — none detected.
//
// The menu is read by codex.PaneScreen, which the free-text gate shares.
func codexTerminalState(name string) string {
	pane := tmuxx.SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return ""
	}
	out, err := tmuxx.Cmd("capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return ""
	}
	if isCodexUpdateMenu(string(out)) {
		return "update"
	}
	return ""
}

// isCodexUpdateMenu matches the update menu in captured pane text. The banner the menu leaves
// above the composer after a choice must not re-trigger the state (codex.PaneScreen).
func isCodexUpdateMenu(s string) bool {
	return codex.PaneScreen(s) == "update"
}


