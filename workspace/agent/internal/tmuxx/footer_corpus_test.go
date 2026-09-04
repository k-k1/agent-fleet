package tmuxx

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// verdict is what the pane-reading pair (IsBusy / AtIdlePrompt) should conclude about a
// frame. They are not mutually exclusive by construction — busy and idle are independent
// predicates — so the corpus pins BOTH for every frame, which is what catches a change
// that makes a frame read as neither (or both).
type verdict struct {
	busy      bool
	idle      bool
	agents    bool // the pane's input box is bound to a background agent, not the session
	rateLimit bool // parked on the usage-limit menu (/rate-limit-options)
}

var (
	busyV    = verdict{busy: true, idle: false}  // a turn is in flight
	idleV    = verdict{busy: false, idle: true}  // sitting at the ready input box
	modalV   = verdict{busy: false, idle: false} // a dialog is up: neither working nor takeable as idle
	corpusWD = "testdata/footers"
)

// corpus pins the expected reading of every recorded pane in testdata/footers. See that
// directory's SOURCE.txt for provenance (claude 2.1.212) and how to re-capture.
//
// This locks the two regressions that shipped to the fleet on 2026-07-17:
//   - busy_thinking_no_tokens: the spinner carries NO token count while claude is still
//     thinking. The old spinnerRe required "tokens" and false-idled the whole phase.
//   - idle_manual_mode / idle_bypass_bg_shell: the footer's trailing hint is contextual —
//     absent in the default mode, displaced by "· 1 shell ·" when background work runs.
//     The old AtIdlePrompt keyed on that hint and never fired the stale→idle self-heal.
//
// NOTE ON WHAT THIS CANNOT DO: these are recordings, so they cannot detect a FUTURE drift
// — a 4th change in claude's TUI would leave this test green. It guards the code against
// regressing on formats we have already seen. Detecting new drift needs the real CLI
// driven live and this corpus re-captured (see SOURCE.txt).
var corpus = map[string]verdict{
	"busy_thinking_no_tokens.txt":    busyV,
	"busy_tokens_early.txt":          busyV,
	"busy_tokens_glyph_asterisk.txt": busyV,
	"idle_manual_mode.txt":           idleV,
	"idle_bypass_bg_shell.txt":       idleV,
	"idle_bypass_hint.txt":           idleV,
	"idle_plan_mode.txt":             idleV,
	"idle_post_turn_summary.txt":     idleV,
	"modal_plan_approval.txt":        modalV,
	"modal_folder_trust.txt":         modalV,
	// Background-agent frames. What made the misdelivery possible is that only the
	// selection differs: footer and input box look identical whether main or an agent is
	// selected. While the rail is being operated the mode footer is replaced entirely, and
	// on the agents home the input box means "create a new session" — none of these reach
	// the main conversation.
	"agents_rail_main_selected.txt":    idleV,
	"agents_rail_agent_selected.txt":   {busy: true, agents: true},
	"agents_rail_navigating_main.txt":  {},
	"agents_rail_navigating_agent.txt": {busy: true, agents: true},
	"agents_home_screen.txt":           {agents: true},
	// The /rate-limit-options menu after a turn was cut off by the usage limit. It is
	// neither idle nor busy, like modal_*, but here that "neither" lasts forever: the
	// limit modal never dismisses itself, AtIdlePrompt stays false permanently, the
	// self-heal cannot fire and the session sticks at "running" (measured 2026-07-31,
	// about 16 hours).
	"modal_rate_limit.txt": {rateLimit: true},
}

// TestFooterCorpus replays every recorded pane through the real predicates.
func TestFooterCorpus(t *testing.T) {
	for _, name := range corpusFiles(t) {
		t.Run(name, func(t *testing.T) {
			want, ok := corpus[name]
			if !ok {
				t.Fatalf("%s is in testdata/footers but not in the corpus table — add it with its expected verdict (or delete the file)", name)
			}
			s := readFrame(t, name)
			if got := spinnerActive(s); got != want.busy {
				t.Errorf("IsBusy(%s) = %v, want %v\nspinner line: %s", name, got, want.busy, spinnerLine(s))
			}
			if got := atIdlePrompt(s); got != want.idle {
				t.Errorf("AtIdlePrompt(%s) = %v, want %v\nfooter line: %s", name, got, want.idle, footerLine(s))
			}
			// A false positive rejects an injection that should have gone through, so
			// every frame is pinned, including that a frame without the rail is false.
			if got := agentsViewActive(s); got != want.agents {
				t.Errorf("AgentsViewActive(%s) = %v, want %v", name, got, want.agents)
			}
			// The limit-menu verdict is pinned on every frame too, and the false side
			// is the important one: a false positive reads a running turn as stopped
			// at the limit and goes on to call HealIdle, so the whole corpus holds
			// down that spinner frames and the plan-approval dialog do not match.
			if got := atRateLimitModal(s); got != want.rateLimit {
				t.Errorf("AtRateLimitModal(%s) = %v, want %v", name, got, want.rateLimit)
			}
		})
	}
}

// TestFooterCorpusComplete fails when a frame is recorded but never pinned. Without it a
// dropped table entry would silently shrink the coverage this corpus exists to provide.
func TestFooterCorpusComplete(t *testing.T) {
	files := corpusFiles(t)
	if len(files) != len(corpus) {
		t.Errorf("testdata/footers has %d frames but the corpus table pins %d — they must agree\nframes: %v", len(files), len(corpus), files)
	}
}

// corpusFiles lists the recorded frames (SOURCE.txt is prose, not a frame).
func corpusFiles(t *testing.T) []string {
	t.Helper()
	es, err := os.ReadDir(corpusWD)
	if err != nil {
		t.Fatalf("read %s: %v", corpusWD, err)
	}
	var out []string
	for _, e := range es {
		if e.Name() == "SOURCE.txt" || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func readFrame(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(corpusWD, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// spinnerLine / footerLine surface the line a failure is about, so a drift shows the
// actual new wording in the test output instead of just a false/true.
func spinnerLine(s string) string { return findLine(s, spinnerRe.MatchString) }
func footerLine(s string) string  { return findLine(s, modeFooterRe.MatchString) }

func findLine(s string, match func(string) bool) string {
	for _, ln := range strings.Split(s, "\n") {
		if match(ln) {
			return strings.TrimSpace(ln)
		}
	}
	return "(none in frame)"
}

// TestRateLimitModalDismissed pins the reason atRateLimitModal needs TWO markers. The
// banner ("You've hit your session limit …") is transcript text and stays on screen after
// the menu is answered, and claude echoes the chosen option into the transcript too — so
// keying on either alone would report a menu that is long gone, and the session would be
// badged as stopped at the usage limit forever instead of returning to waiting for input.
// Only the confirm footer disappears with the menu.
func TestRateLimitModalDismissed(t *testing.T) {
	frame := readFrame(t, "modal_rate_limit.txt")
	if !atRateLimitModal(frame) {
		t.Fatal("the recorded menu frame must read as the rate-limit modal")
	}
	// The menu is answered: its footer goes away, the banner and the echoed option stay.
	dismissed := strings.Replace(frame, "  Enter to confirm · Esc to cancel", "⏵⏵ bypass permissions on", 1)
	if atRateLimitModal(dismissed) {
		t.Error("atRateLimitModal = true after the menu was dismissed (banner/option text lingering in the transcript)")
	}
	if !atIdlePrompt(dismissed) {
		t.Error("a dismissed menu must read as the ready prompt again, or the session stays stuck")
	}
}

// TestRateLimitDefaultSelected pins the precondition DismissRateLimitModal checks before
// it presses Enter. Enter confirms whatever the cursor stands on, so the guard is the
// only thing keeping the automatic dismissal from choosing option 2 ("Ask your admin for
// more usage" — a request the user did not make) when a human already moved the cursor.
func TestRateLimitDefaultSelected(t *testing.T) {
	frame := readFrame(t, "modal_rate_limit.txt")
	if !rateLimitDefaultRe.MatchString(frame) {
		t.Fatal("the recorded menu stands on option 1 — the guard must recognise it, or the dismissal never fires")
	}
	// The shape after a human moved the cursor to 2 (❯ moves to the second line and is
	// gone from the first).
	moved := strings.Replace(frame, "❯ 1. Stop and wait", "  1. Stop and wait", 1)
	moved = strings.Replace(moved, "  2. Ask your admin", "❯ 2. Ask your admin", 1)
	if rateLimitDefaultRe.MatchString(moved) {
		t.Error("the default guard is true on a frame whose selection moved to 2 — the automatic dismissal would ask the admin for more usage")
	}
	if !atRateLimitModal(moved) {
		t.Error("a menu stays a menu after the cursor moves (lose the detection and the session is not even reported as blocked)")
	}
}

// TestComposerEmpty pins the precondition LeaveAgentsView uses before it sends any key:
// a draft in the input box must block the automatic return to the main conversation.
// The bare prompt is "❯" followed by a NON-BREAKING space (U+00A0) — a real capture, and
// the reason the check trims Unicode space rather than ASCII blanks.
func TestComposerEmpty(t *testing.T) {
	for _, tc := range []struct {
		frame string
		want  bool
	}{
		{"agents_rail_agent_selected.txt", true},
		{"agents_rail_main_selected.txt", true},
		{"idle_bypass_hint.txt", true},
	} {
		if got := composerEmpty(readFrame(t, tc.frame)); got != tc.want {
			t.Errorf("composerEmpty(%s) = %v, want %v", tc.frame, got, tc.want)
		}
	}
	// A draft makes it false — otherwise the recovery keys could submit a human's
	// half-written message.
	drafted := strings.Replace(readFrame(t, "agents_rail_agent_selected.txt"),
		"❯ ", "❯ DRAFT-NOT-SUBMITTED", 1)
	if composerEmpty(drafted) {
		t.Error("composerEmpty = true with a draft in the composer")
	}
}
