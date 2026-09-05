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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

const (
	// deliveryConfirmWindow is how long the first evidence wait runs. A healthy claude
	// appends the user line in <1s; the window is generous for a cold resume on a busy
	// host, because a premature retry is worse than a slow confirm.
	deliveryConfirmWindow = 12 * time.Second
	// deliveryRetryWindow is the evidence wait after the one self-heal attempt.
	deliveryRetryWindow = 12 * time.Second
	deliveryPoll        = 500 * time.Millisecond
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
		paneBusy:  tmuxx.IsBusy(m.Name),
	}
}

// deliveryEvidenced reports whether the prompt provably reached the session. The jsonl
// half also catches a turn that already FINISHED between polls (the user line persists)
// and the queued case (typed mid-turn: claude holds the prompt and replays it later).
//
// The spinner is kept only as insurance against a delayed jsonl flush, and does not count
// when the baseline was already busy: that rotation has nothing to do with our prompt.
// Measured: the spinner on screen was a background subagent's, and an instruction that never
// reached the main conversation was judged delivered.
func deliveryEvidenced(m session.Meta, base deliverySnapshot, prompt string) bool {
	if claude.PromptAcceptedSince(session.UUID(m.Dir, m.Name), base.logs, prompt) {
		return true
	}
	return !base.paneBusy && tmuxx.IsBusy(m.Name)
}

func awaitDeliveryEvidence(m session.Meta, base deliverySnapshot, prompt string, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for {
		if deliveryEvidenced(m, base, prompt) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(deliveryPoll)
	}
}

// confirmPromptDelivery blocks until the just-typed prompt provably started a turn,
// self-healing once if it did not. nil = confirmed (or unverifiable kind).
func confirmPromptDelivery(m session.Meta, pane, prompt string, base deliverySnapshot) error {
	if base.logs == nil {
		return nil
	}
	if awaitDeliveryEvidence(m, base, prompt, deliveryConfirmWindow) {
		return nil
	}
	// Misdelivery: if the prompt landed in a background agent's transcript, the pane's input
	// box was bound to that agent rather than to the main conversation (the agents view).
	// Self-healing from here would only fire the same interruption at the agent again, so fail
	// without resending. The pre-typing guard (agents_view in session_io.go) normally catches
	// this; the check here is the last line of defence when a changed rail rendering lets the
	// guard through.
	if claude.SubagentReceivedSince(session.UUID(m.Dir, m.Name), base.subagents, prompt) {
		return fmt.Errorf("prompt was delivered to a background agent, not the session " +
			"(the pane's input box was bound to an agent); return the pane to the main conversation and resend")
	}
	// Self-healing. The draft still visible in the composer means only Enter was eaten, so
	// resend Enter (safe: if it was submitted after all, Enter on an empty composer is a
	// no-op). The draft gone as well means the whole line was swallowed, so retype and submit
	// it again - no evidence means nothing was submitted, so this cannot double-execute.
	if promptDraftVisible(tmuxx.CapturePane(session.TmuxName(m.Name)), prompt) {
		log.Printf("delivery: %s composer still holds the prompt — resending Enter", m.Name)
		_ = tmuxx.Cmd("send-keys", "-t", pane, "Enter").Run()
	} else {
		log.Printf("delivery: %s prompt vanished without a turn — retyping", m.Name)
		if err := typeLineAndSubmit(m.Name, pane, prompt); err != nil {
			return fmt.Errorf("delivery retry: %v", err)
		}
	}
	if awaitDeliveryEvidence(m, base, prompt, deliveryRetryWindow) {
		return nil
	}
	return fmt.Errorf("prompt did not become a turn (no user turn appended, pane not working)")
}

// promptDraftVisible reports whether the captured pane still shows the typed prompt as
// an unsubmitted composer draft. Best-effort pane heuristics: match the first line's
// head against the tail region where the composer sits. The head is kept short
// (12 runes, rune-safe) because the composer WRAPS long lines at pane width — a longer
// needle can straddle a wrap point and false-negative. A false positive only costs a
// harmless extra Enter (no-op on an empty composer); a false negative costs a retype,
// which is safe because this path is only reached when no turn evidence exists.
func promptDraftVisible(captured, prompt string) bool {
	first := strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0])
	if first == "" || captured == "" {
		return false
	}
	if r := []rune(first); len(r) > 12 {
		first = string(r[:12])
	}
	return strings.Contains(paneTail(captured, 6), first)
}
