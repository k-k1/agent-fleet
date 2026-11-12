package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// withENI is the world of a workspace that was running when its box went dark: the task's
// network interface is still attached.
func withENI(w *rescueWorld) *rescueWorld {
	w.h.ec2.giveTaskENI("i-dark")
	return w
}

func (w *rescueWorld) phase() rescuePhase {
	m, _ := parseRescueMarker(w.marker())
	return m.Phase
}

func (w *rescueWorld) stopCalls() int { return len(w.h.ec2.stopForce) }

func (w *rescueWorld) detachCalls() int { return w.h.ec2.countCalls("DetachVolume") }

func shortBounds(t *testing.T) {
	oe, os := rescueENIPolls, rescueStopPolls
	rescueENIPolls, rescueStopPolls = 2, 2
	t.Cleanup(func() { rescueENIPolls, rescueStopPolls = oe, os })
}

func TestRescueDeregistersOnlyForATaskENIAndStopsOnlyAfterItIsGone(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		withENI(w)
		w.arm()
		var trail []string
		rescueStepHook = func(_ context.Context, step string) error {
			switch step {
			case "deregistering", "stopping", "detaching":
				trail = append(trail, step+":"+string(w.phase()))
				if step == "deregistering" && (w.stopCalls() != 0 || len(w.h.ci.deregistered) != 0) {
					t.Errorf("the intent phase must be written before the call")
				}
				if step == "stopping" && (len(w.h.ci.deregistered) != 1 || instancesHoldTaskENI(describeOne(w))) {
					t.Errorf("stopping before the ENI is gone")
				}
				if step == "stopping" && w.stopCalls() != 0 {
					t.Errorf("the stop intent must precede the stop")
				}
				if step == "detaching" && (attachedInstance(w.vol()) != "i-dark" || w.detachCalls() != 0 ||
					w.h.ec2.instances["i-dark"].State.Name != ec2types.InstanceStateNameStopped) {
					t.Errorf("detaching must follow a verified stop and precede the detach")
				}
			}
			return nil
		}
		w.pass(w.f)
		want := []string{"deregistering:deregistering", "stopping:stopping", "detaching:detaching"}
		if strings.Join(trail, ",") != strings.Join(want, ",") {
			t.Fatalf("intent phases = %v, want %v", trail, want)
		}
		if len(w.h.ci.deregistered) != 1 {
			t.Fatalf("deregistered %v", w.h.ci.deregistered)
		}
		w.rescued()
	})
	// No task ENI: nothing to release, so nothing is deregistered.
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		w.arm()
		w.pass(w.f)
		if len(w.h.ci.deregistered) != 0 {
			t.Fatalf("a box with no task ENI was deregistered: %v", w.h.ci.deregistered)
		}
		w.rescued()
	})
}

func describeOne(w *rescueWorld) *ec2.DescribeInstancesOutput {
	out, _ := w.h.ec2.DescribeInstances(context.Background(), &ec2.DescribeInstancesInput{InstanceIds: []string{"i-dark"}})
	return out
}

func TestRescueCrashAtEveryDestructiveStepThenResume(t *testing.T) {
	// step -> the phase a crash there leaves on the box.
	steps := map[string]rescuePhase{
		"deregistering":     rescuePhaseDeregistering,
		"deregistered-call": rescuePhaseDeregistering,
		"deregistered":      rescuePhaseDeregistered,
		"stopping":          rescuePhaseStopping,
		"stop-sent":         rescuePhaseStopping,
		"stopped":           rescuePhaseStopped,
		"detaching":         rescuePhaseDetaching,
	}
	for step, phase := range steps {
		t.Run(step, func(t *testing.T) {
			bothModes(t, func(t *testing.T, w *rescueWorld) {
				withENI(w)
				w.arm()
				gd := w.count()
				rescueStepHook = func(_ context.Context, s string) error {
					if s == step {
						return errRescueCrash
					}
					return nil
				}
				w.pass(w.f)
				rescueStepHook = nil
				if w.phase() != phase {
					t.Fatalf("crash at %q left phase %q (%q), want %q", step, w.phase(), w.marker(), phase)
				}
				f2 := w.restarted()
				f2.onSlotDark = w.f.onSlotDark
				f2.resumeRescues(context.Background())
				if w.count() != gd+1 {
					t.Fatalf("count %d, want %d", w.count(), gd+1)
				}
				w.rescued() // exactly one graceful stop, home off, episode recorded, marker gone
				if w.detachCalls() != 1 {
					t.Fatalf("detach calls = %d", w.detachCalls())
				}
			})
		})
	}
	t.Run("crash after the detach", func(t *testing.T) {
		bothModes(t, func(t *testing.T, w *rescueWorld) {
			w.arm()
			rescueStepHook = func(_ context.Context, s string) error {
				if s == "detached" {
					return errRescueCrash
				}
				return nil
			}
			w.pass(w.f)
			rescueStepHook = nil
			before := len(*w.events)
			f2 := w.restarted()
			f2.onSlotDark = w.f.onSlotDark
			f2.resumeRescues(context.Background())
			if w.detachCalls() != 1 || w.marker() != "" || w.wedgeAt() == "" {
				t.Fatalf("detach calls %d marker %q wedge-at %q", w.detachCalls(), w.marker(), w.wedgeAt())
			}
			if evs := *w.events; len(evs) != before+1 || evs[before].Action != "rescued" {
				t.Fatalf("events after the resume = %+v", evs[before:])
			}
		})
	})
}

func TestRescueBlockedWhenTheENIStaysThenFinishesWhenItGoes(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		shortBounds(t)
		withENI(w)
		w.h.ci.stickyENI = true
		w.arm()
		w.pass(w.f)
		m, ok := parseRescueMarker(w.marker())
		if !ok || m.Phase != rescuePhaseDeregistering || m.BlockedSince.IsZero() {
			t.Fatalf("marker %q: want deregistering with blocked_since", w.marker())
		}
		if w.stopCalls() != 0 || w.detachCalls() != 0 || attachedInstance(w.vol()) != "i-dark" {
			t.Fatal("a blocked rescue must do nothing further")
		}
		if _, rung := darkView(*w.h.ec2.instances["i-dark"]); rung != darkRungBlocked {
			t.Fatalf("rung %q", rung)
		}
		since := m.BlockedSince
		w.pass(w.f)
		m2, _ := parseRescueMarker(w.marker())
		if !m2.BlockedSince.Equal(since) {
			t.Fatalf("blocked_since moved %v -> %v on a retry", since, m2.BlockedSince)
		}
		blocked := 0
		for _, ev := range *w.events {
			if ev.Action == "blocked" {
				blocked++
			}
		}
		if blocked != 1 {
			t.Fatalf("blocked events = %d, want one per blocking", blocked)
		}
		w.h.ci.stickyENI = false
		w.h.ec2.dropTaskENIs("i-dark") // ECS lets go at last
		w.pass(w.f)
		w.rescued()
	})
}

func TestRescueForcesOnlyAStopThatDidNotFinish(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		shortBounds(t)
		w.h.ec2.stopHold = true
		w.arm()
		w.pass(w.f)
		if got := w.h.ec2.stopForce; len(got) != 1 || got[0] {
			t.Fatalf("first stop Force flags %v, want one graceful stop", got)
		}
		m, _ := parseRescueMarker(w.marker())
		if m.Phase != rescuePhaseStopping || m.BlockedSince.IsZero() || w.detachCalls() != 0 {
			t.Fatalf("marker %q detach calls %d", w.marker(), w.detachCalls())
		}
		w.pass(w.f)
		if got := w.h.ec2.stopForce; len(got) != 2 || got[1] != true {
			t.Fatalf("Force flags %v: the second stop is the forced one", got)
		}
		if w.marker() != "" || attachedInstance(w.vol()) != "" || w.detachCalls() != 1 {
			t.Fatalf("marker %q home on %q", w.marker(), attachedInstance(w.vol()))
		}
	})
}

func TestRescueNeverStopsOrDetachesABoxStartedSince(t *testing.T) {
	for _, tc := range []struct {
		name, step string
		stops      int
	}{{"before the stop", "deregistered", 0}, {"before the detach", "stopped", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			bothModes(t, func(t *testing.T, w *rescueWorld) {
				w.arm()
				hookAt(tc.step, func() {
					// An operator starts the box from the console: LaunchTime moves, the count does not.
					w.h.ec2.instances["i-dark"].LaunchTime = aws.Time(time.Now().Add(-time.Minute))
					w.h.ec2.instances["i-dark"].State.Name = ec2types.InstanceStateNameRunning
				})
				w.pass(w.f)
				if w.stopCalls() != tc.stops || w.detachCalls() != 0 || attachedInstance(w.vol()) != "i-dark" {
					t.Fatalf("stops %d detaches %d home on %q", w.stopCalls(), w.detachCalls(), attachedInstance(w.vol()))
				}
				for _, f := range w.h.ec2.stopForce {
					if f {
						t.Fatal("Force on a box started since")
					}
				}
				if w.marker() != "" || w.wedgeAt() == "" {
					t.Fatalf("marker %q wedge-at %q: the episode is kept, the rest dropped", w.marker(), w.wedgeAt())
				}
			})
		})
	}
	t.Run("running again at the same launch", func(t *testing.T) {
		bothModes(t, func(t *testing.T, w *rescueWorld) {
			w.arm()
			hookAt("stopped", func() { w.h.ec2.instances["i-dark"].State.Name = ec2types.InstanceStateNameRunning })
			w.pass(w.f)
			if w.detachCalls() != 0 || w.phase() != rescuePhaseStopped {
				t.Fatalf("detaches %d phase %q: a box that is not stopped is never detached from", w.detachCalls(), w.phase())
			}
		})
	})
}

func TestRescueIsOvertakenByAStartBetweenItsSteps(t *testing.T) {
	// A Start counted between two stages: the next stage's token check stops the rescue before
	// its destructive call, the episode is recorded and the marker dropped.
	for _, tc := range []struct {
		step          string
		stops, detach int
	}{{"deregistered", 0, 0}, {"stopped", 1, 0}} {
		t.Run(tc.step, func(t *testing.T) {
			bothModes(t, func(t *testing.T, w *rescueWorld) {
				withENI(w)
				w.arm()
				gd := w.count()
				hookAt(tc.step, w.startLands)
				w.pass(w.f)
				if w.stopCalls() != tc.stops || w.detachCalls() != tc.detach {
					t.Fatalf("stops %d detaches %d", w.stopCalls(), w.detachCalls())
				}
				if w.marker() != "" || w.wedgeAt() == "" || w.count() != gd+2 || attachedInstance(w.vol()) != "i-dark" {
					t.Fatalf("marker %q wedge-at %q count %d", w.marker(), w.wedgeAt(), w.count())
				}
			})
		})
	}
}

// A Start cannot slip between the last check and the destructive call: its count increment
// needs the claim lock the rescue holds through the call.
func TestRescueStartCannotOvertakeAfterTheLastCheck(t *testing.T) {
	for _, step := range []string{"deregistering", "stopping", "detaching"} {
		t.Run(step, func(t *testing.T) {
			bothModes(t, func(t *testing.T, w *rescueWorld) {
				withENI(w)
				w.arm()
				gd := w.count()
				started := make(chan int64, 1)
				rt := w.rt() // built here: it reads the fake's tags, which the rescue writes
				hookAt(step, func() {
					go func() {
						g, _ := rt.beginStart(context.Background())
						started <- g
					}()
					select {
					case <-started:
						t.Errorf("a Start was counted while the rescue held the claim lock at %q", step)
					case <-time.After(60 * time.Millisecond):
					}
				})
				w.pass(w.f)
				select {
				case g := <-started:
					if g != gd+2 {
						t.Fatalf("the Start was counted as %d, want %d", g, gd+2)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("the Start never got through after the rescue released the lock")
				}
				// The call in flight completed before the Start was counted; whatever followed
				// found the count moved and stopped.
				done := map[string]bool{
					"deregistering": len(w.h.ci.deregistered) == 1 && w.stopCalls() == 0,
					"stopping":      w.stopCalls() == 1 && w.detachCalls() == 0,
					"detaching":     w.detachCalls() == 1,
				}
				if !done[step] {
					t.Fatalf("deregistered %v stops %d detaches %d", w.h.ci.deregistered, w.stopCalls(), w.detachCalls())
				}
			})
		})
	}
}

func TestRescueMakesNoCallWithoutItsIntentPhaseOrItsLease(t *testing.T) {
	t.Run("lease lapsed before the call", func(t *testing.T) {
		for _, step := range []string{"deregistering", "stopping", "detaching"} {
			t.Run(step, func(t *testing.T) {
				bothModes(t, func(t *testing.T, w *rescueWorld) {
					withENI(w)
					w.arm()
					rescueStepHook = func(ctx context.Context, s string) error {
						if s == step {
							homeGuardOf(ctx).markLost()
						}
						return nil
					}
					w.pass(w.f)
					rescueStepHook = nil
					switch step {
					case "deregistering":
						if len(w.h.ci.deregistered) != 0 {
							t.Fatalf("deregistered %v", w.h.ci.deregistered)
						}
					case "stopping":
						if w.stopCalls() != 0 {
							t.Fatal("stopped")
						}
					case "detaching":
						if w.detachCalls() != 0 {
							t.Fatal("detached")
						}
					}
				})
			})
		}
	})
	t.Run("intent write fails", func(t *testing.T) {
		bothModes(t, func(t *testing.T, w *rescueWorld) {
			w.arm()
			hookAt("deregistered", func() { w.h.ec2.createTagsErr = map[string]error{ec2TagRescue: errBoom} })
			w.pass(w.f)
			w.h.ec2.createTagsErr = nil
			if w.stopCalls() != 0 || w.phase() != rescuePhaseDeregistered {
				t.Fatalf("stops %d phase %q: no side effect without its intent phase", w.stopCalls(), w.phase())
			}
			w.pass(w.f) // the next pass carries on
			w.rescued()
		})
	})
	t.Run("box unreadable", func(t *testing.T) {
		bothModes(t, func(t *testing.T, w *rescueWorld) {
			w.arm()
			hookAt("deregistered", func() { w.h.ec2.describeInstancesErr = func() error { return errBoom } })
			w.pass(w.f)
			w.h.ec2.describeInstancesErr = nil
			if w.stopCalls() != 0 || w.detachCalls() != 0 {
				t.Fatal("a failed read was taken as permission")
			}
			if w.marker() == "" {
				t.Fatal("the marker must stay after a failed read")
			}
		})
	})
}

func TestRescueDetachingWithTheHomeGoneIsDecidedByTheCount(t *testing.T) {
	for name, tc := range map[string]struct {
		moved  bool
		rescue bool
	}{"count still g_r: this rescue's own detach": {false, true}, "count moved: the owner's Start moved it": {true, false}} {
		t.Run(name, func(t *testing.T) {
			w := newRescueWorld(t, true)
			f2 := w.restarted()
			f2.onSlotDark = w.f.onSlotDark
			gd := w.count()
			m := rescueMarker{Volume: "vol-1", Epoch: w.decision().launch, Phase: rescuePhaseDetaching, Gen: gd + 1,
				Began: time.Now().Add(-time.Hour).UTC().Truncate(time.Second)}
			w.startLands() // g_r
			if tc.moved {
				w.startLands()
			}
			w.h.ec2.detach("vol-1")
			w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, m.String())
			f2.resumeRescues(context.Background())
			rescued := 0
			for _, ev := range *w.events {
				if ev.Action == "rescued" {
					rescued++
				}
			}
			if (rescued == 1) != tc.rescue || rescued > 1 {
				t.Fatalf("rescued events = %d, want rescue=%v", rescued, tc.rescue)
			}
			if w.marker() != "" || w.wedgeAt() != m.Began.Format(time.RFC3339) {
				t.Fatalf("marker %q wedge-at %q: the episode is recorded either way", w.marker(), w.wedgeAt())
			}
		})
	}
}

func TestRescueEveryStageResumesFromItsPhase(t *testing.T) {
	// A marker crafted at each phase, strict, resumed: the rescue finishes without repeating
	// what its phase says was done.
	for _, phase := range []rescuePhase{rescuePhaseBegun, rescuePhaseDeregistering, rescuePhaseDeregistered, rescuePhaseStopping, rescuePhaseStopped, rescuePhaseDetaching} {
		t.Run(string(phase), func(t *testing.T) {
			w := newRescueWorld(t, true)
			if phase.rank() >= rescuePhaseStopping.rank() && phase != rescuePhaseStopping {
				w.h.ec2.instances["i-dark"].State.Name = ec2types.InstanceStateNameStopped
			}
			gd := w.count()
			w.startLands()
			m := rescueMarker{Volume: "vol-1", Epoch: w.decision().launch, Phase: phase, Gen: gd + 1, Began: time.Now().UTC().Truncate(time.Second)}
			w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, m.String())
			f2 := w.restarted()
			f2.onSlotDark = w.f.onSlotDark
			f2.resumeRescues(context.Background())
			if attachedInstance(w.vol()) != "" || w.marker() != "" || w.wedgeAt() == "" {
				t.Fatalf("home on %q marker %q wedge-at %q", attachedInstance(w.vol()), w.marker(), w.wedgeAt())
			}
			wantStops := 1
			if phase.rank() >= rescuePhaseStopped.rank() {
				wantStops = 0 // the box was already stopped: no second stop
			}
			if w.stopCalls() != wantStops {
				t.Fatalf("stop calls = %d, want %d", w.stopCalls(), wantStops)
			}
		})
	}
}
