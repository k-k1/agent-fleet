package main

// alias_mcp.go — MCP 家系（`internal/mcpx`）の移送で開いた口を 1 枚で塞ぐ。
//
// 家系は `mcp_*.go` 6 ファイル 3,461 行ごと `internal/mcpx` へ動いた。**呼び出し側は
// 1 行も変えていない**（routes.go / main.go / connections.go / chat_report.go /
// session_tmux.go …）ので、動いたことに気付くのはこのファイルだけである。
// 剥がすのはウェーブ境界の別セッションの仕事（ADR 0067）。
//
// 依存は双方向だったので、向きごとに扱いを変えている:
//
//   - **main → mcpx**（呼び出し側が使う名前）… 下の `var x = mcpx.Y`。
//   - **mcpx → main**（stdio サーバが main の各家系へ伸ばしていた手 19 本）…
//     `mcpx.Configure` で関数値として配線する。mcpx は main を import できないので、
//     これが唯一の方法である（詳細は internal/mcpx/deps.go）。

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// --- main → mcpx -----------------------------------------------------------
//
// 遠側は全部 `func`（＝安全）。⚠️ **`var` を var エイリアスで受けてはいけない**：
// 写しになり、遠側の代入が届かない（ウェーブ A の #295 F-2 / #297 F-1 で 2 回踏んだ）。
// このファイルで唯一 var を受けているのは OutputCursors で、理由は下に書いた。
var (
	runMCPStdio        = mcpx.RunStdio
	runMCPRun          = mcpx.RunSubcommand
	materializeMCP     = mcpx.Materialize
	materializeMCPAll  = mcpx.MaterializeAll
	startMCPTenantSync = mcpx.StartTenantSync

	startManagedSession = mcpx.StartManagedSession

	handleMCPServersGet    = mcpx.HandleServersGet
	handleMCPServerCreate  = mcpx.HandleServerCreate
	handleMCPServerTest    = mcpx.HandleServerTest
	handleMCPServerUpdate  = mcpx.HandleServerUpdate
	handleMCPServerEnabled = mcpx.HandleServerEnabled
	handleMCPServerDelete  = mcpx.HandleServerDelete
	handleMCPServerSecrets = mcpx.HandleServerSecrets
	handleMCPTenantRefresh = mcpx.HandleTenantRefresh

	handleRepoMCP      = mcpx.HandleRepo
	handleRepoMCPPlan  = mcpx.HandleRepoPlan
	handleRepoMCPApply = mcpx.HandleRepoApply

	createSessionKey     = mcpx.CreateSessionKey
	withOwnerConv        = mcpx.WithOwnerConv
	mcpSessionOutputTail = mcpx.SessionOutputTail
	awsMCPArgs           = mcpx.AWSMCPArgs

	agentPOST          = mcpx.AgentPOST
	agentSendToSession = mcpx.AgentSendToSession
	cpScheduleDo       = mcpx.CPScheduleDo
	writeOpsAWSConfig  = mcpx.WriteOpsAWSConfig
)

// const は写しで構わない（値が動かない）。
const (
	awsMCPDefaultEndpoint     = mcpx.AWSMCPDefaultEndpoint
	mcpSessionOutputTailBytes = mcpx.SessionOutputTailBytes
)

// outputCursors は遠側が `var` だが、**中身の無い値ハンドル**（fstore.Store は
// base 関数・subdir・ext・enc/dec を持つだけで、可変な状態を持たない）なので、
// 写しを取っても同じファイルを読み書きする。差し替えて試験する対象でもない。
var outputCursors = mcpx.OutputCursors

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

		ReportKindSelfReport: reportKindSelfReport,

		ApprovalGate:      bridgeApprovalGate,
		ApprovalLabel:     approvalLabel,
		ShellCreateTarget: shellCreateTarget,
		ShellSendTarget:   shellSendTarget,
		SessionIsShell:    sessionIsShell,

		ReadUIPrefs:                uiprefs.Read,
		EnsureClaudeSettingsWiring: ensureClaudeSettingsWiring,

		RepoAnyDirFromPath: repoAnyDirFromPath,

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
