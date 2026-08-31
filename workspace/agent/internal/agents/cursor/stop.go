package cursor

// TUI ルートの graceful stop。cursor は JSONL をライブ追記し resume は AF 採番の
// UUID 固定なので、agy のような「終了しないと resume ID を失う」制約は無い。ただし
// cursor-agent はターン後に worker-server 常駐プロセスを残す（実測 — docs/log/40）ので、
// kill-session でパネルを潰す前に一度だけ正規終了（Ctrl+D 二度押し — 実測/docs）を
// 試し、CLI 自身に後片付けさせる。保留メニュー（許可/plan）が出ている間の Enter は
// ハイライト行を承認してしまう（copilot c639973 と同型リスク）——Escape で棄却して
// から打つ。

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// gracefulStopWindow は Ctrl+D 二度押し後に終了を待つ上限。idle 終了は速いが、
// ターン中の TUI は無視し得るのでタイムアウトで caller が kill にフォールバック。
const gracefulStopWindow = 4 * time.Second

func (agentImpl) GracefulStop(m session.Meta) bool {
	tn := session.TmuxName(m.Name)
	pane := tmuxx.SessionPaneID(tn)
	if pane == "" {
		return false
	}
	if LiveState(m) == "working" {
		// 進行中なら先に中断（Esc）してから終了する。
		_ = tmuxx.Cmd("send-keys", "-t", pane, "Escape").Run()
		time.Sleep(300 * time.Millisecond)
	}
	// C-u: コンポーザのドラフトを消す（残っていると Ctrl+D が別解釈され得る）。
	_ = tmuxx.Cmd("send-keys", "-t", pane, "C-u").Run()
	// Ctrl+D 二度押しで終了（実測: 一度目は確認、二度目で exit）。
	_ = tmuxx.Cmd("send-keys", "-t", pane, "C-d").Run()
	time.Sleep(300 * time.Millisecond)
	_ = tmuxx.Cmd("send-keys", "-t", pane, "C-d").Run()
	deadline := time.Now().Add(gracefulStopWindow)
	for time.Now().Before(deadline) {
		if !tmuxx.HasSession(tn) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
