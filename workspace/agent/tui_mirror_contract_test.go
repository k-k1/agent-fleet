//go:build tui_contract

package main

// The shared skeleton of the minimal contract between a real interactive TUI and the mirror.
// Each CLI-specific test hands in the production BuildLaunch, and this file checks composer
// readiness -> a real turn -> the transcript -> idle, always in that observation order. Going
// through the production Agent interface rather than launching the CLI directly means the
// launch flags, the trust preparation and sid assignment/discovery are the production paths
// too.

import (
	"fmt"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

const (
	tuiContractReadyWait = 120 * time.Second
	tuiContractTurnWait  = 3 * time.Minute
	// How long to wait for one Enter to be taken up before pressing it again, and
	// how many presses to make in total (see submitPrompt).
	tuiContractSubmitWait  = 15 * time.Second
	tuiContractSubmitTries = 3
)

type tuiMirrorContractSpec struct {
	kind  string
	agent agents.Agent
}

func requireTUIContract(t *testing.T, ok bool, reason string) {
	t.Helper()
	if ok {
		return
	}
	if os.Getenv("E2E_REQUIRE") == "1" {
		t.Fatal(reason)
	}
	t.Skip(reason)
}

func runTUIMirrorContract(t *testing.T, spec tuiMirrorContractSpec) {
	t.Helper()
	for _, bin := range []string{"tmux"} {
		if _, err := exec.LookPath(bin); err != nil {
			requireTUIContract(t, false, fmt.Sprintf("%s is not on PATH: %v", bin, err))
		}
	}

	name := fmt.Sprintf("contract-%s-%d", spec.kind, os.Getpid())
	// A fresh tmux server inherits this test process's isolated HOME and auth env;
	// a pre-existing default server would retain the workspace's old environment and
	// make BuildLaunch's trust/auth preparation invisible to the TUI child. The
	// production tmux wrapper honours AF_TMUX_SOCKET, so paneMode and WireLive read
	// this same isolated server without reimplementing any product command.
	sock := fmt.Sprintf("af-tui-contract-%s-%d", spec.kind, os.Getpid())
	t.Setenv("AF_TMUX_SOCKET", sock)
	// This must be a defer, not t.Cleanup: each caller's isolated HOME is a
	// t.TempDir, whose cleanup may otherwise race Cursor's short-lived compiler
	// cache writer after the test function has returned.  Defers run before
	// testing.T's registered TempDir cleanup, and the socket is private to this
	// contract, so killing its server cannot affect a production tmux session.
	defer func() {
		_ = tmuxx.Cmd("kill-server").Run()
		time.Sleep(750 * time.Millisecond)
	}()

	meta := session.Meta{Name: name, Dir: t.TempDir(), Kind: spec.kind}
	plan, err := spec.agent.BuildLaunch(meta, agents.LaunchOpts{})
	if err != nil {
		t.Fatalf("BuildLaunch: %v", err)
	}
	tn := session.TmuxName(name)
	_ = tmuxx.Cmd("kill-session", "-t", tn).Run()
	if out, err := tmuxx.Cmd("new-session", "-d", "-s", tn, "-x", "200", "-y", "50", "-c", plan.Cwd, plan.Program).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}

	// paneMode IS the Console's launch-seed readiness gate. When the composer cannot be
	// recognised here, the first mirror prompt is swallowed by the startup screen.
	deadline := time.Now().Add(tuiContractReadyWait)
	for time.Now().Before(deadline) {
		if got := sessionx.PaneMode(spec.kind, tn); got != "" {
			if got != "Default" {
				t.Fatalf("composer mode = %q, want Default\npane:\n%s", got, tmuxx.CapturePane(tn))
			}
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if got := sessionx.PaneMode(spec.kind, tn); got == "" {
		t.Fatalf("composer readiness was never detected within %s\npane:\n%s", tuiContractReadyWait, tmuxx.CapturePane(tn))
	}
	// The first captured footer can precede the TUI's input-handler attachment by a
	// single render tick (observed with copilot): typing is accepted but that first
	// Enter is dropped. The fleet's launch seed already waits/polls before injecting;
	// retain one tick here so this contract validates the real prompt route, not that
	// transient UI race. submitPrompt covers the case where one tick is not enough.
	time.Sleep(500 * time.Millisecond)

	marker := "AF_" + strings.ToUpper(spec.kind) + "_TUI_MIRROR_OK"
	prompt := "Reply with exactly: " + marker
	if out, err := tmuxx.Cmd("send-keys", "-t", tn, "-l", prompt).CombinedOutput(); err != nil {
		t.Fatalf("send prompt: %v: %s", err, out)
	}
	seenWorking := submitPrompt(t, spec, meta, tn, marker)

	// Always read production's Transcript/WireLive: that is the path /messages and the
	// sessions poll use, so an answer that is merely visible in the TUI cannot produce a
	// false green.
	deadline = time.Now().Add(tuiContractTurnWait)
	for time.Now().Before(deadline) {
		live := spec.agent.WireLive(meta, true)
		if live.State == "working" {
			seenWorking = true
		}
		td, ok := spec.agent.Transcript(meta)
		if ok && mirrorHasTurn(td.Turns, "user", marker) && mirrorHasTurn(td.Turns, "assistant", marker) && live.State == "idle" {
			t.Logf("TUI/mirror contract ok: kind=%s source=%s observedWorking=%v", spec.kind, td.Path, seenWorking)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	td, _ := spec.agent.Transcript(meta)
	t.Fatalf("mirror did not show the completed user/assistant turn and idle recovery within %s; live=%q turns=%+v\npane:\n%s",
		tuiContractTurnWait, spec.agent.WireLive(meta, true).State, td.Turns, tmuxx.CapturePane(tn))
}

// submitPrompt presses Enter and CONFIRMS the turn actually started, pressing again
// when it did not. It reports whether the "working" state was observed while waiting,
// so a turn that finishes before the caller's loop starts is not miscounted.
//
// A TUI can attach its input handler a render tick after the composer footer first
// becomes readable (observed with copilot): the typed text lands but that Enter is
// swallowed, leaving the prompt sitting in the composer forever. A single unverified
// Enter then burns the whole turn window and reports a contract failure that is really
// one lost keystroke — how copilot-contract failed in CI (runs 30584244520 /
// 30667091055: pane still showed the prompt in the composer and "0 AIC used") while the
// same CLI version passed on a warm local machine.
//
// Confirmation goes through the production reads (WireLive / Transcript) rather than
// pane text, so it stays agent-agnostic: the turn is under way as soon as the state
// turns "working" or the user turn reaches the mirror. Pressing Enter again after the
// prompt did submit is a no-op — the composer is empty by then.
func submitPrompt(t *testing.T, spec tuiMirrorContractSpec, meta session.Meta, tn, marker string) bool {
	t.Helper()
	for attempt := 1; ; attempt++ {
		if out, err := tmuxx.Cmd("send-keys", "-t", tn, "Enter").CombinedOutput(); err != nil {
			t.Fatalf("submit prompt: %v: %s", err, out)
		}
		deadline := time.Now().Add(tuiContractSubmitWait)
		for time.Now().Before(deadline) {
			if spec.agent.WireLive(meta, true).State == "working" {
				return true
			}
			if td, ok := spec.agent.Transcript(meta); ok && mirrorHasTurn(td.Turns, "user", marker) {
				return false
			}
			time.Sleep(500 * time.Millisecond)
		}
		if attempt >= tuiContractSubmitTries {
			// Fall through rather than fail here: the turn wait below produces the
			// richer diagnostic, and a mirror that is merely slow still passes there.
			t.Logf("prompt submission unconfirmed after %d Enter(s); continuing to the turn wait\npane:\n%s",
				attempt, tmuxx.CapturePane(tn))
			return false
		}
		t.Logf("prompt submission unconfirmed within %s (Enter %d likely dropped); pressing Enter again",
			tuiContractSubmitWait, attempt)
	}
}

func mirrorHasTurn(turns []transcript.Turn, role, marker string) bool {
	for _, turn := range turns {
		if turn.Role == role && strings.Contains(turn.Text, marker) {
			return true
		}
	}
	return false
}
