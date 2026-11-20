package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The kind capabilities resolve by exact registry key: an empty, unknown or differently
// spelled kind must not fall into claude's answer or plan paths, nor another kind's switch rules.
func TestKindCapabilityLookupsAreExact(t *testing.T) {
	for _, tc := range []struct {
		kind                           string
		answers, plan, needsStop, drop bool
	}{
		{session.KindClaude, true, true, false, false},
		{session.KindCodex, false, false, true, false},
		{session.KindKiro, false, false, false, true},
		{session.KindOpencode, false, false, false, false},
		{"", false, false, false, false},
		{"unknown", false, false, false, false},
		{"Claude", false, false, false, false},
		{"Codex", false, false, false, false},
		{"KIRO", false, false, false, false},
	} {
		if got := answersPendingQuestion(tc.kind); got != tc.answers {
			t.Errorf("answersPendingQuestion(%q) = %v, want %v", tc.kind, got, tc.answers)
		}
		if got := planRespondSupported(tc.kind); got != tc.plan {
			t.Errorf("planRespondSupported(%q) = %v, want %v", tc.kind, got, tc.plan)
		}
		if got := terminalSwitchNeedsStopped(tc.kind); got != tc.needsStop {
			t.Errorf("terminalSwitchNeedsStopped(%q) = %v, want %v", tc.kind, got, tc.needsStop)
		}
		if got := dropForTerminalSwitch(tc.kind, "no_such_session"); got != tc.drop {
			t.Errorf("dropForTerminalSwitch(%q) = %v, want %v", tc.kind, got, tc.drop)
		}
	}
}
