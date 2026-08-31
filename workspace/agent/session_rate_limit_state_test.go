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
	"os"
	"os/exec"
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
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tn := session.TmuxName(name)
	// 幅を実ペイン相当に取る: 折返しが変わるとフッタ/選択肢行の見え方が変わる。
	out, err := tmuxx.Cmd("new-session", "-d", "-s", tn, "-x", "200", "-y", "50",
		"sh", "-c", fmt.Sprintf("cat %q; sleep 60", frame)).CombinedOutput()
	if err != nil {
		t.Fatalf("new-session %s: %v\n%s", tn, err, out)
	}
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	// cat がペインへ描き終わるのを待つ（capture-pane は描画済みの画面を読む）。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tmuxx.CapturePane(tn) != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return m
}

func isolateAgentState(t *testing.T) {
	t.Helper()
	t.Setenv("AF_TMUX_SOCKET", fmt.Sprintf("af-test-%d", os.Getpid()))
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	// status ストアは HOME 直下（paths.AgentConfigDir）— 実フリートのマーカーを書かない。
	t.Setenv("HOME", t.TempDir())
	// claude の設定/資格情報も隔離する。HOME だけでは足りない: このコンテナでは
	// CLAUDE_CONFIG_DIR が実フリートの木を指しているので、状態判定（認証切れ・docs/log/47
	// §4-8）が実際のログイン期限に左右されてしまう。
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	// 専用ソケットに対してのみ kill-server が許される（dev/04 §4.11）。
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

	if got := driveState(m, true, true); got != agents.StateBlocked {
		t.Fatalf("driveState = %q, want %q（上限メニューは 進行中 ではない）", got, agents.StateBlocked)
	}
	// 自己修復が通っていること。ここが効かないのが元のバグで、マーカーが working のまま
	// 残ると reaper が busy と数え続けコンテナが解放されない。
	if st, ok := status.Read(sid); ok && st.State == "working" {
		t.Error("status marker はまだ working — 自己修復が走っていない（元の貼り付きと同じ状態）")
	}
	// メニューが出ている間は何度 poll しても blocked のまま（状態が振動しない）。
	if got := driveState(m, true, true); got != agents.StateBlocked {
		t.Errorf("2 回目の driveState = %q, want %q", got, agents.StateBlocked)
	}
}

// TestDriveStateIdlePaneNotBlocked: 通常の待機ペインを blocked と誤判定しないこと。
// 誤検知側は「走っているセッションを停止扱いにして注入を弾く」ので、こちらも固定する。
func TestDriveStateIdlePaneNotBlocked(t *testing.T) {
	isolateAgentState(t)
	m := paneShowing(t, "ratelimit2", "internal/tmuxx/testdata/footers/idle_bypass_hint.txt")
	if got := driveState(m, true, true); got == agents.StateBlocked {
		t.Fatalf("driveState = %q — 通常の待機ペインを上限メニューと誤判定している", got)
	}
}
