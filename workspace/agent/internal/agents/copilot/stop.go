package copilot

// TUI ルートの graceful stop。copilot は events.jsonl をライブ追記するため agy の
// ような「/exit しないと resume ID を失う」制約は無いが、/exit は inuse ロックの
// 解放とセッションチェックポイントの確定を伴う（実測: 終了サマリ表示）ので、
// kill 前に一度だけ試す。保留メニュー（許可/plan）が出ている間の Enter は
// ハイライト行の**承認を確定**してしまう（agy c639973 の実機実証と同型のリスク）
// — Escape で棄却してから打つ。

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// gracefulStopWindow is how long we wait for copilot to exit after /exit before
// the caller falls back to kill-session. Idle exit is fast; a mid-turn TUI may
// ignore the command and time out.
const gracefulStopWindow = 4 * time.Second

func (agentImpl) GracefulStop(m session.Meta) bool {
	tn := session.TmuxName(m.Name)
	pane := tmuxx.SessionPaneID(tn)
	if pane == "" {
		return false
	}
	if LiveState(m) == "question" {
		// 許可メニューが開いている: Enter が承認を踏む前に Escape で棄却する。
		_ = tmuxx.Cmd("send-keys", "-t", pane, "Escape").Run()
		time.Sleep(300 * time.Millisecond)
	}
	// C-u first: a draft in the composer would otherwise be submitted as
	// "<draft>/exit". Enter は別打鍵（実測: 同一 send-keys の Enter はペースト
	// 折り畳みに食われて確定しない）。
	_ = tmuxx.Cmd("send-keys", "-t", pane, "C-u").Run()
	_ = tmuxx.Cmd("send-keys", "-t", pane, "-l", "/exit").Run()
	time.Sleep(300 * time.Millisecond)
	_ = tmuxx.Cmd("send-keys", "-t", pane, "Enter").Run()
	deadline := time.Now().Add(gracefulStopWindow)
	for time.Now().Before(deadline) {
		if !tmuxx.HasSession(tn) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
