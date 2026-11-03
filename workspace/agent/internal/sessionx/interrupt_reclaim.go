package sessionx

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// A claude prompt stopped before its first streamed byte comes back into the input box; the
// next send is then typed after it and both run as one prompt (issue #3290). Measured on claude
// 2.1.316 with a fake API server: the text reappears about 200 ms after the Esc, a stop after
// the first byte leaves the box empty, and the transcript keeps the stopped prompt's user line.
const (
	// reclaimWindow is how long after the stop keys the box is watched for the prompt.
	reclaimWindow = time.Second
	reclaimPoll   = 100 * time.Millisecond
	// clearHint is what claude draws after one Esc on a non-empty box (measured 2.1.316). The
	// second Esc is sent only when it is on screen: a double Esc on an EMPTY box opens claude's
	// rewind menu instead.
	clearHint = "Esc again to clear"
)

// reclaimCapture reads the pane; replaceable in tests.
var reclaimCapture = func(pane string) string {
	out, err := tmuxx.Cmd("capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// restoresInterruptedPrompt reports that kind puts a stopped prompt back into its input box.
func restoresInterruptedPrompt(kind string) bool {
	return terminalTraits(kind).InterruptedPrompt != nil
}

// interruptedPrompt is the kind's agents.TerminalTraits.InterruptedPrompt; replaceable in tests.
var interruptedPrompt = func(m session.Meta) string {
	if f := terminalTraits(m.Kind).InterruptedPrompt; f != nil {
		return f(m)
	}
	return ""
}

// reclaimSleep is time.Sleep; replaceable in tests.
var reclaimSleep = time.Sleep

// composerEmpty reports that the pane shows claude's input box and nothing is typed in it.
// An unreadable pane or one with no box on screen is not empty: the reclaim must not run
// when it cannot tell a member's own draft from a restored prompt.
func composerEmpty(captured string) bool {
	draft, _, ok := claudeComposer(captured)
	return ok && squashSpace(draft) == ""
}

// restoredDraft reports that the pane's input box holds exactly prompt (whitespace aside: the
// box wraps at the pane width and indents continuation rows). Deliberately stricter than
// promptDraftVisible, which confirms a send and may err towards true: here a false positive
// erases text someone typed in the terminal, so a box scrolled off screen, a paste placeholder
// or a partial match is never taken for the prompt. Such a prompt is left where claude put it.
func restoredDraft(captured, prompt string) bool {
	draft, _, ok := claudeComposer(captured)
	d := squashSpace(draft)
	return ok && d != "" && d == squashSpace(prompt)
}

// reclaimInterruptedPrompt takes a stopped prompt that claude put back into its input box out
// again and returns its text, or "" when nothing was taken. emptyBefore is composerEmpty of
// the pane read before the stop keys: text already in the box then was typed by someone, and
// it is left alone. The caller holds the session's terminal lock (lockTerminal).
func reclaimInterruptedPrompt(m session.Meta, pane string, emptyBefore bool) string {
	if !emptyBefore || !restoresInterruptedPrompt(m.Kind) {
		return ""
	}
	prompt := interruptedPrompt(m)
	if strings.TrimSpace(prompt) == "" {
		return ""
	}
	for waited := time.Duration(0); waited < reclaimWindow; waited += reclaimPoll {
		reclaimSleep(reclaimPoll)
		if !restoredDraft(reclaimCapture(pane), prompt) {
			continue
		}
		if clearComposer(pane, prompt) {
			return prompt
		}
		log.Printf("interrupt: %s: the stopped prompt came back into the input box and could not be cleared", m.Name)
		return ""
	}
	return ""
}

// clearComposer empties claude's input box with its own "Esc, Esc to clear" and reports
// whether the box reads empty afterwards. Line-wise deletion (C-u) works per wrapped row and
// would leave a long draft half-deleted. Before the second Esc the box must still hold the
// prompt and the hint must be the line right above it, not a match anywhere in the history.
func clearComposer(pane, prompt string) bool {
	if err := sendNamedKeys(pane, []string{"Escape"}); err != nil {
		return false
	}
	reclaimSleep(reclaimPoll)
	captured := reclaimCapture(pane)
	_, above, _ := claudeComposer(captured)
	if !restoredDraft(captured, prompt) || !strings.Contains(above, clearHint) {
		return false
	}
	if err := sendNamedKeys(pane, []string{"Escape"}); err != nil {
		return false
	}
	for i := 0; i < 5; i++ {
		reclaimSleep(reclaimPoll)
		if composerEmpty(reclaimCapture(pane)) {
			return true
		}
	}
	return false
}

// terminalLockWait bounds how long a stop or a send waits for the other to finish with the
// pane: a stop's keys, reclaim window and clear take under 2 s.
const terminalLockWait = 3 * time.Second

// terminalLocks holds one mutex per session name (lockTerminal).
var terminalLocks sync.Map

// lockTerminal serialises what types into a Terminal pane's input box: a send's guards and
// typing, and a stop's keys and reclaim. Interleaved, a send typed between the stop and the
// clear lands after the restored prompt, or the clearing Esc lands in the middle of the send.
// It returns the unlock, or ok=false when the other party held the pane past
// terminalLockWait; the caller then must not type.
func lockTerminal(name string) (unlock func(), ok bool) {
	v, _ := terminalLocks.LoadOrStore(name, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	for waited := time.Duration(0); ; waited += reclaimPoll {
		if mu.TryLock() {
			return mu.Unlock, true
		}
		if waited >= terminalLockWait {
			return nil, false
		}
		reclaimSleep(reclaimPoll)
	}
}
