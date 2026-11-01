package sessionx

// Early lift (the limit ended before the booked instant) and moved-on-by-hand cancellation of a
// booked usage-limit auto-resume.

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

type earlyFixture struct {
	*rateLimitFixture
	lifted bool
	ran    bool
	liftN  int
	m      session.Meta
	now    time.Time
	seq    int
}

func newEarlyFixture(t *testing.T) *earlyFixture {
	t.Helper()
	f := &earlyFixture{rateLimitFixture: newRateLimitFixture(t), now: time.Now()}
	setRateLimitPref(t, true)
	// Distinct ids so a test can tell the moved booking from the old one.
	putRateLimitSchedule = func(_ session.Meta, at time.Time) (string, error) {
		f.scheduled++
		f.scheduleAt = at
		f.seq++
		return "sch_new" + strconv.Itoa(f.seq), nil
	}
	editUsageLimitTraits(t, session.KindCodex, func(tr *agents.UsageLimitTraits) {
		tr.AtLimit = func(session.Meta) (agents.LimitKind, bool) { return agents.LimitWindow, f.codexLimited }
		tr.LiftedEarly = func(source string, _, _ time.Time) bool {
			f.liftN++
			return f.lifted && source != rateLimitSourceResetEarly
		}
		tr.RanTurnSince = func(session.Meta, time.Time) bool { return f.ran }
	})
	f.m = rlCodexMeta()
	session.WriteMeta(f.m)
	if err := RateLimitStates.Write(f.m.Name, rateLimitState{
		At: f.now.Add(-time.Hour).Format(time.RFC3339), ResumeAt: f.now.Add(48 * time.Hour).Format(time.RFC3339),
		Source: "codex:weekly", ScheduleID: "sch_old",
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRateLimitEarlyLiftMovesTheBooking(t *testing.T) {
	f := newEarlyFixture(t)
	f.lifted = true
	rateLimitFollowUp(f.m, stateOf(t, f.m.Name), f.now, false) // a stopped session
	st := stateOf(t, f.m.Name)
	want := f.now.Add(rateLimitResumeLead)
	if st.ScheduleID != "sch_new1" || st.Source != rateLimitSourceResetEarly || st.ResumeAt != want.Format(time.RFC3339) {
		t.Fatalf("episode = %+v, want the booking moved to %v", st, want)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "sch_old" {
		t.Errorf("deleted = %v, want the old booking only", f.deleted)
	}
	// Already moved: later sweeps (even with the evidence still true) leave it alone.
	rateLimitFollowUp(f.m, st, f.now.Add(time.Minute), false)
	if f.scheduled != 1 {
		t.Errorf("bookings = %d, want 1: the moved booking must not move again", f.scheduled)
	}
}

func TestRateLimitEarlyLiftAlsoOnALiveSessionAtItsLimit(t *testing.T) {
	f := newEarlyFixture(t)
	f.lifted, f.codexLimited = true, true
	rateLimitRecover(f.m, stateOf(t, f.m.Name), f.now, false, agents.LimitWindow)
	if st := stateOf(t, f.m.Name); st.Source != rateLimitSourceResetEarly {
		t.Fatalf("episode = %+v, want the booking moved", st)
	}
}

func TestRateLimitStillFullLeavesTheBookingAlone(t *testing.T) {
	f := newEarlyFixture(t)
	rateLimitFollowUp(f.m, stateOf(t, f.m.Name), f.now, false)
	st := stateOf(t, f.m.Name)
	if f.liftN == 0 {
		t.Fatal("the evidence was never asked: this test would pass on a dead path")
	}
	if st.ScheduleID != "sch_old" || f.scheduled != 0 || len(f.deleted) != 0 {
		t.Errorf("episode = %+v scheduled=%d deleted=%v, want untouched", st, f.scheduled, f.deleted)
	}
}

func TestRateLimitMovedOnByHandCancelsTheBooking(t *testing.T) {
	f := newEarlyFixture(t)
	f.ran = true
	rateLimitFollowUp(f.m, stateOf(t, f.m.Name), f.now, true)
	if _, has := RateLimitStates.Read(f.m.Name); has {
		t.Error("the episode survived a successful manual turn")
	}
	if len(f.deleted) != 1 || f.deleted[0] != "sch_old" {
		t.Errorf("deleted = %v, want the booking cancelled", f.deleted)
	}
}

// Stopped or idle-at-limit alone is not moving on.
func TestRateLimitNoTurnSinceKeepsTheBooking(t *testing.T) {
	f := newEarlyFixture(t)
	rateLimitFollowUp(f.m, stateOf(t, f.m.Name), f.now, false)
	if _, has := RateLimitStates.Read(f.m.Name); !has || len(f.deleted) != 0 {
		t.Errorf("episode dropped / schedule cancelled without a turn since: deleted=%v", f.deleted)
	}
}

// A moved booking that the early resume then fails on is re-booked by the ordinary path.
func TestRateLimitRefusedEarlyResumeIsRebooked(t *testing.T) {
	f := newEarlyFixture(t)
	f.lifted, f.codexLimited = true, true
	rateLimitRecover(f.m, stateOf(t, f.m.Name), f.now, false, agents.LimitWindow)
	f.codexReset, f.codexResetOK = f.now.Add(3*time.Hour), true
	f.lifted = false
	later := f.now.Add(rateLimitResumeLead + rateLimitRebookSettle + time.Minute)
	rateLimitRecover(f.m, stateOf(t, f.m.Name), later, false, agents.LimitWindow)
	st := stateOf(t, f.m.Name)
	if st.Source != "codex:5h" || st.ResumeAt != f.codexReset.Format(time.RFC3339) {
		t.Fatalf("episode = %+v, want re-booked at the window's reset", st)
	}
}

// The "resumed" notice is keyed on the booking now in the episode, so the moved one still raises it.
func TestRateLimitResumedNoticeFiresForTheMovedBooking(t *testing.T) {
	f := newEarlyFixture(t)
	f.lifted = true
	rateLimitFollowUp(f.m, stateOf(t, f.m.Name), f.now, false)
	notifyRateLimitResumeDelivered(f.m.Name, rateLimitResumePromptFor("en"), TurnSourceSchedule, f.now.Add(3*time.Minute))
	var n int
	for _, ev := range notice.List() {
		if ev.Kind == rateLimitNoticeResumed {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("resumed notices = %d, want 1", n)
	}
}

// A crash between recording the moved booking and cancelling the old one: the episode carries
// the old id (MovedFrom), and the next sweep cancels it. Nothing forgets the old booking.
func TestRateLimitMoveResumesAnInterruptedSwap(t *testing.T) {
	f := newEarlyFixture(t)
	st := stateOf(t, f.m.Name)
	st.MovedFrom, st.ScheduleID, st.Source = "sch_old", "sch_new9", rateLimitSourceResetEarly
	if err := RateLimitStates.Write(f.m.Name, st); err != nil {
		t.Fatal(err)
	}
	rateLimitTickSession(f.m, f.now)
	if len(f.deleted) != 1 || f.deleted[0] != "sch_old" {
		t.Fatalf("deleted = %v, want the replaced booking cancelled", f.deleted)
	}
	if got := stateOf(t, f.m.Name); got.MovedFrom != "" || got.ScheduleID != "sch_new9" {
		t.Errorf("episode = %+v, want MovedFrom cleared and the new id kept", got)
	}
}

// If the episode cannot be written, the new booking is cancelled rather than orphaned, and the
// old one stays the episode's booking.
func TestRateLimitMoveWriteFailureDropsTheNewBooking(t *testing.T) {
	f := newEarlyFixture(t)
	f.lifted = true
	dir := filepath.Dir(RateLimitStates.Path(f.m.Name))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if err := RateLimitStates.Write("probe-writable", rateLimitState{At: "x"}); err == nil {
		t.Skip("the state directory is writable despite chmod (running as root?)")
	}
	rateLimitFollowUp(f.m, stateOf(t, f.m.Name), f.now, false)
	if got := stateOf(t, f.m.Name); got.ScheduleID != "sch_old" {
		t.Errorf("episode = %+v, want the old booking kept", got)
	}
	for _, id := range f.deleted {
		if id == "sch_old" {
			t.Errorf("deleted = %v: the old booking must survive a failed write", f.deleted)
		}
	}
}
