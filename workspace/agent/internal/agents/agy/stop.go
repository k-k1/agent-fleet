package agy

// agy は cwd→会話マップ（cache/last_conversations.json = resume 用 UUID の即時
// ソース）を **graceful exit 時にしか書かない**（v1.1.4、統合 E2E 実測）。Track D
// の「初回プロンプトで書く」観測は `-p` 実行がプロンプト毎にプロセス終了して
// いたための見え方で、TUI 常駐セッションでは会話 DB だけが先に生まれ、マップは
// /exit まで更新されない。pane を kill-session で即死させると UUID が永久に
// 失われるため、停止前に /exit を打って flush の機会を与える（agents.GracefulStopper）。

import (
	"os/exec"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// gracefulStopWindow is how long we wait for agy to exit after /exit before the
// caller falls back to kill-session. The flush is a small JSON write — exit is
// fast when the TUI is idle; a mid-turn TUI may ignore the command and time out.
const gracefulStopWindow = 4 * time.Second

func (agentImpl) GracefulStop(m session.Meta) bool {
	tn := session.TmuxName(m.Name)
	pane := tmuxx.SessionPaneID(tn)
	if pane == "" {
		return false
	}
	// A pending interactive prompt (ASK_QUESTION / permission menu) swallows the
	// "/exit" text but its Enter CONFIRMS the highlighted first row — halting a
	// session mid-permission silently APPROVED the tool call (実機実証: 保留中
	// halt でファイル作成が承認された)。Escape dismisses either menu (question:
	// Skip, permission: cancel) without choosing, so clear the modal first.
	if st, _ := Probe(m); st != "" {
		_ = exec.Command("tmux", "send-keys", "-t", pane, "Escape").Run()
		time.Sleep(300 * time.Millisecond)
	}
	// C-u first: a draft sitting in the input box would otherwise be submitted
	// as "<draft>/exit" — a quota-burning prompt instead of an exit.
	_ = exec.Command("tmux", "send-keys", "-t", pane, "C-u").Run()
	_ = exec.Command("tmux", "send-keys", "-t", pane, "-l", "/exit").Run()
	// Enter as a separate keystroke after a beat (same Ink/bubbletea quirk as the
	// auth/usage flows: a combined write can swallow the CR).
	time.Sleep(300 * time.Millisecond)
	_ = exec.Command("tmux", "send-keys", "-t", pane, "Enter").Run()
	deadline := time.Now().Add(gracefulStopWindow)
	for time.Now().Before(deadline) {
		if !tmuxx.HasSession(tn) {
			// The exit flushed the map — adopt the conversation UUID right now
			// rather than waiting for a poll that may never come (stop forgets
			// the meta immediately).
			captureConversation(m)
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
