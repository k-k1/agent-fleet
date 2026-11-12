package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// startRT is the member's runtime, in rescue mode, sharing the rescue world's count and
// lease store. Its background work is queued (the harness's bg) and run by runBg.
func (w *rescueWorld) startRT() *ecsEC2Runtime {
	w.t.Helper()
	rt := w.h.rt
	rt.pool.darkAction = darkActionRescue
	rt.pool.darkAfter = 10 * time.Minute
	rt.leases = w.f.leases
	rt.onSlotDark = w.f.onSlotDark
	startPhase.Delete(rescueWorkspace)
	oldPolls := darkStartPolls
	darkStartPolls = 3
	w.t.Cleanup(func() {
		darkStartPolls, darkStartHook = oldPolls, nil
		startPhase.Delete(rescueWorkspace)
	})
	return rt
}

func (w *rescueWorld) runBg() { w.h.runDeferred(context.Background()) }

// freeSlot is a hot, registered box the home can be placed on once it is off the dark one.
func (w *rescueWorld) freeSlot() {
	w.h.ec2.addSlot("i-free", "ap-northeast-1a", "m7i.large", true, false)
	w.h.ci.registered["i-free"] = true
}

func (w *rescueWorld) lineUp() bool { return isDarkRescuePhase(w.h.rt.BootPhase()) }

func (w *rescueWorld) noPlacement() {
	w.t.Helper()
	if n := w.h.ec2.countCalls("AttachVolume"); n != 0 {
		w.t.Fatalf("%d AttachVolume calls: the home must not be placed", n)
	}
	if got := attachedInstance(w.vol()); got != "i-dark" {
		w.t.Fatalf("the home is on %q", got)
	}
}

// A Start of a stopped workspace whose home is on a dark slot triggers the rescue (the
// service is down, which is the one case #3993 allows), ends without counting itself, shows
// the blocked-phase line, and the follow-up places the home on another slot once the rescue
// has detached it.
func TestStartOnADarkSlotRescuesAndThenPlacesTheHome(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		rt := w.startRT()
		w.freeSlot()
		w.arm()
		gd := w.count()

		if err := rt.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		m, ok := parseRescueMarker(w.marker())
		if !ok || m.Phase != rescuePhaseBegun || m.Gen != gd+1 {
			t.Fatalf("marker %q: want begun at g_r=%d", w.marker(), gd+1)
		}
		if w.count() != gd+1 {
			t.Fatalf("count %d, want %d: the Start counted itself as well as the rescue", w.count(), gd+1)
		}
		if !w.lineUp() || !strings.HasPrefix(rt.BootPhase(), blockedPhasePrefix) {
			t.Fatalf("phase %q: want the blocked-phase line", rt.BootPhase())
		}
		w.untouched() // nothing destructive and no placement in the request itself
		if len(w.h.deferred) != 1 {
			t.Fatalf("%d background steps, want the one that carries the rescue on", len(w.h.deferred))
		}

		w.runBg()
		if got := w.h.ec2.stopForce; len(got) != 1 || got[0] {
			t.Fatalf("StopInstances Force flags = %v, want one graceful stop", got)
		}
		if w.marker() != "" || w.wedgeAt() == "" {
			t.Fatalf("marker %q wedge-at %q: the episode must be recorded and the marker gone", w.marker(), w.wedgeAt())
		}
		if got := attachedInstance(w.vol()); got != "i-free" {
			t.Fatalf("the home is on %q, want the other slot i-free", got)
		}
		if w.count() != gd+2 {
			t.Fatalf("count %d, want %d: the rescue's bump and the follow-up Start's", w.count(), gd+2)
		}
		if w.lineUp() {
			t.Fatalf("the line is still up: %q", rt.BootPhase())
		}
		var rescued bool
		for _, ev := range *w.events {
			rescued = rescued || ev.Action == "rescued"
		}
		if !rescued {
			t.Fatalf("no rescued event: %v", *w.events)
		}
	})
}

// With `detect` a Start on a dark slot is today's Start: it counts itself and no rescue
// starts.
func TestStartInDetectModeIsTodaysStart(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		rt := w.startRT()
		w.arm()
		gd := w.count()
		rt.pool.darkAction = darkActionDetect
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if w.marker() != "" || w.count() != gd+1 || w.lineUp() {
			t.Fatalf("marker %q count %d (want %d) phase %q", w.marker(), w.count(), gd+1, rt.BootPhase())
		}
	})
}

// A stamp that does not prove darkness of this launch is no reason to rescue: from another
// epoch (LaunchTime changed), not yet past darkAfter, or absent.
func TestStartDoesNotRescueOnAStaleOrYoungStamp(t *testing.T) {
	cases := map[string]func(w *rescueWorld){
		"stamp of an earlier launch": func(w *rescueWorld) {
			w.h.ec2.instances["i-dark"].LaunchTime = aws.Time(time.Now().Add(-time.Hour))
		},
		"stamp younger than darkAfter": func(w *rescueWorld) {
			backdateDark(w.t, w.h, time.Minute)
		},
		"no stamp": func(w *rescueWorld) {
			w.h.ec2.setInstanceTag("i-dark", ec2TagDarkSince, "")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bothModes(t, func(t *testing.T, w *rescueWorld) {
				rt := w.startRT()
				w.arm()
				mutate(w)
				gd := w.count()
				if err := rt.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
				if w.marker() != "" || w.count() != gd+1 || w.lineUp() {
					t.Fatalf("marker %q count %d (want %d) phase %q: a Start counts itself as before",
						w.marker(), w.count(), gd+1, rt.BootPhase())
				}
			})
		})
	}
}

// A rescue that already owns the home (begun by the sweeper, its process gone) is not
// overtaken by a Start: the Start does not count, shows the line, and two in a row both
// wait. When the rescue is finished by the sweeper, the member's next Start places the home.
func TestStartsYieldToARescueTheSweeperBegan(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		rt := w.startRT()
		w.freeSlot()
		w.arm()
		rescueStepHook = func(_ context.Context, step string) error {
			if step == "cancelled" {
				return errRescueCrash
			}
			return nil
		}
		w.pass(w.f)
		rescueStepHook = nil
		m, _ := parseRescueMarker(w.marker())
		if m.Phase != rescuePhaseBegun {
			t.Fatalf("marker %q", w.marker())
		}
		gr := w.count()

		for i := 0; i < 2; i++ {
			if err := rt.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			if w.count() != gr {
				t.Fatalf("Start %d counted: %d, want %d", i+1, w.count(), gr)
			}
			if !w.lineUp() {
				t.Fatalf("Start %d: phase %q", i+1, rt.BootPhase())
			}
		}
		w.runBg() // both waiters give up at their bound and leave the line up
		if w.count() != gr || !w.lineUp() || w.h.ec2.countCalls("AttachVolume") != 0 {
			t.Fatalf("a waiter acted on a rescue still in progress: count %d phase %q", w.count(), rt.BootPhase())
		}
		w.untouched()

		// The sweeper's resume (a restarted CP) finishes the rescue.
		w.pass(w.restarted())
		if got := attachedInstance(w.vol()); got != "" || w.marker() != "" {
			t.Fatalf("home on %q marker %q after the resume", got, w.marker())
		}
		if st := rt.State(context.Background()); st != "stopped" || w.lineUp() {
			t.Fatalf("state %q phase %q: the line must go once the home is off the box", st, rt.BootPhase())
		}
		// The member's retry places it.
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := attachedInstance(w.vol()); got != "i-free" {
			t.Fatalf("the home is on %q, want i-free", got)
		}
	})
}

// A rescue that is blocked is surfaced, not waited out in silence: the request ends at once,
// the line names the phase it stopped at, and another Start does not count past it.
func TestStartSurfacesABlockedRescue(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		shortBounds(t)
		withENI(w)
		w.h.ci.stickyENI = true
		rt := w.startRT()
		w.arm()
		gd := w.count()
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		w.runBg()
		m, _ := parseRescueMarker(w.marker())
		if m.Phase != rescuePhaseDeregistering || m.BlockedSince.IsZero() {
			t.Fatalf("marker %q: want blocked at deregistering", w.marker())
		}
		if !strings.Contains(rt.BootPhase(), "stopped at deregistering") {
			t.Fatalf("phase %q: want the line that names where the rescue is stuck", rt.BootPhase())
		}
		if w.count() != gd+1 {
			t.Fatalf("count %d, want %d", w.count(), gd+1)
		}
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if w.count() != gd+1 || w.h.ec2.countCalls("AttachVolume") != 0 {
			t.Fatalf("a second Start went past the blocked rescue: count %d", w.count())
		}
	})
}

// A rescue that fails to start fails the Start; it does not leave it `starting` for nothing.
func TestStartFailsWhenTheRescueCannotBegin(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		rt := w.startRT()
		w.arm()
		w.h.ssmc.listErr = errBoom
		err := rt.Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "could not be started") {
			t.Fatalf("err = %v", err)
		}
		if len(w.h.deferred) != 0 || w.lineUp() {
			t.Fatalf("a failed start left background work or a line: %d %q", len(w.h.deferred), rt.BootPhase())
		}
	})
}

// A Stop (or a Destroy / EraseHome, which Stop first) or a newer Start that lands between the
// Start's decision and the rescue's locked step makes the rescue step aside; the Start then
// decides again on what is there. If that keeps happening it counts itself, as today.
func TestStartDecidesAgainWhenTheRescueStepsAside(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		rt := w.startRT()
		w.arm()
		gd := w.count()
		once := false
		darkStartHook = func() {
			if !once {
				once = true
				w.startLands()
			}
		}
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		m, ok := parseRescueMarker(w.marker())
		if !ok || m.Gen != gd+2 || w.count() != gd+2 {
			t.Fatalf("marker %q count %d: want one rescue at %d", w.marker(), w.count(), gd+2)
		}
	})
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		rt := w.startRT()
		w.arm()
		gd := w.count()
		darkStartHook = w.startLands // every time
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if w.marker() != "" {
			t.Fatalf("marker %q: after %d tries the Start counts itself", w.marker(), darkAdmitTries)
		}
		if w.count() != gd+darkAdmitTries+1 {
			t.Fatalf("count %d, want %d", w.count(), gd+darkAdmitTries+1)
		}
	})
}

// A Stop after the Start yielded overtakes the rescue and ends the wait; nothing is placed.
func TestAStopAfterTheStartYieldedWinsOverTheRescueAndTheWaiter(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		rt := w.startRT()
		w.freeSlot()
		w.arm()
		gd := w.count()
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := rt.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if w.lineUp() {
			t.Fatalf("Stop left the line up: %q", rt.BootPhase())
		}
		w.runBg()
		if w.marker() != "" || w.count() != gd+2 {
			t.Fatalf("marker %q count %d, want none and %d", w.marker(), w.count(), gd+2)
		}
		w.noPlacement()
		if n := len(w.h.ec2.stopForce); n != 0 {
			t.Fatalf("the overtaken rescue stopped the box %d times", n)
		}
	})
}

// The same, landing at each boundary of the rescue's own steps: the rescue stops, and the
// follow-up never counts past a count that is not the rescue's.
func TestAStopAtEveryRescueBoundaryKeepsTheWaiterFromPlacing(t *testing.T) {
	for _, step := range []string{"before-cancel", "cancelled", "guarded", "detaching"} {
		t.Run(step, func(t *testing.T) {
			bothModes(t, func(t *testing.T, w *rescueWorld) {
				rt := w.startRT()
				w.freeSlot()
				w.arm()
				if err := rt.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
				gr := w.count()
				hookAt(step, w.startLands)
				w.runBg()
				if w.count() != gr+1 {
					t.Fatalf("count %d, want %d: only the Stop counted", w.count(), gr+1)
				}
				if w.h.ec2.countCalls("AttachVolume") != 0 {
					t.Fatalf("the waiter placed the home after a Stop")
				}
			})
		})
	}
}

// The waiter alone: the rescue is done and a Stop lands before it looks.
func TestWaiterDoesNotPlaceOnACountThatIsNotTheRescues(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		rt := w.startRT()
		w.freeSlot()
		w.arm()
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		m, _ := parseRescueMarker(w.marker())
		cmds := w.h.ssmc
		rt.rescueContinue(context.Background(), cmds, "i-dark", m, []string{"cmd-1"})
		if attachedInstance(w.vol()) != "" {
			t.Fatalf("the rescue did not finish")
		}
		w.startLands()
		rt.awaitRescue(context.Background(), "i-dark", m)
		if got := attachedInstance(w.vol()); got != "" {
			t.Fatalf("the waiter placed the home on %q after a Stop", got)
		}
	})
}
