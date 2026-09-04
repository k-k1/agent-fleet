package agy

// Answers agy's workspace trust prompt ("Do you trust the contents of this project?")
// over the PTY. trust.go owns the settings.json side (stopping it from appearing at all);
// this file owns the answer for when it appears anyway.
//
// Never assume "Yes is the default, so sending Enter is enough". Upstream reorders the
// choices without warning: in claude 2.1.248 the same dialog flipped (2.1.247: `❯ 1. Yes, I
// trust this folder / 2. No, exit` -> 2.1.248: `❯ No, exit / Yes, I trust this folder`), and
// a blind Enter meant "exit" instead of "approve" from that day on. The CLI exits at once, so
// to the caller it just looks stuck on an empty frame (PR #229). agy had the same blind
// keystroke in three more places: login / usage / context.
//
// Frame measured on 1.1.22 (on the runner, tmux capture-pane; the marker is the ASCII "> "):
//
//	Accessing workspace:
//	/tmp/tmp.aGbfGwpLSk
//	Do you trust the contents of this project?
//	Antigravity CLI requires permission to read, edit, and execute files here.
//	> Yes, I trust this folder
//	  No, exit
//	  ↑/↓ Navigate · enter Confirm
//
// Yes is still first today, but nothing here depends on that: the row is chosen by its text,
// not its position, and when it cannot be reached the whole frame becomes an error.

import (
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

const (
	// trustPrompt is the prompt's question line (the screen's identity).
	trustPrompt = "Do you trust the contents of this project?"
	// trustYesLabel is the prefix of the accepting row. A prefix, not the whole label, so
	// a rewording on the order of "this folder" / "this project" does not break it.
	trustYesLabel = "Yes, I trust"
	// trustAnswerTries bounds the move-and-recheck loop. With two choices one move is
	// enough, so the rest is slack for the redraw (400ms x 6 ~= 2.4s).
	trustAnswerTries = 6
)

var (
	trustRe = regexp.MustCompile(regexp.QuoteMeta(trustPrompt))
	// The marker at the head of the selected row. 1.1.22 uses the ASCII "> " (unselected
	// rows are indented by two spaces). Both markers are matched so a switch to ❯ upstream
	// still reads. A row with an empty label (the main screen's "> " input line) is not a
	// choice, so `\S` is required to reject it.
	trustMarkedRe = regexp.MustCompile(`^\s*[>❯]\s+(\S.*?)\s*$`)
)

// trustSelected returns the label of the currently highlighted row of the trust
// prompt as rendered in out (the CUMULATIVE agents.Flow.Clean() buffer), or "" when
// no highlighted row can be read.
//
// Because the buffer is cumulative, only the text after the LAST occurrence of the question
// line counts (the same "trust only the latest render" shape as parseUsage / parseContext).
// Ink redraws just the choice rows and appends them when the selection moves, so scanning
// that region BACKWARDS makes the first marker row found the current selection.
func trustSelected(out string) string {
	i := strings.LastIndex(out, trustPrompt)
	if i < 0 {
		return ""
	}
	lines := strings.Split(out[i:], "\n")
	for k := len(lines) - 1; k >= 0; k-- {
		if m := trustMarkedRe.FindStringSubmatch(lines[k]); m != nil {
			return m[1]
		}
	}
	return ""
}

// trustFrame returns the tail of out from the last trust prompt on, for error messages:
// without seeing what was on screen, the next upstream change goes unnoticed.
func trustFrame(out string) string {
	if i := strings.LastIndex(out, trustPrompt); i >= 0 {
		return out[i:]
	}
	// When even the question line changed, show the tail. Cut on a line boundary so it never
	// splits a UTF-8 sequence.
	if len(out) > 800 {
		out = out[len(out)-800:]
		if k := strings.IndexByte(out, '\n'); k >= 0 {
			out = out[k+1:]
		}
	}
	return out
}

// trustAction is what to do with the prompt as currently rendered.
type trustAction int

const (
	trustUnreadable trustAction = iota // the selected row cannot be read -> fail, press nothing
	trustConfirm                       // sitting on the Yes row -> Enter
	trustMove                          // sitting on another row -> move with ↓
)

// trustStep decides the next keystroke from the accumulated PTY buffer. Pure: this is the
// body of the fix for the blind keystroke, split out so it can be verified without a real CLI.
func trustStep(out string) trustAction {
	switch sel := trustSelected(out); {
	case sel == "":
		return trustUnreadable
	case strings.HasPrefix(sel, trustYesLabel):
		return trustConfirm
	default:
		return trustMove
	}
}

// answerTrustPrompt accepts the workspace-trust prompt on f by moving the
// selection onto the "Yes, I trust …" row and only then pressing Enter. It never
// presses Enter on a row it has not read.
func answerTrustPrompt(f *agents.Flow) error {
	for i := 0; i < trustAnswerTries; i++ {
		switch trustStep(f.Clean()) {
		case trustConfirm:
			_, err := f.Ptmx.Write([]byte(keyEnter))
			return err
		case trustMove:
			if _, err := f.Ptmx.Write([]byte(keyDown)); err != nil {
				return err
			}
		}
		// trustUnreadable waits here and re-reads too. WaitFor returns the instant the question
		// line appears, so the choice rows may not have arrived yet - that is "not drawn yet",
		// not "the shape changed". Either way the point is to press nothing.
		time.Sleep(400 * time.Millisecond)
	}
	return errString("agy の信頼プロンプトで「" + trustYesLabel + "」の行を選べませんでした" +
		"（選択肢の形が変わった可能性）:\n" + trustFrame(f.Clean()))
}
