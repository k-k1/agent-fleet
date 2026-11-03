package sessionx

import (
	"encoding/json"
	"net/http"
	"os"
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
	t.Cleanup(clearStrandedPrompts)
}

func clearStrandedPrompts() {
	strandedPrompts.Range(func(k, _ any) bool {
		strandedPrompts.Delete(k)
		return true
	})
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

// The clear is for the stopped prompt only. Each pane below is what follows the stop when
// the prompt did NOT come back as such; none may be cleared and none may get a second Esc
// (on an empty box, a double Esc opens claude's rewind menu).
func TestInterruptLeavesWhatIsNotExactlyThePrompt(t *testing.T) {
	cases := map[string]string{
		// Same first 12 characters: a partial match is not the prompt.
		"similar text":      rule + "\n❯ Please review project beta\n" + rule + "\n",
		"paste placeholder": rule + "\n❯ [Pasted text #2 +100 lines]\n" + rule + "\n",
		// No box on screen; the prompt shows only in the history.
		"history only": "❯ Please review project alpha\n✽ Zigzagging…\n",
	}
	for name, after := range cases {
		t.Run(name, func(t *testing.T) {
			keys, _, has := runClaudeInterrupt(t, "Please review project alpha", func(esc int) string {
				if esc == 0 {
					return paneWorking
				}
				return after
			})
			if has || strings.Join(keys, ",") != "Escape" {
				t.Fatalf("keys = %v, restored present = %v", keys, has)
			}
		})
	}
}

// The hint has to be the line right above the box: the same words in the history (a
// conversation about claude's keys, say) do not make a second Esc safe.
func TestInterruptClearHintMustSitAboveTheBox(t *testing.T) {
	hintInHistory := "the hint reads Esc again to clear\n✽ done\n\n" + rule + "\n❯ SLOW 再レビュー\n" + rule + "\n"
	keys, _, has := runClaudeInterrupt(t, "SLOW 再レビュー", func(esc int) string {
		switch esc {
		case 0:
			return paneWorking
		case 1:
			return paneRestored
		}
		return hintInHistory
	})
	if has || strings.Join(keys, ",") != "Escape,Escape" {
		t.Fatalf("keys = %v, restored present = %v", keys, has)
	}
}

// A stop pressed while an earlier one still watches the box is not absorbed: claude may
// already be running the queued prompt the first Esc released. It waits for the pane and
// goes out.
func TestInterruptDuringReclaimWaitsAndIsSent(t *testing.T) {
	logPath := fakeTmux(t)
	stubInterruptState(t, "working")
	stubReclaim(t, logPath, "SLOW 再レビュー", func(int) string { return paneEmpty })
	name := "turn_reclaim_second"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
	unlock, ok := lockTerminal(name, stopLockWait) // the first stop, mid-reclaim
	if !ok {
		t.Fatal("lock")
	}
	polls, early := 0, false
	reclaimSleep = func(time.Duration) {
		if polls++; polls == 3 {
			early = len(sendKeysLines(t, logPath)) != 0
			unlock()
		}
	}
	rec := postTurn(t, name, `{"op":"interrupt"}`)
	if early {
		t.Fatal("the stop key went out while the first stop still held the pane")
	}
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "coalesced") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := sendKeysLines(t, logPath); strings.Join(got, ",") != "Escape" {
		t.Fatalf("keys = %v", got)
	}
}

// A send never types while a stop holds the pane: past the wait it is refused, untyped.
func TestSendRefusedWhileAStopHoldsThePane(t *testing.T) {
	logPath := fakeTmux(t)
	old := reclaimSleep
	t.Cleanup(func() { reclaimSleep = old })
	reclaimSleep = func(time.Duration) {}
	name := "turn_send_blocked"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
	unlock, ok := lockTerminal(name, stopLockWait)
	if !ok {
		t.Fatal("lock")
	}
	defer unlock()
	rec := postTurn(t, name, `{"op":"start","prompt":"ごめん、先に1詰めよう"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "stop_in_progress") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := sendKeysLines(t, logPath); len(got) != 0 {
		t.Fatalf("typed while a stop held the pane: %v", got)
	}
}

// Once the stop lets go, the send goes through.
func TestSendWaitsForAStopToFinish(t *testing.T) {
	logPath := fakeTmux(t)
	old := reclaimSleep
	t.Cleanup(func() { reclaimSleep = old })
	name := "turn_send_waits"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
	unlock, _ := lockTerminal(name, stopLockWait)
	polls := 0
	reclaimSleep = func(time.Duration) {
		if polls++; polls == 3 {
			unlock()
		}
	}
	rec := postTurn(t, name, `{"op":"start","prompt":"next"}`)
	if rec.Code != http.StatusOK || polls < 3 {
		t.Fatalf("status = %d after %d polls, body = %s", rec.Code, polls, rec.Body.String())
	}
	if got := sendKeysLines(t, logPath); len(got) == 0 {
		t.Fatal("nothing typed")
	}
}

// Absorbing a second click covers the stop keys only: by the time the box is watched, a new
// stop must no longer be taken for this one.
func TestInterruptStopsAbsorbingBeforeTheReclaim(t *testing.T) {
	logPath := fakeTmux(t)
	stubInterruptState(t, "working")
	stubReclaim(t, logPath, "", func(esc int) string {
		if esc == 0 {
			return paneWorking
		}
		return paneEmpty
	})
	name := "turn_reclaim_absorb"
	absorbing := true
	interruptedPrompt = func(session.Meta) string {
		_, absorbing = interruptsInFlight.Load(name)
		return ""
	}
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
	if rec := postTurn(t, name, `{"op":"interrupt"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if absorbing {
		t.Fatal("a second stop would still be absorbed while the box is watched")
	}
}

// A box captured from claude 2.1.316 (120 columns, a two-line draft, "❯" followed by NBSP, a
// blank row before the bottom rule) after one Esc.
var paneCaptured = strings.Repeat(" ", 100) + "Esc again to clear\n" + strings.Repeat("─", 120) +
	"\n❯\u00a0SLOW 一行目\n  二行目\n\n" + strings.Repeat("─", 120) + "\n  ⏵⏵ auto mode on (shift+tab to cycle)\n"

func TestClaudeBox(t *testing.T) {
	draft, above, ok := claudeBox(paneCaptured)
	if !ok || squashSpace(draft) != "SLOW一行目二行目" || above != clearHint {
		t.Fatalf("draft = %q, above = %q, ok = %v", draft, above, ok)
	}
	for name, pane := range map[string]string{
		// A row of the draft that starts at column 0 is not claude's layout.
		"unindented row": rule + "\n❯ a\nb\n" + rule + "\n",
		"no top rule":    "❯ a\n" + rule + "\n",
		"rules differ":   rule + "\n❯ a\n" + rule + "──\n",
		"no prompt mark": rule + "\n  a\n" + rule + "\n",
	} {
		if _, _, ok := claudeBox(pane); ok {
			t.Errorf("%s: read as a box", name)
		}
	}
}

// Rules and "❯" a member typed into the box are text inside the frame, not the frame: a draft
// that holds the stopped prompt plus more is not the stopped prompt, and nothing is erased.
func TestInterruptLeavesADraftThatOnlyLooksLikeTheBox(t *testing.T) {
	cases := map[string]string{
		"typed rule":       "❯ SLOW 再レビュー\n  ────────────────────────\n  消してはいけない追記",
		"typed hint and ❯": "❯ an example of the keys\n  Esc again to clear\n  ❯ SLOW 再レビュー",
	}
	for name, box := range cases {
		t.Run(name, func(t *testing.T) {
			restored := rule + "\n" + box + "\n" + rule + "\n  ⏵⏵ auto mode on\n"
			keys, _, has := runClaudeInterrupt(t, "SLOW 再レビュー", func(esc int) string {
				switch esc {
				case 0:
					return paneWorking
				case 1:
					return restored
				}
				return clearHint + "\n" + restored
			})
			if has || strings.Join(keys, ",") != "Escape" {
				t.Fatalf("keys = %v, restored present = %v", keys, has)
			}
		})
	}
}

// A stopped prompt the stop could not take out (here a paste placeholder) stays in the box, and
// the next send is refused rather than typed after it; once the box is empty again it goes out.
func TestSendRefusedWhileAStoppedPromptIsLeftInTheBox(t *testing.T) {
	placeholder := rule + "\n❯ [Pasted text #1 +50 lines]\n" + rule + "\n"
	pane := placeholder
	keys, _, has := runClaudeInterrupt(t, "Please review project alpha", func(esc int) string {
		if esc == 0 {
			return paneWorking
		}
		return pane
	})
	if has || strings.Join(keys, ",") != "Escape" {
		t.Fatalf("keys = %v, restored present = %v", keys, has)
	}
	rec := postTurn(t, "turn_reclaim", `{"op":"start","prompt":"next prompt"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "stopped_prompt_in_box") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(sendKeysLines(t, os.Getenv("TMUX_TEST_LOG")), ","); got != "Escape" {
		t.Fatalf("typed after the stranded prompt: %v", got)
	}
	pane = paneEmpty
	if rec := postTurn(t, "turn_reclaim", `{"op":"start","prompt":"next prompt"}`); rec.Code != http.StatusOK {
		t.Fatalf("after the box was cleared: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// The state that allows Ctrl+C is read once the pane is ours: a turn that ended while the stop
// waited for a send leaves an idle CLI, which Ctrl+C can exit.
func TestInterruptRereadsTheStateAfterWaitingForTheLock(t *testing.T) {
	logPath := fakeTmux(t)
	name := "turn_ctrlc_wait"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCursor})
	unlock, _ := lockTerminal(name, stopLockWait) // a send typing
	st := "working"
	old, oldSleep := interruptLiveState, reclaimSleep
	t.Cleanup(func() { interruptLiveState, reclaimSleep = old, oldSleep })
	interruptLiveState = func(string, session.Meta) string { return st }
	polls := 0
	reclaimSleep = func(time.Duration) {
		if polls++; polls == 3 {
			st = "idle" // the turn ended meanwhile
			unlock()
		}
	}
	rec := postTurn(t, name, `{"op":"interrupt"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not_working") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := sendKeysLines(t, logPath); len(got) != 0 {
		t.Fatalf("keys = %v", got)
	}
}

// A send that holds the pane past the stop's wait gets no stop key in the middle of its typing:
// the stop is refused instead.
func TestInterruptRefusedWhileASendHoldsThePane(t *testing.T) {
	logPath := fakeTmux(t)
	stubInterruptState(t, "working")
	stubReclaim(t, logPath, "SLOW 再レビュー", func(int) string { return paneEmpty })
	name := "turn_stop_blocked"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
	unlock, _ := lockTerminal(name, stopLockWait)
	defer unlock()
	rec := postTurn(t, name, `{"op":"interrupt"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "terminal_busy") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := sendKeysLines(t, logPath); len(got) != 0 {
		t.Fatalf("keys = %v", got)
	}
}
