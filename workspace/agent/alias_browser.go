// alias_browser.go — browser 家系（ADR 0067 WP-A3 / AG-BROWSER）を
// internal/browserx へ移した後、package main 側から見える名前をここ 1 枚で保つ。
//
// 呼び出し側（routes.go・mcp_stdio.go・main.go・shutdown.go・preview.go）は
// 1 行も変えていない。エイリアスの回収——呼び出し側を browserx. 直参照に書き換える
// ——はウェーブ境界で別セッションが行う（ADR 0067 決定 1）。
package main

import "github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"

// browserx が package main から借りるホスト機能を 1 度だけ結線する。
// browserx 側は同名の関数変数で受けているので、移送されたファイルの呼び出しは
// 移送前と 1 文字も変わらない（internal/browserx/deps.go 参照）。
func init() {
	browserx.SetDeps(
		agentSendToSession,
		chromiumDefaultPin,
		chromiumPinnedBinary,
		installChromium,
	)
}

// ---- 型（mcp_stdio.go が JSON を decode する DTO）----

type (
	browserAttachTargetsResponse = browserx.BrowserAttachTargetsResponse
	browserAttachmentResponse    = browserx.BrowserAttachmentResponse
)

// ---- 定数 ----

const (
	browserAttachmentLabelHeader = browserx.BrowserAttachmentLabelHeader
	browserAttachmentMaxLabel    = browserx.BrowserAttachmentMaxLabel
)

// ---- HTTP ハンドラ（routes.go が登録する）----

var (
	handleBrowserPagesCreate = browserx.HandleBrowserPagesCreate
	handleBrowserPageGet     = browserx.HandleBrowserPageGet
	handleBrowserPageDelete  = browserx.HandleBrowserPageDelete
	handleBrowserWebSocket   = browserx.HandleBrowserWebSocket

	handleBrowserAttachTargets            = browserx.HandleBrowserAttachTargets
	handleBrowserAttachmentCreate         = browserx.HandleBrowserAttachmentCreate
	handleBrowserAttachmentList           = browserx.HandleBrowserAttachmentList
	handleBrowserAttachmentGet            = browserx.HandleBrowserAttachmentGet
	handleBrowserAttachmentDelete         = browserx.HandleBrowserAttachmentDelete
	handleBrowserAttachmentSiblingTargets = browserx.HandleBrowserAttachmentSiblingTargets
	handleBrowserAttachmentRetarget       = browserx.HandleBrowserAttachmentRetarget
	handleBrowserAttachmentControlMode    = browserx.HandleBrowserAttachmentControlMode
	handleBrowserAttachmentHandoff        = browserx.HandleBrowserAttachmentHandoff
	handleBrowserAttachmentHandoffResult  = browserx.HandleBrowserAttachmentHandoffResult
	handleBrowserAttachmentWebSocket      = browserx.HandleBrowserAttachmentWebSocket
)

// ---- 関数 ----

var (
	normalizeCDPBrowserID           = browserx.NormalizeCDPBrowserID
	reservedBrowserAgentPort        = browserx.ReservedBrowserAgentPort
	runBrowserImageSmoke            = browserx.RunBrowserImageSmoke
	sweepUndeliveredBrowserHandoffs = browserx.SweepUndeliveredBrowserHandoffs
)

// ---- プロセス全体で 1 つのマネージャ（shutdown.go が Close する）----

var (
	workspaceBrowserManager           = browserx.WorkspaceBrowserManager
	workspaceBrowserAttachmentManager = browserx.WorkspaceBrowserAttachmentManager
)
