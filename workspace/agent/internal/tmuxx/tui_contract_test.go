//go:build tui_contract

// claude TUI footer contract probe (P1).
//
// What this test protects: state detection (spinnerActive / atPromptFooter) depends on
// claude's TUI output, a set of strings that is neither a contract nor versioned, and it has
// broken three times (79b582b, deac672, fce5c5e). Every time the unit tests stayed green and
// a human found the breakage in the live fleet. The golden corpus under testdata/footers only
// pins the shapes already known, so it stays green through a fourth drift on the CLI side: it
// is a lock, not a detector. Only running the real CLI reveals a drift — that is this file.
//
// Why it lives inside the agent module: e2e/ is a separate module and Go's internal rule
// keeps it from importing internal/tmuxx. Rewriting the detection logic on the e2e side would
// make it a test that does not exercise the real code, repeating the failure it is meant to
// catch. This is the only place the real functions can be called directly. CI installs Go,
// tmux and claude on the runner through the shared setup action and runs
// `go test -tags tui_contract ./internal/tmuxx/` (claude-tui-contract.yml); the full Workspace
// image is not needed.
//
// Why `claude -p` will not do: the existing L4 (e2e/live_test.go) uses headless -p, which
// draws neither footer nor spinner, which is why it never caught any of the three breakages.
// Here the interactive TUI has to be started under tmux.
//
// Cost: two real turns (manual / bypass). The prompt is a single short essay, a few hundred
// tokens. On an OAuth token (subscription quota) there is no extra charge.
package tmuxx

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The prompt must make the model think without calling a tool: in manual mode Bash and
// friends raise a permission dialog and the turn stalls on a modal. Generating an essay needs
// no permission in either mode and gives 10-30 seconds of thinking plus streaming, which
// reliably hits the thinking phase where the token counter is not yet drawn — the regression
// this test exists for.
const contractPrompt = "Think step by step, then write a 120-word essay about the tmux terminal multiplexer."

const (
	tick      = 500 * time.Millisecond
	readyWait = 90 * time.Second
	turnWait  = 3 * time.Minute
)

func TestClaudeTUIContractLive(t *testing.T) {
	for _, b := range []string{"tmux", "claude"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s is missing — this test is meant to run inside the real image", b)
		}
	}
	if v, err := exec.Command("claude", "--version").Output(); err == nil {
		t.Logf("claude version: %s", strings.TrimSpace(string(v)))
	}

	for _, m := range []struct {
		name string
		args []string
	}{
		// Default (manual) mode: the footer is "⏸ manual mode on · ← for agents" and shows
		// no hint at all, the condition under which AtIdlePrompt broke.
		{"manual", nil},
		// The main use in the live fleet.
		{"bypass", []string{"--dangerously-skip-permissions"}},
	} {
		t.Run(m.name, func(t *testing.T) { runContract(t, m.name, m.args) })
	}
}

func runContract(t *testing.T, mode string, args []string) {
	t.Helper()
	name := "tuictr-" + mode
	tn := session.TmuxName(name) // AtIdlePrompt/IsBusy look at claude_<name>, so match it
	dir := t.TempDir()

	// Write the folder trust before launching, the way production's
	// claude.ensureFolderTrusted does. The point is to keep the dialog from appearing at all:
	// 2.1.248 reversed the option order so the default became "No, exit" (measured; in
	// 2.1.247 "Yes, I trust this folder" was the default), which turns "let it appear, then
	// press Enter to accept" into an exit. That is what happened — the pane vanished and the
	// test waited 90 seconds on empty frames before failing. Production always writes it
	// first, so matching that is also the more correct contract: what this test wants to see
	// is the footer, not onboarding.
	preTrustFolder(t, dir)

	argv := append([]string{"new-session", "-d", "-s", tn, "-x", "200", "-y", "50", "-c", dir, "claude"}, args...)
	if out, err := exec.Command("tmux", argv...).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", tn).Run() })

	waitReady(t, name, tn)

	// D: the ready prompt right after launch, checked through the real functions (capture
	// included). The manual-mode regression (always false once the hint disappeared) fails
	// here.
	if !AtIdlePrompt(name) {
		t.Errorf("at the ready prompt but AtIdlePrompt=false — the footer contract may have changed\n%s", frameDump(tn))
	}
	if IsBusy(name) {
		t.Errorf("at the ready prompt but IsBusy=true\n%s", frameDump(tn))
	}

	// Send the turn.
	if out, err := exec.Command("tmux", "send-keys", "-t", tn, contractPrompt).CombinedOutput(); err != nil {
		t.Fatalf("send-keys: %v: %s", err, out)
	}
	time.Sleep(time.Second)
	if out, err := exec.Command("tmux", "send-keys", "-t", tn, "Enter").CombinedOutput(); err != nil {
		t.Fatalf("send-keys Enter: %v: %s", err, out)
	}

	frames, seen := sampleTurn(t, tn)

	// A: if the working display is never observed at all, either the display spec changed
	// fundamentally or the turn is not running. 2.1.220 draws no elapsed timer on short turns
	// and shows only the footer's "esc to interrupt", so both count as independent evidence.
	nGT := 0
	for _, f := range frames {
		if f.gt != "" {
			nGT++
		}
	}
	if nGT == 0 {
		t.Errorf("a turn was run, yet neither the elapsed timer nor the esc-to-interrupt display was "+
			"observed once (%d frames) — the TUI display spec may have changed fundamentally\n"+
			"observed lines:\n%s",
			len(frames), strings.Join(seen, "\n"))
		return
	}

	// B (the main check): every frame showing the working display must be judged busy. All
	// three regressions took the shape "spinnerRe is narrower than reality" ("esc to
	// interrupt" required, then it disappeared on rotation; "tokens" required, then thinking
	// draws no tokens). This differential check goes straight at that failure mode: working
	// frames are picked by a loose criterion independent of the detection logic (a
	// parenthesised elapsed timer, or the esc-to-interrupt footer, the only thing left on
	// 2.1.220's short turns), then production's spinnerActive is asked to keep up.
	miss := 0
	for i, f := range frames {
		if f.gt != "" && !f.busy {
			if miss++; miss == 1 {
				// With idle=true the Console goes as far as showing the "waiting for input"
				// badge (the stop button disappears) — exactly the symptom users saw.
				t.Errorf("the spinner is showing yet IsBusy=false (frame#%d, AtIdlePrompt=%v). "+
					"spinnerRe is not keeping up with reality:\n  %q", i, f.idle, f.gt)
			}
		}
	}
	if miss > 0 {
		t.Errorf("of %d frames showing the spinner, %d failed busy detection\nobserved lines:\n%s",
			nGT, miss, strings.Join(seen, "\n"))
	}

	// C: after the turn it settles back to idle. Mistaking the post-turn summary
	// ("✻ Worked for 13m 53s") for busy leaves the stop bar up forever, so check that
	// direction too.
	if IsBusy(name) {
		t.Errorf("IsBusy=true after the turn ended — the post-turn summary may be judged busy\n%s", frameDump(tn))
	}
	if !AtIdlePrompt(name) {
		t.Errorf("AtIdlePrompt=false after the turn ended\n%s", frameDump(tn))
	}

	nBusy, nTokenless := 0, 0
	for _, f := range frames {
		if f.busy {
			nBusy++
		}
		if f.gt != "" && !strings.Contains(f.gt, "tokens") {
			nTokenless++
		}
	}
	t.Logf("mode=%s: %d frames observed / spinner shown %d / judged busy %d / of those tokenless %d",
		mode, len(frames), nGT, nBusy, nTokenless)
	if nTokenless == 0 {
		// This is where the third regression lived. A run that never hits it is that much
		// weaker (an instant answer from the model shows no thinking phase). Not worth
		// failing over, but say so rather than trusting the green.
		t.Logf("note: this turn never hit the thinking phase (a tokenless spinner). " +
			"A token-dependent regression would not be detected by this run " +
			"(busy_thinking_no_tokens in testdata/footers pins it instead)")
	}
	t.Logf("observed spinner/footer lines:\n%s", strings.Join(seen, "\n"))
}

// waitReady waits until the composer prompt is reached. Failing to reach it means either "not
// authenticated" (the prime suspect in CI) or "the footer contract changed"; the two cannot be
// told apart here, so the failure names both.
func waitReady(t *testing.T, name, tn string) {
	t.Helper()
	deadline := time.Now().Add(readyWait)
	for time.Now().Before(deadline) {
		s := CapturePane(tn)
		// A pristine hosted runner has no saved theme. The workspace image normally
		// inherits an initialized persistent Claude config, so this one-time selector
		// is harness onboarding rather than the composer contract under test.
		if strings.Contains(s, "Syntax theme:") &&
			(strings.Contains(s, "Dark mode") || strings.Contains(s, "Light mode")) {
			_ = exec.Command("tmux", "send-keys", "-t", tn, "Enter").Run()
			time.Sleep(2 * time.Second)
			continue
		}
		// The folder-trust dialog on launch (it appears even with
		// --dangerously-skip-permissions). preTrustFolder normally suppresses it, but a
		// change to the upstream storage format brings it back. Never press Enter blind: the
		// default option moves at upstream's convenience (it actually flipped in 2.1.248),
		// and from that day on "accept" means "exit". Always select the Yes row first.
		if strings.Contains(s, "trust this folder") || strings.Contains(s, "Do you trust the files") {
			chooseTrustYes(t, tn)
			time.Sleep(2 * time.Second)
			continue
		}
		if AtIdlePrompt(name) {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("the composer prompt was not reached within %s.\n"+
		"Possible causes: (1) not authenticated (CLAUDE_CODE_OAUTH_TOKEN may not work in the\n"+
		"interactive TUI; ANTHROPIC_API_KEY raises a confirmation dialog), (2) the footer contract\n"+
		"changed and atPromptFooter no longer matches, (3) an unknown onboarding screen.\n%s",
		readyWait, frameDump(tn))
}

// frame is one observation of a single capture. busy and idle are decided against the SAME
// frame: calling IsBusy and AtIdlePrompt back to back would look at two different frames and
// race, so this one place uses the internal pure functions. The exported versions are
// exercised, execution path included, by the one-shot checks D and C.
type frame struct {
	busy bool
	idle bool
	gt   string // evidence line for "the spinner is showing", by the independent criterion
}

// gtSpinnerRe detects "the spinner is showing" deliberately loosely and independently of
// production's spinnerRe: it looks only for a parenthesised elapsed timer, regardless of the
// gerund, the ellipsis or the start of the line. Using spinnerRe here would mean no evidence
// exactly when it is broken, so the criterion must stay separate.
//
// The post-turn summary "✻ Cogitated for 6s" has no parentheses and so does not match, i.e.
// it does not demand busy.
var gtSpinnerRe = regexp.MustCompile(`\([^)\n]*[0-9]+(?:h|m|s)\b`)

// gtSpinnerLine returns the evidence line for "the working display is showing" ("" if none).
// On short turns 2.1.220 omits the header with the timer and draws only the footer's "esc to
// interrupt". Border lines such as the welcome box are skipped.
func gtSpinnerLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "│") || strings.HasPrefix(t, "╭") || strings.HasPrefix(t, "╰") {
			continue
		}
		if gtSpinnerRe.MatchString(t) || strings.Contains(t, "esc to interrupt") {
			return t
		}
	}
	return ""
}

// sampleTurn observes the turn, one capture per frame.
//
// spinnerActive cannot be used to detect the end (when it is the broken thing, the wait never
// returns). Nor can "busy stayed false for a while": no spinner is drawn while the answer text
// streams (measured on a 6-second turn: 1s thinking, spinner gone, 5s streaming, summary). So
// the end is taken from an independent signal, the appearance of the post-turn summary (a past
// tense verb plus " for ").
func sampleTurn(t *testing.T, tn string) (frames []frame, seen []string) {
	t.Helper()
	uniq := map[string]bool{}
	deadline := time.Now().Add(turnWait)
	for time.Now().Before(deadline) {
		s := CapturePane(tn)
		f := frame{busy: spinnerActive(s), idle: atIdlePrompt(s), gt: gtSpinnerLine(s)}
		frames = append(frames, f)
		for _, ln := range spinnerishLines(s) {
			uniq[ln] = true
		}
		if ln := findLine(s, modeFooterRe.MatchString); ln != "(none in frame)" {
			uniq[ln] = true
		}
		if f.gt == "" && postTurnSummary(s) != "" {
			break // summary is up and the spinner is gone: the turn ended
		}
		time.Sleep(tick)
	}
	for k := range uniq {
		seen = append(seen, "  "+k)
	}
	sort.Strings(seen)
	time.Sleep(2 * time.Second) // let the drawing settle before check C
	return frames, seen
}

// postTurnSummaryRe matches the summary line left after a turn ends ("✻ Worked for 13m 53s").
// The past tense verbs come from a different vocabulary than the spinner's gerunds, which is
// what makes them usable as an independent end-of-turn signal.
var postTurnSummaryRe = regexp.MustCompile(`(?m)^\S? ?(?:Baked|Brewed|Churned|Cogitated|Cooked|Crunched|Saut\x{00E9}ed|Worked) for [0-9]`)

func postTurnSummary(s string) string {
	if ln := findLine(s, postTurnSummaryRe.MatchString); ln != "(none in frame)" {
		return ln
	}
	return ""
}

// spinnerishLines picks up "lines that look like a spinner" by a condition unrelated to the
// detection logic. Using spinnerRe would produce nothing precisely when it is broken, i.e. no
// diagnosis exactly when one is wanted, so the criterion here is deliberately different.
//
// It returns every matching line: returning only the first one let the welcome box's
// "│ Opus 4.8 (1M context) with hi… · Claude Max · │" match first and swallow the real spinner
// line (measured). Border lines are excluded.
func spinnerishLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "│") || strings.HasPrefix(t, "╭") || strings.HasPrefix(t, "╰") {
			continue // border of the welcome box and friends
		}
		if strings.Contains(t, "…") && strings.Contains(t, "(") && strings.Contains(t, ")") {
			out = append(out, t)
			continue
		}
		if strings.HasPrefix(t, "✻") && strings.Contains(t, " for ") {
			out = append(out, t)
		}
	}
	return out
}

func frameDump(tn string) string {
	s := CapturePane(tn)
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 14 { // the tail (around the footer) is enough
		lines = lines[len(lines)-14:]
	}
	return "--- last frame (tail) ---\n" + strings.Join(lines, "\n") + "\n---"
}

var _ = os.Getenv // Authentication is left to the env (CLAUDE_CODE_OAUTH_TOKEN /
// ANTHROPIC_API_KEY) or an existing .credentials.json. Nothing is read here, so no
// credential is ever typed into the pane or written to a log.

// claudeStateFile is where claude keeps per-user state (onboarding + per-dir trust).
// Mirrors the CLI's own resolution: $CLAUDE_CONFIG_DIR/.claude.json when set (the
// Workspace sets it), else ~/.claude.json at the home ROOT — not ~/.claude/.
func claudeStateFile(t *testing.T) string {
	t.Helper()
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot resolve the home directory: %v", err)
	}
	return filepath.Join(home, ".claude.json")
}

// preTrustFolder pre-accepts the folder-trust dialog for dir, the way production's
// claude.ensureFolderTrusted does before every TUI launch. Merges into the existing
// file: the harness's own onboarding seed and whatever claude has written since must
// survive.
func preTrustFolder(t *testing.T, dir string) {
	t.Helper()
	p := claudeStateFile(t)
	root := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &root)
	}
	root["hasCompletedOnboarding"] = true
	if _, ok := root["theme"]; !ok {
		root["theme"] = "dark"
	}
	projects, _ := root["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	entry, _ := projects[dir].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	entry["hasTrustDialogAccepted"] = true
	projects[dir] = entry
	root["projects"] = projects
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatalf("cannot build %s: %v", p, err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatalf("cannot write %s: %v", p, err)
	}
}

// chooseTrustYes moves the selection onto "Yes, I trust this folder" and only then
// presses Enter. The highlighted row is marked with ❯; the option order is NOT stable
// across CLI versions, so the row is found by its text, never by position.
func chooseTrustYes(t *testing.T, tn string) {
	t.Helper()
	const yes = "Yes, I trust"
	for i := 0; i < 4; i++ {
		var cur string
		for _, ln := range strings.Split(CapturePane(tn), "\n") {
			if strings.Contains(ln, "❯") {
				cur = ln
				break
			}
		}
		if cur == "" {
			break // no selection marker found — report the whole frame below
		}
		if strings.Contains(cur, yes) {
			_ = exec.Command("tmux", "send-keys", "-t", tn, "Enter").Run()
			return
		}
		_ = exec.Command("tmux", "send-keys", "-t", tn, "Down").Run()
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("could not select the %q row in the trust dialog (the option shape may have changed)\n%s", yes, frameDump(tn))
}
