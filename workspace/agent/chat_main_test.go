package main

// Helpers for the tests that stay in main (the ones driving main's other families, such as
// the chat handlers or mcp argument parsing). The same-named helpers on the chatx side live
// in _test.go files and are invisible from outside that package, so main keeps its own one
// set, as is usual in Go.

import (
	"context"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// withTempHome points HOME at a temp dir so the fstore/conversation stores write
// under the test's own tree.
//
// The wait for the delivery goroutine must be registered AFTER `t.Setenv` (Cleanup runs
// LIFO, so it runs before HOME is restored). Without the wait, chatx's delivery writes a
// notification into the real HOME once it is back and a ghost notification shows up in the
// user's Console (interimDeliveries in chatx/chat_report.go).
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Cleanup(chatx.WaitInterimDeliveries)
	return dir
}

// mainStubProvider is the provider main substitutes in. It is only writable because
// `chatx.ChatProvider`'s `Send` is exported; an unexported method cannot be stubbed from
// outside the package.
type mainStubProvider struct {
	reply string
	model string
	err   error
}

// Start the turn and record the mock model through chatx's own path. Assigning `c.TurnModel`
// directly leaves the value in place across a turn boundary, which fails the check that the
// model does not leak into the stored conversation (observed).
func (p mainStubProvider) Send(_ context.Context, c *chatx.ChatConversation, _ string) (string, error) {
	c.StartTurn()
	if p.model != "" {
		c.NoteTurnModel(p.model)
	}
	return p.reply, p.err
}
func oneShotLiveTurns() []transcript.Turn {
	return []transcript.Turn{
		{Role: "user", Text: "使用量のグラフを作りたい", TS: "2026-07-25T09:00:00Z"},
		{Role: "assistant", Text: "機能別トークン計測の台帳を設計します。まず補助呼び出しの実在箇所を洗い出しました。", TS: "2026-07-25T09:01:00Z"},
	}
}

// withTestReconciler installs the real reconciler on a short interval. The implementation is
// inside chatx, so only the installation is called through the seam.
func withTestReconciler(t *testing.T, interval time.Duration) {
	t.Helper()
	t.Cleanup(chatx.InstallReconcilerForTest(interval))
}

// awaitReported waits until the instruction line turns reported by a delivered report.
func awaitReported(t *testing.T, name string) {
	t.Helper()
	for i := 0; i < 150; i++ {
		if !chatx.SessionReportPending(name) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("instruction line should become reported by a delivered report (1 instruction = 1 report): %s", name)
}
func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
