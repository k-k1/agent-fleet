package agy

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// InputIdle answers from the frame on screen now, with no settle window: the nudge calls it
// once, after its own grace, and a first sighting must already read idle.
func TestInputIdleReadsTheCurrentFooterOnly(t *testing.T) {
	frames := map[string]string{
		session.TmuxName("idle1"): "> \n? for shortcuts   Gemini · high\n",
		session.TmuxName("busy1"): "thinking…\nesc to cancel\n",
		session.TmuxName("other"): "some other screen\n",
	}
	prev := capturePaneFrame
	capturePaneFrame = func(tn string) string { return frames[tn] }
	t.Cleanup(func() { capturePaneFrame = prev })
	for name, want := range map[string]bool{"idle1": true, "busy1": false, "other": false} {
		if got := (agentImpl{}).InputIdle(session.Meta{Name: name}); got != want {
			t.Errorf("%s: InputIdle = %v, want %v", name, got, want)
		}
	}
}
