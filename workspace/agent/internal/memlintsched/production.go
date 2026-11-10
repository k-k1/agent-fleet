package memlintsched

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memconsol"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// Production is the Agent's wiring: the member's switch and the same session-activity signal the
// consolidator reads.
func Production() Deps {
	return Deps{Enabled: uiprefs.MemoryLintScheduleEnabled, Activity: memconsol.SessionActivity, Run: memoryx.LintApplyAll}
}
