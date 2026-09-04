// browser_seam.go — the one thing browserx needs from package main at boot.
//
// The browser family (ADR 0067 WP-A3 / AG-BROWSER) lives in internal/browserx and callers
// name browserx.X directly. All that is left here is this wiring: the four capabilities
// browserx borrows from the host side. These are not aliases but dependency injection at
// boot (see internal/browserx/deps.go).
package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
)

func init() {
	browserx.SetDeps(
		mcpx.AgentSendToSession,
		chromiumDefaultPin,
		chromiumPinnedBinary,
		installChromium,
	)
}
