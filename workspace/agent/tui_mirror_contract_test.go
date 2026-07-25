//go:build tui_contract

package main

// 実対話 TUI とミラーの最小 contract 共通骨格。各 CLI 固有のテストが本番の
// BuildLaunch を渡し、ここは「composer readiness → 実ターン → 転写 → idle」を
// 同じ観測順で検証する。単に CLI を直接起動するのでなく production の Agent
// interface を通すため、起動フラグ・trust 準備・sid 採番/発見も本番経路になる。

import (
	"fmt"
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
			requireTUIContract(t, false, fmt.Sprintf("%s が PATH にありません: %v", bin, err))
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

	// paneMode は Console の launch-seed readiness gate そのもの。ここで composer
	// を認識できない場合は、最初のミラープロンプトが起動画面に食われる。
	deadline := time.Now().Add(tuiContractReadyWait)
	for time.Now().Before(deadline) {
		if got := paneMode(spec.kind, tn); got != "" {
			if got != "Default" {
				t.Fatalf("composer mode = %q, want Default\npane:\n%s", got, tmuxx.CapturePane(tn))
			}
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if got := paneMode(spec.kind, tn); got == "" {
		t.Fatalf("composer readiness was never detected within %s\npane:\n%s", tuiContractReadyWait, tmuxx.CapturePane(tn))
	}
	// The first captured footer can precede the TUI's input-handler attachment by a
	// single render tick (observed with copilot): typing is accepted but that first
	// Enter is dropped. The fleet's launch seed already waits/polls before injecting;
	// retain one tick here so this contract validates the real prompt route, not that
	// transient UI race.
	time.Sleep(500 * time.Millisecond)

	marker := "AF_" + strings.ToUpper(spec.kind) + "_TUI_MIRROR_OK"
	prompt := "Reply with exactly: " + marker
	if out, err := tmuxx.Cmd("send-keys", "-t", tn, "-l", prompt).CombinedOutput(); err != nil {
		t.Fatalf("send prompt: %v: %s", err, out)
	}
	if out, err := tmuxx.Cmd("send-keys", "-t", tn, "Enter").CombinedOutput(); err != nil {
		t.Fatalf("submit prompt: %v: %s", err, out)
	}

	// 毎回 production の Transcript/WireLive を読む。これは /messages と sessions
	// poll が使う経路なので、TUI に返答が見えるだけの false green を避けられる。
	deadline = time.Now().Add(tuiContractTurnWait)
	seenWorking := false
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

func mirrorHasTurn(turns []transcript.Turn, role, marker string) bool {
	for _, turn := range turns {
		if turn.Role == role && strings.Contains(turn.Text, marker) {
			return true
		}
	}
	return false
}
