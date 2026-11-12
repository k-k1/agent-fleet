package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// relWorld is the slot of the 2026-11-12 incident with the action on `release`: wedged for
// longer than wedgedAfter, ECS agent connected, SSM online. The umount ends the task, as it
// did in the incident.
type relWorld struct {
	t      *testing.T
	h      *ec2Harness
	f      *ecsEC2Factory
	events *[]SlotWedged
	doer   *fakeAgentDoer
	st     *flakyLeaseStore
}

func newRelWorld(t *testing.T, store bool) *relWorld {
	t.Helper()
	h, f, events, doer := wedgedHarness(t)
	f.pool.wedgedAction = wedgedActionRelease
	w := &relWorld{t: t, h: h, f: f, events: events, doer: doer}
	if store {
		w.st = &flakyLeaseStore{fakeLeaseStore: newFakeLeaseStore()}
		f.leases = w.st
	}
	h.ssmc.onSend = func(cmd string) {
		if strings.HasPrefix(cmd, "af-umount") {
			s := h.ecs.services[rescueWorkspace]
			s.RunningCount = 0
			h.ecs.services[rescueWorkspace] = s
			h.ci.running, h.ci.stopping = nil, nil // the task stops after the umount
		}
	}
	oldC, oldM, oldW := rescueClaimBudget, rescueMountBudget, rescueWedgeBudget
	rescueClaimBudget, rescueMountBudget, rescueWedgeBudget = 150*time.Millisecond, 150*time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() {
		rescueClaimBudget, rescueMountBudget, rescueWedgeBudget = oldC, oldM, oldW
		rescueStepHook = nil
		homeMountLocks.Delete(rescueWorkspace)
		claimGenLocks.Delete(rescueWorkspace)
		wedgeLocks.Delete("i-dark")
	})
	return w
}

func relModes(t *testing.T, fn func(t *testing.T, w *relWorld)) {
	for name, store := range map[string]bool{"local": false, "store": true} {
		t.Run(name, func(t *testing.T) { fn(t, newRelWorld(t, store)) })
	}
}

func (w *relWorld) rt() *ecsEC2Runtime { return w.f.runtimeForVolume(w.h.ec2.volumes["vol-1"]) }
func (w *relWorld) count() int64 {
	w.t.Helper()
	g, err := w.rt().startGenNow(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	return g
}
func (w *relWorld) marker() string {
	return ec2TagValue(w.h.ec2.instances["i-dark"].Tags, ec2TagRescue)
}
func (w *relWorld) sweep() { _ = w.f.sweep(context.Background()) }

// arm stamps the slot and backdates the stamp past wedgedAfter, so the next sweep acts.
func (w *relWorld) arm() {
	w.t.Helper()
	w.sweep()
	backdateWedged(w.t, w.h, 11*time.Minute)
}

func (w *relWorld) calls(prefix string) (n int) {
	for _, c := range w.h.ec2.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// boxUntouched: the wedged release never deregisters, stops or terminates the box.
func (w *relWorld) boxUntouched() {
	w.t.Helper()
	for _, p := range []string{"StopInstances", "TerminateInstances"} {
		if w.calls(p) != 0 {
			w.t.Fatalf("%s was called: %v", p, w.h.ec2.calls)
		}
	}
	if len(w.h.ci.deregistered) != 0 {
		w.t.Fatalf("deregistered %v", w.h.ci.deregistered)
	}
	if w.h.ec2.instances["i-dark"].State.Name != ec2types.InstanceStateNameRunning {
		w.t.Fatalf("the box is %s", w.h.ec2.instances["i-dark"].State.Name)
	}
}

func (w *relWorld) attached() bool { return attachedInstance(w.h.ec2.volumes["vol-1"]) != "" }

func actions(evs []SlotWedged) (out []string) {
	for _, e := range evs {
		out = append(out, e.Action)
	}
	return out
}

func TestWedgedReleaseTakesTheHomeOffTheSlot(t *testing.T) {
	relModes(t, func(t *testing.T, w *relWorld) {
		w.arm()
		gd := w.count()
		w.sweep()

		if w.attached() {
			t.Fatalf("the home is still on the slot; calls %v", w.h.ec2.calls)
		}
		// umount before detach, as the ordinary release does.
		umount, detach := -1, -1
		for i, c := range w.h.ec2.calls {
			if strings.HasPrefix(c, "SSM af-umount") && umount < 0 {
				umount = i
			}
			if strings.HasPrefix(c, "DetachVolume") && detach < 0 {
				detach = i
			}
		}
		if umount < 0 || detach < 0 || umount > detach {
			t.Fatalf("umount at %d, detach at %d: %v", umount, detach, w.h.ec2.calls)
		}
		w.boxUntouched()
		if got := w.h.ecs.services[rescueWorkspace].DesiredCount; got != 0 {
			t.Fatalf("service desired = %d; the release owes the Stop's scale-to-zero", got)
		}
		if w.count() != gd+1 {
			t.Fatalf("count %d, want %d (the token's bump)", w.count(), gd+1)
		}
		if w.marker() != "" {
			t.Fatalf("marker left: %q", w.marker())
		}
		if s := wedgedStampOf(w.h); s != "" {
			t.Fatalf("the stamp outlived the episode: %q", s)
		}
		if got := actions(*w.events); len(got) != 2 || got[0] != "" || got[1] != "released" {
			t.Fatalf("events = %v, want detected then released", got)
		}
		if ev := (*w.events)[1]; ev.Volume != "vol-1" || ev.Slot != "i-dark" || ev.Workspace != rescueWorkspace {
			t.Fatalf("released event = %+v", ev)
		}
	})
}

func TestWedgedReleaseStuckStoppingTaskNeedsNoScaleDown(t *testing.T) {
	w := newRelWorld(t, false)
	w.h.ecs.services[rescueWorkspace] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0, RunningCount: 1}
	w.h.ci.running = nil
	w.h.ci.stopping = []ecstypes.Task{stuckStoppingTask(40 * time.Minute)}
	w.arm()
	w.sweep()
	if w.attached() {
		t.Fatalf("not released: %v", w.h.ec2.calls)
	}
	if n := len(w.h.ecs.updateCalls); n != 0 {
		t.Fatalf("the service was updated %d times; it was already at 0", n)
	}
	if len(*w.events) != 2 || (*w.events)[1].Action != "released" || (*w.events)[0].Signal != wedgedSignalStopping {
		t.Fatalf("events = %+v", *w.events)
	}
}

// Detect and off never act, whatever the stamp says: the default stays detect.
func TestWedgedDetectAndOffNeverRelease(t *testing.T) {
	for _, action := range []string{wedgedActionDetect, wedgedActionOff} {
		t.Run(action, func(t *testing.T) {
			w := newRelWorld(t, false)
			w.f.pool.wedgedAction = wedgedActionDetect
			w.arm()
			w.f.pool.wedgedAction = action
			w.sweep()
			w.sweep()
			if !w.attached() || w.marker() != "" || w.calls("DetachVolume") != 0 || len(w.h.ssmc.commands) != 0 {
				t.Fatalf("acted under %s: marker %q calls %v", action, w.marker(), w.h.ec2.calls)
			}
		})
	}
}

func TestWedgedReleaseWaitsOutTheWedgedAfterWindow(t *testing.T) {
	w := newRelWorld(t, false)
	w.sweep() // stamps
	backdateWedged(t, w.h, 5*time.Minute)
	w.sweep()
	if !w.attached() || w.marker() != "" {
		t.Fatalf("acted inside the window: marker %q", w.marker())
	}
}

func TestWedgedReleaseHealthyBoxIsNeverReleased(t *testing.T) {
	w := newRelWorld(t, false)
	w.arm()
	w.doer.err, w.doer.status = nil, 200 // it answers again
	w.sweep()
	if !w.attached() || w.marker() != "" || w.calls("DetachVolume") != 0 {
		t.Fatalf("a recovered slot was touched: marker %q calls %v", w.marker(), w.h.ec2.calls)
	}
	if wedgedStampOf(w.h) != "" {
		t.Fatal("the stamp should have cleared")
	}
}

// The verdict that armed the action is re-asked under the claim lock: a slot that answers by
// then is left alone, and nothing was written.
func TestWedgedReleaseReverifiesUnderTheLock(t *testing.T) {
	relModes(t, func(t *testing.T, w *relWorld) {
		w.arm()
		gd := w.count()
		var calls atomic.Int32
		w.doer.onDo = func() {
			// The first probe is the scan's verdict (wedged); the second, under the lock,
			// finds the Agent healthy again.
			if calls.Add(1) >= 2 {
				w.doer.err, w.doer.status = nil, 200
			}
		}
		w.sweep()
		if calls.Load() < 2 {
			t.Fatalf("the verdict was not asked again under the lock (%d probes)", calls.Load())
		}
		if w.marker() != "" || w.count() != gd || w.calls("DetachVolume") != 0 || !w.attached() {
			t.Fatalf("a slot that recovered was taken: marker %q count %d (was %d)", w.marker(), w.count(), gd)
		}
	})
}

func TestWedgedReleaseStepsAsideForAStartCountedSinceTheDecision(t *testing.T) {
	relModes(t, func(t *testing.T, w *relWorld) {
		w.arm()
		inst := w.h.ec2.instances["i-dark"]
		d := darkDecision{slot: "i-dark", vol: w.h.ec2.volumes["vol-1"], launch: inst.LaunchTime.UTC().Truncate(time.Second),
			gd: w.count(), wedged: true, stillWedged: func(context.Context) bool { return true }}
		rt := w.rt()
		if rt.leases == nil {
			rt.generation().Add(1)
		} else if _, err := w.st.fakeLeaseStore.BumpCPCounter(context.Background(), homeStartGen+rt.base.name); err != nil {
			t.Fatal(err)
		}
		_, _, err := rt.rescuePrepare(context.Background(), w.h.ssmc, d)
		if !errors.Is(err, errRescueStepAside) || w.marker() != "" {
			t.Fatalf("err %v marker %q", err, w.marker())
		}
	})
}

// A member's Start counted while the release works overtakes it: the home stays on the slot
// (unmounted at worst, which a Start mounts), nothing is detached, and no release is reported.
func TestWedgedReleaseIsOvertakenByAStart(t *testing.T) {
	for _, step := range []string{"counted", "service-stopped"} {
		t.Run(step, func(t *testing.T) {
			relModes(t, func(t *testing.T, w *relWorld) {
				w.arm()
				hookAt(step, func() {
					rt := w.rt()
					if rt.leases == nil {
						rt.generation().Add(1)
					} else if _, err := w.st.fakeLeaseStore.BumpCPCounter(context.Background(), homeStartGen+rt.base.name); err != nil {
						t.Fatal(err)
					}
				})
				w.sweep()
				if !w.attached() || w.calls("DetachVolume") != 0 {
					t.Fatalf("detached from under a Start: %v", w.h.ec2.calls)
				}
				if w.marker() != "" {
					t.Fatalf("marker left after being overtaken: %q", w.marker())
				}
				for _, e := range *w.events {
					if e.Action == "released" {
						t.Fatalf("reported a release that did not happen: %+v", *w.events)
					}
				}
				w.boxUntouched()
			})
		})
	}
}

func TestWedgedReleaseCrashAtEveryStepThenResume(t *testing.T) {
	for _, step := range []string{"marked", "counted", "ended-start", "listed", "before-cancel", "cancelled", "guarded", "detaching", "service-stopped"} {
		t.Run(step, func(t *testing.T) {
			relModes(t, func(t *testing.T, w *relWorld) {
				w.arm()
				rescueStepHook = func(_ context.Context, s string) error {
					if s == step {
						return errRescueCrash
					}
					return nil
				}
				w.sweep()
				rescueStepHook = nil
				if !w.attached() {
					t.Fatalf("detached despite the crash at %q", step)
				}
				if step != "marked" {
					m, ok := parseRescueMarker(w.marker())
					if !ok || !m.Wedged {
						t.Fatalf("marker after the crash = %q", w.marker())
					}
				}
				// The restarted CP: new factory, same AWS and store.
				f2 := w.h.factory()
				f2.pool.wedgedAction = wedgedActionRelease
				f2.leases = w.f.leases
				f2.agentHTTP = w.f.agentHTTP
				f2.onSlotWedged = w.f.onSlotWedged
				f2.resumeRescues(context.Background())
				if w.marker() != "" {
					t.Fatalf("the resume left %q", w.marker())
				}
				if step == "marked" {
					if !w.attached() {
						t.Fatal("an unconfirmed marker must only be finalized")
					}
					return
				}
				if w.attached() {
					t.Fatalf("the resume did not finish the release: %v", w.h.ec2.calls)
				}
				w.boxUntouched()
			})
		})
	}
}

// With the wedged action no longer `release`, a wedged marker is finalized, never continued:
// the marker outlives the setting that wrote it, and the dark ladder must not pick it up.
func TestWedgedMarkerIsNotContinuedByAnotherLadder(t *testing.T) {
	w := newRelWorld(t, false)
	w.arm()
	rescueStepHook = func(_ context.Context, s string) error {
		if s == "guarded" {
			return errRescueCrash
		}
		return nil
	}
	w.sweep()
	rescueStepHook = nil
	if w.marker() == "" {
		t.Fatal("setup: no marker")
	}
	f2 := w.h.factory()
	f2.pool.wedgedAction = wedgedActionDetect
	f2.pool.darkAction = darkActionRescue // the dark ladder is on, and must not touch it
	f2.leases = w.f.leases
	f2.agentHTTP = w.f.agentHTTP
	f2.resumeRescues(context.Background())
	if w.marker() != "" || !w.attached() || w.calls("DetachVolume") != 0 || w.calls("StopInstances") != 0 || len(w.h.ci.deregistered) != 0 {
		t.Fatalf("marker %q attached %v calls %v", w.marker(), w.attached(), w.h.ec2.calls)
	}
}

// One marker per home: a slot that already carries one (the dark ladder's, or another
// replica's wedged release) is not taken a second time.
func TestWedgedReleaseStepsAsideOnAMarkedBox(t *testing.T) {
	relModes(t, func(t *testing.T, w *relWorld) {
		w.arm()
		inst := w.h.ec2.instances["i-dark"]
		d := darkDecision{slot: "i-dark", vol: w.h.ec2.volumes["vol-1"], launch: inst.LaunchTime.UTC().Truncate(time.Second),
			gd: w.count(), wedged: true, stillWedged: func(context.Context) bool { return true }}
		w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, rescueFixture().String())
		gd := w.count()
		_, _, err := w.rt().rescuePrepare(context.Background(), w.h.ssmc, d)
		if !errors.Is(err, errRescueStepAside) || w.marker() != rescueFixture().String() || w.count() != gd {
			t.Fatalf("err %v marker %q count %d", err, w.marker(), w.count())
		}
	})
}

func TestWedgedReleaseSSMOfflineIsReportedBlockedOnceAndLeftAlone(t *testing.T) {
	w := newRelWorld(t, false)
	w.arm()
	w.h.ssmc.online = map[string]bool{"i-dark": false}
	gd := w.count()
	w.sweep()
	w.sweep()
	w.sweep()
	if w.marker() != "" || w.count() != gd || w.calls("DetachVolume") != 0 || len(w.h.ssmc.commands) != 0 || !w.attached() {
		t.Fatalf("a slot whose SSM is offline was taken: marker %q count %d", w.marker(), w.count())
	}
	if got := actions(*w.events); len(got) != 2 || got[1] != "blocked" || (*w.events)[1].Reason != wedgedReasonSSMOffline {
		t.Fatalf("events = %+v, want detected then one blocked(ssm_offline)", *w.events)
	}
	if len(w.h.ecs.updateCalls) != 0 {
		t.Fatalf("the service was touched: %+v", w.h.ecs.updateCalls)
	}
	// SSM comes back: the release runs.
	w.h.ssmc.online = map[string]bool{"i-dark": true}
	w.sweep()
	if w.attached() {
		t.Fatalf("not released once SSM was online: %v", w.h.ec2.calls)
	}
}

// A umount that never ends must not hold the mount lock for good: it is bounded, cancelled,
// and the release is blocked (reported once) with the home still attached; the next pass
// resumes and finishes it.
func TestWedgedReleaseUmountIsBounded(t *testing.T) {
	relModes(t, func(t *testing.T, w *relWorld) {
		old := wedgedUmountBudget
		wedgedUmountBudget = 100 * time.Millisecond
		t.Cleanup(func() { wedgedUmountBudget = old })
		var hang atomic.Bool
		hang.Store(true)
		w.h.ssmc.status = func(cmd string) string {
			if hang.Load() && strings.HasPrefix(cmd, "af-umount") {
				// Called under the fake's lock. A cancelled command ends, as in SSM; without
				// the cancel it would stay in progress and keep the lock.
				for _, c := range w.h.ssmc.cancelled {
					if c == cmd {
						return "Cancelled"
					}
				}
				return "InProgress"
			}
			return ""
		}
		w.h.rt.sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Millisecond):
				return nil
			}
		}
		w.f.sleep = w.h.rt.sleep
		w.arm()
		w.sweep()
		if !w.attached() || w.calls("DetachVolume") != 0 {
			t.Fatalf("detached after a umount that never finished: %v", w.h.ec2.calls)
		}
		m, ok := parseRescueMarker(w.marker())
		if !ok || m.BlockedSince.IsZero() || m.Phase != rescuePhaseDetaching {
			t.Fatalf("marker = %q, want detaching with blocked_since", w.marker())
		}
		if got := actions(*w.events); len(got) != 2 || got[1] != "blocked" {
			t.Fatalf("events = %v", got)
		}
		// Retried by the resume scan, reported blocked only once.
		w.sweep()
		if got := actions(*w.events); len(got) != 2 {
			t.Fatalf("blocked reported again: %v", got)
		}
		hang.Store(false)
		w.sweep()
		if w.attached() || w.marker() != "" {
			t.Fatalf("not completed after the umount worked: marker %q calls %v", w.marker(), w.h.ec2.calls)
		}
		if got := actions(*w.events); got[len(got)-1] != "released" {
			t.Fatalf("events = %v", got)
		}
	})
}

func TestWedgedMarkerRoundTripAndOldReaders(t *testing.T) {
	m := rescueFixture()
	m.Wedged = true
	got, ok := parseRescueMarker(m.String())
	if !ok || !got.Wedged || !got.sameRescue(m) || !strings.HasSuffix(m.String(), "/wedged") {
		t.Fatalf("round trip: %q -> %+v %v", m.String(), got, ok)
	}
	plain := rescueFixture()
	if got, ok := parseRescueMarker(plain.String()); !ok || got.Wedged || strings.Contains(plain.String(), "wedged") {
		t.Fatalf("a dark marker grew a kind: %q", plain.String())
	}
	if _, ok := parseRescueMarker(plain.String() + "/other"); ok {
		t.Fatal("an unknown kind must not parse: an older reader fences instead of guessing")
	}
}

func TestPoolStatusShowsTheWedgedRungs(t *testing.T) {
	w := newRelWorld(t, false)
	w.arm()
	inst := w.h.ec2.instances["i-dark"]
	m := rescueMarker{Volume: "vol-1", Epoch: inst.LaunchTime.UTC().Truncate(time.Second), Phase: rescuePhaseDetaching, Gen: 3, Began: time.Now().UTC().Truncate(time.Second), Wedged: true}
	for name, tc := range map[string]struct {
		mutate func(*rescueMarker)
		want   string
	}{
		"releasing": {func(*rescueMarker) {}, wedgedRungReleasing},
		"blocked":   {func(m *rescueMarker) { m.BlockedSince = m.Began }, darkRungBlocked},
		"released":  {func(m *rescueMarker) { m.Phase = rescuePhaseDetached }, wedgedRungReleased},
	} {
		t.Run(name, func(t *testing.T) {
			mm := m
			tc.mutate(&mm)
			w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, mm.String())
			st, err := w.f.PoolStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range st.Slots {
				if s.InstanceID == "i-dark" {
					if s.WedgedRung != tc.want || s.WedgedSince == "" {
						t.Fatalf("wedged_rung=%q since=%q, want %q", s.WedgedRung, s.WedgedSince, tc.want)
					}
					if s.Rung != "" {
						t.Fatalf("a wedged release shown on the dark ladder: rung=%q", s.Rung)
					}
					return
				}
			}
			t.Fatal("slot missing")
		})
	}
}

// The service's running/pending counts do not see a STOPPING task, and a host umount does not
// prove the container let go of its bind mount: the detach waits for the tasks themselves to be
// STOPPED, and never forces.
func TestWedgedReleaseDetachesOnlyAfterEveryTaskIsStopped(t *testing.T) {
	for name, task := range map[string]func() ecstypes.Task{
		"still STOPPING": func() ecstypes.Task { return stuckStoppingTask(40 * time.Minute) },
		"still RUNNING": func() ecstypes.Task {
			x := runningTask(rescueWorkspace, "i-dark")
			x.HealthStatus = ecstypes.HealthStatusHealthy
			return x
		},
	} {
		t.Run(name, func(t *testing.T) {
			relModes(t, func(t *testing.T, w *relWorld) {
				old := wedgedTaskPolls
				wedgedTaskPolls = 3
				t.Cleanup(func() { wedgedTaskPolls = old })
				// The service counts say 0 once the umount ran, but the task survives it.
				w.h.ssmc.onSend = func(cmd string) {
					if strings.HasPrefix(cmd, "af-umount") {
						s := w.h.ecs.services[rescueWorkspace]
						s.RunningCount, s.PendingCount = 0, 0
						w.h.ecs.services[rescueWorkspace] = s
						w.h.ci.running, w.h.ci.stopping = nil, []ecstypes.Task{task()}
						if name == "still RUNNING" {
							w.h.ci.running, w.h.ci.stopping = []ecstypes.Task{task()}, nil
						}
					}
				}
				w.arm()
				w.sweep()
				if !w.attached() || w.calls("DetachVolume") != 0 {
					t.Fatalf("detached from under a task that has not STOPPED: %v", w.h.ec2.calls)
				}
				m, ok := parseRescueMarker(w.marker())
				if !ok || m.BlockedSince.IsZero() {
					t.Fatalf("marker %q, want blocked", w.marker())
				}
				if got := actions(*w.events); got[len(got)-1] != "blocked" {
					t.Fatalf("events %v", got)
				}
				for _, c := range w.h.ec2.calls {
					if strings.Contains(c, "force") {
						t.Fatalf("a forced call: %v", w.h.ec2.calls)
					}
				}
				// The task reaches STOPPED later: the next pass detaches, after that.
				stopped := runningTask(rescueWorkspace, "i-dark")
				stopped.LastStatus = aws.String("STOPPED")
				w.h.ci.running, w.h.ci.stopping = nil, []ecstypes.Task{stopped}
				w.h.ssmc.onSend = nil
				w.sweep()
				if w.attached() || w.marker() != "" {
					t.Fatalf("not released once the task STOPPED: marker %q calls %v", w.marker(), w.h.ec2.calls)
				}
			})
		})
	}
}
