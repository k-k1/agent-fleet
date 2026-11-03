package sessionx

import (
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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

// claudeBox reads claude's input box for a decision that erases text, so unlike claudeComposer
// (which serves send confirmation and splits at any "❯" or "─") it trusts only the frame claude
// draws: two full-width rules of one width at column 0 (the top one may carry the session's
// title, which claude draws into it for --name: "──── [AF:x] title ─"), the first row inside
// starting with "❯" at column 0,
// every further row indented or blank (measured 2.1.316: continuation and wrapped rows are
// indented two columns). Text a member typed, rules and "❯" included, can only appear inside that
// indentation, so it is never taken for the frame. Anything else is ok=false. above is the first
// non-blank line above the top rule, trimmed.
func claudeBox(captured string) (draft, above string, ok bool) {
	lines := strings.Split(strings.TrimRight(captured, "\n"), "\n")
	bottom := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if boxRule(lines[i]) {
			bottom = i
			break
		}
	}
	top := -1
	for i := bottom - 1; i >= 0; i-- {
		if topRule(lines[i]) {
			top = i
			break
		}
	}
	if top < 0 || bottom-top < 2 || ruleWidth(lines[top]) != ruleWidth(lines[bottom]) {
		return "", "", false
	}
	rows := lines[top+1 : bottom]
	if !strings.HasPrefix(rows[0], "❯") {
		return "", "", false
	}
	for _, r := range rows[1:] {
		if r != "" && !strings.HasPrefix(r, " ") {
			return "", "", false
		}
	}
	draft = strings.TrimPrefix(strings.Join(rows, "\n"), "❯")
	for i := top - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			above = t
			break
		}
	}
	return draft, above, true
}

// boxRule reports a line that is one of claude's input box rules: "─" from column 0 to the end,
// at least minBoxRule wide (claude draws them the pane's full width).
func boxRule(line string) bool {
	return ruleWidth(line) >= minBoxRule && strings.Trim(strings.TrimRight(line, " "), "─") == ""
}

// topRule reports a line that can be the box's top rule: a boxRule, or one with a title drawn
// into it, which still starts and ends with "─" (measured on claude in a Fleet session, which is
// always started with --name: "───────────────────────── [AF:solkwlt] Live check … ─").
func topRule(line string) bool {
	t := strings.TrimRight(line, " ")
	return ruleWidth(line) >= minBoxRule && strings.HasPrefix(t, "─") && strings.HasSuffix(t, "─")
}

func ruleWidth(line string) int {
	return utf8.RuneCountInString(strings.TrimRight(line, " "))
}

const minBoxRule = 20

// composerEmpty reports that the pane shows claude's input box and nothing is typed in it.
// An unreadable pane or one with no box on screen is not empty: the reclaim must not run
// when it cannot tell a member's own draft from a restored prompt.
func composerEmpty(captured string) bool {
	draft, _, ok := claudeBox(captured)
	return ok && squashSpace(draft) == ""
}

// restoredDraft reports that the pane's input box holds exactly prompt (whitespace aside: the
// box wraps at the pane width and indents continuation rows). A false positive erases text
// someone typed in the terminal, so a box scrolled off screen, a paste placeholder or a partial
// match is never taken for the prompt; such a prompt is left where claude put it and the next
// send is refused instead (strandedPrompts).
func restoredDraft(captured, prompt string) bool {
	draft, _, ok := claudeBox(captured)
	d := squashSpace(draft)
	return ok && d != "" && d == squashSpace(prompt)
}

// reclaimInterruptedPrompt takes a stopped prompt that claude put back into its input box out
// again and returns its text, or "" when nothing was taken. emptyBefore is composerEmpty of
// the pane read before the stop keys: text already in the box then was typed by someone, and
// it is left alone. Text the stop left in the box that could not be taken out marks the session
// in strandedPrompts. The caller holds the session's terminal lock (lockTerminal).
func reclaimInterruptedPrompt(m session.Meta, pane string, emptyBefore bool) string {
	if !emptyBefore || !restoresInterruptedPrompt(m.Kind) {
		return ""
	}
	// The prompt is read on every poll, not once: claude logs the stopped prompt's user line
	// about 200-250 ms after the Enter (measured 2.1.317), and a stop sent right after a send
	// reads the transcript before that, finding the previous prompt or none.
	for waited := time.Duration(0); waited < reclaimWindow; waited += reclaimPoll {
		reclaimSleep(reclaimPoll)
		prompt := interruptedPrompt(m)
		if strings.TrimSpace(prompt) == "" || !restoredDraft(reclaimCapture(pane), prompt) {
			continue
		}
		if clearComposer(pane, prompt) {
			return prompt
		}
		log.Printf("interrupt: %s: the stopped prompt came back into the input box and could not be cleared", m.Name)
		break
	}
	if !composerEmpty(reclaimCapture(pane)) {
		strandedPrompts.Store(m.Name, struct{}{})
	}
	return ""
}

// strandedPrompts holds the sessions whose input box a stop left holding text it could not take
// out: a prompt shown as a paste placeholder or scrolled, one that would not clear. The next send
// would be typed after it and both would run as one prompt (issue #3290), so submitPromptTUIRev
// refuses until the box reads empty.
var strandedPrompts sync.Map

// strandedPromptBlocks reports that a send to name must be refused: a stop left text in the
// input box and the box does not read empty yet. Reading it empty lifts the mark.
func strandedPromptBlocks(name, pane string) bool {
	if _, marked := strandedPrompts.Load(name); !marked {
		return false
	}
	if composerEmpty(reclaimCapture(pane)) {
		strandedPrompts.Delete(name)
		return false
	}
	return true
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
	_, above, _ := claudeBox(captured)
	if !restoredDraft(captured, prompt) || above != clearHint {
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

const (
	// sendLockWait bounds how long a send waits for a stop to finish with the pane: a stop's
	// keys, reclaim window and clear take under 2 s.
	sendLockWait = 3 * time.Second
	// stopLockWait bounds how long a stop waits for a send: a send holds the pane through its
	// guards, typing and submit delay (AGENT_INPUT_SUBMIT_DELAY_MS, up to 5 s). A send still
	// waiting out a booting CLI's composer (up to 15 s) outlasts it, and the stop is refused.
	stopLockWait = 10 * time.Second
)

// terminalLocks holds one mutex per session name (lockTerminal).
var terminalLocks sync.Map

// lockTerminal serialises what types into a Terminal pane's input box: a send's guards and
// typing, and a stop's keys and reclaim. Interleaved, a send typed between the stop and the
// clear lands after the restored prompt, or the clearing Esc lands in the middle of the send.
// It returns the unlock, or ok=false when the other party held the pane past wait; the caller
// then must not type or send keys.
func lockTerminal(name string, wait time.Duration) (unlock func(), ok bool) {
	v, _ := terminalLocks.LoadOrStore(name, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	for waited := time.Duration(0); ; waited += reclaimPoll {
		if mu.TryLock() {
			return mu.Unlock, true
		}
		if waited >= wait {
			return nil, false
		}
		reclaimSleep(reclaimPoll)
	}
}
