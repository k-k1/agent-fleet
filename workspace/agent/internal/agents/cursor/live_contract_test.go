package cursor

// 実バイナリ契約テスト（opt-in）: AF_CURSOR_LIVE=1 のときだけ実 `cursor-agent acp` を
// 子プロセスとして起動し、docs/log/40 の managed 契約が実 CLI で成立することを検証する —
// spawn→initialize→session/new→prompt(completed)→転写がメモリ構築される→（別プロセスで）
// session/load resume→文脈保持＋転写がリプレイから再構築される。認証は環境の Cursor
// ログイン（~/.config/cursor/auth.json の ambient 認証）前提。
// 実行例: AF_CURSOR_LIVE=1 go test -run TestLive -v ./internal/agents/cursor/
//
// 週次リリースの CLI なので、これがドリフト検知の一次防衛線（ACP の sessionUpdate 判別子・
// session/load リプレイ形状・stopReason が変わればここで落ちる）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func liveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("AF_CURSOR_LIVE") != "1" {
		t.Skip("AF_CURSOR_LIVE=1 で実 CLI 契約テストを有効化")
	}
}

// isolateHomeKeepAuth points HOME at a tempdir so AF state (sids / status) is isolated,
// while symlinking ~/.config/cursor so the CLI still finds its ambient login. We never
// read or copy the token — the symlink lets cursor-agent read its OWN config file.
func isolateHomeKeepAuth(t *testing.T) {
	t.Helper()
	real, _ := os.UserHomeDir()
	home := t.TempDir()
	if real != "" {
		if _, err := os.Stat(filepath.Join(real, ".config", "cursor")); err == nil {
			_ = os.MkdirAll(filepath.Join(home, ".config"), 0o755)
			_ = os.Symlink(filepath.Join(real, ".config", "cursor"), filepath.Join(home, ".config", "cursor"))
		}
	}
	t.Setenv("HOME", home)
}

func waitCompleted(t *testing.T, h *threadHandle) {
	t.Helper()
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		st := h.currentState()
		if st == agents.TurnCompleted {
			return
		}
		if st == agents.TurnFailed || st == agents.TurnUnknown || st == agents.TurnCancelled {
			t.Fatalf("turn ended abnormally: %s", st)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("turn did not complete (state %s)", h.currentState())
}

func TestLiveSpawnPromptResume(t *testing.T) {
	liveGate(t)
	isolateHomeKeepAuth(t)
	work := t.TempDir()

	h := &threadHandle{
		name: "live1", dir: work, slotSid: "live-slot-1",
		events: make(chan agents.Event, 64),
	}
	if err := h.spawn(agents.ThreadSettings{}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() { DropHandle("live1"); Shutdown() })
	handlesMu.Lock()
	handles["live1"] = h
	handlesMu.Unlock()

	if err := h.Send(agents.TurnInput{Prompt: "Reply with exactly: LIVE-OK", ClientMessageID: "lm1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitState(t, h, agents.TurnRunning) // spawn の初期 Completed を追い越さないよう turn 開始を待つ
	waitCompleted(t, h)

	sid := h.sid
	if sid == "" || sids.Read("live-slot-1") != sid {
		t.Fatalf("sid not captured: %q store=%q", sid, sids.Read("live-slot-1"))
	}
	// 転写がメモリ構築されている（ACP はローカルファイルを書かない）。
	turns := h.buf.snapshot()
	found := false
	for _, tn := range turns {
		if tn.Role == "assistant" && strings.Contains(tn.Text, "LIVE-OK") {
			found = true
		}
	}
	if !found {
		t.Fatalf("assistant turn missing from in-memory transcript: %+v", turns)
	}

	// 別プロセスでの resume（session/load）: 子を落として spawn し直し、文脈と転写が
	// 残っていることを実プロンプトで確認する。
	h.mu.Lock()
	oldCmd := h.cmd
	h.alive = false
	h.mu.Unlock()
	stopChild(oldCmd)
	time.Sleep(1 * time.Second)
	if err := h.spawn(agents.ThreadSettings{}); err != nil {
		t.Fatalf("respawn: %v", err)
	}
	if h.sid != sid {
		t.Fatalf("resume changed sid: %q → %q", sid, h.sid)
	}
	// session/load リプレイで転写が再構築されている（前ターンが残る）。
	replayed := h.buf.snapshot()
	if len(replayed) == 0 {
		t.Fatalf("transcript not rebuilt from session/load replay")
	}
	if err := h.Send(agents.TurnInput{Prompt: "What exact string did I ask you to reply with before? Answer with just that string.", ClientMessageID: "lm2"}); err != nil {
		t.Fatalf("send2: %v", err)
	}
	waitState(t, h, agents.TurnRunning)
	waitCompleted(t, h)
	turns = h.buf.snapshot()
	last := turns[len(turns)-1]
	if last.Role != "assistant" || !strings.Contains(last.Text, "LIVE-OK") {
		t.Fatalf("resume lost context; last turn: %+v", last)
	}
}
