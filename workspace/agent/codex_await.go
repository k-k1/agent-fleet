package main

import (
	"fmt"
	"os"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
)

// runCodexAwaitThread is the `codex-await-thread <addr> <thread id>` pane prefix: block until
// the shared app-server has unloaded the thread the TUI is about to resume. It never fails the
// launch — every outcome exits 0 and codex starts next.
func runCodexAwaitThread(args []string) {
	if len(args) != 2 {
		return
	}
	addr, tid := args[0], args[1]
	done := make(chan bool, 1)
	go func() { done <- codex.AwaitThreadUnloaded(addr, tid, codex.ThreadReleaseTimeout, 2*time.Second) }()
	select {
	case ok := <-done:
		if !ok {
			fmt.Fprintln(os.Stderr, "managed 側がこの会話を手放しませんでした。codex を起動します（ロック画面が出たら r で再試行してください）。")
		}
		return
	case <-time.After(time.Second):
	}
	// Only say something when the wait is real: an already released thread returns at once.
	fmt.Fprintln(os.Stderr, "managed 実行方式がこの会話を手放すのを待っています（通常 1 分ほど、最長 3 分）…")
	if !<-done {
		fmt.Fprintln(os.Stderr, "managed 側がこの会話を手放しませんでした。codex を起動します（ロック画面が出たら r で再試行してください）。")
	}
}
