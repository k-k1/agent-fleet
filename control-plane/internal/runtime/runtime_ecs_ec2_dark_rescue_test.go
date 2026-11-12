package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

var errBoom = errors.New("boom")

// flakyLeaseStore is the fake store with failures a test can switch on.
type flakyLeaseStore struct {
	*fakeLeaseStore
	counterErr, bumpErr error
	acquireErr          func(name string) error
}

func (s *flakyLeaseStore) CPCounter(ctx context.Context, name string) (int64, error) {
	if s.counterErr != nil {
		return 0, s.counterErr
	}
	return s.fakeLeaseStore.CPCounter(ctx, name)
}

func (s *flakyLeaseStore) BumpCPCounter(ctx context.Context, name string) (int64, error) {
	if s.bumpErr != nil {
		return 0, s.bumpErr
	}
	return s.fakeLeaseStore.BumpCPCounter(ctx, name)
}

func (s *flakyLeaseStore) AcquireCPLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error) {
	if s.acquireErr != nil {
		if err := s.acquireErr(name); err != nil {
			return false, err
		}
	}
	return s.fakeLeaseStore.AcquireCPLease(ctx, name, holder, ttl)
}

// failingServices is the ECS fake whose service read can be made to fail.
type failingServices struct {
	*fakeECS
	err error
}

func (f failingServices) DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, o ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.fakeECS.DescribeServices(ctx, in, o...)
}

type rescueWorld struct {
	t      *testing.T
	h      *ec2Harness
	f      *ecsEC2Factory
	st     *flakyLeaseStore // nil: process-local locks and count
	events *[]SlotDark
}

const rescueWorkspace = "af-ws-acme-alice"

// newRescueWorld is darkHarness with the service at desired 0 (the slot a member stopped and
// that then went dark), the action on `rescue`, and one pending command of the CP's own on
// the box next to one that is somebody else's.
func newRescueWorld(t *testing.T, store bool) *rescueWorld {
	t.Helper()
	h, f, events := darkHarness(t)
	h.ecs.services[rescueWorkspace] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	h.ci.running = nil
	f.pool.darkAction = darkActionRescue
	w := &rescueWorld{t: t, h: h, f: f, events: events}
	if store {
		w.st = &flakyLeaseStore{fakeLeaseStore: newFakeLeaseStore()}
		f.leases = w.st
	}
	h.ssmc.unfinished = []ssmtypes.Command{
		{CommandId: aws.String("cmd-1"), Comment: aws.String(slotVolumeCommandComment)},
		{CommandId: aws.String("cmd-x"), Comment: aws.String("somebody else")},
	}
	oldClaim, oldMount, oldWedge := rescueClaimBudget, rescueMountBudget, rescueWedgeBudget
	rescueClaimBudget, rescueMountBudget, rescueWedgeBudget = 150*time.Millisecond, 150*time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() {
		rescueClaimBudget, rescueMountBudget, rescueWedgeBudget = oldClaim, oldMount, oldWedge
		rescueStepHook = nil
		homeMountLocks.Delete(rescueWorkspace)
		claimGenLocks.Delete(rescueWorkspace)
		wedgeLocks.Delete("i-dark")
	})
	return w
}

func bothModes(t *testing.T, fn func(t *testing.T, w *rescueWorld)) {
	for name, store := range map[string]bool{"local": false, "store": true} {
		t.Run(name, func(t *testing.T) { fn(t, newRescueWorld(t, store)) })
	}
}

func (w *rescueWorld) vol() *ec2types.Volume { return w.h.ec2.volumes["vol-1"] }
func (w *rescueWorld) rt() *ecsEC2Runtime    { return w.f.runtimeForVolume(w.vol()) }

func (w *rescueWorld) count() int64 {
	w.t.Helper()
	g, err := w.rt().startGenNow(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	return g
}

// startLands is a member's Start counted on another replica, which does not wait for anything
// the rescue holds.
func (w *rescueWorld) startLands() {
	w.t.Helper()
	rt := w.rt()
	if rt.leases == nil {
		rt.generation().Add(1)
		return
	}
	if _, err := w.st.fakeLeaseStore.BumpCPCounter(context.Background(), homeStartGen+rt.base.name); err != nil {
		w.t.Fatal(err)
	}
}

func (w *rescueWorld) pass(f *ecsEC2Factory) {
	w.t.Helper()
	vols, err := f.ec2.DescribeVolumes(context.Background(), &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{tagFilter(EC2TagPool, f.pool.pool), tagFilter(EC2TagRole, ec2RoleHome)},
	})
	if err != nil {
		w.t.Fatal(err)
	}
	f.sweepDarkSlots(context.Background(), vols.Volumes)
}

// arm stamps the slot and moves the stamp far enough back that the next pass acts.
func (w *rescueWorld) arm() {
	w.t.Helper()
	w.pass(w.f)
	backdateDark(w.t, w.h, 30*time.Minute)
}

// restarted is a Control Plane that came up after the crash: new factory, same AWS and store.
func (w *rescueWorld) restarted() *ecsEC2Factory {
	f := w.h.factory()
	f.pool.darkAction = darkActionRescue
	f.leases = w.f.leases
	f.agentHTTP = w.f.agentHTTP
	return f
}

func (w *rescueWorld) marker() string {
	return ec2TagValue(w.h.ec2.instances["i-dark"].Tags, ec2TagRescue)
}

func (w *rescueWorld) wedgeAt() string {
	return ec2TagValue(w.h.ec2.instances["i-dark"].Tags, ec2TagWedgeAt)
}

// untouched asserts the destructive half of the ladder never ran: this phase has none.
func (w *rescueWorld) untouched() {
	w.t.Helper()
	for _, c := range w.h.ec2.calls {
		if strings.HasPrefix(c, "StopInstances") || strings.HasPrefix(c, "DetachVolume") || strings.HasPrefix(c, "TerminateInstances") {
			w.t.Fatalf("a destructive call was made: %q", c)
		}
	}
	if len(w.h.ci.deregistered) != 0 {
		w.t.Fatalf("deregistered %v", w.h.ci.deregistered)
	}
	if len(w.h.ecs.updateCalls) != 0 {
		w.t.Fatalf("the service was updated: %+v", w.h.ecs.updateCalls)
	}
	if got := attachedInstance(w.vol()); got != "i-dark" {
		w.t.Fatalf("the home moved to %q", got)
	}
}

func (w *rescueWorld) cancelled() []string {
	w.h.ssmc.mu.Lock()
	defer w.h.ssmc.mu.Unlock()
	return append([]string(nil), w.h.ssmc.cancelled...)
}

func (w *rescueWorld) decision() darkDecision {
	w.t.Helper()
	inst := w.h.ec2.instances["i-dark"]
	return darkDecision{slot: "i-dark", vol: w.vol(), launch: inst.LaunchTime.UTC().Truncate(time.Second), gd: w.count()}
}

func hookAt(step string, fn func()) {
	done := false
	rescueStepHook = func(_ context.Context, s string) error {
		if s == step && !done {
			done = true
			fn()
		}
		return nil
	}
}

func TestRescuePreparesInTheOrderTheADRFixes(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		w.arm()
		gd := w.count()
		seen, markers := map[string]string{}, map[string]string{}
		rescueStepHook = func(_ context.Context, step string) error {
			seen[step] = fmt.Sprintf("count=%d cancelled=%d", w.count(), len(w.cancelled()))
			markers[step] = w.marker()
			return nil
		}
		w.pass(w.f)

		// The marker is written before the count moves and before any command is cancelled,
		// and names the g_r the bump is about to produce.
		m, ok := parseRescueMarker(markers["marked"])
		if !ok || m.Phase != rescuePhasePreparing || m.Gen != gd+1 || m.Volume != "vol-1" {
			t.Fatalf("marker at 'marked' = %q (%+v)", markers["marked"], m)
		}
		if want := fmt.Sprintf("count=%d cancelled=0", gd); seen["marked"] != want {
			t.Fatalf("at 'marked': %s, want %s", seen["marked"], want)
		}
		if want := fmt.Sprintf("count=%d cancelled=0", gd+1); seen["counted"] != want || !strings.Contains(markers["counted"], "/begun/") {
			t.Fatalf("at 'counted': %s %q, want %s", seen["counted"], markers["counted"], want)
		}
		if !strings.Contains(seen["cancelled"], "cancelled=1") {
			t.Fatalf("at 'cancelled': %s", seen["cancelled"])
		}
		if got := w.cancelled(); len(got) != 1 || got[0] != "cmd-1" {
			t.Fatalf("cancelled %v; only the CP's own command is the rescue's to cancel", got)
		}
		w.untouched()
		if w.marker() != "" {
			t.Fatalf("marker %q left behind; this phase has no later step to carry it", w.marker())
		}
		if w.wedgeAt() != "" {
			t.Fatalf("a rescue that never acted on the box recorded an episode: %q", w.wedgeAt())
		}
		if w.count() != gd+1 {
			t.Fatalf("count %d, want %d", w.count(), gd+1)
		}
		if len(*w.events) != 1 {
			t.Fatalf("detected events = %d, want 1", len(*w.events))
		}
	})
}

func TestRescueNeverTakesAServiceThatIsUp(t *testing.T) {
	// Which task answered the Service Connect alias cannot be proven, so a service that is up
	// is left alone even when the probe says failed.
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		w.h.ecs.services[rescueWorkspace] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}
		w.h.ci.running = append(w.h.ci.running, runningTask(rescueWorkspace, "i-dark"))
		w.arm()
		gd := w.count()
		w.pass(w.f)
		if w.marker() != "" || w.count() != gd || len(w.cancelled()) != 0 {
			t.Fatalf("a service-up slot was touched: marker %q count %d cancelled %v", w.marker(), w.count(), w.cancelled())
		}
		if len(*w.events) != 1 {
			t.Fatalf("it is still detected: events = %d", len(*w.events))
		}
		w.untouched()
	})
}

func TestRescueStepsAsideWhenTheDecisionIsStale(t *testing.T) {
	for name, stale := range map[string]func(w *rescueWorld, d *darkDecision){
		"a Start counted since the decision": func(w *rescueWorld, d *darkDecision) { w.startLands() },
		"a live claim": func(w *rescueWorld, d *darkDecision) {
			w.h.ec2.setTag("vol-1", EC2TagClaim, "i-dark")
			w.h.ec2.setTag("vol-1", ec2TagClaimAt, time.Now().UTC().Format(time.RFC3339))
		},
		"the home left the box": func(w *rescueWorld, d *darkDecision) { w.h.ec2.detach("vol-1") },
		"the box started again": func(w *rescueWorld, d *darkDecision) {
			w.h.ec2.instances["i-dark"].LaunchTime = aws.Time(time.Now().Add(-time.Minute))
		},
		"a marker is already there": func(w *rescueWorld, d *darkDecision) {
			w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, rescueFixture().String())
		},
		"the box is stopped": func(w *rescueWorld, d *darkDecision) { w.h.ec2.instances["i-dark"].State.Name = "stopped" },
	} {
		t.Run(name, func(t *testing.T) {
			bothModes(t, func(t *testing.T, w *rescueWorld) {
				d := w.decision()
				gd := d.gd
				stale(w, &d)
				before := w.marker()
				countBefore := w.count()
				_, _, err := w.rt().rescuePrepare(context.Background(), w.h.ssmc, d)
				if !errors.Is(err, errRescueStepAside) {
					t.Fatalf("err = %v, want it to step aside", err)
				}
				if w.marker() != before || len(w.cancelled()) != 0 {
					t.Fatalf("a rescue that stepped aside wrote: marker %q cancelled %v", w.marker(), w.cancelled())
				}
				if w.count() != countBefore {
					t.Fatalf("count moved %d -> %d (decided at %d)", countBefore, w.count(), gd)
				}
				if attachedInstance(w.vol()) == "" {
					w.h.ec2.attach("vol-1", "i-dark", time.Now()) // the staleness this case staged
				}
				w.untouched()
			})
		})
	}
}

func TestRescueCrashAtEveryStepThenResume(t *testing.T) {
	steps := []string{"marked", "counted", "ended-start", "listed", "before-cancel", "cancelled", "guarded"}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			bothModes(t, func(t *testing.T, w *rescueWorld) {
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

				// What a dead process leaves: the marker, written before the first side effect.
				m, ok := parseRescueMarker(w.marker())
				wantPhase := rescuePhaseBegun
				if step == "marked" {
					wantPhase = rescuePhasePreparing // the bump is not confirmed yet
				}
				if !ok || m.Phase != wantPhase || m.Gen != gd+1 {
					t.Fatalf("after a crash at %q the marker is %q", step, w.marker())
				}
				wantCount := gd + 1
				if step == "marked" {
					wantCount = gd // the bump had not happened
				}
				if w.count() != wantCount {
					t.Fatalf("count %d, want %d", w.count(), wantCount)
				}
				w.untouched()

				// The restarted CP finds the marker whatever the box looks like now.
				f2 := w.restarted()
				f2.resumeRescues(context.Background())
				if w.marker() != "" {
					t.Fatalf("the resume scan left %q", w.marker())
				}
				if w.wedgeAt() != "" {
					t.Fatalf("a rescue that never acted on the box recorded %q", w.wedgeAt())
				}
				if w.count() != wantCount {
					t.Fatalf("resume moved the count %d -> %d", wantCount, w.count())
				}
				if step != "marked" {
					// A marker whose count never moved is not a rescue to continue: the commands are
					// listed again only under the claim lock with the count still g_r.
					found := false
					for _, c := range w.cancelled() {
						if c == "cmd-1" {
							found = true
						}
						if c == "cmd-x" {
							t.Fatalf("cancelled somebody else's command")
						}
					}
					if !found {
						t.Fatalf("the CP's pending command was never cancelled: %v", w.cancelled())
					}
				}
				w.untouched()
			})
		})
	}
}

func TestRescueIsOvertakenByAStartAtEveryBoundary(t *testing.T) {
	steps := []string{"marked", "counted", "ended-start", "listed", "before-cancel", "cancelled"}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			bothModes(t, func(t *testing.T, w *rescueWorld) {
				w.arm()
				gd := w.count()
				hookAt(step, func() {
					w.startLands()
					if step == "listed" {
						// A command the Start sends after the token was taken is not this rescue's.
						w.h.ssmc.mu.Lock()
						w.h.ssmc.unfinished = append(w.h.ssmc.unfinished,
							ssmtypes.Command{CommandId: aws.String("cmd-late"), Comment: aws.String(slotVolumeCommandComment)})
						w.h.ssmc.mu.Unlock()
					}
				})
				w.pass(w.f)

				if w.marker() != "" {
					t.Fatalf("marker %q left after the Start overtook the rescue", w.marker())
				}
				if w.wedgeAt() != "" {
					t.Fatalf("a rescue that never acted on the box recorded %q", w.wedgeAt())
				}
				if w.count() != gd+2 {
					t.Fatalf("count %d, want %d: the rescue's bump and the Start's, and nothing else", w.count(), gd+2)
				}
				for _, c := range w.cancelled() {
					if c != "cmd-1" {
						t.Fatalf("cancelled %q; the set is fixed in prepare", c)
					}
				}
				w.untouched()
			})
		})
	}
}

func TestRescueAbortsOnAReadThatFails(t *testing.T) {
	type tc struct {
		name      string
		storeOnly bool
		inject    func(w *rescueWorld)
		// bumped: the count had moved by the time the read failed.
		bumped bool
	}
	cases := []tc{
		{name: "start count", storeOnly: true, inject: func(w *rescueWorld) { w.st.counterErr = errBoom }},
		{name: "home volume", inject: func(w *rescueWorld) { w.h.ec2.describeVolumesErr = errBoom }},
		{name: "service", inject: func(w *rescueWorld) { w.f.base.ecs = failingServices{fakeECS: w.h.ecs, err: errBoom} }},
		{name: "the box", inject: func(w *rescueWorld) { w.h.ec2.describeInstancesErr = func() error { return errBoom } }},
		{name: "marker write", inject: func(w *rescueWorld) { w.h.ec2.createTagsErr = map[string]error{ec2TagRescue: errBoom} }},
		{name: "count bump", storeOnly: true, inject: func(w *rescueWorld) { w.st.bumpErr = errBoom }},
		{name: "pending commands", bumped: true, inject: func(w *rescueWorld) { w.h.ssmc.listErr = errBoom }},
		{name: "claim lock", inject: func(w *rescueWorld) {
			if w.st != nil {
				w.st.acquireErr = func(name string) error {
					if strings.HasPrefix(name, homeLockClaim) {
						return errBoom
					}
					return nil
				}
				return
			}
			localSem(&claimGenLocks, rescueWorkspace) <- struct{}{}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bothModes(t, func(t *testing.T, w *rescueWorld) {
				if c.storeOnly && w.st == nil {
					t.Skip("the process-local count cannot fail to read")
				}
				d := w.decision()
				w.f.base.ecs = w.h.ecs
				c.inject(w)
				rt := w.rt() // built after the injection: it copies the factory's clients
				_, _, err := rt.rescuePrepare(context.Background(), w.h.ssmc, d)
				if err == nil || errors.Is(err, errRescueStepAside) {
					t.Fatalf("err = %v; a read that fails must abort, not proceed and not look like a decision", err)
				}
				if w.marker() != "" {
					t.Fatalf("marker %q left behind", w.marker())
				}
				if len(w.cancelled()) != 0 {
					t.Fatalf("cancelled %v after a failed read", w.cancelled())
				}
				w.h.ec2.describeVolumesErr = nil
				w.h.ec2.describeInstancesErr = nil
				if w.st != nil {
					w.st.counterErr = nil
				}
				want := d.gd
				if c.bumped {
					want = d.gd + 1
				}
				if w.count() != want {
					t.Fatalf("count %d, want %d", w.count(), want)
				}
				w.untouched()
			})
		})
	}
}

func TestRescueGuardedStepAbortsWithoutItsLocksOrCount(t *testing.T) {
	for name, tc := range map[string]struct {
		inject func(w *rescueWorld)
		want   error
	}{
		"count unreadable": {want: errRescueAborted, inject: func(w *rescueWorld) { w.st.counterErr = errBoom }},
		"claim lock not had": {want: errRescueAborted, inject: func(w *rescueWorld) {
			w.st.acquireErr = func(n string) error {
				if strings.HasPrefix(n, homeLockClaim) {
					return errBoom
				}
				return nil
			}
		}},
		"mount lock not had": {want: errRescueBusy, inject: func(w *rescueWorld) {
			w.st.acquireErr = func(n string) error {
				if strings.HasPrefix(n, homeLockMount) {
					return errBoom
				}
				return nil
			}
		}},
		"box unreadable":  {want: errRescueAborted, inject: func(w *rescueWorld) { w.h.ec2.describeInstancesErr = func() error { return errBoom } }},
		"home unreadable": {want: errRescueAborted, inject: func(w *rescueWorld) { w.h.ec2.describeVolumesErr = errBoom }},
		"count moved":     {want: errRescueOvertaken, inject: func(w *rescueWorld) { w.startLands() }},
		"marker replaced": {want: errRescueOvertaken, inject: func(w *rescueWorld) {
			m := rescueFixture()
			m.Volume, m.Gen = "vol-1", 999
			w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, m.String())
		}},
		"marker gone": {want: errRescueOvertaken, inject: func(w *rescueWorld) {
			_, _ = w.h.ec2.DeleteTags(context.Background(), &ec2.DeleteTagsInput{Resources: []string{"i-dark"}, Tags: []ec2types.Tag{{Key: aws.String(ec2TagRescue)}}})
		}},
		"box started again": {want: errRescueOvertaken, inject: func(w *rescueWorld) {
			w.h.ec2.instances["i-dark"].LaunchTime = aws.Time(time.Now().Add(-time.Minute))
		}},
		"home moved": {want: errRescueOvertaken, inject: func(w *rescueWorld) { w.h.ec2.detach("vol-1") }},
	} {
		t.Run(name, func(t *testing.T) {
			w := newRescueWorld(t, true)
			d := w.decision()
			m, _, err := w.rt().rescuePrepare(context.Background(), w.h.ssmc, d)
			if err != nil {
				t.Fatal(err)
			}
			tc.inject(w)
			ran := false
			err = w.rt().rescueGuarded(context.Background(), m, "i-dark", func(context.Context) error { ran = true; return nil })
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if ran {
				t.Fatal("the guarded step ran")
			}
		})
	}
}

func TestRescueGuardedStepRunsWhenItOwnsTheHome(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		m, _, err := w.rt().rescuePrepare(context.Background(), w.h.ssmc, w.decision())
		if err != nil {
			t.Fatal(err)
		}
		ran := false
		if err := w.rt().rescueGuarded(context.Background(), m, "i-dark", func(c context.Context) error {
			ran = true
			// Both locks are held while the step runs: neither can be taken from outside.
			for _, which := range []string{homeLockMount, homeLockClaim} {
				select {
				case localSem(lockMap(which), w.rt().replica+rescueWorkspace) <- struct{}{}:
					t.Errorf("%s was free inside the guarded step", which)
				default:
				}
			}
			return nil
		}); err != nil || !ran {
			t.Fatalf("ran=%v err=%v", ran, err)
		}
	})
}

func TestRescueBusyMountLockLeavesTheMarkerAndTheNextPassFinishes(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		w.arm()
		gd := w.count()
		sem := localSem(&homeMountLocks, rescueWorkspace)
		sem <- struct{}{} // somebody else's release holds the home's mount lock
		w.pass(w.f)
		m, ok := parseRescueMarker(w.marker())
		if !ok || m.Phase != rescuePhaseBegun || w.count() != gd+1 {
			t.Fatalf("marker %q count %d: a rescue that cannot take the mount lock keeps its marker", w.marker(), w.count())
		}
		if got := w.cancelled(); len(got) != 1 || got[0] != "cmd-1" {
			t.Fatalf("cancelled %v: the cancel needs no lock and runs before the mount lock is asked for", got)
		}
		<-sem
		w.pass(w.f)
		if w.marker() != "" {
			t.Fatalf("the next pass left %q", w.marker())
		}
		w.untouched()
	})
}

func TestRescueActionNoLongerRescueFinalizesWhatItWrote(t *testing.T) {
	w := newRescueWorld(t, true)
	w.arm()
	rescueStepHook = func(_ context.Context, s string) error {
		if s == "listed" {
			return errRescueCrash
		}
		return nil
	}
	w.pass(w.f)
	rescueStepHook = nil
	if w.marker() == "" {
		t.Fatal("setup: no marker")
	}
	f2 := w.restarted()
	f2.pool.darkAction = darkActionDetect
	w.pass(f2)
	if w.marker() != "" || len(w.cancelled()) != 0 {
		t.Fatalf("marker %q cancelled %v: with the action back on detect nothing continues", w.marker(), w.cancelled())
	}
}

func TestRescueFinalizerRecordsAnEpisodeOnlyBeyondBegun(t *testing.T) {
	began := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	phases := []rescuePhase{rescuePhaseBegun, rescuePhaseDeregistering, rescuePhaseDeregistered, rescuePhaseStopping,
		rescuePhaseStopped, rescuePhaseDetaching, rescuePhaseDetached}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			bothModes(t, func(t *testing.T, w *rescueWorld) {
				m := rescueMarker{Volume: "vol-1", Epoch: time.Now().Add(-90 * time.Minute).UTC().Truncate(time.Second),
					Phase: phase, Gen: 99, Began: began}
				w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, m.String())
				w.f.resumeRescues(context.Background())
				if w.marker() != "" {
					t.Fatalf("marker %q survived", w.marker())
				}
				want := began.Format(time.RFC3339)
				if phase == rescuePhaseBegun {
					want = "" // its preparation touched the Start and the claim only
				}
				if w.wedgeAt() != want {
					t.Fatalf("af-wedge-at = %q, want %q", w.wedgeAt(), want)
				}
				w.untouched()
			})
		})
	}
}

func TestRescueFinalizerOnlyMovesAfterWedgeAtForward(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for name, tc := range map[string]struct{ existing, began, want time.Time }{
		"later than what is there":   {now.Add(-5 * time.Hour), now.Add(-time.Hour), now.Add(-time.Hour)},
		"earlier than what is there": {now.Add(-time.Hour), now.Add(-5 * time.Hour), now.Add(-time.Hour)},
		"nothing there":              {time.Time{}, now.Add(-time.Hour), now.Add(-time.Hour)},
	} {
		t.Run(name, func(t *testing.T) {
			bothModes(t, func(t *testing.T, w *rescueWorld) {
				if !tc.existing.IsZero() {
					w.h.ec2.setInstanceTag("i-dark", ec2TagWedgeAt, tc.existing.Format(time.RFC3339))
				}
				m := rescueMarker{Volume: "vol-gone", Epoch: now.Add(-2 * time.Hour), Phase: rescuePhaseStopped, Gen: 5, Began: tc.began}
				w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, m.String())
				w.f.resumeRescues(context.Background())
				if w.wedgeAt() != tc.want.Format(time.RFC3339) || w.marker() != "" {
					t.Fatalf("af-wedge-at %q marker %q, want %q and none", w.wedgeAt(), w.marker(), tc.want.Format(time.RFC3339))
				}
			})
		})
	}
}

func TestRescueFinalizerKeepsTheMarkerWhenItCannotRecordTheEpisode(t *testing.T) {
	began := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	m := rescueMarker{Volume: "vol-1", Epoch: time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second), Phase: rescuePhaseStopped, Gen: 5, Began: began}
	for name, inject := range map[string]func(w *rescueWorld){
		"the lease is held elsewhere": func(w *rescueWorld) { localSem(&wedgeLocks, "i-dark") <- struct{}{} },
		"the lease store fails": func(w *rescueWorld) {
			w.st.acquireErr = func(n string) error {
				if strings.HasPrefix(n, homeLockWedge) {
					return errBoom
				}
				return nil
			}
		},
		"the box cannot be read": func(w *rescueWorld) {
			calls := 0
			w.h.ec2.describeInstancesErr = func() error {
				calls++
				if calls > 1 { // the marker scan answers, the read under the lease does not
					return errBoom
				}
				return nil
			}
		},
		"the tag write fails": func(w *rescueWorld) { w.h.ec2.createTagsErr = map[string]error{ec2TagWedgeAt: errBoom} },
	} {
		t.Run(name, func(t *testing.T) {
			w := newRescueWorld(t, true)
			w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, m.String())
			inject(w)
			w.f.resumeRescues(context.Background())
			if w.marker() != m.String() {
				t.Fatalf("marker %q: the episode was not recorded, so the marker must stay", w.marker())
			}
			if w.wedgeAt() != "" {
				t.Fatalf("af-wedge-at = %q", w.wedgeAt())
			}
		})
	}
}

func TestRescueRemovesOnlyItsOwnExactMarker(t *testing.T) {
	w := newRescueWorld(t, false)
	old := rescueFixture()
	old.Volume = "vol-1"
	newer := old
	newer.Gen = old.Gen + 1
	w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, newer.String())
	// A late-resuming old rescue asks for its own value to go; the newer rescue's marker stays.
	if err := w.rt().dropRescueMarker(context.Background(), "i-dark", old.String()); err != nil {
		t.Fatal(err)
	}
	if w.marker() != newer.String() {
		t.Fatalf("marker %q: an old rescue removed a newer one's", w.marker())
	}
	if err := w.rt().dropRescueMarker(context.Background(), "i-dark", newer.String()); err != nil {
		t.Fatal(err)
	}
	if w.marker() != "" {
		t.Fatalf("marker %q: its own value must remove it", w.marker())
	}
}

func TestRescueAnUnparsableMarkerIsLeftAlone(t *testing.T) {
	w := newRescueWorld(t, false)
	w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, "vol-1/garbage")
	w.f.resumeRescues(context.Background())
	if w.marker() != "vol-1/garbage" {
		t.Fatalf("marker %q: a value that cannot be read is never removed, it keeps fencing its home", w.marker())
	}
}

func TestRescueTwoReplicasTakeTheHomeOnce(t *testing.T) {
	w := newRescueWorld(t, true)
	fa, fb := w.f, w.restarted()
	// Both replicas decided on the same count before either began.
	da := darkDecision{slot: "i-dark", vol: w.vol(), launch: w.decision().launch, gd: w.count()}
	db := da
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, f := range []*ecsEC2Factory{fa, fb} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rt := f.runtimeForVolume(w.vol())
			_, _, errs[i] = rt.rescuePrepare(context.Background(), w.h.ssmc, map[int]darkDecision{0: da, 1: db}[i])
		}()
	}
	wg.Wait()
	ok, aside := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, errRescueStepAside):
			aside++
		default:
			t.Fatalf("unexpected %v", err)
		}
	}
	if ok != 1 || aside != 1 {
		t.Fatalf("ok=%d aside=%d: exactly one replica takes the home and the other steps aside", ok, aside)
	}
	if w.count() != da.gd+1 {
		t.Fatalf("count %d, want %d: one bump", w.count(), da.gd+1)
	}
}

func TestPoolStatusShowsTheRescueRung(t *testing.T) {
	now := time.Now()
	launch := now.Add(-2 * time.Hour)
	mk := func(state ec2types.InstanceStateName, m rescueMarker) ec2types.Instance {
		return ec2types.Instance{State: &ec2types.InstanceState{Name: state}, LaunchTime: &launch,
			Tags: []ec2types.Tag{ec2tagKV(ec2TagRescue, m.String())}}
	}
	m := rescueFixture()
	if _, rung := darkView(mk(ec2types.InstanceStateNameRunning, m)); rung != darkRungRescuing {
		t.Fatalf("begun: rung %q", rung)
	}
	m.Phase = rescuePhaseStopping
	if _, rung := darkView(mk(ec2types.InstanceStateNameStopping, m)); rung != darkRungRescuing {
		t.Fatalf("stopping: rung %q", rung)
	}
	m.BlockedSince = now.UTC()
	if _, rung := darkView(mk(ec2types.InstanceStateNameStopping, m)); rung != darkRungBlocked {
		t.Fatalf("blocked: rung %q", rung)
	}
	m.Phase, m.BlockedSince = rescuePhaseDetached, time.Time{}
	since, rung := darkView(mk(ec2types.InstanceStateNameStopped, m))
	if rung != darkRungRescued || since != m.Began.UTC().Format(time.RFC3339) {
		t.Fatalf("detached: since %q rung %q", since, rung)
	}
}

// A rescue that died after writing its marker and before its bump leaves a marker naming g+1.
// A member's Start that reaches g+1 next must not read as that rescue's own count.
func TestRescueUnconfirmedMarkerIsNeverResumedAgainstAnotherStart(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		w.arm()
		gd := w.count()
		rescueStepHook = func(_ context.Context, s string) error {
			if s == "marked" {
				return errRescueCrash
			}
			return nil
		}
		w.pass(w.f)
		rescueStepHook = nil

		// The member starts: the real count increment, and a mount command of its own.
		g, err := w.rt().beginStart(context.Background())
		if err != nil || g != gd+1 {
			t.Fatalf("beginStart = %d, %v; want %d", g, err, gd+1)
		}
		w.h.ssmc.mu.Lock()
		w.h.ssmc.unfinished = append(w.h.ssmc.unfinished,
			ssmtypes.Command{CommandId: aws.String("cmd-new-start"), Comment: aws.String(slotVolumeCommandComment)})
		w.h.ssmc.mu.Unlock()

		w.restarted().resumeRescues(context.Background())
		for _, c := range w.cancelled() {
			t.Fatalf("cancelled %q: the new Start's command is not the rescue's", c)
		}
		if w.marker() != "" || w.count() != gd+1 {
			t.Fatalf("marker %q count %d", w.marker(), w.count())
		}
		w.untouched()
	})
}

// The claim lease lapsing while the marker is being written must stop the rescue before its
// bump: another CP may have counted a Start meanwhile, and the bump would overtake it.
func TestRescueLeaseLapsedAfterTheMarkerDoesNotBump(t *testing.T) {
	bothModes(t, func(t *testing.T, w *rescueWorld) {
		w.arm()
		gd := w.count()
		rescueStepHook = func(ctx context.Context, s string) error {
			if s == "marked" {
				homeGuardOf(ctx).markLost() // the lease ran out during the marker write
				w.startLands()              // and another CP counted a Start
			}
			return nil
		}
		w.pass(w.f)
		rescueStepHook = nil
		if w.count() != gd+1 {
			t.Fatalf("count %d, want %d: the rescue bumped past a Start counted under the lapsed lease", w.count(), gd+1)
		}
		if w.marker() != "" || len(w.cancelled()) != 0 {
			t.Fatalf("marker %q cancelled %v", w.marker(), w.cancelled())
		}
		w.untouched()
	})
}
