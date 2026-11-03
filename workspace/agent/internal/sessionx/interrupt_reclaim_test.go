package sessionx

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Panes captured from claude 2.1.316 in the issue #3290 probe, trimmed.
const (
	rule = "────────────────────────────────────────"
	// The turn runs; the stopped prompt is in the history above an empty box.
	paneWorking = "❯ SLOW 再レビュー\n✽ Zigzagging…\n" + rule + "\n❯ \n" + rule + "\n  ⏵⏵ auto mode on · esc to interrupt\n"
	// Esc before the first byte: the prompt is back in the box.
	paneRestored = rule + "\n❯ SLOW 再レビュー\n" + rule + "\n  ⏵⏵ auto mode on (shift+tab to cycle)\n"
	// One Esc on a non-empty box.
	paneClearArmed = "                    Esc again to clear\n" + rule + "\n❯ SLOW 再レビュー\n" + rule + "\n  ⏵⏵ auto mode on\n"
	paneEmpty      = rule + "\n❯ \n" + rule + "\n  ⏵⏵ auto mode on (shift+tab to cycle)\n"
	paneOwnDraft   = "✽ Zigzagging…\n" + rule + "\n❯ a draft typed in the terminal\n" + rule + "\n  ⏵⏵ auto mode on\n"
)

// stubReclaim makes the pane a function of how many Escapes were sent so far (read from the
// fake tmux log), the stopped prompt fixed, and the polls instant.
func stubReclaim(t *testing.T, logPath, prompt string, pane func(escapes int) string) {
	t.Helper()
	oldCap, oldPrompt, oldSleep := reclaimCapture, interruptedPrompt, reclaimSleep
	reclaimCapture = func(string) string {
		n := 0
		for _, k := range sendKeysLines(t, logPath) {
			if k == "Escape" {
				n++
			}
		}
		return pane(n)
	}
	interruptedPrompt = func(session.Meta) string { return prompt }
	reclaimSleep = func(time.Duration) {}
	t.Cleanup(func() { reclaimCapture, interruptedPrompt, reclaimSleep = oldCap, oldPrompt, oldSleep })
}

func runClaudeInterrupt(t *testing.T, prompt string, pane func(int) string) (keys []string, restored string, has bool) {
	t.Helper()
	logPath := fakeTmux(t)
	stubInterruptState(t, "working")
	stubReclaim(t, logPath, prompt, pane)
	name := "turn_reclaim"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
	rec := postTurn(t, name, `{"op":"interrupt"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	r, has := body["restored"].(string)
	return sendKeysLines(t, logPath), r, has
}

// The prompt claude put back is cleared with Esc, Esc and handed to the Console; without
// that, the next send is typed after it and both run as one prompt.
func TestInterruptReclaimsRestoredPrompt(t *testing.T) {
	keys, restored, _ := runClaudeInterrupt(t, "SLOW 再レビュー", func(esc int) string {
		switch esc {
		case 0:
			return paneWorking
		case 1:
			return paneRestored
		case 2:
			return paneClearArmed
		}
		return paneEmpty
	})
	if restored != "SLOW 再レビュー" {
		t.Fatalf("restored = %q", restored)
	}
	if strings.Join(keys, ",") != "Escape,Escape,Escape" {
		t.Fatalf("keys = %v, want the stop and the two clearing Escapes", keys)
	}
}

// A stop after the first streamed byte restores nothing: one Escape and no "restored".
func TestInterruptWithoutRestoreSendsOnlyTheStop(t *testing.T) {
	keys, _, has := runClaudeInterrupt(t, "SLOW 再レビュー", func(esc int) string {
		if esc == 0 {
			return paneWorking
		}
		return paneEmpty
	})
	if has || strings.Join(keys, ",") != "Escape" {
		t.Fatalf("keys = %v, restored present = %v", keys, has)
	}
}

// Text that was in the box before the stop was typed by someone in the terminal: the box is
// left alone even when it shows the stopped prompt afterwards.
func TestInterruptLeavesADraftTypedBeforeTheStop(t *testing.T) {
	keys, _, has := runClaudeInterrupt(t, "SLOW 再レビュー", func(esc int) string {
		if esc == 0 {
			return paneOwnDraft
		}
		return paneRestored
	})
	if has || strings.Join(keys, ",") != "Escape" {
		t.Fatalf("keys = %v, restored present = %v", keys, has)
	}
}

// Text in the box that is not the stopped prompt is not ours to clear.
func TestInterruptLeavesOtherTextInTheBox(t *testing.T) {
	keys, _, has := runClaudeInterrupt(t, "a different prompt entirely", func(esc int) string {
		if esc == 0 {
			return paneWorking
		}
		return paneRestored
	})
	if has || strings.Join(keys, ",") != "Escape" {
		t.Fatalf("keys = %v, restored present = %v", keys, has)
	}
}

// Without claude's "Esc again to clear" the second Esc is not sent: on an empty box a double
// Esc opens the rewind menu. Nothing is reported as restored either.
func TestInterruptClearNeedsTheClearHint(t *testing.T) {
	keys, _, has := runClaudeInterrupt(t, "SLOW 再レビュー", func(esc int) string {
		switch esc {
		case 0:
			return paneWorking
		case 1:
			return paneRestored
		}
		return paneEmpty // the first clearing Esc emptied it, or the hint never came
	})
	if has || strings.Join(keys, ",") != "Escape,Escape" {
		t.Fatalf("keys = %v, restored present = %v", keys, has)
	}
}

// A send waits while a stop to the same session is still in flight.
func TestWaitInterruptSettledWaitsForTheStop(t *testing.T) {
	old := reclaimSleep
	t.Cleanup(func() { reclaimSleep = old })
	name := "settle_wait"
	interruptsInFlight.Store(name, struct{}{})
	polls := 0
	reclaimSleep = func(time.Duration) {
		if polls++; polls == 3 {
			interruptsInFlight.Delete(name)
		}
	}
	waitInterruptSettled(name)
	if polls != 3 {
		t.Fatalf("polls = %d, want the wait to end when the stop finished (3)", polls)
	}
	// Bounded: a stop that never finishes does not hold a send forever.
	interruptsInFlight.Store(name, struct{}{})
	t.Cleanup(func() { interruptsInFlight.Delete(name) })
	polls = 0
	reclaimSleep = func(time.Duration) { polls++ }
	waitInterruptSettled(name)
	if want := int(interruptSettleLimit / reclaimPoll); polls != want {
		t.Fatalf("polls = %d, want %d", polls, want)
	}
}
