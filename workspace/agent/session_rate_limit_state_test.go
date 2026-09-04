package main

// 利用上限メニュー（claude の /rate-limit-options）に貼り付いたセッションが
// 「進行中」ではなく blocked として読まれること — 実ペインを隔離 tmux サーバに立てて
// driveState をそのまま走らせる（docs/log/47 §4-3）。
//
// 判定そのもの（フレーム → 真偽）は internal/tmuxx のゴールデンコーパスが押さえている。
// ここで見るのは配線の側: capture → 分類 → 自己修復 → 返す状態、という経路が claude の
// 実メタに対して繋がっていること。壊れた本番の形は「マーカーが working のまま永久に
// 残る」だったので、マーカー側も併せて確認する。

import (
	"fmt"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// paneShowing starts an isolated tmux session whose pane displays frame's contents and
// then stays alive, and returns the session meta for it.
func paneShowing(t *testing.T, name, frame string) session.Meta {
	t.Helper()
	// Check that the frame exists, here. Without it only `cat` fails while `new-session` still
	// succeeds, so callers wave an EMPTY pane through as the thing under test — which is exactly
	// what happened when the move changed the depth of the relative paths. A check that lists
	// the callers by hand can only notice the list shrinking, so the call site itself is guarded.
	if _, err := os.Stat(frame); err != nil {
		t.Fatalf("frame %s is missing: %v (suspect the depth of the relative path; left alone the pane shows nothing and the check stays green)", frame, err)
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tn := session.TmuxName(name)
	// Take a real pane's width: different wrapping changes how the footer/choice lines look.
	out, err := tmuxx.Cmd("new-session", "-d", "-s", tn, "-x", "200", "-y", "50",
		"sh", "-c", fmt.Sprintf("cat %q; sleep 60", frame)).CombinedOutput()
	if err != nil {
		t.Fatalf("new-session %s: %v\n%s", tn, err, out)
	}
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	// Wait for cat to finish drawing into the pane (capture-pane reads the drawn screen).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tmuxx.CapturePane(tn) != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return m
}

// tmuxSocketSeq は隔離 tmux サーバに**毎回違う名前**を与える連番。
var tmuxSocketSeq atomic.Int64

// isolatedTmuxSocket は**誰とも共有しない** tmux ソケット名を返す。
//
// 🔥 隔離ソケットの名前を固定すると、`kill-server` を撃つテストどうしが競る（理由は
// isolateAgentState の注記）。名前の作り方をここ 1 箇所に置いているのは、**同じ規則を
// 2 度書くと片方だけ古くなる**から —— 実際 `shutdown_isolation_test.go` が同じ名前を
// 独自に組み立てていて、そこだけ直っていなかった（#311 では所有権の外だった）。
//
// この関数がこのファイルに居るのは所有権の都合で、意味の上では tmux 隔離の共有部品である。
func isolatedTmuxSocket() string {
	return fmt.Sprintf("af-test-%d-%d", os.Getpid(), tmuxSocketSeq.Add(1))
}

func isolateAgentState(t *testing.T) {
	t.Helper()
	// Vary the socket name per test. It used to be a fixed `af-test-<pid>`, so every test in
	// the four files that use this isolation (plus shutdown_isolation_test.go, which builds the
	// same name) shared ONE tmux server. Each test's Cleanup fires `kill-server`, but tmux
	// returns as soon as it has taken the command and the server's shutdown is asynchronous. So
	// the next test's `new-session` reaches a dying server and fails with
	// `server exited unexpectedly` — a red with no visible reason, unrelated to the test body.
	//
	// The window widens with load (measured 2026-09-02: 0 occurrences at `-count=30` with no
	// load, 7 at `-count=40` under 6 CPU hogs; the failures were TestDriveStateIdlePaneNotBlocked
	// and TestDriveStateAuthValid, the same shape as real CI run 33584943716).
	//
	// The serial number is there for `-count=N`: with the test name alone, a round races the
	// kill-server of the PREVIOUS round of the same name.
	t.Setenv("AF_TMUX_SOCKET", isolatedTmuxSocket())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	// The status store sits directly under HOME (paths.AgentConfigDir) — never write a marker
	// into the real fleet.
	t.Setenv("HOME", t.TempDir())
	// Isolate claude's config/credentials too. HOME alone is not enough: in this container
	// CLAUDE_CONFIG_DIR points at the real fleet's tree, so the state decision (auth expired,
	// docs/log/47 §4-8) would depend on the real login's expiry.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	// kill-server is allowed only against a dedicated socket (dev/04 §4.11).
	t.Cleanup(func() { _ = tmuxx.Cmd("kill-server").Run() })
}

// TestDriveStateRateLimitModalBlocks: 上限メニューのペインは blocked を返し、貼り付きの
// 原因だった working マーカーが解消されること。
func TestDriveStateRateLimitModalBlocks(t *testing.T) {
	isolateAgentState(t)
	m := paneShowing(t, "ratelimit1", "internal/tmuxx/testdata/footers/modal_rate_limit.txt")
	sid := session.UUID(m.Dir, m.Name)
	// 本番と同じ初期条件: ターンは開始済み（working）で Stop は鳴っていない。
	status.Persist(sid, "working")

	if got := sessionx.DriveState(m, true, true); got != agents.StateBlocked {
		t.Fatalf("driveState = %q, want %q（上限メニューは 進行中 ではない）", got, agents.StateBlocked)
	}
	// 自己修復が通っていること。ここが効かないのが元のバグで、マーカーが working のまま
	// 残ると reaper が busy と数え続けコンテナが解放されない。
	if st, ok := status.Read(sid); ok && st.State == "working" {
		t.Error("status marker はまだ working — 自己修復が走っていない（元の貼り付きと同じ状態）")
	}
	// メニューが出ている間は何度 poll しても blocked のまま（状態が振動しない）。
	if got := sessionx.DriveState(m, true, true); got != agents.StateBlocked {
		t.Errorf("2 回目の driveState = %q, want %q", got, agents.StateBlocked)
	}
}

// TestDriveStateIdlePaneNotBlocked: 通常の待機ペインを blocked と誤判定しないこと。
// 誤検知側は「走っているセッションを停止扱いにして注入を弾く」ので、こちらも固定する。
func TestDriveStateIdlePaneNotBlocked(t *testing.T) {
	isolateAgentState(t)
	m := paneShowing(t, "ratelimit2", "internal/tmuxx/testdata/footers/idle_bypass_hint.txt")
	if got := sessionx.DriveState(m, true, true); got == agents.StateBlocked {
		t.Fatalf("driveState = %q — 通常の待機ペインを上限メニューと誤判定している", got)
	}
}
