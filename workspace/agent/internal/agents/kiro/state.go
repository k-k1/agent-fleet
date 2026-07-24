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

// footerWindow bounds classification to the composer footer region（下端の非空行）。
// 画面全文を走査すると、assistant が本文で「Kiro is working」等の契約句を引用した
// とき idle 復帰後も working 判定が続き MarkTurnEnd が飛ばない（完了報告不発）。
// paneMode（session_io.go）も同じ理由で paneTail に限定している。フッタ＋承認パネル
// （最大 5 行程度）が収まる幅に採る。
const footerWindow = 8

// classifyPane is the pure decision over one captured frame（テストのため純関数に分離）。
// フッタ数行に限定したうえで idle → question → working の順に判定する。idle の
// プレースホルダ（「ask a question or describe a task」）は idle コンポーザにのみ出て
// working/question とは排他（実測）なので**最優先**——フッタ窓に working/approval の
// 引用が紛れても idle を正しく返す（A-3 の二重防御）。承認待ちはコンポーザを置き換える
// ので idle 句とは同時に出ない。
func classifyPane(s string) string {
	if s == "" {
		return ""
	}
	footer := tailLines(s, footerWindow)
	switch {
	case strings.Contains(footer, "ask a question or describe a task"):
		return "idle"
	case strings.Contains(footer, "requires approval"):
		return "question"
	case strings.Contains(footer, "Kiro is working"):
		return "working"
	}
	return ""
}

// tailLines returns the last n non-empty lines of s（paneMode の paneTail と同型）。
func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append(out, lines[i])
		}
	}
	return strings.Join(out, "\n")
}
