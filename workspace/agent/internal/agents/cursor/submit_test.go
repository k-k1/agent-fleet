package cursor

import (
	"strings"
	"testing"
)

// The captures are cursor 2026.10.16 panes of one send ("say hello in one word", pasted, 200 ms,
// Enter), on a Free plan: with a named model the TUI took the prompt for about a second and then
// put it back in the composer under an error; with Auto it ran. head and tail are the first and
// last 12 runes of that line without whitespace, the way the sender compares it.
const (
	submitHead = "sayhelloinon"
	submitTail = "lloinoneword"
)

func TestComposerHoldsTheRejectedPrompt(t *testing.T) {
	for file, want := range map[string]bool{
		"pane-submit-typed.txt":     true,  // pasted, Enter not yet pressed
		"pane-submit-rejected.txt":  true,  // handed back with the error row
		"pane-submit-composing.txt": false, // the first second: composer emptied, spinner up
		"pane-submit-working.txt":   false, // Auto: the turn under way
		"pane-submit-answered.txt":  false, // Auto: answered, the prompt only echoed above
		"pane-composer-narrow.txt":  false, // an idle composer
	} {
		if got := ComposerHolds(readPane(t, file), submitHead, submitTail); got != want {
			t.Errorf("%s: ComposerHolds = %v, want %v", file, got, want)
		}
	}
}

// A frame without a composer (a version that draws it differently) is not read as a rejection,
// and an empty prompt matches nothing.
func TestComposerHoldsFailsOpen(t *testing.T) {
	if ComposerHolds("some other screen\nsay hello in one word\n", submitHead, submitTail) {
		t.Error("a screen with no composer read as a held draft")
	}
	if ComposerHolds(readPane(t, "pane-submit-rejected.txt"), "", "") {
		t.Error("an empty prompt read as a held draft")
	}
}

// The model row, the directory row and the error under the composer are not the draft: a prompt
// whose ends the error text happens to contain is not held once the composer is empty.
func TestComposerHoldsIgnoresTheRowsUnderIt(t *testing.T) {
	pane := strings.Replace(readPane(t, "pane-submit-rejected.txt"), "say hello in one word", "", 1)
	if ComposerHolds(pane, "Freeplans", "Auto.") {
		t.Error("the error row under the composer read as the draft")
	}
}

func TestSubmitErrorCarriesTheToastText(t *testing.T) {
	got := SubmitError(readPane(t, "pane-submit-rejected.txt"))
	want := "Error: Named models unavailable Free plans can only use Auto. Switch to Auto or upgrade plans to continue."
	if got != want {
		t.Errorf("SubmitError = %q, want %q", got, want)
	}
	for _, f := range []string{"pane-submit-typed.txt", "pane-submit-working.txt", "pane-submit-answered.txt"} {
		if got := SubmitError(readPane(t, f)); got != "" {
			t.Errorf("%s: SubmitError = %q, want none", f, got)
		}
	}
	// An "Error:" line in the transcript above the composer is prose, not the toast.
	prose := "  Error: something the assistant quoted\n\n  → Add a follow-up\n\n  Auto\n  ~/proj\n"
	if got := SubmitError(prose); got != "" {
		t.Errorf("SubmitError read transcript prose: %q", got)
	}
}

// The placeholder and the working composer's key hint are not a draft. A peer message ends in
// the sender's last line, so a body ending in "stop" or "follow-up" must not match the idle or
// working composer and turn a normal send into a held one.
func TestComposerHoldsIgnoresThePlaceholderAndKeyHint(t *testing.T) {
	for _, f := range []string{"pane-submit-working.txt", "pane-submit-answered.txt", "pane-composer-narrow.txt"} {
		pane := readPane(t, f)
		for _, ends := range [][2]string{{"[agent-fleet", "stop"}, {"[agent-fleet", "follow-up"}, {"Addafollow", "ollow-up"}} {
			if ComposerHolds(pane, ends[0], ends[1]) {
				t.Errorf("%s: ComposerHolds(%q, %q) = true, want false", f, ends[0], ends[1])
			}
		}
	}
	// A real draft still matches, and carries no hint of its own.
	if !ComposerHolds(readPane(t, "pane-submit-rejected.txt"), "[agent-fleet", "lloinoneword") {
		t.Error("the rejected draft no longer matches by its tail")
	}
}

func TestComposerDraftSeesOnlyTypedText(t *testing.T) {
	for file, want := range map[string]bool{
		"pane-submit-typed.txt":     true,
		"pane-submit-rejected.txt":  true,
		"pane-submit-composing.txt": false,
		"pane-submit-working.txt":   false,
		"pane-submit-answered.txt":  false,
		"pane-composer-narrow.txt":  false,
	} {
		if got := ComposerDraft(readPane(t, file)); got != want {
			t.Errorf("%s: ComposerDraft = %v, want %v", file, got, want)
		}
	}
	if ComposerDraft("no composer here\n") {
		t.Error("a screen with no composer read as a draft")
	}
}

// A draft that merely starts with the placeholder's words is still a draft.
func TestComposerDraftKeepsTextThatStartsLikeThePlaceholder(t *testing.T) {
	const typed = "Add a follow-up regression test for this bug"
	for _, f := range []string{"pane-submit-rejected.txt", "pane-submit-typed.txt"} {
		pane := strings.Replace(readPane(t, f), "say hello in one word", typed, 1)
		if !ComposerDraft(pane) {
			t.Errorf("%s: ComposerDraft = false for %q", f, typed)
		}
		if !ComposerHolds(pane, "Addafollow-up", "forthisbug") {
			t.Errorf("%s: ComposerHolds = false for %q", f, typed)
		}
	}
	// Wrapped over two rows, the second row is part of the draft too.
	wrapped := "  → Add a follow-up regression\n  test for this bug\n\n\n  Auto\n  ~/proj\n"
	if !ComposerHolds(wrapped, "Addafollow-up", "forthisbug") {
		t.Error("a wrapped draft starting with the placeholder's words was not held")
	}
	// The first row is exactly the placeholder and the rest follows on the next row.
	broken := "  → Add a follow-up\n  regression test for this bug\n\n\n  Auto\n  ~/proj\n"
	if !ComposerDraft(broken) || !ComposerHolds(broken, "Addafollow-up", "forthisbug") {
		t.Error("a draft whose first row equals the placeholder was read as empty")
	}
}

// PaneWorking is what tells a long turn from an idle composer: the JSONL stays empty until the
// turn ends. pane-working-turn.txt / pane-interrupted.txt are 2026.10.22 captures of one prompt
// before and after Ctrl+C (the prompt comes back into the composer as a draft).
func TestPaneWorking(t *testing.T) {
	for file, want := range map[string]bool{
		"pane-working-turn.txt":    true,  // spinner row with a token count
		"pane-submit-working.txt":  true,  // 2026.10.16: bare spinner row and the ctrl+c hint
		"pane-interrupted.txt":     false, // back on the composer, prompt as a draft
		"pane-submit-typed.txt":    false,
		"pane-submit-answered.txt": false,
		"pane-composer-narrow.txt": false,
	} {
		if got := PaneWorking(readPane(t, file)); got != want {
			t.Errorf("%s: PaneWorking = %v, want %v", file, got, want)
		}
	}
	// Each signal on its own, so removing either branch fails: a version without the hint, and
	// one without the spinner row.
	noHint := "  some output\n\n ⠠⠜ Working  286 tokens\n\n  → Add a follow-up\n\n  Auto\n"
	bare := "  some output\n\n ⠀⠞ Working\n    Tip: Use /plan to plan.\n\n  → Add a follow-up\n\n  Auto\n"
	hintOnly := "  → Add a follow-up      ctrl+c to stop\n\n  Auto\n"
	for name, pane := range map[string]string{"spinner with tokens": noHint, "bare spinner + tip": bare, "hint only": hintOnly} {
		if !PaneWorking(pane) {
			t.Errorf("%s: not recognised as working", name)
		}
	}
	// Spinner-shaped text that is not status: plain prose, an answer that quotes a spinner row
	// far above the composer, and an unsent draft that contains one.
	spin := " ⠠⠜ Working  286 tokens\n"
	for name, pane := range map[string]string{
		"prose":         "  Working  5 tokens\n  → Add a follow-up\n",
		"quoted answer": spin + "  first line of prose\n  second line of prose\n\n  → Add a follow-up\n\n  Auto\n",
		"draft":         "  answer\n\n  → " + "fix this:\n" + spin + "\n  Auto\n",
	} {
		if PaneWorking(pane) {
			t.Errorf("%s: read as working", name)
		}
	}
}
