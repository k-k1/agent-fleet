package codex

import (
	"strings"
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

// The check runs on the reconciler's synchronous sweep, so it must read a bounded tail and
// never fold the whole rollout into the shared parse cache (a cold parse of a large rollout
// takes seconds): a long old history ahead of the tail is neither parsed nor needed.
func TestStopContinuedReadsOnlyTheTail(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "cx-1600-big", Dir: t.TempDir(), Kind: session.KindCodex}
	const thread = "01a0ee31-0000-7000-8000-000000016001"
	marker := time.Now().Truncate(time.Second)
	ts := func(d time.Duration) string { return marker.Add(d).UTC().Format(time.RFC3339Nano) }
	pad := strings.Repeat("x", 4<<10)
	var lines [][]byte
	// An old history several windows long, ending in a finished turn.
	for i := 0; i < 4*rolloutLifecycleTail/len(pad); i++ {
		lines = append(lines, userSays(ts(-time.Hour), pad))
	}
	lines = append(lines, taskStarted(ts(-time.Minute), "t1"), userSays(ts(-time.Minute), "do it"))
	writeSlotRollout(t, m, thread, lines...)
	path := rolloutPath(thread)

	if !StopContinued(m, marker) {
		t.Fatal("an open task_started in the tail must answer true")
	}
	if _, cached := rolloutCache.Load(path); cached {
		t.Fatal("StopContinued folded the whole rollout into the shared parse")
	}

	// One output bigger than the window hides the lifecycle: unknown answers false.
	writeSlotRollout(t, m, thread, append(lines, userSays(ts(time.Second), strings.Repeat("y", rolloutLifecycleTail+1)))...)
	if StopContinued(m, marker) {
		t.Fatal("a tail with no lifecycle event must answer false")
	}
}
