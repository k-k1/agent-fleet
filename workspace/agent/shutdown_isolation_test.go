package main

// shutdown の対象選定の回帰テスト（docs/log/32 M1 E2E インシデントの恒久対応）:
// 停止処理が触ってよいのは「自メタ ∩ live」だけで、同じ tmux サーバに同居する
// 他インスタンスのセッション（＝自分のメタが無い live セッション）は対象外である
// ことを、AF_TMUX_SOCKET で隔離した専用サーバ上の実 tmux で確認する。

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// TestTmuxCmdSocketScope: AF_TMUX_SOCKET が全 tmux 呼び出しに -L として乗ること
// （サーバ不要の argv 検査）。
func TestTmuxCmdSocketScope(t *testing.T) {
	t.Setenv("AF_TMUX_SOCKET", "af-test-sock")
	got := tmuxx.Cmd("list-sessions").Args
	want := []string{"tmux", "-L", "af-test-sock", "list-sessions"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("Cmd args = %v, want %v", got, want)
	}
	t.Setenv("AF_TMUX_SOCKET", "")
	if got := tmuxx.Cmd("list-sessions").Args; strings.Join(got, " ") != "tmux list-sessions" {
		t.Fatalf("Cmd args (no socket) = %v", got)
	}
}

// TestOwnedLiveSessionsScopedToOwnMetas: 隔離ソケット上に「自メタ有り」と
// 「自メタ無し（他インスタンス相当）」の 2 セッションを並べ、shutdown の対象選定が
// 前者だけを返すこと。
func TestOwnedLiveSessionsScopedToOwnMetas(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := fmt.Sprintf("af-test-%d", os.Getpid())
	t.Setenv("AF_TMUX_SOCKET", sock)
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	// 専用ソケットに対してのみ kill-server が許される（dev/04 §4.11）。
	t.Cleanup(func() { _ = tmuxx.Cmd("kill-server").Run() })

	for _, name := range []string{"owned1", "foreign1"} {
		tn := session.TmuxName(name)
		if out, err := tmuxx.Cmd("new-session", "-d", "-s", tn, "sleep", "60").CombinedOutput(); err != nil {
			t.Fatalf("new-session %s: %v\n%s", tn, err, out)
		}
	}
	session.WriteMeta(session.Meta{Name: "owned1", Dir: t.TempDir(), Kind: session.KindShell})
	// live だがメタ無しの stopped も混ぜる: メタだけあって pane の無いものは対象外。
	session.WriteMeta(session.Meta{Name: "stopped1", Dir: t.TempDir(), Kind: session.KindShell})

	owned := ownedLiveSessions()
	if len(owned) != 1 || owned[0] != session.TmuxName("owned1") {
		t.Fatalf("ownedLiveSessions = %v, want [claude_owned1] (foreign/live-without-meta and meta-without-live must be excluded)", owned)
	}
}
