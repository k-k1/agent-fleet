package sessionx

import (
	"log"
	"strings"
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

// reclaimInterruptedPrompt takes a stopped prompt that claude put back into its input box out
// again and returns its text, or "" when nothing was taken. emptyBefore is composerEmpty of
// the pane read before the stop keys: text already in the box then was typed by someone, and
// it is left alone.
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
		if !promptDraftVisible(reclaimCapture(pane), prompt) {
			continue
		}
		if clearComposer(pane) {
			return prompt
		}
		log.Printf("interrupt: %s: the stopped prompt came back into the input box and could not be cleared", m.Name)
		return ""
	}
	return ""
}

// clearComposer empties claude's input box with its own "Esc, Esc to clear" and reports
// whether the box reads empty afterwards. Line-wise deletion (C-u) works per wrapped row and
// would leave a long draft half-deleted.
func clearComposer(pane string) bool {
	if err := sendNamedKeys(pane, []string{"Escape"}); err != nil {
		return false
	}
	reclaimSleep(reclaimPoll)
	if !strings.Contains(reclaimCapture(pane), clearHint) {
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

// interruptSettleLimit bounds how long a send waits for a stop in flight: the stop keys, the
// reclaim window and the clear fit well inside it.
const interruptSettleLimit = 3 * time.Second

// waitInterruptSettled waits until no stop is being sent to the session, at most
// interruptSettleLimit.
func waitInterruptSettled(name string) {
	for waited := time.Duration(0); waited < interruptSettleLimit; waited += reclaimPoll {
		if _, busy := interruptsInFlight.Load(name); !busy {
			return
		}
		reclaimSleep(reclaimPoll)
	}
}
