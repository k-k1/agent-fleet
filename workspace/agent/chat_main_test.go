package main

// chat 家系を internal/chatx へ移したあと、**main に残ったテスト**（chat のハンドラや
// mcp の引数解釈のように、main の他家系を駆動するもの）が使うヘルパ。
// chatx 側の同名ヘルパは _test.go に居てパッケージ外から見えないので、ここに 1 組だけ持つ
// （テストヘルパはパッケージごとに持つのが Go の通例）。
//
// ⚠️ **駆動を変えていないこと**は、移送前後の両方に同じ変異を当てて確かめる（PR 本文）。

import (
	"context"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// withTempHome points HOME at a temp dir so the fstore/conversation stores write
// under the test's own tree（移送前の chat_report_test.go と同じ形）。
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

// mainStubProvider は main 側から差し替えるためのプロバイダ。
// **`chatx.ChatProvider` の `Send` が公開されているからこそ書ける** — 移送前は
// `chatProvider.send` が未公開で、パッケージ外からはスタブを作れなかった。
type mainStubProvider struct {
	reply string
	model string
	err   error
}

// 🔥 **ターンの開始と模擬の記録は chatx の作法どおりに通す。** `c.TurnModel` へ直接
// 代入すると**ターンの区切りを跨いで値が残り**、「stored conversation に漏れていないこと」を
// 見ている検査が落ちる（実際に落として気付いた）。移送前の modelChatProv と同じ手順。
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

// withTestReconciler は本物の reconciler を短い間隔で据える（移送前と同じ駆動）。
// 実体は chatx の内側なので、据え付けだけを継ぎ目から呼ぶ。
func withTestReconciler(t *testing.T, interval time.Duration) {
	t.Helper()
	t.Cleanup(chatx.InstallReconcilerForTest(interval))
}

// awaitReported は「指示行が配送された報告で reported になる」まで待つ（移送前と同じ）。
func awaitReported(t *testing.T, name string) {
	t.Helper()
	for i := 0; i < 150; i++ {
		if !chatx.SessionReportPending(name) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("指示行は配送された報告で reported になるべき (指示1件=報告1回): %s", name)
}
func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
