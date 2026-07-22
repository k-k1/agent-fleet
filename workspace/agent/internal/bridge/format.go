package bridge

import (
	"os"
	"strings"
)

// Text renders the chat message body. Display data only — session display
// name, kind, and the event headline (docs/37 P1: 通知文にはセッション表示名・
// kind・要約) — never tokens or raw log content. Japanese to match the product's
// notification voice (chat_report.go). The Console link is appended only when
// the CP injected its public base URL (AF_CP_BASE_URL rides PUBLIC_BASE_URL —
// docs/37 残課題: per-session deep links need a URL scheme first).
func (m Message) Text() string {
	var b strings.Builder
	b.WriteString("【agent-fleet】")
	b.WriteString(m.headline())
	if m.DisplayName != "" {
		b.WriteString("\nセッション: 「" + m.DisplayName + "」")
		if m.SessionKind != "" {
			b.WriteString("（" + m.SessionKind + "）")
		}
	}
	if base := os.Getenv("AF_CP_BASE_URL"); base != "" {
		b.WriteString("\n" + base)
	}
	return b.String()
}

func (m Message) headline() string {
	switch m.Kind {
	case "answer-ready":
		return "応答あり — セッションが入力待ちになりました"
	case "question":
		return "質問があります — 回答待ちです"
	case "plan-approval":
		return "計画の承認待ちです"
	case "permission-request":
		return "ツール実行の許可待ちです"
	case "session-report":
		return "セッションの完了報告が届きました"
	case "exit":
		return "セッションが異常終了しました（" + exitLabel(m.Detail) + "）"
	}
	return "状態が変化しました（" + m.Kind + "）"
}

// exitLabel mirrors record_exit's reason vocabulary (status.ExitReasonFor).
func exitLabel(reason string) string {
	switch reason {
	case "oom":
		return "OOM・メモリ不足"
	case "crashed":
		return "クラッシュ"
	case "killed":
		return "強制終了"
	}
	return reason
}
