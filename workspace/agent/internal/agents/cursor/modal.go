package cursor

// The TUI's two modals, read off the pane. Neither leaves anything in the JSONL while it
// waits (measured 2026.09.28): a Shell row lands only after the approval, and a plan waits
// behind its CreatePlan row with the turn still open. A pasted line + Enter confirms the
// highlighted first row of both — the command ran, the plan was built — so the free-text gate
// (promptBlocker) has to see them, and the pane is the only place they exist.
//
// A command outside the allowlist (skip-permissions off, or a plan launch):
//
//	$  touch out3.txt in .
//	Run this command?
//	Not in allowlist: touch
//	 → Run (once) (y)
//	   Add Shell(touch) to allowlist? (tab)
//	   Run Everything (shift+tab)
//	   Skip & tell the agent what to do instead (esc or n)
//
// Esc there opens "Tell the agent what to do instead (Enter to send, empty to skip, Esc to
// cancel)": the same approval, still undecided. A plan launch ends its plan on:
//
//	Ready to build?
//	 → 1. Yes, build locally (b)
//	   2. No, propose changes (p or Esc)
//
// When a version rewords these, the read comes back "" and sends pass the way they did before
// this existed; the fixtures in testdata/ are the captures these strings come from.

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// modalWindow bounds the read to the bottom of the pane, where a menu replaces the composer,
// so the assistant quoting "Run this command?" in its prose does not read as the menu. The
// approval menu with the footer under it is 9 non-empty lines.
const modalWindow = 12

// paneModal classifies one captured frame: "permission" (a command approval), "plan" (the
// build approval) or "". Each needs its title and one of its own rows inside the window.
func paneModal(s string) string {
	tail := tailLines(s, modalWindow)
	switch {
	case strings.Contains(tail, "Run this command?") && strings.Contains(tail, "Skip & tell the agent what to do instead"):
		return "permission"
	case strings.Contains(tail, "Tell the agent what to do instead") && strings.Contains(tail, "empty to skip"):
		return "permission"
	case strings.Contains(tail, "Ready to build?") && strings.Contains(tail, "build locally"):
		return "plan"
	}
	return ""
}

// TerminalModal is the modal a cursor Terminal pane shows that typed text would decide
// ("permission" or "plan"), or "". A managed session answers "": its driver refuses free text
// itself (ErrQuestionPending).
func TerminalModal(m session.Meta) string {
	if m.DriverKind() == session.DriverManaged {
		return ""
	}
	return paneModal(tmuxx.CapturePane(session.TmuxName(m.Name)))
}

// terminalPendingModal hands a Terminal pane's modal to the carry-over (docs/log/75 P5). An
// approval carries only the fact of what was asked — its answer dies with the pane — and a
// plan carries its body, which the CreatePlan call it waits behind recorded in the JSONL.
func terminalPendingModal(m session.Meta) (agents.PendingModal, bool) {
	s := tmuxx.CapturePane(session.TmuxName(m.Name))
	switch paneModal(s) {
	case "permission":
		return agents.PendingModal{Kind: "permission", Detail: approvalLine(s)}, true
	case "plan":
		if chatID := ChatID(m); chatID != "" {
			if plan := lastPlan(transcriptPath(m.Dir, chatID)); plan != "" {
				return agents.PendingModal{Kind: "plan", Plan: plan}, true
			}
		}
	}
	return agents.PendingModal{}, false
}

// approvalLine returns the approval's command line as the pane draws it ("$  touch out3.txt
// in ."), stripped of the box it sits in once Esc was pressed. The nearest one above the menu
// wins: the transcript over it also draws "$ …" lines for commands already run. The layout
// moves between versions, so the line is carried whole rather than parsed.
func approvalLine(s string) string {
	for _, ln := range strings.Split(tailLines(s, modalWindow), "\n") { // bottom line first
		if ln = strings.Trim(strings.TrimSpace(ln), "│ "); strings.HasPrefix(ln, "$ ") {
			return ln
		}
	}
	return ""
}

// lastPlan returns the body of the last CreatePlan call in the transcript at path, "" when
// there is none.
func lastPlan(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	plan := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	for sc.Scan() {
		var ln line
		if json.Unmarshal(sc.Bytes(), &ln) != nil || ln.Role != "assistant" {
			continue
		}
		for _, b := range ln.Message.Content {
			if b.Type != "tool_use" || b.Name != "CreatePlan" {
				continue
			}
			var in struct {
				Plan string `json:"plan"`
			}
			if json.Unmarshal(b.Input, &in) == nil && strings.TrimSpace(in.Plan) != "" {
				plan = in.Plan
			}
		}
	}
	return plan
}

// tailLines returns the last n non-empty lines of s, bottom line first.
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
