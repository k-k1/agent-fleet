// browser_seam.go — the one thing browserx needs from package main at boot.
//
// browser 家系（ADR 0067 WP-A3 / AG-BROWSER）は internal/browserx にあり、呼び出し側は
// browserx.X を直接名指しする（ADR 0067 決定 2 のエイリアス回収で alias_browser.go は
// 消えた）。残るのはこの結線だけ —— browserx がホスト側に借りている 4 つの機能で、
// これはエイリアスではなく起動時の依存注入である（internal/browserx/deps.go 参照）。
package main

import "github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"

func init() {
	browserx.SetDeps(
		agentSendToSession,
		chromiumDefaultPin,
		chromiumPinnedBinary,
		installChromium,
	)
}
