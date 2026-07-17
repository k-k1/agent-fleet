package agents

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

type transition struct{ sid, previous, state, excerpt string }

// captureNotifier wires a notifier for the test and restores the previous wiring.
// Buffered so the async notify never blocks on an assertion that already returned.
func captureNotifier(t *testing.T) chan transition {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // status ストアの書き先をテスト内に隔離
	got := make(chan transition, 8)
	SetStateNotifier(func(sid, previous, state, excerpt string) {
		got <- transition{sid, previous, state, excerpt}
	})
	t.Cleanup(func() { SetStateNotifier(nil) })
	return got
}

func waitTransition(t *testing.T, got chan transition) transition {
	t.Helper()
	select {
	case tr := <-got:
		return tr
	case <-time.After(2 * time.Second):
		t.Fatal("no state transition notified")
		return transition{}
	}
}

func mustNotNotify(t *testing.T, got chan transition) {
	t.Helper()
	select {
	case tr := <-got:
		t.Fatalf("unexpected notification: %+v", tr)
	case <-time.After(150 * time.Millisecond):
	}
}

// A completed managed turn must notify working→idle — the transition
// recordSessionNotification turns into 応答あり + the docs/30 operator report. This is
// the hole managed sessions had: the driver wrote the status and told nobody.
func TestMarkTurnEndNotifiesCompletion(t *testing.T) {
	got := captureNotifier(t)
	MarkTurnStart("sid-1")
	if st, _ := status.Read("sid-1"); st.State != "working" {
		t.Fatalf("status after start = %q, want working", st.State)
	}
	mustNotNotify(t, got) // 開始は報告対象ではない

	MarkTurnEnd("sid-1", TurnCompleted)
	tr := waitTransition(t, got)
	if tr.sid != "sid-1" || tr.previous != "working" || tr.state != "idle" {
		t.Fatalf("transition = %+v, want sid-1 working→idle", tr)
	}
	if st, _ := status.Read("sid-1"); st.State != "idle" {
		t.Fatalf("status after end = %q, want idle", st.State)
	}
}

// TurnFailed/TurnCancelled still end the turn: the session is idle awaiting input, the
// same shape the claude Stop hook reports for an errored turn.
func TestMarkTurnEndNotifiesFailedAndCancelled(t *testing.T) {
	for _, st := range []TurnState{TurnFailed, TurnCancelled} {
		t.Run(string(st), func(t *testing.T) {
			got := captureNotifier(t)
			MarkTurnStart("sid-x")
			MarkTurnEnd("sid-x", st)
			if tr := waitTransition(t, got); tr.state != "idle" || tr.previous != "working" {
				t.Fatalf("transition = %+v", tr)
			}
		})
	}
}

// TurnUnknown = we lost the runtime, not "the turn finished". The turn may still be
// running on the other side, so reporting 完了 to the operator would be a lie — but the
// status must still fall back to idle or the session sticks at 進行中 forever.
func TestMarkTurnEndUnknownPersistsIdleWithoutNotifying(t *testing.T) {
	got := captureNotifier(t)
	MarkTurnStart("sid-2")
	MarkTurnEnd("sid-2", TurnUnknown)
	if st, _ := status.Read("sid-2"); st.State != "idle" {
		t.Fatalf("status = %q, want idle even when unknown", st.State)
	}
	mustNotNotify(t, got)
}

// A turn adopted across an agent restart (reconcile) has no "working" marker of its own:
// its completion must still report. previous=="" is a turn that ENDED (main's
// recordSessionNotification decides that) — the seam must pass it through, not swallow it.
func TestMarkTurnEndReportsWithoutPriorMarker(t *testing.T) {
	got := captureNotifier(t)
	MarkTurnEnd("sid-3", TurnCompleted)
	if tr := waitTransition(t, got); tr.previous != "" || tr.state != "idle" {
		t.Fatalf("transition = %+v, want empty previous → idle", tr)
	}
}

// The seam must be inert until main wires it (hook subcommands, tests, any binary that
// never calls SetStateNotifier still persist status).
func TestMarkTurnEndWithoutNotifierPersistsStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	SetStateNotifier(nil)
	MarkTurnStart("sid-4")
	MarkTurnEnd("sid-4", TurnCompleted)
	if st, _ := status.Read("sid-4"); st.State != "idle" {
		t.Fatalf("status = %q, want idle", st.State)
	}
}
