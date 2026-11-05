package sessionx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
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

// The same after one Esc in a Fleet session (claude 2.1.316 started with --name, 80 columns):
// the title is drawn into the top rule.
var paneTitled = strings.Repeat(" ", 60) + "Esc again to clear\n" + titledRule +
	"\n❯ 1から20までの素数を全部挙げて理由も説明して\n" + strings.Repeat("─", 80) + "\n\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"

var titledRule = strings.Repeat("─", 25) + " [AF:solkwlt] Live check #3290 (stop restores prompt) ─"

func TestClaudeBox(t *testing.T) {
	draft, above, ok := claudeBox(paneCaptured)
	if !ok || squashSpace(draft) != "SLOW一行目二行目" || above != clearHint {
		t.Fatalf("draft = %q, above = %q, ok = %v", draft, above, ok)
	}
	draft, above, ok = claudeBox(paneTitled)
	if !ok || squashSpace(draft) != "1から20までの素数を全部挙げて理由も説明して" || above != clearHint {
		t.Fatalf("titled: draft = %q, above = %q, ok = %v", draft, above, ok)
	}
	for name, pane := range map[string]string{
		// A row of the draft that starts at column 0 is not claude's layout.
		"unindented row": rule + "\n❯ a\nb\n" + rule + "\n",
		"no top rule":    "❯ a\n" + rule + "\n",
		"rules differ":   rule + "\n❯ a\n" + rule + "──\n",
		"no prompt mark": rule + "\n  a\n" + rule + "\n",
		// A title only ever sits in the top rule, and both rules have one width.
		"titled bottom":    titledRule + "\n❯ a\n" + titledRule + "\n",
		"titled, narrower": titledRule + "\n❯ a\n" + strings.Repeat("─", 79) + "\n",
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

// A Fleet session's box has its title in the top rule; the reclaim works there too.
func TestInterruptReclaimsFromATitledBox(t *testing.T) {
	prompt := "1から20までの素数を全部挙げて理由も説明して"
	bottom := strings.Repeat("─", 80)
	box := func(draft string) string {
		return "◐ medium · /effort\n" + titledRule + "\n❯ " + draft + "\n" + bottom + "\n\n  ⏵⏵ bypass permissions on\n"
	}
	keys, restored, _ := runClaudeInterrupt(t, prompt, func(esc int) string {
		switch esc {
		case 0:
			return box("")
		case 1:
			return box(prompt)
		case 2:
			return paneTitled
		}
		return box("")
	})
	if restored != prompt || strings.Join(keys, ",") != "Escape,Escape,Escape" {
		t.Fatalf("restored = %q, keys = %v", restored, keys)
	}
}

// claude logs the stopped prompt some 200 ms after the Enter, so a stop right after a send
// first reads the previous prompt (or none) from the transcript; the reclaim keeps reading.
func TestInterruptReclaimsAPromptLoggedLate(t *testing.T) {
	logPath := fakeTmux(t)
	stubInterruptState(t, "working")
	stubReclaim(t, logPath, "", func(esc int) string {
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
	reads := 0
	interruptedPrompt = func(session.Meta) string {
		if reads++; reads < 3 {
			return []string{"", "the previous prompt"}[reads-1]
		}
		return "SLOW 再レビュー"
	}
	name := "turn_reclaim_late"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
	rec := postTurn(t, name, `{"op":"interrupt"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"restored":"SLOW 再レビュー"`) {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// A paste placeholder is never erased, even one whose line count fits the stopped prompt: a
// member can paste into the terminal during the watch window. The send is refused instead.
func TestInterruptKeepsAPlaceholderThatFitsThePrompt(t *testing.T) {
	prompt := strings.Repeat("line\n", 50) + "end"
	keys, _, has := runClaudeInterrupt(t, prompt, func(esc int) string {
		if esc == 0 {
			return paneWorking
		}
		return rule + "\n❯ [Pasted text #1 +50 lines]\n" + rule + "\n"
	})
	if has || strings.Join(keys, ",") != "Escape" {
		t.Fatalf("keys = %v, restored present = %v", keys, has)
	}
	if rec := postTurn(t, "turn_reclaim", `{"op":"start","prompt":"next"}`); rec.Code != http.StatusConflict {
		t.Fatalf("status = %d", rec.Code)
	}
}

// lateRun stops with the box empty throughout the watch window, then lets late(esc) draw the
// pane for the next send.
func lateRun(t *testing.T, prompt string, late func(esc int) string, between ...func()) (code int, body string, keys []string) {
	t.Helper()
	windowOver := false
	_, _, _ = runClaudeInterrupt(t, prompt, func(esc int) string {
		if !windowOver {
			if esc == 0 {
				return paneWorking
			}
			return paneEmpty
		}
		return late(esc)
	})
	windowOver = true
	for _, f := range between {
		f()
	}
	rec := postTurn(t, "turn_reclaim", `{"op":"start","prompt":"next prompt"}`)
	return rec.Code, rec.Body.String(), sendKeysLines(t, os.Getenv("TMUX_TEST_LOG"))
}

// A prompt that reappears after the watch window is cleared when the next send looks again
// (issue #3302, case 2); the send then goes on and is not typed after it.
func TestSendClearsAPromptThatReappearedAfterTheWindow(t *testing.T) {
	code, body, keys := lateRun(t, "SLOW 再レビュー", func(esc int) string {
		switch esc {
		case 1:
			return paneRestored
		case 2:
			return paneClearArmed
		}
		return paneEmpty
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	if got := strings.Join(keys[:3], ","); got != "Escape,Escape,Escape" {
		t.Fatalf("keys = %v, want the stop and the two clearing Escapes first", keys)
	}
}

// Late, a placeholder is not ours to erase: the send is refused instead.
func TestSendRefusedForALatePlaceholder(t *testing.T) {
	prompt := strings.Repeat("line\n", 50) + "end"
	code, body, keys := lateRun(t, prompt, func(int) string {
		return rule + "\n❯ [Pasted text #1 +50 lines]\n" + rule + "\n"
	})
	if code != http.StatusConflict || !strings.Contains(body, "stopped_prompt_in_box") {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	if strings.Join(keys, ",") != "Escape" {
		t.Fatalf("keys = %v", keys)
	}
}

// Text in the box after a stop that cannot be proven to be the stopped prompt refuses the send
// and is not erased; the refusal holds until the box reads empty.
func TestSendRefusedForOtherTextAfterTheWindow(t *testing.T) {
	for name, pane := range map[string]string{
		"member draft":     paneOwnDraft,
		"prompt plus more": rule + "\n❯ SLOW 再レビュー and then some\n" + rule + "\n",
		"unreadable":       "no box here\n",
	} {
		t.Run(name, func(t *testing.T) {
			code, body, keys := lateRun(t, "SLOW 再レビュー", func(int) string { return pane })
			if code != http.StatusConflict || !strings.Contains(body, "stopped_prompt_in_box") || strings.Join(keys, ",") != "Escape" {
				t.Fatalf("status = %d, body = %s, keys = %v", code, body, keys)
			}
		})
	}
}

// Past the erase TTL even the exact prompt is left alone and the send refused.
func TestSendRefusedForAnExpiredLateRestore(t *testing.T) {
	code, body, keys := lateRun(t, "SLOW 再レビュー", func(int) string { return paneRestored }, func() {
		v, _ := lateWatch.Load("turn_reclaim")
		e := v.(lateWatchEntry)
		e.at = e.at.Add(-lateEraseTTL - time.Second)
		lateWatch.Store("turn_reclaim", e)
	})
	if code != http.StatusConflict || strings.Join(keys, ",") != "Escape" {
		t.Fatalf("status = %d, body = %s, keys = %v", code, body, keys)
	}
}

// An entry armed for another pane (restarted session) or superseded by a newer stop does not
// apply.
func TestLateWatchIsBoundToPaneAndStop(t *testing.T) {
	code, _, _ := lateRun(t, "SLOW 再レビュー", func(int) string { return paneRestored }, func() {
		v, _ := lateWatch.Load("turn_reclaim")
		e := v.(lateWatchEntry)
		e.pane = "other-pane"
		lateWatch.Store("turn_reclaim", e)
	})
	if code != http.StatusOK {
		t.Fatalf("stale pane: status = %d", code)
	}
	// A newer stop that takes the prompt out (or has an unmeasured box) drops the armed entry.
	lateWatch.Store("turn_reclaim", lateWatchEntry{pane: "x", at: time.Now()})
	runClaudeInterrupt(t, "p", func(int) string { return paneOwnDraft })
	if _, ok := lateWatch.Load("turn_reclaim"); ok {
		t.Fatal("a newer stop left the older armed entry in place")
	}
}

// Sweeping an old entry must keep the guard: the session's next send still sees a prompt that
// reappeared meanwhile and is refused.
func TestLateWatchSweepKeepsTheGuard(t *testing.T) {
	code, body, keys := lateRun(t, "SLOW 再レビュー", func(int) string { return paneRestored }, func() {
		v, _ := lateWatch.Load("turn_reclaim")
		e := v.(lateWatchEntry)
		e.at = e.at.Add(-lateWatchKeep - time.Minute)
		lateWatch.Store("turn_reclaim", e)
		armLateWatch("other_session", "p", "")
		t.Cleanup(func() { lateWatch.Delete("other_session") })
	})
	if code != http.StatusConflict || !strings.Contains(body, "stopped_prompt_in_box") || strings.Join(keys, ",") != "Escape" {
		t.Fatalf("status = %d, body = %s, keys = %v", code, body, keys)
	}
}

// firstPromptRun stops a session whose transcript yields no prompt (the first prompt, issue
// #3345) after the Agent submitted submitted into pane "%7"; the box then shows the paneAt(esc).
func firstPromptRun(t *testing.T, submitted string, age time.Duration, pane func(int) string) string {
	t.Helper()
	logPath := fakeTmux(t)
	stubReclaim(t, logPath, "", pane)
	t.Cleanup(func() { submittedPrompts.Delete("first_prompt"); lateWatch.Delete("first_prompt") })
	if submitted != "" {
		rememberSubmitted("first_prompt", "%7", submitted)
		v, _ := submittedPrompts.Load("first_prompt")
		sp := v.(submittedPrompt)
		sp.at = sp.at.Add(-age)
		submittedPrompts.Store("first_prompt", sp)
	}
	m := session.Meta{Name: "first_prompt", Dir: t.TempDir(), Kind: session.KindClaude}
	// The stop key itself, which also creates the fake tmux log the pane stub counts from.
	if err := sendNamedKeys("%7", []string{"Escape"}); err != nil {
		t.Fatal(err)
	}
	return reclaimInterruptedPrompt(m, "%7", true)
}

func restoredPaneSteps(esc int) string {
	switch esc {
	case 0, 1:
		return paneRestored
	case 2:
		return paneClearArmed
	}
	return paneEmpty
}

// With no transcript prompt, the Agent's own fresh submission is reclaimed.
func TestReclaimFirstPromptFromOwnSubmission(t *testing.T) {
	if got := firstPromptRun(t, "SLOW 再レビュー", time.Second, restoredPaneSteps); got != "SLOW 再レビュー" {
		t.Fatalf("restored = %q", got)
	}
}

// A submission that is too old, or that the box does not hold exactly, is never erased.
func TestReclaimFirstPromptRefusesStaleOrDifferent(t *testing.T) {
	if got := firstPromptRun(t, "SLOW 再レビュー", submittedTTL+time.Minute, restoredPaneSteps); got != "" {
		t.Fatalf("stale submission reclaimed: %q", got)
	}
	if got := firstPromptRun(t, "another prompt", time.Second, restoredPaneSteps); got != "" {
		t.Fatalf("different text reclaimed: %q", got)
	}
}

// A submission serves one stop only, and only its own pane.
func TestSubmittedPromptIsConsumedAndPaneBound(t *testing.T) {
	rememberSubmitted("s_consume", "%7", "x")
	t.Cleanup(func() { submittedPrompts.Delete("s_consume") })
	if takeSubmitted("s_consume", "%1") != "" {
		t.Fatal("taken for another pane")
	}
	rememberSubmitted("s_consume", "%7", "x")
	if takeSubmitted("s_consume", "%7") != "x" || takeSubmitted("s_consume", "%7") != "" {
		t.Fatal("must be taken exactly once")
	}
}

// The transcript's copy wins when the box matches it.
func TestMatchingPromptPrefersTheTranscript(t *testing.T) {
	if got := matchingPrompt(paneRestored, "SLOW 再レビュー", "SLOW  再レビュー"); got != "SLOW 再レビュー" {
		t.Fatalf("got %q", got)
	}
}

// The late re-check at the next send uses the same fallback (issue #3345).
func TestLateRestoreClearsOwnSubmission(t *testing.T) {
	logPath := fakeTmux(t)
	stubReclaim(t, logPath, "", func(esc int) string {
		switch esc {
		case 1:
			return paneRestored
		case 2:
			return paneClearArmed
		}
		return paneEmpty
	})
	if err := sendNamedKeys("%7", []string{"Escape"}); err != nil { // the stop; creates the log
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "late_first", Dir: t.TempDir(), Kind: session.KindClaude})
	t.Cleanup(func() { lateWatch.Delete("late_first") })
	armLateWatch("late_first", "%7", "SLOW 再レビュー")
	if lateRestoredBlocks("late_first", "%7") {
		t.Fatal("send refused; the own submission should have been cleared")
	}
	reclaimCapture = func(string) string { return paneRestored } // restored by stubReclaim's cleanup
	armLateWatch("late_first", "%7", "")
	if !lateRestoredBlocks("late_first", "%7") {
		t.Fatal("without a known prompt the send must stay refused")
	}
}

// Every Agent-side submission path ends in typeLineAndSubmit (initial_prompt, sends, delivery
// retries), and that is what must remember the text; a slash command must not replace it.
func TestTypeLineAndSubmitRemembersTheSubmission(t *testing.T) {
	fakeTmux(t)
	t.Setenv("AGENT_INPUT_SUBMIT_DELAY_MS", "0")
	session.WriteMeta(session.Meta{Name: "remember_me", Dir: t.TempDir(), Kind: session.KindClaude})
	t.Cleanup(func() { submittedPrompts.Delete("remember_me") })
	if err := typeLineAndSubmit("remember_me", "%7", "the first task"); err != nil {
		t.Fatal(err)
	}
	if err := typeLineAndSubmit("remember_me", "%7", "/remote-control"); err != nil {
		t.Fatal(err)
	}
	if got := takeSubmitted("remember_me", "%7"); got != "the first task" {
		t.Fatalf("remembered %q, want the prompt, not the slash command", got)
	}
}

// A stop puts the prompt back into the box as it was typed, so the remembered submission is the
// typed (shaped) form: an LRM the Agent removed before typing, or an image path it quoted, would
// otherwise keep restoredDraft from matching a first prompt claude had not logged yet (#3375).
func TestTypeLineAndSubmitRemembersTheTypedForm(t *testing.T) {
	fakeTmux(t)
	t.Setenv("AGENT_INPUT_SUBMIT_DELAY_MS", "0")
	session.WriteMeta(session.Meta{Name: "remember_shaped", Dir: t.TempDir(), Kind: session.KindClaude})
	t.Cleanup(func() { submittedPrompts.Delete("remember_shaped") })
	const p = "/home/dev/.cache/agent-fleet/pasted/sid/paste-1.png"
	sent := "the first\u200e task Open the following file(s) with the Read tool: " + p
	if err := typeLineAndSubmit("remember_shaped", "%7", sent); err != nil {
		t.Fatal(err)
	}
	want := "the first task Open the following file(s) with the Read tool: `" + p + "`"
	got := takeSubmitted("remember_shaped", "%7")
	if got != want {
		t.Fatalf("remembered %+q, want the typed form %+q", got, want)
	}
	if !restoredDraft(rule+"\n❯ "+want+"\n"+rule+"\n  ⏵⏵ auto mode on\n", got) {
		t.Fatal("the restored draft does not match the remembered submission")
	}
}

// claude restores the prompt it submitted, without the invisible characters it strips on submit
// (a zero-width space), while the remembered submission is the typed text that still has them.
// The box must still be taken for the prompt, or the stop strands it and refuses the next send.
func TestRestoredDraftIgnoresWhatClaudeStrips(t *testing.T) {
	box := func(s string) string { return rule + "\n❯ " + s + "\n" + rule + "\n  ⏵⏵ auto mode on\n" }
	typed := claude.ShapePrompt("a\u200b\u200eb c")
	if !restoredDraft(box("ab c"), typed) {
		t.Fatalf("box %q not taken for the typed %+q", "ab c", typed)
	}
	if restoredDraft(box("ab d"), typed) || restoredDraft(box("a b"), typed) {
		t.Fatal("a box with other visible text was taken for the prompt")
	}
	// The box is erased on a match, so a joiner or variation selector the typed text lacks (the
	// member edited the restored draft in the terminal) must not match, while one claude kept
	// does.
	if restoredDraft(box("👩\u200d💻"), "👩💻") || restoredDraft(box("❤\ufe0f"), "❤") {
		t.Fatal("a box with an added joiner or selector was taken for the prompt")
	}
	if !restoredDraft(box("👩\u200d💻"), "👩\u200d💻\u200b") {
		t.Fatal("a box keeping the emoji joiner was not taken for the prompt")
	}
}

// The submission is remembered before the Enter goes out, so a stop that runs right after the
// Enter without the terminal lock always finds it; a failed Enter takes it back.
func TestSubmissionIsRememberedBeforeTheEnter(t *testing.T) {
	logPath := fakeTmux(t)
	bin := filepath.Dir(logPath)
	gate := filepath.Join(bin, "go")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
case "$1" in
  list-panes) printf '1 %%7\n' ;;
  send-keys) case "$*" in *Enter) while [ ! -e "` + gate + `" ]; do sleep 0.01; done; [ -e "` + gate + `.fail" ] && exit 1 ;; esac ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "before_enter", Dir: t.TempDir(), Kind: session.KindClaude})
	t.Cleanup(func() { submittedPrompts.Delete("before_enter") })
	done := make(chan error, 1)
	go func() { done <- typeLineAndSubmit("before_enter", "%7", "first task") }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := submittedPrompts.Load("before_enter"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not remembered while the Enter was still pending")
		}
		time.Sleep(5 * time.Millisecond)
	}
	os.WriteFile(gate+".fail", nil, 0o644)
	os.WriteFile(gate, nil, 0o644)
	if err := <-done; err == nil {
		t.Fatal("expected the failing Enter to be reported")
	}
	if got := takeSubmitted("before_enter", "%7"); got != "" {
		t.Fatalf("a prompt whose Enter failed stayed remembered: %q", got)
	}
}

// A failed Enter takes back only its own entry: an equal-text entry a later send stored stays.
func TestForgetSubmittedKeepsALaterEqualEntry(t *testing.T) {
	t.Cleanup(func() { submittedPrompts.Delete("forget_own") })
	a := rememberSubmitted("forget_own", "%7", "X")
	time.Sleep(2 * time.Millisecond)
	rememberSubmitted("forget_own", "%7", "X")
	forgetSubmitted("forget_own", a)
	if got := takeSubmitted("forget_own", "%7"); got != "X" {
		t.Fatalf("the later send's entry was removed: %q", got)
	}
	b := rememberSubmitted("forget_own", "%7", "X")
	forgetSubmitted("forget_own", b)
	if got := takeSubmitted("forget_own", "%7"); got != "" {
		t.Fatalf("own entry stayed: %q", got)
	}
}
