//go:build tui_contract

// Live TUI contract probe for plan approval - Tier 2 (run from claude-tui-contract.yml).
//
// What it protects: the badge on the Console's plan card agrees with the real outcome. This
// has broken twice, and both times the unit tests stayed green:
//   - reject was bound to a fixed key position (Down x3) and wrapped around to Yes on a short
//     menu, so a reject became an approval;
//   - the approval result grew to carry the whole plan body, and the keyword check picked up
//     the word "reject" out of that body, badging an approval as rejected.
//
// Two parts, neither of which a fixed capture can detect:
//
//	A. The premise behind the approve key - the default row (❯) of the ExitPlanMode menu is
//	   always "Yes". Production approves by pressing Enter, i.e. by choosing the default, so
//	   the day the default stops being Yes the approve button stops approving. Checked
//	   against the live TUI, not a fixed capture under testdata.
//	B. How the outcome text is read - approve for real and confirm the Answer that
//	   production's transcript read returns reads as approved and has not swallowed the plan
//	   body.
//
// Cost: one real turn (one short plan plus one move after approval).
package main

import (
	"bytes"
	"fmt"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

const (
	planModalWait   = 4 * time.Minute
	planOutcomeWait = 3 * time.Minute
)

// A tiny request that only makes claude produce a plan. In plan mode it stops at
// ExitPlanMode asking for a decision, and the work after approval is a single file.
const planContractPrompt = "Plan only: propose creating a file notes.txt whose single line is the word tmux. " +
	"Keep the plan to two short bullets and present it for approval now."

// planMenuLineRe matches an ExitPlanMode option row. Same shape as in
// tmuxx/plan_approval_test.go, which locks fixed captures; this one runs against live frames.
var planMenuLineRe = regexp.MustCompile(`^(❯\s+)?([0-9]+)\.\s+(.*\S)\s*$`)

type planMenuOption struct {
	n         int
	label     string
	isDefault bool
}

func parseLivePlanMenu(capture string) []planMenuOption {
	var opts []planMenuOption
	for _, ln := range strings.Split(capture, "\n") {
		m := planMenuLineRe.FindStringSubmatch(strings.TrimSpace(ln))
		if m == nil {
			continue
		}
		n := 0
		_, _ = fmt.Sscanf(m[2], "%d", &n)
		opts = append(opts, planMenuOption{n: n, label: m[3], isDefault: m[1] != ""})
	}
	return opts
}

func TestClaudePlanApprovalContractLive(t *testing.T) {
	for _, bin := range []string{"tmux", "claude"} {
		if _, err := exec.LookPath(bin); err != nil {
			requireTUIContract(t, false, fmt.Sprintf("%s is not on PATH: %v", bin, err))
		}
	}
	if v, err := exec.Command("claude", "--version").Output(); err == nil {
		t.Logf("claude version: %s", strings.TrimSpace(string(v)))
	}

	name := fmt.Sprintf("contract-plan-%d", os.Getpid())
	sock := fmt.Sprintf("af-plan-contract-%d", os.Getpid())
	t.Setenv("AF_TMUX_SOCKET", sock)
	defer func() {
		_ = tmuxx.Cmd("kill-server").Run()
		time.Sleep(750 * time.Millisecond)
	}()

	// Use production's launch plan unchanged: the plan-mode flags and the folder trust both
	// go through the production path.
	meta := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, Mode: "plan"}
	agent := claude.New()
	launch, err := agent.BuildLaunch(meta, agents.LaunchOpts{})
	if err != nil {
		t.Fatalf("BuildLaunch: %v", err)
	}
	tn := session.TmuxName(name)
	if out, err := tmuxx.Cmd("new-session", "-d", "-s", tn, "-x", "200", "-y", "50", "-c", launch.Cwd, launch.Program).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}

	// Composer readiness uses the same check as the Console's launch seed. We started in plan
	// mode, so the mode chip should read Plan; if it does not, --permission-mode is being
	// treated differently.
	deadline := time.Now().Add(tuiContractReadyWait)
	mode := ""
	for time.Now().Before(deadline) {
		// Always select the row before pressing Enter on a startup dialog. Upstream swaps the
		// default around at will, and on 2.1.248 the trust dialog defaulted to "No, exit", so
		// a blind Enter ends the session. Production launches with
		// --allow-dangerously-skip-permissions, so seeing this confirmation is normal in an
		// environment where no ack has been stored.
		frame := tmuxx.CapturePane(tn)
		switch {
		case strings.Contains(frame, "Bypass Permissions mode") && strings.Contains(frame, "Yes, I accept"):
			chooseDialogRow(t, tn, "Yes, I accept")
			time.Sleep(2 * time.Second)
			continue
		case strings.Contains(frame, "trust this folder") || strings.Contains(frame, "Do you trust the files"):
			chooseDialogRow(t, tn, "Yes, I trust")
			time.Sleep(2 * time.Second)
			continue
		}
		if mode = sessionx.PaneMode(session.KindClaude, tn); mode != "" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if mode == "" {
		t.Fatalf("composer was not recognized within %s\npane:\n%s", tuiContractReadyWait, tmuxx.CapturePane(tn))
	}
	if !strings.EqualFold(mode, "Plan") {
		t.Errorf("mode chip = %q, want Plan - the plan-mode launch flags may have changed", mode)
	}

	if out, err := tmuxx.Cmd("send-keys", "-t", tn, "-l", planContractPrompt).CombinedOutput(); err != nil {
		t.Fatalf("send prompt: %v: %s", err, out)
	}
	time.Sleep(time.Second)
	if out, err := tmuxx.Cmd("send-keys", "-t", tn, "Enter").CombinedOutput(); err != nil {
		t.Fatalf("submit prompt: %v: %s", err, out)
	}

	// --- A: confirm on the live menu that the default is always Yes ----------------
	var opts []planMenuOption
	deadline = time.Now().Add(planModalWait)
	for time.Now().Before(deadline) {
		frame := tmuxx.CapturePane(tn)
		if o := parseLivePlanMenu(frame); len(o) >= 2 && hasYesRow(o) {
			opts = o
			t.Logf("ExitPlanMode modal:\n%s", frame)
			break
		}
		time.Sleep(time.Second)
	}
	if opts == nil {
		t.Fatalf("no plan approval modal appeared within %s (the model never called ExitPlanMode, "+
			"or the modal changed shape)\npane:\n%s", planModalWait, tmuxx.CapturePane(tn))
	}
	def := -1
	for i, o := range opts {
		if o.isDefault {
			def = i
		}
	}
	if def < 0 {
		t.Fatalf("no default row (❯) found - production approves by Enter, i.e. by taking the "+
			"default, so there is no telling what would be chosen:\n%+v", opts)
	}
	if !isYesLabel(opts[def].label) {
		t.Fatalf("the default row is not Yes: %q. The Console's approve button (Enter) now "+
			"picks something other than approve - revisit PLAN_APPROVE_KEYS in planDecision.ts\n%+v", opts[def].label, opts)
	}
	t.Logf("approve-key premise OK: default = %q (%d options)", opts[def].label, len(opts))

	// The same keystroke production approves with (PLAN_APPROVE_KEYS = ["Enter"] in planDecision.ts).
	if out, err := tmuxx.Cmd("send-keys", "-t", tn, "Enter").CombinedOutput(); err != nil {
		t.Fatalf("approve: %v: %s", err, out)
	}

	// --- B: classify the outcome text through production's read path ---------------
	// Read exactly as /messages does: pick it up with CollectTurns (resolution within the
	// window) and, when that is empty, fill in from CollectInteractionAnswers (the qid →
	// outcome map over the whole transcript), as the Console does. claude has no generic
	// Agent.Transcript - package main's /messages calls these two directly - so calling the
	// same two here is the production path.
	sid := session.UUID(meta.Dir, meta.Name)
	var part *transcript.Part
	var lines [][]byte
	outcome := ""
	deadline = time.Now().Add(planOutcomeWait)
	for time.Now().Before(deadline) && outcome == "" {
		lines = claude.TranscriptLines(sid)
		turns := claude.CollectTurns(lines, 0, len(lines))
		answers := claude.CollectInteractionAnswers(lines)
		for i := range turns {
			for pi := range turns[i].Parts {
				p := &turns[i].Parts[pi]
				if p.Kind != "plan" {
					continue
				}
				part = p
				if outcome = p.Answer; outcome == "" {
					outcome = answers[p.QID].Text
				}
			}
		}
		if outcome == "" {
			time.Sleep(time.Second)
		}
	}
	if part == nil {
		t.Fatalf("the plan never appeared in the transcript within %s (ExitPlanMode's record format "+
			"may have changed)\nsid=%s\nrecords mentioning ExitPlanMode:\n%s\npane:\n%s",
			planOutcomeWait, sid, planRecordDump(lines), tmuxx.CapturePane(tn))
	}
	if outcome == "" {
		t.Fatalf("the plan's outcome never appeared in the transcript within %s (approval went "+
			"through, yet the tool_result is not being read = the card stays undecided)\nsid=%s\npane:\n%s", planOutcomeWait, sid, tmuxx.CapturePane(tn))
	}
	t.Logf("ExitPlanMode outcome (after planAnswerHead): %q", outcome)
	if v := claude.PlanVerdict(outcome); v != claude.PlanApproved {
		t.Errorf("approved, yet the verdict is %q - with this wording the Console badge will not "+
			"read approved. Realign the vocabulary in planDecision.ts / plan_verdict.go with the "+
			"live text (this is the 2026-08-31 regression again):\n  %q",
			v, outcome)
	}
	// If the form that appends the plan body to the approval result (`## Approved Plan:`)
	// switches to a different marker, the body flows into the verdict and the badge goes
	// wrong. Watch for it by requiring no plan to remain after the head is cut off.
	if body := strings.TrimSpace(part.Plan); body != "" && strings.Contains(outcome, body) {
		t.Errorf("the outcome text still carries the whole plan body = the embedded marker changed. "+
			"Realign planAnswerHead / PLAN_BODY_MARKER with the live text:\n  %q", outcome)
	}
}

// chooseDialogRow moves the ❯ marker onto the row containing want and only then presses
// Enter. Never blind-Enter a startup dialog: the highlighted row is whatever upstream
// decided this week, and on 2.1.248 that was "No, exit".
func chooseDialogRow(t *testing.T, tn, want string) {
	t.Helper()
	for i := 0; i < 5; i++ {
		cur := ""
		for _, ln := range strings.Split(tmuxx.CapturePane(tn), "\n") {
			if strings.Contains(ln, "❯") {
				cur = ln
				break
			}
		}
		if cur == "" {
			break
		}
		if strings.Contains(cur, want) {
			_ = tmuxx.Cmd("send-keys", "-t", tn, "Enter").Run()
			return
		}
		_ = tmuxx.Cmd("send-keys", "-t", tn, "Down").Run()
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("could not select the %q row in the startup dialog (the options may have changed shape)\npane:\n%s",
		want, tmuxx.CapturePane(tn))
}

func isYesLabel(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "yes")
}

func hasYesRow(opts []planMenuOption) bool {
	for _, o := range opts {
		if isYesLabel(o.label) {
			return true
		}
	}
	return false
}

// planRecordDump lists the transcript records that mention ExitPlanMode. Without it a broken
// read path only says "the old shape is gone", which is the half of the answer that does not
// help - the new shape is the half that does. The prompt this test sends is synthetic (notes.txt
// containing "tmux"), so the records carry nothing worth withholding from the run log; they are
// cut to a readable size instead.
func planRecordDump(lines [][]byte) string {
	const (
		maxRecords = 5
		maxBytes   = 2000
	)
	var b strings.Builder
	n := 0
	for _, ln := range lines {
		if !bytes.Contains(ln, []byte("ExitPlanMode")) {
			continue
		}
		s := string(ln)
		if len(s) > maxBytes {
			s = strings.ToValidUTF8(s[:maxBytes], "") + "…(truncated)"
		}
		b.WriteString("  " + s + "\n")
		if n++; n >= maxRecords {
			b.WriteString("  …(further records omitted)\n")
			break
		}
	}
	if n == 0 {
		return "  (nothing mentions ExitPlanMode at all - the tool call itself never reached the transcript)"
	}
	return b.String()
}
