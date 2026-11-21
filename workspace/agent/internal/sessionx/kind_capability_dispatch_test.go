package sessionx

import (
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The kind lists that used to be literal branches now come from the kinds' own capabilities.
// These pin the lists, so a kind that gains or loses a capability changes a test, not a screen.
func TestKindCapabilityDispatchMatchesTheOldLiterals(t *testing.T) {
	var hooked, mirror, full, guarded []string
	for _, k := range RegisteredKinds() {
		if terminalNotifyHasHooks(k) {
			hooked = append(hooked, k)
		}
		if managedMoveIsMirror(session.Meta{Kind: k, Driver: session.DriverManaged}) {
			mirror = append(mirror, k)
		}
		if _, ok := AgentOf(k).(agents.FullTranscriptReader); ok {
			full = append(full, k)
		}
		if terminalSwitchNeedsStopped(k) {
			guarded = append(guarded, k)
		}
	}
	for name, c := range map[string]struct{ got, want []string }{
		"attention hooks": {hooked, []string{session.KindClaude, session.KindCodex, session.KindOpencode}},
		"managed mirror":  {mirror, []string{session.KindCursor}},
		"full transcript": {full, []string{session.KindClaude}},
		"terminal switch": {guarded, []string{session.KindCodex}},
	} {
		g := slices.Clone(c.got)
		slices.Sort(g)
		slices.Sort(c.want)
		if !slices.Equal(g, c.want) {
			t.Errorf("%s: got %v, want %v", name, g, c.want)
		}
	}
	if managedMoveIsMirror(session.Meta{Kind: session.KindCursor}) {
		t.Error("a Terminal cursor session is not a mirror")
	}
}
