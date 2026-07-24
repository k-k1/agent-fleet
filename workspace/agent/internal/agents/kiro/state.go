package kiro

// TUI 文字列契約による live 状態分類（working / question / idle）。docs/43 の実装時
// 判断（§5-1）を実測で決着させた結果、状態源は**明示テキスト契約**に置く:
//
//   - working : フッタ「Kiro is working · Type to steer · Ctrl+S to queue」
//   - question: 承認待ちパネル「shell requires approval」（plan モード等・trust-all を
//     外したとき）——cursor では取れなかった許可待ちが kiro では明示テキストで拾える
//   - idle    : プレースホルダ「ask a question or describe a task ↵」
//
// なぜ JSONL 末尾分類（cursor 方式）でないか: v2 JSONL には turn_ended 相当のマーカー
// が無く、最終 AssistantMessage が「本当に最後か」を確証できない。一方 kiro の TUI
// 文字列は**固定の明示句**（スピナーグリフではない）で版ドリフトに強く、cursor/agy の
// 教訓（false-idle）にも整合する。加えて 2.14.1 のバイナリは Stop hook を持たない
// （hook トリガは AgentSpawn/PrePrompt/PreToolUse/PostToolUse のみ・実測）ので、hook
// マーカー方式（claude 型）は現状取り得ない。文字列が版で消えたら空を返し、driveState
// の generic 経路（/input が積む楽観 working）へフォールバックする。

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// LiveState classifies the running TUI's state from its visible pane ("" when
// unknowable —— pane 未取得／ブート直後でフッタ未描画)。
func LiveState(m session.Meta) string {
	return classifyPane(tmuxx.CapturePane(session.TmuxName(m.Name)))
}

// classifyPane is the pure decision over one captured frame（テストのため純関数に分離）。
// 判定順は question → working → idle: 承認待ちはコンポーザを置き換えるので working/idle
// の句とは同時に出ない（実測）が、明示に順序で優先する。
func classifyPane(s string) string {
	if s == "" {
		return ""
	}
	switch {
	case strings.Contains(s, "requires approval"):
		return "question"
	case strings.Contains(s, "Kiro is working"):
		return "working"
	case strings.Contains(s, "ask a question or describe a task"):
		return "idle"
	}
	return ""
}
