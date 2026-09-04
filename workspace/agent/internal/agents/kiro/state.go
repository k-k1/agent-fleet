package kiro

// Live state classification (working / question / idle) from the TUI's string contract.
// Measurement settled the open implementation question in docs/log/43 §5-1: the state source
// is an explicit text contract.
//
//   - working : the footer "Kiro is working · Type to steer · Ctrl+S to queue"
//   - question: the approval panel "shell requires approval" (plan mode and the like, once
//     trust-all is off) — the permission wait that cursor never exposed is explicit text here
//   - idle    : the placeholder "ask a question or describe a task ↵"
//
// Why not classify from the JSONL tail (cursor's approach): the v2 JSONL has no equivalent of
// a turn_ended marker, so there is no way to confirm the final AssistantMessage really is the
// last one. kiro's TUI strings, by contrast, are fixed explicit phrases (not spinner glyphs),
// which resists version drift and matches the cursor/agy lesson about false idle. On top of
// that the 2.14.1 binary has no Stop hook (measured: the hook triggers are only
// AgentSpawn/PrePrompt/PreToolUse/PostToolUse), so the hook-marker approach (claude's) is not
// available today. When a version drops the strings this returns empty and falls back to
// driveState's generic route (the optimistic working that /input pushes).

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// LiveState classifies the running TUI's state from its visible pane ("" when unknowable: the
// pane could not be captured, or the footer is not drawn yet right after boot).
func LiveState(m session.Meta) string {
	// managed (ACP) has no pane, so the string contract below always returns empty. Without
	// feeding it from the turn state machine, neither the list's chip nor the reaper's
	// classification appears (driver.go managedLiveState).
	if m.DriverKind() == session.DriverManaged {
		return managedLiveState(m)
	}
	return classifyPane(tmuxx.CapturePane(session.TmuxName(m.Name)))
}

// approvalDetail returns, in one line, what the TUI's approval panel is asking to approve (for
// the carry-forward in docs/log/75 P5). "" when nothing is waiting for approval.
//
// It returns the line containing the contract phrase verbatim (something like "shell requires
// approval"). The panel's layout moves between versions, so the whole line is carried and NOT
// interpreted: what the carry-forward card shows is a fact, not a structured set of choices
// (the place to answer yes or no is gone along with the pane).
func approvalDetail(m session.Meta) string {
	return approvalLine(tmuxx.CapturePane(session.TmuxName(m.Name)))
}

func approvalLine(s string) string {
	if classifyPane(s) != "question" {
		return ""
	}
	for _, ln := range strings.Split(tailLines(s, footerWindow), "\n") {
		if strings.Contains(ln, "requires approval") {
			return strings.TrimSpace(ln)
		}
	}
	return ""
}

// footerWindow bounds classification to the composer footer region (the non-empty lines at the
// bottom). Scanning the whole screen means that when the assistant quotes a contract phrase
// such as "Kiro is working" in its prose, the working verdict persists after the return to
// idle and MarkTurnEnd never fires (no completion report). paneMode (session_io.go) restricts
// itself to paneTail for the same reason. The width is chosen to fit the footer plus the
// approval panel (about 5 lines at most).
const footerWindow = 8

// classifyPane is the pure decision over one captured frame (split out as a pure function so
// it can be tested). It restricts itself to the last few footer lines and then decides in the
// order idle, question, working. The idle placeholder ("ask a question or describe a task")
// appears only in the idle composer and is mutually exclusive with working/question
// (measured), so it comes first: idle is still returned correctly when a quote of a
// working/approval phrase strays into the footer window (the second line of defence for A-3).
// An approval wait replaces the composer, so it never coexists with the idle phrase.
func classifyPane(s string) string {
	if s == "" {
		return ""
	}
	footer := tailLines(s, footerWindow)
	switch {
	case strings.Contains(footer, "ask a question or describe a task"):
		return "idle"
	case strings.Contains(footer, "requires approval"):
		return "question"
	case strings.Contains(footer, "Kiro is working"):
		return "working"
	}
	return ""
}

// tailLines returns the last n non-empty lines of s (the same shape as paneMode's paneTail).
func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append(out, lines[i])
		}
	}
	return strings.Join(out, "\n")
}
