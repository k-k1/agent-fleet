package main

// Records WHY an agent session's process terminated (normal quit / crash / OOM kill).
//
// Sessions run as `tmux new-session -d <program>`, so the Agent never gets a
// cmd.Wait() on them — the pane's shell is the only place the exit status is visible.
// startSessionTmux therefore appends `; __af_ec=$?; workspace-agent record-exit <name>
// "$__af_ec"` after the agent CLI, and this subcommand runs in that shell once the CLI
// exits. $? is the CLI's wait status: 128+N on a signal kill, so an OOM SIGKILL(9)
// arrives here as 137. A deliberate `tmux kill-session` tears the wrapping shell down
// too, so record-exit never runs for an intentional stop — we therefore can't mislabel
// a stop as a crash (verified on tmux 3.3a: kill-session leaves no record; an inner
// SIGKILL records 137).
//
// この pane ラッパー経路は tui ドライバ専用。managed セッション（docs/log/27 §10.2-2）は
// daemon が supervisor の子プロセスなので cmd.Wait() で直接取れ、判定ロジック
// （status.ExitReasonFor / status.OOMKillCount — 本ファイルから internal/status へ
// 移設）を共用する。

import (
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// runRecordExit is `workspace-agent record-exit <name> <code>`. It reads the launch
// baseline (for OOM attribution) and writes the interpreted ExitInfo the sessions list
// surfaces. Best-effort and silent: it runs in a dying shell with nowhere to report to.
func runRecordExit(args []string) {
	if len(args) < 2 {
		return
	}
	name := args[0]
	if !session.ValidName(name) {
		return
	}
	code, err := strconv.Atoi(strings.TrimSpace(args[1]))
	if err != nil {
		return
	}
	// Baseline oom_kill captured at launch, so an OOM is attributed only when the
	// counter advanced DURING this session — not a stale count from an earlier death.
	base, _ := status.ReadExit(name)
	oomNow, okOOM := status.OOMKillCount()
	oomDuringSession := okOOM && oomNow > base.OOMBase

	sig := 0
	if code >= 128 {
		sig = code - 128
	}
	reason := status.ExitReasonFor(code, sig, oomDuringSession)
	status.PersistExit(name, status.ExitInfo{
		Reason: reason,
		Code:   code, Signal: sig,
		At:      time.Now().Format(time.RFC3339),
		OOMBase: base.OOMBase,
	})
	// Abnormal death of an operator-armed session reports to its conversation
	// (docs/log/30) — a clean quit / graceful stop is not report-worthy.
	switch reason {
	case "oom", "crashed", "killed":
		if chatx.SessionReportPending(name) {
			chatx.KickSessionReport(name, "exit", reason)
		}
		// Abnormal exits don't pass through the notice outbox (the sessions list
		// surfaces ExitInfo directly), so the chat bridge (docs/log/37 P1) gets its
		// own enqueue here. Plain file write — safe in this dying shell.
		display, sessKind := name, ""
		if m, ok := session.ReadMeta(name); ok {
			display, sessKind = session.Display(m), m.Kind
		}
		bridge.Enqueue(bridge.Message{Kind: "exit", SessionName: name,
			SessionKind: sessKind, DisplayName: display, Detail: reason})
	}
}
