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
// draws: two rules at column 0 (the bottom one full width; the top one either the same width or
// carrying the session's title, which claude draws into it for --name: "──── [AF:x] title ─"), the
// first row inside starting with "❯" at column 0, every further row indented or blank (measured
// 2.1.316: continuation and wrapped rows are indented two columns). Text a member typed, rules and
// "❯" included, can only appear inside that indentation, so it is never taken for the frame.
// Anything else is ok=false. above is the first non-blank line above the top rule, trimmed.
//
// A titled top rule is not compared in width: the title can hold any text, and its column width
// depends on how tmux draws wide runes, emoji and combining marks. Counting it in runes left a
// Japanese title's box unreadable, so a stop's guard never lifted and every send was refused
// (issue #3408); the "❯" row and the indentation rule are what keep typed text out of the frame.
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
	if top < 0 || bottom-top < 2 || boxRule(lines[top]) && ruleWidth(lines[top]) != ruleWidth(lines[bottom]) {
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
	return boxRule(line) || strings.HasPrefix(t, "─") && strings.HasSuffix(t, "─") && strings.Contains(t, " ")
}

// ruleWidth counts a rule's runes; it is compared only between rules made of "─" alone, which
// tmux draws one column per rune.
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
// someone typed in the terminal, so a box scrolled off screen or a partial match is never taken
// for the prompt; such a prompt is left where claude put it and the next send is refused instead
// (strandedPrompts). A paste placeholder ("[Pasted text #N +M lines]") is deliberately not
// recognised: its line count does not prove who pasted it, and a terminal paste in the watch
// window (the terminal lock does not cover direct input) would be erased.
func restoredDraft(captured, prompt string) bool {
	draft, _, ok := claudeBox(captured)
	d := squashSpace(draft)
	return ok && d != "" && d == squashSpace(prompt)
}

// reclaimInterruptedPrompt takes a stopped prompt that claude put back into its input box out
// again and returns its text, or "" when nothing was taken. before is the pane read before the
// stop keys: text already in the box then was typed by someone (or the box could not be read),
// and it is left alone, but the next send is still guarded (armUnprovenBox). Text the stop left
// in the box that could not be taken out marks the session in strandedPrompts. The caller holds
// the session's terminal lock (lockTerminal).
func reclaimInterruptedPrompt(m session.Meta, pane string, before string) string {
	// A new stop supersedes whatever an earlier one armed. The remembered submission belongs to
	// this stop alone, whatever the stop finds.
	lateWatch.Delete(m.Name)
	unprovenBoxes.Delete(m.Name)
	own := takeSubmitted(m.Name, pane)
	if !restoresInterruptedPrompt(m.Kind) {
		return ""
	}
	if !composerEmpty(before) {
		armUnprovenBox(m.Name, pane, before)
		return ""
	}
	// The prompt is read on every poll, not once: claude logs the stopped prompt's user line
	// about 200-250 ms after the Enter (measured 2.1.317), and a stop sent right after a send
	// reads the transcript before that, finding the previous prompt or none.
	for waited := time.Duration(0); waited < reclaimWindow; waited += reclaimPoll {
		reclaimSleep(reclaimPoll)
		captured := reclaimCapture(pane)
		prompt := matchingPrompt(captured, interruptedPrompt(m), own)
		if prompt == "" {
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
	} else {
		// The box reads empty now, but claude may restore the prompt after the window on a slow
		// host: the next send looks again (lateRestoredBlocks).
		armLateWatch(m.Name, pane, own)
	}
	return ""
}

// submittedPrompt is the text the Agent last typed and submitted into a session's pane.
type submittedPrompt struct {
	pane, text string
	at         time.Time
}

// submittedPrompts holds, per session, the Agent's latest submission (rememberSubmitted). It is
// the fallback source of a stopped prompt for the one case the transcript cannot serve: the
// session's first prompt, which claude has not logged (no transcript file or no user line yet)
// when the stop reads it (issue #3345).
var submittedPrompts sync.Map

// submittedTTL bounds how old a submission may be and still be taken for the prompt a stop
// interrupted; the stop of a turn the member has been watching for longer is not "before the
// first streamed byte".
const submittedTTL = lateEraseTTL

// rememberSubmitted is called after a send was typed and submitted. Entries expire after
// submittedTTL and are swept on the next store, so a deleted session's entry does not outlive it
// by more than that.
func rememberSubmitted(name, pane, text string) submittedPrompt {
	now := time.Now()
	submittedPrompts.Range(func(k, v any) bool {
		if now.Sub(v.(submittedPrompt).at) > submittedTTL {
			submittedPrompts.CompareAndDelete(k, v)
		}
		return true
	})
	sp := submittedPrompt{pane: pane, text: text, at: now}
	submittedPrompts.Store(name, sp)
	return sp
}

// forgetSubmitted drops exactly the entry rememberSubmitted returned when the Enter that would
// have submitted it failed. It compares the whole stored value (time included), so an equal-text
// entry a concurrent send stored meanwhile is never removed.
func forgetSubmitted(name string, sp submittedPrompt) {
	submittedPrompts.CompareAndDelete(name, sp)
}

// takeSubmitted consumes the session's remembered submission and returns its text when it was
// made for this pane and is recent; otherwise "". Consuming it makes a submission serve the
// first stop after it only: a later stop of text the member typed themselves, which happens to
// equal it, never matches.
func takeSubmitted(name, pane string) string {
	v, ok := submittedPrompts.LoadAndDelete(name)
	if !ok {
		return ""
	}
	sp := v.(submittedPrompt)
	if sp.pane != pane || time.Since(sp.at) > submittedTTL {
		return ""
	}
	return sp.text
}

// matchingPrompt picks which known prompt the box holds exactly. The transcript's copy wins when
// it matches (after a completed turn it is the truth); the Agent's own submission only stands in
// when it does not. "" means the box holds neither.
func matchingPrompt(captured string, candidates ...string) string {
	for _, c := range candidates {
		if strings.TrimSpace(c) != "" && restoredDraft(captured, c) {
			return c
		}
	}
	return ""
}

// lateWatchEntry is one armed late-restore check: the pane the stop was sent to and when.
type lateWatchEntry struct {
	pane string
	at   time.Time
	// own is the Agent's submission this stop consumed (takeSubmitted), "" when none.
	own string
}

// lateWatch holds, per session, a stop that ended its watch window with the box empty and no
// prompt taken: claude may still restore the prompt on a slow host. The next send re-reads the
// box (lateRestoredBlocks).
var lateWatch sync.Map

const (
	// lateEraseTTL is how long after the stop an exact copy of the stopped prompt in the box may
	// still be erased; later, the member's own text is as likely.
	lateEraseTTL = 2 * time.Minute
	// lateWatchKeep is when an entry nobody consumed is converted to a stranded mark.
	lateWatchKeep = time.Hour
)

func armLateWatch(name, pane, own string) {
	now := time.Now()
	lateWatch.Range(func(k, v any) bool {
		if now.Sub(v.(lateWatchEntry).at) > lateWatchKeep {
			// Dropping the entry would drop the guard: the prompt may have reappeared since.
			// Hand it to the stranded mark, which holds until the box reads empty. Delete only
			// the value that was read, never a newer entry another goroutine stored.
			strandedPrompts.Store(k, struct{}{})
			lateWatch.CompareAndDelete(k, v)
		}
		return true
	})
	lateWatch.Store(name, lateWatchEntry{pane: pane, at: now, own: own})
}

// lateRestoredBlocks is the send-time half of the reclaim, for a prompt that reappeared after
// the watch window. It runs under the terminal lock and consumes the armed entry. An empty box
// lets the send go on. Text in the box is erased only when it is exactly the stopped prompt
// (the transcript's copy) and the stop is under lateEraseTTL old; any other text — expired, a
// placeholder, scrolled, unreadable, the prompt mixed with more text — marks the session
// stranded and refuses, until the box reads empty (strandedPromptBlocks). An entry for another
// pane is stale (the session was restarted) and is dropped.
func lateRestoredBlocks(name, pane string) bool {
	v, ok := lateWatch.LoadAndDelete(name)
	if !ok || v.(lateWatchEntry).pane != pane {
		return false
	}
	captured := reclaimCapture(pane)
	if composerEmpty(captured) {
		return false
	}
	if m, ok := session.ReadMeta(name); ok && restoresInterruptedPrompt(m.Kind) &&
		time.Since(v.(lateWatchEntry).at) <= lateEraseTTL {
		if prompt := matchingPrompt(captured, interruptedPrompt(m), v.(lateWatchEntry).own); prompt != "" &&
			clearComposer(pane, prompt) {
			return false
		}
	}
	log.Printf("interrupt: %s: the input box holds text after a stop; the send is refused", name)
	strandedPrompts.Store(name, struct{}{})
	return true
}

// unprovenEntry is a stop whose pre-stop box was not provably empty: the pane, and what the box
// held before the keys (baselineOK false when the frame could not be read at all).
type unprovenEntry struct {
	pane, baseline string
	baselineOK     bool
}

// unprovenBoxes holds, per session, a stop on a kind that restores prompts which could not prove
// the box was empty before the keys: a draft someone typed, or a pane claudeBox does not read
// (observed: a stop during a UserPromptSubmit hook, issue #3373). Nothing is erased, since
// whose text it is cannot be told; but returning unguarded let claude's restored prompt sit in
// the box and the next send was typed after it, so both ran as one prompt.
var unprovenBoxes sync.Map

func armUnprovenBox(name, pane, before string) {
	e := unprovenEntry{pane: pane}
	if draft, _, ok := claudeBox(before); ok {
		e.baseline, e.baselineOK = squashSpace(draft), true
	}
	unprovenBoxes.Store(name, e)
}

// unprovenBoxBlocks reports that a send to name must be refused: after a stop that could not
// prove the box empty, the box holds text it did not hold before the keys. The rule is
// "different from before", not "non-empty", so a member's own draft that merely sat there is
// not refused (the send was typed after it before this guard existed, and refusing it forever
// would lock the session on a draft the member chose to keep). When the frame is unreadable
// (the case that armed this) the guard stays: claudeComposer's lax read picks the last "❯" row
// and trims indentation, so it cannot prove text absent (a multiline prompt ending in an
// indented "❯" reads empty). The guard never expires; only the strict claudeBox reading an
// empty box, or one equal to the baseline, lifts it. Trade-off: a pane that stays unreadable
// refuses sends until the box reads empty or a new stop supersedes the mark.
func unprovenBoxBlocks(name, pane string) bool {
	v, ok := unprovenBoxes.Load(name)
	if !ok {
		return false
	}
	e := v.(unprovenEntry)
	lift := func() bool { unprovenBoxes.CompareAndDelete(name, v); return false }
	if e.pane != pane {
		return lift()
	}
	if draft, _, ok := claudeBox(reclaimCapture(pane)); ok {
		d := squashSpace(draft)
		if d == "" || (e.baselineOK && d == e.baseline) {
			return lift()
		}
	}
	log.Printf("interrupt: %s: the input box holds text it did not hold before the stop; the send is refused", name)
	return true
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
