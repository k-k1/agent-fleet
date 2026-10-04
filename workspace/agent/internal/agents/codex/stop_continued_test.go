package codex

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A Stop another hook blocked continues the same turn, so codex writes no task_complete for
// it (core/src/session/turn.rs): an open task_started is a turn the idle marker did not end
// (#1600).
func TestStopContinuedReadsRolloutLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "cx-1600", Dir: t.TempDir(), Kind: session.KindCodex}
	marker := time.Now().Truncate(time.Second)
	if StopContinued(m, marker) {
		t.Fatal("no rollout must answer false")
	}
	const thread = "01a0ee31-0000-7000-8000-000000001600"
	ts := func(d time.Duration) string { return marker.Add(d).UTC().Format(time.RFC3339Nano) }
	running := [][]byte{
		taskStarted(ts(-time.Minute), "t1"),
		userSays(ts(-time.Minute), "do the task"),
		userSays(ts(time.Second), "run the tests first"), // the blocking hook's prompt
	}
	with := func(more ...[]byte) [][]byte { return append(append([][]byte(nil), running...), more...) }
	for _, c := range []struct {
		what      string
		lines     [][]byte
		marker    time.Time
		continued bool
	}{
		{"blocked stop, turn still open", running, marker, true},
		{"no marker", running, time.Time{}, false},
		{"the real end", with(taskComplete(ts(5*time.Second), "t1")), marker, false},
		{"Esc on the continued turn", with(turnAborted(ts(5*time.Second), "t1")), marker, false},
	} {
		writeSlotRollout(t, m, thread, c.lines...)
		if got := StopContinued(m, c.marker); got != c.continued {
			t.Errorf("%s: StopContinued = %v, want %v", c.what, got, c.continued)
		}
	}
}
