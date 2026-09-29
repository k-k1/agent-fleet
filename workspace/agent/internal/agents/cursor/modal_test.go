package cursor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func readPane(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The captures are the cursor 2026.09.28 panes of #1227's measurement: a command outside the
// allowlist, the same approval after Esc, and a plan launch's build approval, at 140 columns
// and on a narrow pane, where cursor wraps the rows itself ("Skip & tell the agent what to do"
// / "instead (esc or n)" at 44 columns; the plan box at 30). Each is a decision a pasted line +
// Enter makes silently (the command ran, the plan was built), so each must read as one.
func TestPaneModalReadsMeasuredCaptures(t *testing.T) {
	for file, want := range map[string]string{
		"pane-approval.txt":        "permission",
		"pane-approval-esc.txt":    "permission",
		"pane-approval-narrow.txt": "permission",
		"pane-plan.txt":            "plan",
		"pane-plan-narrow.txt":     "plan",
		"pane-composer-narrow.txt": "",
	} {
		if got := paneModal(readPane(t, file)); got != want {
			t.Errorf("%s: paneModal = %q, want %q", file, got, want)
		}
	}
}

// Outside the menu the same words are prose: the assistant quoting them — this package's own
// source shown in a transcript — or the scrollback of a menu already answered. Reading them as a
// modal refuses every send with no menu to answer, so a menu counts only when no composer is
// drawn after it, however close to the bottom the quote sits.
func TestPaneModalIgnoresTheWordsAboveTheComposer(t *testing.T) {
	composer := readPane(t, "pane-composer-narrow.txt")
	at := strings.Index(composer, "  → Add a follow-up")
	if at < 0 {
		t.Fatal("fixture lost its composer")
	}
	for name, quote := range map[string]string{
		"approval": "  case strings.Contains(tail, \"Run this command?\") &&\n  \"Skip & tell the agent what to do instead\"\n",
		"feedback": "  Tell the agent what to do instead (Enter to send, empty to skip)\n",
		"plan":     "  Ready to build? → 1. Yes, build locally (b)\n",
	} {
		pane := composer[:at] + quote + composer[at:]
		if got := paneModal(pane); got != "" {
			t.Errorf("%s quoted right above the composer: paneModal = %q, want none", name, got)
		}
	}
	for name, pane := range map[string]string{
		"empty capture": "",
		"new chat":      "  → Plan, search, build anything\n\n  Auto\n  /home/dev/repos/proj\n",
	} {
		if got := paneModal(pane); got != "" {
			t.Errorf("%s: paneModal = %q, want none", name, got)
		}
	}
}

// The carried card says what was being approved. The transcript above the menu also draws "$ …"
// lines for commands already run, so the one right above the menu is the one taken.
func TestApprovalLineTakesTheMenusCommand(t *testing.T) {
	for file, want := range map[string]string{
		"pane-approval.txt":        "$  touch out3.txt in .",
		"pane-approval-esc.txt":    "$  touch out3.txt in .",
		"pane-approval-narrow.txt": "$  touch narrow.txt in .",
	} {
		if got := approvalLine(readPane(t, file)); got != want {
			t.Errorf("%s: approvalLine = %q, want %q", file, got, want)
		}
	}
}

// fakePane puts a tmux on PATH whose one session shows the capture in pane (a testdata file,
// or "" for a blank screen). It logs every call, so a test can tell whether the pane was read.
func fakePane(t *testing.T, pane string) (logPath string) {
	t.Helper()
	bin := t.TempDir()
	logPath = filepath.Join(bin, "tmux.log")
	paneFile := filepath.Join(bin, "pane.txt")
	if pane != "" {
		if err := os.WriteFile(paneFile, []byte(readPane(t, pane)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
case "$1" in
  list-panes) printf '1 %%7\n' ;;
  capture-pane) [ -f "` + paneFile + `" ] && /bin/cat "` + paneFile + `" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("AF_TMUX_SOCKET", "")
	return logPath
}

// writeTranscript lays the JSONL rows where the Terminal route's cursor writes them.
func writeTranscript(t *testing.T, m session.Meta, chatID string, rows ...string) {
	t.Helper()
	sids.Write(session.UUID(m.Dir, m.Name), chatID)
	p := transcriptPath(m.Dir, chatID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(rows, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

const (
	userRow      = `{"role":"user","message":{"content":[{"type":"text","text":"<user_query>\nplan it\n</user_query>"}]}}`
	createPlan   = `{"role":"assistant","message":{"content":[{"type":"text","text":"Here is the plan."},{"type":"tool_use","name":"CreatePlan","input":{"name":"Create plan.txt","plan":"# Create plan.txt\n\nWrite hello to plan.txt."}}]}}`
	turnEndedRow = `{"type":"turn_ended","status":"success"}`
)

// While a turn is open the pane decides between "working" and the modal holding it — the list
// badge and the chat chip read this, and they must say what the free-text gate refuses on. A
// closed turn cannot be waiting on a menu, so the idle poll never pays for a capture.
func TestLiveStateReadsTheModalOnlyWhileATurnIsOpen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "cu-modal", Dir: t.TempDir(), Kind: session.KindCursor}

	fakePane(t, "pane-approval.txt")
	writeTranscript(t, m, "11111111-1111-4111-8111-111111111111", userRow)
	if got := LiveState(m); got != "permission" {
		t.Errorf("open turn on the approval menu: LiveState = %q, want permission", got)
	}

	fakePane(t, "pane-plan.txt")
	writeTranscript(t, m, "11111111-1111-4111-8111-111111111111", userRow, createPlan)
	if got := LiveState(m); got != "plan" {
		t.Errorf("open turn on the build approval: LiveState = %q, want plan", got)
	}

	fakePane(t, "")
	if got := LiveState(m); got != "working" {
		t.Errorf("open turn, no menu on screen: LiveState = %q, want working", got)
	}

	logPath := fakePane(t, "pane-approval.txt")
	writeTranscript(t, m, "11111111-1111-4111-8111-111111111111", userRow, turnEndedRow)
	if got := LiveState(m); got != "idle" {
		t.Errorf("closed turn: LiveState = %q, want idle", got)
	}
	if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "capture-pane") {
		t.Errorf("an idle poll captured the pane:\n%s", b)
	}
}

// Folding a Terminal session hands its modal to the carry-over before the pane goes: the fact of
// the approval (its answer dies with the pane), and the plan's body from the CreatePlan call the
// build approval waits behind.
func TestTerminalPendingModalCarriesWhatThePaneShows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "cu-carry", Dir: t.TempDir(), Kind: session.KindCursor}
	writeTranscript(t, m, "22222222-2222-4222-8222-222222222222", userRow, createPlan)

	fakePane(t, "pane-approval.txt")
	pm, ok := agentImpl{}.PendingModal(m)
	if !ok || pm.Kind != "permission" || pm.Detail != "$  touch out3.txt in ." {
		t.Errorf("approval: PendingModal = %+v, %v", pm, ok)
	}

	fakePane(t, "pane-plan.txt")
	pm, ok = agentImpl{}.PendingModal(m)
	if !ok || pm.Kind != "plan" || pm.Plan != "# Create plan.txt\n\nWrite hello to plan.txt." {
		t.Errorf("build approval: PendingModal = %+v, %v", pm, ok)
	}

	fakePane(t, "")
	if pm, ok := (agentImpl{}).PendingModal(m); ok {
		t.Errorf("nothing on screen: PendingModal = %+v, want none", pm)
	}
}

// The gate's probe answers only for the Terminal route; a managed session's driver refuses free
// text itself, and reading a pane it does not have would only cost a tmux call.
func TestTerminalModalLeavesManagedToItsDriver(t *testing.T) {
	logPath := fakePane(t, "pane-approval.txt")
	if got := TerminalModal(session.Meta{Name: "cu-tui", Dir: t.TempDir(), Kind: session.KindCursor}); got != "permission" {
		t.Errorf("Terminal: TerminalModal = %q, want permission", got)
	}
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	managed := session.Meta{Name: "cu-managed", Dir: t.TempDir(), Kind: session.KindCursor, Driver: session.DriverManaged}
	if got := TerminalModal(managed); got != "" {
		t.Errorf("managed: TerminalModal = %q, want none", got)
	}
	if b, _ := os.ReadFile(logPath); len(b) > 0 {
		t.Errorf("managed session ran tmux:\n%s", b)
	}
}
