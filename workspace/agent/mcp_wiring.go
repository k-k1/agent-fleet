package main

// mcp_wiring.go — MCP 家系（`internal/mcpx`）の**配線**だけを持つ。
// ウェーブ B の別名 alias_mcp.go は RECLAIM-B で回収し、呼び出し側は mcpx を直接呼ぶ。
// ここに残るのは別名ではなく、**main 側にしか置けない 2 つ**である:
//
//   - 「写しにできない 4 つ」の**保管場所**（下記）。
//   - **mcpx → main** の逆向き依存 19 本を関数値で配線する `mcpx.Configure`。
//     mcpx は main を import できないので、これが唯一の方法（internal/mcpx/deps.go）。

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// 🔥 **写しにできない 4 つ**は、値の**保管場所をこちら側に置く**（mcpx は読み書きの口
// だけを預かる。internal/mcpx/deps.go の注記）。理由は 2 つあり、どちらも「var を
// エイリアスで受けると壊れる」の実例である:
//
//   - `--conv <id>` と `--write` は**起動時の代入より後**（引数解釈のとき）に決まる。
//     写しは空/false のまま固まり、bridge_approval.go は「どの会話か分からない」まま
//     承認を投げる。
//   - package main のテストはこれらに**直接代入して** mcpx の道具集合を切り替え、
//     解釈結果を**読み返す**（chat_report_test.go / bridge_approval_test.go）。
//     写しだと代入が届かず、**スタブが効かないのに緑**になる。
var (
	mcpConvID                 string
	mcpWriteEnabled           bool
	mcpSelfReportOnly         bool
	mcpSessionChromiumEnabled bool
)

// --- mcpx → main -----------------------------------------------------------
//
// 配線漏れは mcpx.Configure が panic で落とす。**既定値で黙って埋めない** —
// 承認ゲートが未配線のまま素通りする方が、起動しないことより悪い。
func init() {
	mcpx.Configure(mcpx.Deps{
		CleanTitle:           cleanTitle,
		SessionTitleMaxRunes: sessionTitleMaxRunes,

		PeerIntentNames:       peerIntentNames,
		PeerReachableSessions: peerReachableSessions,

		ReportKindSelfReport: chatx.ReportKindSelfReport,

		ApprovalGate:      bridgeApprovalGate,
		ApprovalLabel:     approvalLabel,
		ShellCreateTarget: shellCreateTarget,
		ShellSendTarget:   shellSendTarget,
		SessionIsShell:    sessionIsShell,

		ReadUIPrefs:                uiprefs.Read,
		EnsureClaudeSettingsWiring: ensureClaudeSettingsWiring,

		RepoAnyDirFromPath: gitx.RepoAnyDirFromPath,

		ReadBuildPins:      readBuildPins,
		AgentFleetShareDir: agentFleetShareDir,
		InstallGrafanaMCP:  installGrafanaMCP,

		WriteSSMConfig: writeSSMConfig,

		WriteEnabled:              func() bool { return mcpWriteEnabled },
		SetWriteEnabled:           func(v bool) { mcpWriteEnabled = v },
		SelfReportOnly:            func() bool { return mcpSelfReportOnly },
		SetSelfReportOnly:         func(v bool) { mcpSelfReportOnly = v },
		SessionChromiumEnabled:    func() bool { return mcpSessionChromiumEnabled },
		SetSessionChromiumEnabled: func(v bool) { mcpSessionChromiumEnabled = v },

		ConvID:    func() string { return mcpConvID },
		SetConvID: func(id string) { mcpConvID = id },
	})
}
