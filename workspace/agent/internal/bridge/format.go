package bridge

import (
	"os"
	"strings"
)

// Text renders the chat message body — deliberately terse (the bot name already
// says which app is talking): headline, then 「display」（kind）, then a deep
// link that opens the session's pane directly (?session= — consumed by the
// Console, appended only when the CP injected AF_CP_BASE_URL and the event is
// session-scoped). Display data only; never tokens or raw log content.
// lang is the Console locale captured at connect time ("en"; anything else
// renders Japanese — pre-lang connections and this deployment's default).
func (m Message) Text(lang string) string {
	en := lang == "en"
	var b strings.Builder
	b.WriteString(m.headline(en))
	if m.DisplayName != "" {
		if en {
			b.WriteString("\n\"" + m.DisplayName + "\" (" + kindLabel(m.SessionKind) + ")")
		} else {
			b.WriteString("\n「" + m.DisplayName + "」（" + kindLabel(m.SessionKind) + "）")
		}
	}
	if base := os.Getenv("AF_CP_BASE_URL"); base != "" && m.SessionName != "" {
		// <…> suppresses Discord's link-preview embed (keeps the message compact).
		b.WriteString("\n<" + strings.TrimRight(base, "/") + "/?session=" + m.SessionName + ">")
	}
	return b.String()
}

// textSlack is Text's Slack-mrkdwn twin: same headline + 「display」（kind）, but the deep
// link uses Slack's <url|label> syntax (a bare URL would unfurl into a big preview card).
func (m Message) textSlack(lang string) string {
	en := lang == "en"
	var b strings.Builder
	b.WriteString(m.headline(en))
	if m.DisplayName != "" {
		if en {
			b.WriteString("\n\"" + m.DisplayName + "\" (" + kindLabel(m.SessionKind) + ")")
		} else {
			b.WriteString("\n「" + m.DisplayName + "」（" + kindLabel(m.SessionKind) + "）")
		}
	}
	if base := os.Getenv("AF_CP_BASE_URL"); base != "" && m.SessionName != "" {
		u := strings.TrimRight(base, "/") + "/?session=" + m.SessionName
		if en {
			b.WriteString("\n<" + u + "|Open in Console>")
		} else {
			b.WriteString("\n<" + u + "|Console で開く>")
		}
	}
	return b.String()
}

func (m Message) headline(en bool) string {
	if en {
		switch m.Kind {
		case "answer-ready":
			return "Session is awaiting your input"
		case "question":
			return "A question is awaiting your answer"
		case "plan-approval":
			return "A plan is awaiting your approval"
		case "permission-request":
			return "A tool permission is awaiting your approval"
		case "session-report":
			return "Session report received"
		case "exit":
			return "Session exited abnormally (" + exitLabelEN(m.Detail) + ")"
		case "bridge-test":
			return "Connection test — if this arrived, you're all set"
		}
		return "State changed (" + m.Kind + ")"
	}
	switch m.Kind {
	case "answer-ready":
		return "セッションが入力待ちになりました"
	case "question":
		return "質問への回答待ちです"
	case "plan-approval":
		return "計画の承認待ちです"
	case "permission-request":
		return "ツール実行の許可待ちです"
	case "session-report":
		return "セッションの完了報告が届きました"
	case "exit":
		return "セッションが異常終了しました（" + exitLabel(m.Detail) + "）"
	case "bridge-test":
		return "接続テスト — この通知が届けば設定完了です"
	}
	return "状態が変化しました（" + m.Kind + "）"
}

// kindLabel maps a session kind to its outward product name (the Console's
// agent registry is the naming source of truth — keep in sync with
// console/src/agents/registry.ts).
func kindLabel(kind string) string {
	switch kind {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "opencode":
		return "opencode"
	case "copilot":
		return "GitHub Copilot"
	case "agy":
		return "Antigravity"
	}
	return kind
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

func exitLabelEN(reason string) string {
	switch reason {
	case "oom":
		return "OOM, out of memory"
	case "crashed":
		return "crashed"
	case "killed":
		return "killed"
	}
	return reason
}
