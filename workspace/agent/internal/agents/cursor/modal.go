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
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// paneMenus are the menus by their title and a row of their own. A plan box is read the same
// way: its border is stripped with the rest of the layout (flatten).
var paneMenus = []struct{ title, row, state string }{
	{"Run this command?", "Skip & tell the agent what to do instead", "permission"},
	{"Tell the agent what to do instead", "empty to skip", "permission"},
	{"Ready to build?", "build locally", "plan"},
}

// composerMarks are the composer's placeholders (the same ones PaneMode reads).
var composerMarks = []string{"Add a follow-up", "Plan, search, build anything"}

// paneModal classifies one captured frame of a pane whose CLI runs in one of cwds (the working
// directory as cursor prints it, cwdForms): "permission" (a command approval), "plan" (the
// build approval) or "".
//
// A live menu takes the composer's place at the bottom of the screen, so a frame whose composer
// is still there holds no menu, whatever the prose above it quotes — this very file shown in a
// transcript, say. The composer is recognised by the working directory cursor prints on its last
// lines, which stays when a typed draft replaces the placeholder, and by a placeholder after the
// menu. Of the menus whose title is followed by one of its own rows, the lowest wins: a plan
// that explains the approval menu quotes its words above "Ready to build?".
func paneModal(s string, cwds ...string) string {
	if composerAtBottom(s, cwds) {
		return ""
	}
	flat := flatten(s)
	state, at := "", -1
	for _, m := range paneMenus {
		i := strings.LastIndex(flat, m.title)
		if i <= at {
			continue
		}
		rest := flat[i:]
		if !strings.Contains(rest, m.row) {
			continue
		}
		drawn := false
		for _, c := range composerMarks {
			drawn = drawn || strings.Contains(rest, c)
		}
		if !drawn {
			state, at = m.state, i
		}
	}
	return state
}

// composerAtBottom reports whether the frame ends with one of cwds, the working directory
// cursor prints under its composer. A long path wraps mid-word on a narrow pane (measured), so
// the last lines are joined back, as many as it takes to reach the longest form.
func composerAtBottom(s string, cwds []string) bool {
	longest := 0
	for _, c := range cwds {
		longest = max(longest, len(c))
	}
	var lines []string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			lines = append(lines, ln)
		}
	}
	joined := ""
	for i := len(lines) - 1; i >= 0 && len(joined) < longest; i-- {
		joined = lines[i] + joined
		for _, c := range cwds {
			if c != "" && joined == c {
				return true
			}
		}
	}
	return false
}

// cwdForms are the ways cursor may print the session's working directory under its composer:
// in full, or under $HOME as "~/…" (both measured), for the path as launched and as resolved.
func cwdForms(m session.Meta) []string {
	var forms []string
	home := paths.HomeDir()
	add := func(p string) {
		forms = append(forms, p)
		if home != "" && (p == home || strings.HasPrefix(p, home+"/")) {
			forms = append(forms, "~"+strings.TrimPrefix(p, home))
		}
	}
	cwd := m.CWD()
	add(cwd)
	if real, err := filepath.EvalSymlinks(cwd); err == nil && real != cwd {
		add(real)
	}
	return forms
}

// flatten joins the pane's lines into one, each stripped of its padding and of the border cursor
// draws around a plan. cursor wraps a menu row itself on a narrow pane (measured at 44 columns:
// "Skip & tell the agent what to do" / "instead (esc or n)"), so a phrase is only whole once the
// lines are joined.
func flatten(s string) string {
	var b strings.Builder
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.Trim(ln, " │"); ln != "" {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(ln)
		}
	}
	return b.String()
}

// TerminalModal is the modal a cursor Terminal pane shows that typed text would decide
// ("permission" or "plan"), or "". A managed session answers "": its driver refuses free text
// itself (ErrQuestionPending).
func TerminalModal(m session.Meta) string {
	if m.DriverKind() == session.DriverManaged {
		return ""
	}
	return paneModal(tmuxx.CapturePane(session.TmuxName(m.Name)), cwdForms(m)...)
}

// terminalPendingModal hands a Terminal pane's modal to the carry-over (docs/log/75 P5). An
// approval carries only the fact of what was asked — its answer dies with the pane — and a
// plan carries its body, which the CreatePlan call it waits behind recorded in the JSONL.
func terminalPendingModal(m session.Meta) (agents.PendingModal, bool) {
	s := tmuxx.CapturePane(session.TmuxName(m.Name))
	switch paneModal(s, cwdForms(m)...) {
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
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if ln := strings.Trim(lines[i], " │"); strings.HasPrefix(ln, "$ ") {
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
