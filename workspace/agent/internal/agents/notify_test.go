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
// recordSessionNotification turns into 応答あり + the docs/log/30 operator report. This is
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

// A cancelled turn ended normally (the user interrupted it): idle, nothing special.
func TestMarkTurnEndNotifiesCancelled(t *testing.T) {
	got := captureNotifier(t)
	MarkTurnStart("sid-x")
	MarkTurnEnd("sid-x", TurnCancelled)
	if tr := waitTransition(t, got); tr.state != "idle" || tr.previous != "working" {
		t.Fatalf("transition = %+v", tr)
	}
}

// A FAILED turn also ends (status → idle, the session really is awaiting input) but must
// be distinguishable: reporting it as a plain completion is what made a provider error —
// an exhausted balance, an expired login — read as 応答が完了 with no output at all. The
// reason rides the excerpt so the report and the chat bridge can quote it.
func TestMarkTurnEndFailedNotifiesFailureWithReason(t *testing.T) {
	got := captureNotifier(t)
	MarkTurnStart("sid-f")
	MarkTurnEndErr("sid-f", TurnFailed, "[error] APIError (HTTP 401): Insufficient balance")
	tr := waitTransition(t, got)
	if tr.previous != "working" || tr.state != StateFailed {
		t.Fatalf("transition = %+v, want working→%s", tr, StateFailed)
	}
	if tr.excerpt != "[error] APIError (HTTP 401): Insufficient balance" {
		t.Fatalf("excerpt = %q, want the driver's failure summary", tr.excerpt)
	}
	// The status store must still say idle — WireLive のフォールバックと
	// anySessionWorking はここを読むので、failed を書くと 進行中 に張り付く。
	if st, _ := status.Read("sid-f"); st.State != "idle" {
		t.Fatalf("status = %q, want idle", st.State)
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
