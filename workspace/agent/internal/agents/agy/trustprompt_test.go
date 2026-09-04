package agy

import (
	"strings"
	"testing"
)

// trustFrame122 is the real 1.1.22 workspace-trust screen (measured 2026-08-28 with
// tmux capture-pane on the runner). The marker is the ASCII "> "; unselected rows are
// indented by two spaces.
const trustFrame122 = `Accessing workspace:
/tmp/tmp.aGbfGwpLSk
Do you trust the contents of this project?
Antigravity CLI requires permission to read, edit, and execute files here.
> Yes, I trust this folder
  No, exit
  ↑/↓ Navigate · enter Confirm
`

// The shape after upstream reorders the choices, as claude 2.1.248 actually did. The
// default is No.
const trustFrameFlipped = `Accessing workspace:
/tmp/x
Do you trust the contents of this project?
Antigravity CLI requires permission to read, edit, and execute files here.
> No, exit
  Yes, I trust this folder
  ↑/↓ Navigate · enter Confirm
`

// TestTrustSelectedReadsTheHighlightedRow: the selected row is read from the marker, not
// from its position.
func TestTrustSelectedReadsTheHighlightedRow(t *testing.T) {
	for name, tc := range map[string]struct{ frame, want string }{
		"1.1.22 as measured (Yes first, the default)": {trustFrame122, "Yes, I trust this folder"},
		"order flipped (No is the default)":           {trustFrameFlipped, "No, exit"},
		"still readable when the marker becomes ❯": {
			strings.Replace(trustFrame122, "> Yes", "❯ Yes", 1), "Yes, I trust this folder"},
	} {
		if got := trustSelected(tc.frame); got != tc.want {
			t.Errorf("%s: trustSelected = %q, want %q", name, got, tc.want)
		}
	}
}

// TestTrustSelectedUsesTheLastRender: agents.Flow.Clean() is a cumulative buffer holding
// every redraw concatenated. When the selection moves with ↓, Ink redraws only the choice
// rows and appends them, so a "> Yes" from an older render must not be picked up - doing so
// reads as "sitting on Yes" and presses Enter on No, which exits.
func TestTrustSelectedUsesTheLastRender(t *testing.T) {
	// A full redraw including the question line arrives afterwards.
	full := trustFrame122 + trustFrameFlipped
	if got := trustSelected(full); got != "No, exit" {
		t.Errorf("full redraw: trustSelected = %q, want %q", got, "No, exit")
	}
	// A partial redraw without the question line (just the two choice rows) arrives afterwards.
	partial := trustFrame122 + "  Yes, I trust this folder\n> No, exit\n"
	if got := trustSelected(partial); got != "No, exit" {
		t.Errorf("partial redraw: trustSelected = %q, want %q", got, "No, exit")
	}
}

// TestTrustSelectedUnreadable: what cannot be read is never treated as read. Sending Enter
// without having read the row is the blind keystroke itself, so this returns "" and makes the
// caller fail.
func TestTrustSelectedUnreadable(t *testing.T) {
	for name, frame := range map[string]string{
		"no prompt at all": "? for shortcuts\n> \n",
		"no marker row":    strings.Replace(trustFrame122, "> Yes", "  Yes", 1),
		"empty":            "",
		// The main screen's input line is just "> ". With no label it is not a choice.
		"a bare > line from the input box": trustFrame122[:strings.Index(trustFrame122, "> Yes")] + "> \n",
	} {
		if got := trustSelected(frame); got != "" {
			t.Errorf("%s: trustSelected = %q, want \"\"", name, got)
		}
	}
}

// TestTrustStep: the body of the fix for the blind keystroke - which frames get an Enter and
// which do not. Runs without a real CLI; it only needs the frame strings.
func TestTrustStep(t *testing.T) {
	for name, tc := range map[string]struct {
		frame string
		want  trustAction
	}{
		"1.1.22 as measured (sitting on Yes)": {trustFrame122, trustConfirm},
		// The shape that did real harm in claude 2.1.248: with No as the default, Enter is
		// forbidden and the selection has to move to Yes with ↓ first.
		"order flipped (sitting on No)":               {trustFrameFlipped, trustMove},
		"after moving to Yes with ↓":                  {trustFrameFlipped + "> Yes, I trust this folder\n  No, exit\n", trustConfirm},
		"the choice rows are not drawn yet":           {trustFrame122[:strings.Index(trustFrame122, "> Yes")], trustUnreadable},
		"the marker changed shape and cannot be read": {strings.Replace(trustFrame122, "> Yes", "* Yes", 1), trustUnreadable},
		"the wording changed too (unknown choices only)": {
			strings.Replace(trustFrame122, "> Yes, I trust this folder", "> Sure, go ahead", 1), trustMove},
	} {
		if got := trustStep(tc.frame); got != tc.want {
			t.Errorf("%s: trustStep = %v, want %v", name, got, tc.want)
		}
	}
}

// TestTrustYesLabelMatchesTheMeasuredFrame: since the row is chosen by its text, that text
// hitting the Yes row of the measured frame is the contract. Once it drifts, every run fails
// with "cannot select".
func TestTrustYesLabelMatchesTheMeasuredFrame(t *testing.T) {
	if sel := trustSelected(trustFrame122); !strings.HasPrefix(sel, trustYesLabel) {
		t.Fatalf("the selected row of the measured frame, %q, is not prefixed by trustYesLabel %q", sel, trustYesLabel)
	}
	if !trustRe.MatchString(trustFrame122) {
		t.Fatalf("trustRe does not match the measured frame")
	}
	// In the flipped frame the selection is not on Yes, so Enter must not be sent as is.
	if strings.HasPrefix(trustSelected(trustFrameFlipped), trustYesLabel) {
		t.Fatal("the flipped frame is misread as having Yes selected")
	}
}
