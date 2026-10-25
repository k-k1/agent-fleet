package sessionx

// Delivery verification (docs/log/38 addendum).
//
// A successful tmux send-keys means only that the keys reached the pane. If the CLI happens
// to be momentarily unable to accept them - right after a resume, before slash commands are
// registered; Enter swallowed by paste folding; a modal ignoring typed characters - the typed
// prompt vanishes silently and /input still returns 200. A person watching the Console
// notices and retypes, but an unattended path (the CP scheduler's reuse send) writes "fired"
// into its ledger on the strength of that 200: a false success recorded for a turn that never
// ran, and a gap the readiness gate alone did not close.
//
// So success is redefined here as evidence that a turn actually started:
//
//	evidence = a user turn was appended to claude's conversation jsonl (the primary record
//	           of submission)
//	         | the pane shows a running spinner (insurance against a delayed jsonl flush)
//
// It waits for that evidence, and if none appears it tries one round of self-healing -
// resend Enter (the draft is still there, so only Enter was eaten), then retype the whole
// prompt (the draft was swallowed too) - and returns delivery_unconfirmed if evidence is
// still missing. The CP scheduler records that as error: and notifies.
//
// Verification is opt-in, running only when the caller asks to confirm, so that UI slash
// commands that start no turn (/model) and human-watched Console sends keep their semantics.
// For a kind with no way to verify (a non-claude TUI) it is a no-op: "cannot verify" must not
// be confused with "delivery failed".

import (
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// The evidence waits. Variables so tests shorten them.
var (
	// deliveryConfirmWindow is how long the first evidence wait runs. A healthy claude
	// appends the user line in <1s; the window is generous for a cold resume on a busy
	// host, because a premature retry is worse than a slow confirm.
	deliveryConfirmWindow = 12 * time.Second
	// deliveryRetryWindow is the evidence wait after the one self-heal attempt.
	deliveryRetryWindow = 12 * time.Second
	deliveryPoll        = 500 * time.Millisecond
)

const (
	// deliveryQueuedGrace is how long a prompt claude has only enqueued may still turn into a
	// started turn before it is reported held. An idle claude can write the enqueue and the
	// user line back to back; a busy one holds it for minutes, and waiting out the whole
	// window for that would only delay the sender.
	deliveryQueuedGrace = 2 * time.Second
)

// deliverySnapshot is the pre-typing evidence baseline. logs == nil means this kind has no
// verification primitive (confirmPromptDelivery is then a no-op and reports success).
type deliverySnapshot struct {
	logs      map[string]int64 // conversation jsonl sizes (the primary record, read as a delta)
	subagents map[string]int64 // background agent transcripts (to detect a misdelivery)
	paneBusy  bool             // was the pane already working before we typed?
}

// deliveryBaseline snapshots the session's conversation log sizes before typing, so
// "a user turn was appended" is checkable afterward. claude only for now — the other
// TUI kinds have no equally cheap submit ground-truth, and today's unattended senders
// (the CP scheduler reuse send) target claude sessions.
//
// paneBusy is part of the baseline too: the spinner is used as evidence that OUR prompt
// started a turn, so it proves nothing if it was already spinning beforehand (a previous
// turn, or a background agent).
func deliveryBaseline(m session.Meta) deliverySnapshot {
	if NormalizeKind(m.Kind) != session.KindClaude {
		return deliverySnapshot{}
	}
	sid := session.UUID(m.Dir, m.Name)
	return deliverySnapshot{
		logs:      claude.TranscriptSnapshot(sid),
		subagents: claude.SubagentSnapshot(sid),
		paneBusy:  deliveryPaneBusy(m.Name),
	}
}

// deliveryPaneBusy is the spinner read of delivery verification. A variable so tests stub it.
var deliveryPaneBusy = tmuxx.IsBusy

// deliveryEvidenced reports how far the prompt provably got. The jsonl half also catches
// a turn that already FINISHED between polls (the user line persists). The queued case
// (typed mid-turn: claude holds the prompt in memory and replays it later) is PromptQueued:
// received, but not started, and lost if the pane is killed before the replay.
//
// The spinner is kept only as insurance against a delayed jsonl flush, and does not count
// when the baseline was already busy: that rotation has nothing to do with our prompt.
// Measured: the spinner on screen was a background subagent's, and an instruction that never
// reached the main conversation was judged delivered.
//
// Nor does it count once the log gained a user line that is not ours (PromptStateSince already
// passed over it): a turn claude started itself is running, such as the task notification a
// resumed session replays, and the spinner is that turn's (#2741).
func deliveryEvidenced(m session.Meta, base deliverySnapshot, prompt string) claude.PromptState {
	sid := session.UUID(m.Dir, m.Name)
	st := claude.PromptStateSince(sid, base.logs, prompt)
	if st == claude.PromptAbsent && !base.paneBusy && deliveryPaneBusy(m.Name) &&
		!claude.UserTurnAppendedSince(sid, base.logs) {
		return claude.PromptStarted
	}
	return st
}

// awaitDeliveryEvidence polls until the prompt started a turn (PromptStarted), or is only
// queued once deliveryQueuedGrace has passed (PromptQueued), or the window ends with no
// evidence (PromptAbsent).
func awaitDeliveryEvidence(m session.Meta, base deliverySnapshot, prompt string, window time.Duration) claude.PromptState {
	deadline := time.Now().Add(window)
	var queuedUntil time.Time
	for {
		switch st := deliveryEvidenced(m, base, prompt); st {
		case claude.PromptStarted:
			return st
		case claude.PromptQueued:
			if queuedUntil.IsZero() {
				queuedUntil = time.Now().Add(deliveryQueuedGrace)
			}
			if time.Now().After(queuedUntil) {
				return st
			}
		}
		if time.Now().After(deadline) {
			if queuedUntil.IsZero() {
				return claude.PromptAbsent
			}
			return claude.PromptQueued
		}
		time.Sleep(deliveryPoll)
	}
}

// confirmPromptDelivery blocks until the just-typed prompt provably started a turn,
// self-healing once if it did not. nil = confirmed (or unverifiable kind). held reports a
// prompt claude has only queued behind a running turn: received, no turn yet, never retyped
// (a retype would queue it twice).
func confirmPromptDelivery(m session.Meta, pane, prompt string, base deliverySnapshot) (held bool, err error) {
	if base.logs == nil {
		return false, nil
	}
	// Compare what was actually typed: typePromptText quotes claude's image paths, which is
	// what the transcript and the composer then hold. Retyping it is safe because the
	// quoting is idempotent.
	prompt = claude.QuoteImagePaths(prompt)
	if st := awaitDeliveryEvidence(m, base, prompt, deliveryConfirmWindow); st != claude.PromptAbsent {
		return st == claude.PromptQueued, nil
	}
	// Misdelivery: if the prompt landed in a background agent's transcript, the pane's input
	// box was bound to that agent rather than to the main conversation (the agents view).
	// Self-healing from here would only fire the same interruption at the agent again, so fail
	// without resending. The pre-typing guard (agents_view in session_io.go) normally catches
	// this; the check here is the last line of defence when a changed rail rendering lets the
	// guard through.
	if claude.SubagentReceivedSince(session.UUID(m.Dir, m.Name), base.subagents, prompt) {
		return false, fmt.Errorf("prompt was delivered to a background agent, not the session " +
			"(the pane's input box was bound to an agent); return the pane to the main conversation and resend")
	}
	// Self-healing. The draft still in the composer means only the submit was lost, so
	// resend Enter (safe: if it was submitted after all, Enter on an empty composer is a
	// no-op). The draft gone as well means the whole line was swallowed, so retype and submit
	// it again. Retyping over a draft that is still there is what must never happen: the
	// retyped copy is appended to it and one turn carries the prompt twice (measured on a
	// work-item launch, where claude 2.1.282 held a multi-line draft for review).
	if promptDraftVisible(tmuxx.CapturePane(session.TmuxName(m.Name)), prompt) {
		log.Printf("delivery: %s composer still holds the prompt — resending Enter", m.Name)
		_ = tmuxx.Cmd("send-keys", "-t", pane, "Enter").Run()
	} else {
		log.Printf("delivery: %s prompt vanished without a turn — retyping", m.Name)
		if err := typeLineAndSubmit(m.Name, pane, prompt); err != nil {
			return false, fmt.Errorf("delivery retry: %v", err)
		}
	}
	if st := awaitDeliveryEvidence(m, base, prompt, deliveryRetryWindow); st != claude.PromptAbsent {
		return st == claude.PromptQueued, nil
	}
	return false, fmt.Errorf("prompt did not become a turn (no user turn appended, pane not working)")
}

// withdrawOptimisticWorking takes back the "working" a send wrote on typing (submitPromptTUI)
// once delivery_unconfirmed has shown no turn started: left in place, the status reads working
// for a session that sits idle, and a sender polling it takes the lost message for one being
// worked on (#2741). It writes the unknown idle the SessionStart reset writes, never a turn
// end, and only when nothing else wrote the record since (marked is the write it withdraws) and
// the pane was not already busy before the send, when working is the truth.
func withdrawOptimisticWorking(m session.Meta, base deliverySnapshot, marked status.SessionStatus) {
	if base.paneBusy || marked.State != "working" || marked.Rev == "" || deliveryPaneBusy(m.Name) {
		return
	}
	status.PersistIf(session.UUID(m.Dir, m.Name), "idle", marked.Rev, true)
}

// promptDraftVisible reports whether the captured pane still shows the typed prompt as
// an unsubmitted composer draft, or claude is holding it for review. Best-effort pane
// heuristics, biased towards true: a false positive only costs a harmless extra Enter
// (no-op on an empty composer), a false negative costs a retype appended to the draft.
//
// The draft is looked for in claude's composer — from its last "❯" line down to the rule
// under it — rather than in a fixed tail, because a multi-line draft pushes its first line
// above any fixed tail (a 5-line work-item prompt plus the footer did). Whitespace is
// ignored on both sides, so a line the composer wrapped at pane width still matches. With
// no composer on screen it falls back to the last few lines.
//
// Either end of the prompt counts. The composer keeps the cursor in view, so on a short pane
// a long draft is scrolled and only its END is on screen (measured at 80x23: the 5-line
// work-item prompt showed from its second wrapped line on, so the head alone never matched).
func promptDraftVisible(captured, prompt string) bool {
	head, tail := promptEnds(prompt)
	if head == "" || captured == "" {
		return false
	}
	draft, above, ok := claudeComposer(captured)
	if !ok {
		t := squashSpace(paneTail(captured, 6))
		return strings.Contains(t, head) || strings.Contains(t, tail)
	}
	// claude 2.1.282 strips invisible characters from typed input and then HOLDS the draft
	// ("Removed 1 invisible character · review and press Enter to send"); the Enter that
	// arrived with the text does not submit it. The notice sits just above the composer.
	if strings.Contains(above, "press Enter to send") {
		return true
	}
	// A draft claude folded into a paste placeholder does not show its text.
	if strings.Contains(draft, "[Pasted text") {
		return true
	}
	d := squashSpace(draft)
	return strings.Contains(d, head) || strings.Contains(d, tail)
}

// promptEnds returns the first and last 12 non-space runes of the prompt's first and last
// non-blank lines: short enough to survive a wrap, and each a line claude draws verbatim.
func promptEnds(prompt string) (head, tail string) {
	var lines []string
	for _, l := range strings.Split(prompt, "\n") {
		if l = squashSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return "", ""
	}
	h, t := []rune(lines[0]), []rune(lines[len(lines)-1])
	if len(h) > 12 {
		h = h[:12]
	}
	if len(t) > 12 {
		t = t[len(t)-12:]
	}
	return string(h), string(t)
}

// claudeComposer splits a captured claude pane around its composer: draft is the last "❯"
// line through the line before the rule below it, above is the first non-blank line above
// the composer's top rule (where claude prints notices about the draft). ok is false when
// no "❯" line is on screen.
func claudeComposer(captured string) (draft, above string, ok bool) {
	lines := strings.Split(captured, "\n")
	top := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "❯") {
			top = i
			break
		}
	}
	if top < 0 {
		return "", "", false
	}
	end := len(lines)
	for i := top + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "─") {
			end = i
			break
		}
	}
	draft = strings.TrimPrefix(strings.TrimSpace(strings.Join(lines[top:end], "\n")), "❯")
	// Skip the composer's top rule, then take the next non-blank line.
	sawRule := false
	for i := top - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			continue
		}
		if !sawRule && strings.HasPrefix(t, "─") {
			sawRule = true
			continue
		}
		above = t
		break
	}
	return draft, above, true
}

// squashSpace drops every whitespace rune (NBSP included: claude draws "❯\u00a0").
func squashSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

const (
	// A cursor Terminal send is watched for cursorSubmitPolls reads, cursorSubmitPoll apart,
	// after its Enter. cursor takes a prompt it will refuse for about a second (the composer
	// empties, a spinner shows) and hands it back at 1.0-1.4 s (measured on 2026.10.16, Free
	// plan, named model); neither the pane nor the JSONL shows a started turn earlier than
	// that, so a normal send waits the whole window too. Short enough that a sender is not held
	// up, long enough for a slow host.
	cursorSubmitPoll  = 250 * time.Millisecond
	cursorSubmitPolls = 10 // 10 x 250 ms
)

// cursorCapture and cursorSleep are the pane read and the wait of confirmCursorSubmit, swapped
// by its tests.
var (
	cursorCapture = tmuxx.CapturePane
	cursorSleep   = time.Sleep
)

// isCursorTerminal reports whether m is a cursor session driven through its TUI, the only kind
// whose rejected submit confirmCursorSubmit can see.
func isCursorTerminal(m session.Meta) bool {
	return NormalizeKind(m.Kind) == session.KindCursor && m.DriverKind() != session.DriverManaged
}

// confirmCursorSubmit reports whether the cursor TUI handed the just-submitted prompt back:
// the text is in the composer again and cursor printed an error under it (a model the plan
// cannot use). held is true then, with reason set to the TUI's error text, and the draft is
// left alone: a retype would append to it, and the cause is not one a retry fixes.
//
// Fails toward "delivered" on anything it does not recognise: an unreadable pane, no composer
// on screen, a draft that went away. A draft that stays without an error row for the whole
// window is held too (Enter lost), with no reason. Only a draft seen on two polls in a row
// counts, because a send in flight shows the draft for a moment.
func confirmCursorSubmit(m session.Meta, prompt string) (held bool, reason string) {
	head, tail := promptEnds(prompt)
	if head == "" {
		return false, ""
	}
	tn := session.TmuxName(m.Name)
	streak := 0
	for poll := 1; ; poll++ {
		cursorSleep(cursorSubmitPoll)
		pane := cursorCapture(tn)
		if cursor.ComposerHolds(pane, head, tail) {
			streak++
			reason = cursor.SubmitError(pane)
			if streak >= 2 && reason != "" {
				return true, reason
			}
		} else {
			streak, reason = 0, ""
		}
		if poll >= cursorSubmitPolls {
			return streak >= 2, reason
		}
	}
}
