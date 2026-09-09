package chatx

// Tests for the stop-after-turn arm's consuming side (docs/log/85).
//
// The arm reuses the report reconciler's evidence, so what is pinned here is only what is
// specific to stopping: that it waits for the same two quiet ticks, that busy evidence holds
// it back, that a session which still owes a report is reported BEFORE it is stopped, that an
// expired arm never fires, and that a failed halt is retried instead of swallowed.

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// haltRecorder stands in for sessionx's halt. The reconciler calls it from its own goroutine,
// so every field is read under the lock (-race catches a bare read even inside a t.Fatalf).
type haltRecorder struct {
	mu    sync.Mutex
	names []string
	fail  int // how many more calls return an error
}

func (h *haltRecorder) halt(name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.names = append(h.names, name)
	if h.fail > 0 {
		h.fail--
		return errors.New("tmux refused")
	}
	return nil
}

func (h *haltRecorder) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.names)
}

// withHaltRecorder swaps the StopArmedSession seam for the test's recorder.
func withHaltRecorder(t *testing.T, h *haltRecorder) {
	t.Helper()
	d := testDeps()
	d.StopArmedSession = h.halt
	Configure(d)
	t.Cleanup(func() { Configure(testDeps()) })
}

// armStop writes the arm the way the REST handler does. The instant is the real clock's,
// which is what the status marker is stamped with as well — the fake clock only drives the
// ticks, never the evidence's timestamps.
func armStop(t *testing.T, m session.Meta, at time.Time) {
	t.Helper()
	m.StopAfterTurnAt = at.Format(time.RFC3339)
	session.WriteMeta(m)
}

// TestStopArmWaitsForTwoQuietTicks: the end of the turn is not enough on its own — the arm
// takes the same temporal corroboration as a report, so one misread pane cannot fold a
// session away.
func TestStopArmWaitsForTwoQuietTicks(t *testing.T) {
	m, sid, _ := ledgerFixture(t, "slot80")
	var h haltRecorder
	withHaltRecorder(t, &h)
	rc, clock := newFakeReconciler(t, reportTickDefault, (&countingSink{}).sink)

	armStop(t, m, time.Now())
	status.PersistTurnEnd(sid, "idle") // the armed turn ended

	clock.advance(t, rc, reportTickDefault)
	if h.count() != 0 {
		t.Fatal("halted on the first quiet tick (the debounce is not working)")
	}
	clock.advance(t, rc, reportTickDefault)
	if h.count() != 1 {
		t.Fatalf("two consecutive quiet ticks must halt: %d calls", h.count())
	}
}

// TestStopArmHeldByBusyEvidence: a session parked on a question is not "done". The whole
// reason the decision is not made from the Stop hook alone is that this state looks idle to
// anything simpler.
func TestStopArmHeldByBusyEvidence(t *testing.T) {
	m, sid, _ := ledgerFixture(t, "slot81")
	var h haltRecorder
	withHaltRecorder(t, &h)
	rc, clock := newFakeReconciler(t, reportTickDefault, (&countingSink{}).sink)

	armStop(t, m, time.Now())
	status.Persist(sid, "question")
	for i := 0; i < 4; i++ {
		clock.advance(t, rc, reportTickDefault)
	}
	if h.count() != 0 {
		t.Fatalf("halted while a question was waiting for an answer: %d calls", h.count())
	}

	status.PersistTurnEnd(sid, "idle") // answered, and the turn then ended
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if h.count() != 1 {
		t.Fatalf("must halt once the turn really ended: %d calls", h.count())
	}
}

// TestStopArmHeldByBackgroundBusy: a run_in_background job (or a background shell loop) is
// still running under the pane when the armed turn itself ends. Stopping is haltSessionMeta,
// i.e. a tmux kill-session — the exact action control-plane's tier1 reaper refuses to take
// while backgroundBusy (docs/log/75 §75.11.2), because it kills that job silently and claude
// never learns it died on resume. The arm must wait for it too, not just for the turn.
func TestStopArmHeldByBackgroundBusy(t *testing.T) {
	m, sid, _ := ledgerFixture(t, "slot85")
	var h haltRecorder
	withHaltRecorder(t, &h)
	rc, clock := newFakeReconciler(t, reportTickDefault, (&countingSink{}).sink)

	busy := true
	rc.stopBackgroundBusy = func(session.Meta) bool { return busy }

	armStop(t, m, time.Now())
	status.PersistTurnEnd(sid, "idle") // the armed turn ended, but a background job is still running

	for i := 0; i < 4; i++ {
		clock.advance(t, rc, reportTickDefault)
	}
	if h.count() != 0 {
		t.Fatalf("halted while a run_in_background job was still running: %d calls", h.count())
	}

	busy = false
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if h.count() != 1 {
		t.Fatalf("must stop once the background job finished: %d calls", h.count())
	}
}

// TestStopArmReportsBeforeStopping: an instruction that owes a report is reported first.
// Stopping first parks the report until someone resumes the session (evalReportEvidence
// refuses to settle a stopped session), which for the operator waiting on it is
// indistinguishable from never being told at all.
func TestStopArmReportsBeforeStopping(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot82")
	var h haltRecorder
	withHaltRecorder(t, &h)
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	armStop(t, m, time.Now())
	status.PersistTurnEnd(sid, "idle")

	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 {
		t.Fatalf("the report must go out: %v", cs.callsSnapshot())
	}
	if h.count() != 0 {
		t.Fatal("stopped in the same sweep the report was delivered (the row was still open when the arm was evaluated)")
	}

	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if h.count() != 1 {
		t.Fatalf("must stop once nothing is owed any more: %d calls", h.count())
	}
}

// TestStopArmExpires: an arm nobody consumed stops being honoured. Without the expiry a
// session resumed days later would fold itself away at the end of a turn nobody armed.
func TestStopArmExpires(t *testing.T) {
	m, sid, _ := ledgerFixture(t, "slot83")
	var h haltRecorder
	withHaltRecorder(t, &h)
	rc, clock := newFakeReconciler(t, reportTickDefault, (&countingSink{}).sink)

	// The reconciler reads the arm against ITS clock, so the arm is dated against that one.
	armStop(t, m, clock.Now().Add(-session.StopArmMaxAge-time.Hour))
	status.PersistTurnEnd(sid, "idle")

	for i := 0; i < 4; i++ {
		clock.advance(t, rc, reportTickDefault)
	}
	if h.count() != 0 {
		t.Fatalf("an expired arm must not stop anything: %d calls", h.count())
	}
}

// TestStopArmRetriesAfterFailedHalt: a halt that failed keeps the arm. Dropping it there
// would turn "stop when you are done" into silence — the one outcome the user cannot see.
func TestStopArmRetriesAfterFailedHalt(t *testing.T) {
	m, sid, _ := ledgerFixture(t, "slot84")
	h := haltRecorder{fail: 1}
	withHaltRecorder(t, &h)
	rc, clock := newFakeReconciler(t, reportTickDefault, (&countingSink{}).sink)

	armStop(t, m, time.Now())
	status.PersistTurnEnd(sid, "idle")

	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if h.count() != 1 {
		t.Fatalf("the first attempt must happen: %d calls", h.count())
	}
	if got, ok := session.ReadMeta(m.Name); !ok || got.StopAfterTurnAt == "" {
		t.Fatal("a failed halt must leave the arm in place for the retry")
	}
	clock.advance(t, rc, reportTickDefault)
	if h.count() != 2 {
		t.Fatalf("the next tick must retry: %d calls", h.count())
	}
}
