package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

func slotMarker(h *ec2Harness) string {
	return ec2TagValue(h.ec2.instances["i-wedge"].Tags, ec2TagRescue)
}

func slotEpoch(h *ec2Harness) time.Time {
	return h.ec2.instances["i-wedge"].LaunchTime.UTC().Truncate(time.Second)
}

func stopsOnBox(h *ec2Harness) int { return len(h.ec2.stopForce) }

// The operator's stop runs under the rescue's token: the marker is on the box before the
// first destructive call, the Start count is bumped once, and the episode is recorded and the
// marker gone when the stop has been sent.
func TestForceStopTakesTheRescueTokenAndReleasesItAfterTheStop(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ec2.giveTaskENI("i-wedge")
	before, _ := h.rt.startGenNow(ctx)
	var atDereg string
	var countAtDereg int64
	hookAt("deregistering", func() {
		atDereg = slotMarker(h)
		countAtDereg, _ = h.rt.startGenNow(ctx)
	})
	defer func() { rescueStepHook = nil }()
	res, err := h.factory().ForceStopSlot(ctx, "i-wedge", false)
	if err != nil {
		t.Fatalf("ForceStopSlot: %v", err)
	}
	m, ok := parseRescueMarker(atDereg)
	if !ok || m.Phase != rescuePhaseDeregistering || m.Volume != "vol-1" || m.Gen != countAtDereg {
		t.Fatalf("marker at the deregistration = %q (count %d)", atDereg, countAtDereg)
	}
	if after, _ := h.rt.startGenNow(ctx); after != countAtDereg || after <= before {
		t.Fatalf("count before %d, at the deregistration %d, after %d", before, countAtDereg, after)
	}
	if slotMarker(h) != "" || latestEpisode(h.ec2.instances["i-wedge"].Tags) == "" {
		t.Fatalf("marker %q wedge-at %q: the episode must be recorded and the token released",
			slotMarker(h), latestEpisode(h.ec2.instances["i-wedge"].Tags))
	}
	if !res.Deregistered || stopsOnBox(h) != 1 || res.Predicate != "dark" {
		t.Fatalf("res %+v stops %v", res, h.ec2.stopForce)
	}
	if attachedInstance(h.ec2.volumes["vol-1"]) != "i-wedge" {
		t.Fatal("the home must stay on the slot: the operator's stop does not detach")
	}
}

// The override replaces the health gate and nothing else: the ENI gate still holds.
func TestForceStopOverrideDoesNotSkipTheENIGate(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ci.registered["i-wedge"] = true
	h.ssmc.online["i-wedge"] = true
	h.ec2.giveTaskENI("i-wedge")
	h.ci.stickyENI = true
	res, err := h.factory().ForceStopSlot(ctx, "i-wedge", true)
	if !errors.Is(err, ErrSlotTaskENIHeld) || stopsOnBox(h) != 0 {
		t.Fatalf("override with a task ENI that stays: res %+v err %v stops %v", res, err, h.ec2.stopForce)
	}
	if !res.OverrodeHealthy || !res.Deregistered {
		t.Fatalf("res %+v", res)
	}
	// A failed attempt leaves no marker for the sweeper's resume scan to carry to a detach.
	if m := slotMarker(h); m != "" {
		t.Fatalf("a refused force-stop left the marker %q", m)
	}
	h.ec2.dropTaskENIs("i-wedge")
	if _, err := h.factory().ForceStopSlot(ctx, "i-wedge", true); err != nil || stopsOnBox(h) != 1 {
		t.Fatalf("retry: %v stops %v", err, h.ec2.stopForce)
	}
}

// The gate is the shared predicates: a connected slot whose task is stuck STOPPING is wedged
// and needs no override; one inside the boot grace is neither, and does.
func TestForceStopHealthGateUsesTheSharedPredicates(t *testing.T) {
	ctx := context.Background()
	stuck := func() ecstypes.Task {
		old := runningTask("af-ws-acme-alice", "i-wedge")
		old.TaskArn = aws.String("arn:task/old")
		old.LastStatus = aws.String("STOPPING")
		old.StoppingAt = aws.Time(time.Now().Add(-40 * time.Minute))
		return old
	}
	connected := func(h *ec2Harness) {
		h.ci.registered["i-wedge"] = true
		h.ssmc.online["i-wedge"] = true
		h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
		h.ci.stopping = []ecstypes.Task{stuck()}
	}
	t.Run("wedged", func(t *testing.T) {
		h := wedgedSlot(t)
		connected(h)
		res, err := h.factory().ForceStopSlot(ctx, "i-wedge", false)
		if err != nil || res.Predicate != "wedged" || res.OverrodeHealthy || stopsOnBox(h) != 1 {
			t.Fatalf("res %+v err %v", res, err)
		}
	})
	t.Run("inside the boot grace", func(t *testing.T) {
		h := wedgedSlot(t)
		h.ec2.instances["i-wedge"].LaunchTime = aws.Time(time.Now().Add(-time.Minute))
		f := h.factory()
		f.pool.darkBootGrace = 5 * time.Minute
		if _, err := f.ForceStopSlot(ctx, "i-wedge", false); !errors.Is(err, ErrSlotHealthy) || stopsOnBox(h) != 0 {
			t.Fatalf("err %v stops %v", err, h.ec2.stopForce)
		}
		if res, err := f.ForceStopSlot(ctx, "i-wedge", true); err != nil || !res.OverrodeHealthy {
			t.Fatalf("override: %+v %v", res, err)
		}
	})
	t.Run("an Agent that answers is not dark", func(t *testing.T) {
		h := wedgedSlot(t)
		h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}
		h.ci.running = []ecstypes.Task{runningTask("af-ws-acme-alice", "i-wedge")}
		f := h.factory()
		f.agentHTTP = &fakeAgentDoer{status: 200}
		if _, err := f.ForceStopSlot(ctx, "i-wedge", false); !errors.Is(err, ErrSlotHealthy) || stopsOnBox(h) != 0 {
			t.Fatalf("err %v stops %v", err, h.ec2.stopForce)
		}
	})
}

// One marker per home: a rescue that is running (not blocked, not old) is not raced.
func TestForceStopRefusesWhileARescueIsRunning(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	g, _ := h.rt.startGenNow(ctx)
	m := rescueMarker{Volume: "vol-1", Epoch: slotEpoch(h), Phase: rescuePhaseDeregistering, Gen: g, Began: time.Now().UTC().Truncate(time.Second)}
	h.ec2.setInstanceTag("i-wedge", ec2TagRescue, m.String())
	_, err := h.factory().ForceStopSlot(ctx, "i-wedge", true)
	if !errors.Is(err, ErrSlotRescueActive) {
		t.Fatalf("err = %v, want ErrSlotRescueActive", err)
	}
	if now, _ := h.rt.startGenNow(ctx); now != g || stopsOnBox(h) != 0 || slotMarker(h) != m.String() {
		t.Fatalf("a running rescue was disturbed: count %d (was %d) stops %v marker %q", now, g, h.ec2.stopForce, slotMarker(h))
	}
	// A marker this build cannot read still fences.
	h.ec2.setInstanceTag("i-wedge", ec2TagRescue, "vol-1/garbage")
	if _, err := h.factory().ForceStopSlot(ctx, "i-wedge", true); !errors.Is(err, ErrSlotRescueActive) {
		t.Fatalf("unreadable marker: %v", err)
	}
}

// The operator's way out of a stuck rescue: a blocked one (or one that has outlived
// forceStopAdoptAfter) is continued under its own token, without bumping the count again.
func TestForceStopTakesOverABlockedRescue(t *testing.T) {
	for name, age := range map[string]struct {
		began   time.Duration
		blocked bool
	}{"blocked": {time.Minute, true}, "old": {time.Hour, false}} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			h := wedgedSlot(t)
			h.ec2.giveTaskENI("i-wedge")
			g, _ := h.rt.startGenNow(ctx)
			m := rescueMarker{Volume: "vol-1", Epoch: slotEpoch(h), Phase: rescuePhaseDeregistering, Gen: g,
				Began: time.Now().Add(-age.began).UTC().Truncate(time.Second)}
			if age.blocked {
				m.BlockedSince = time.Now().UTC().Truncate(time.Second)
			}
			h.ec2.setInstanceTag("i-wedge", ec2TagRescue, m.String())
			res, err := h.factory().ForceStopSlot(ctx, "i-wedge", true)
			if err != nil {
				t.Fatalf("ForceStopSlot: %v", err)
			}
			if now, _ := h.rt.startGenNow(ctx); now != g {
				t.Fatalf("count %d, want the rescue's %d: taking over must not overtake it", now, g)
			}
			if stopsOnBox(h) != 1 || slotMarker(h) != "" || !res.Deregistered {
				t.Fatalf("res %+v stops %v marker %q", res, h.ec2.stopForce, slotMarker(h))
			}
		})
	}
}

// A stale `preparing` marker (the count was never confirmed) is removed and the action goes on.
func TestForceStopRemovesAStalePreparingMarker(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	g, _ := h.rt.startGenNow(ctx)
	m := rescueMarker{Volume: "vol-1", Epoch: slotEpoch(h), Phase: rescuePhasePreparing, Gen: g + 1, Began: time.Now().Add(-time.Hour).UTC().Truncate(time.Second)}
	h.ec2.setInstanceTag("i-wedge", ec2TagRescue, m.String())
	if _, err := h.factory().ForceStopSlot(ctx, "i-wedge", true); err != nil || stopsOnBox(h) != 1 || slotMarker(h) != "" {
		t.Fatalf("err %v stops %v marker %q", err, h.ec2.stopForce, slotMarker(h))
	}
}

// A slot with no home has nothing to own: the direct path, as before.
func TestForceStopWithoutAHomeKeepsTheDirectPath(t *testing.T) {
	h := wedgedSlot(t)
	h.ec2.detach("vol-1")
	h.ec2.giveTaskENI("i-wedge")
	res, err := h.factory().ForceStopSlot(context.Background(), "i-wedge", false)
	if err != nil || !res.Deregistered || stopsOnBox(h) != 1 || res.Predicate != "dark" {
		t.Fatalf("res %+v err %v stops %v", res, err, h.ec2.stopForce)
	}
	if strings.Contains(strings.Join(h.ec2.calls, ","), "af-rescue") {
		t.Fatal("a marker for a slot with no home")
	}
}

// A Start that lands after the token is taken overtakes the stop at the guarded step.
func TestForceStopIsOvertakenByAStartAfterTheToken(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	hookAt("prepared", func() {
		if err := h.rt.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	})
	defer func() { rescueStepHook = nil }()
	_, err := h.factory().ForceStopSlot(ctx, "i-wedge", false)
	if !errors.Is(err, ErrSlotStartedAgain) || stopsOnBox(h) != 0 {
		t.Fatalf("err %v stops %v", err, h.ec2.stopForce)
	}
}

// The way out of a wedged release that is stuck unmounting: the operator takes its marker over
// in phase `detaching`, and the ENI gate still runs before the stop.
func TestForceStopTakesOverABlockedWedgedRelease(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ci.registered["i-wedge"] = true // a wedged slot's ECS agent answers
	h.ssmc.online["i-wedge"] = true
	h.ec2.giveTaskENI("i-wedge")
	g, _ := h.rt.startGenNow(ctx)
	m := rescueMarker{Volume: "vol-1", Epoch: slotEpoch(h), Phase: rescuePhaseDetaching, Gen: g, Wedged: true,
		Began: time.Now().Add(-time.Minute).UTC().Truncate(time.Second), BlockedSince: time.Now().UTC().Truncate(time.Second)}
	h.ec2.setInstanceTag("i-wedge", ec2TagRescue, m.String())
	res, err := h.factory().ForceStopSlot(ctx, "i-wedge", true)
	if err != nil {
		t.Fatalf("ForceStopSlot: %v", err)
	}
	if now, _ := h.rt.startGenNow(ctx); now != g {
		t.Fatalf("count %d, want the release's %d", now, g)
	}
	if !res.Deregistered || len(h.ci.deregistered) != 1 || stopsOnBox(h) != 1 || slotMarker(h) != "" {
		t.Fatalf("res %+v deregistered %v stops %v marker %q: the ENI gate must run before the stop", res, h.ci.deregistered, h.ec2.stopForce, slotMarker(h))
	}
}

func resumer(h *ec2Harness, f0 *ecsEC2Factory) *ecsEC2Factory {
	f := h.factory()
	f.pool.darkAction = darkActionRescue
	f.pool.wedgedAction = wedgedActionRelease
	f.leases = f0.leases
	return f
}

// Another CP's resume scan must neither continue nor finalize an operator's marker while its
// handler works: here it runs between the token and the first destructive call.
func TestForceStopMarkerIsNotCarriedOnByAnotherCPsResume(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ec2.giveTaskENI("i-wedge")
	f := h.factory()
	var during string
	hookAt("prepared", func() {
		resumer(h, f).resumeRescues(ctx)
		during = slotMarker(h)
	})
	defer func() { rescueStepHook = nil }()
	if _, err := f.ForceStopSlot(ctx, "i-wedge", false); err != nil {
		t.Fatalf("ForceStopSlot: %v", err)
	}
	m, ok := parseRescueMarker(during)
	if !ok || !m.Operator {
		t.Fatalf("the marker after another CP's resume = %q: it must still be there, as the operator's", during)
	}
	if h.ec2.countCalls("DetachVolume") != 0 || stopsOnBox(h) != 1 {
		t.Fatalf("calls %v stops %v: the resume must not have acted", h.ec2.calls, h.ec2.stopForce)
	}
}

// A handler that dies after taking the token leaves a marker no resume scan carries to a
// detach: young, it is left alone; aged, it is only finalized.
func TestForceStopCrashedHandlerLeavesNothingToDetach(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	f := h.factory()
	rescueStepHook = func(_ context.Context, s string) error {
		if s == "prepared" {
			return errRescueCrash
		}
		return nil
	}
	defer func() { rescueStepHook = nil }()
	if _, err := f.ForceStopSlot(ctx, "i-wedge", false); !errors.Is(err, errRescueCrash) {
		t.Fatalf("err = %v", err)
	}
	rescueStepHook = nil
	if m, ok := parseRescueMarker(slotMarker(h)); !ok || !m.Operator {
		t.Fatalf("marker %q", slotMarker(h))
	}
	resumer(h, f).resumeRescues(ctx)
	if slotMarker(h) == "" {
		t.Fatal("a young operator marker was finalized under its handler")
	}
	m, _ := parseRescueMarker(slotMarker(h))
	m.Began = time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	h.ec2.setInstanceTag("i-wedge", ec2TagRescue, m.String())
	resumer(h, f).resumeRescues(ctx)
	if slotMarker(h) != "" {
		t.Fatalf("an aged operator marker was not finalized: %q", slotMarker(h))
	}
	if h.ec2.countCalls("DetachVolume") != 0 || stopsOnBox(h) != 0 || len(h.ci.deregistered) != 0 {
		t.Fatalf("a resume acted for a crashed operator: %v", h.ec2.calls)
	}
}

// Taking over fixes the command set under the claim lock with the token read fresh: a count
// that has already moved past the marker, or moves while the commands are listed, means a
// newer Start's commands are not cancelled.
func TestForceStopTakeoverCancelsNothingOnceAStartOvertook(t *testing.T) {
	adoptable := func(h *ec2Harness, gen int64) {
		h.ssmc.unfinished = []ssmtypes.Command{{CommandId: aws.String("cmd-start"), Comment: aws.String(slotVolumeCommandComment)}}
		m := rescueMarker{Volume: "vol-1", Epoch: slotEpoch(h), Phase: rescuePhaseDeregistering, Gen: gen,
			Began: time.Now().Add(-time.Hour).UTC().Truncate(time.Second)}
		h.ec2.setInstanceTag("i-wedge", ec2TagRescue, m.String())
	}
	t.Run("count already past the marker", func(t *testing.T) {
		h := wedgedSlot(t)
		g, _ := h.rt.startGenNow(context.Background())
		h.rt.generation().Add(1) // a Start was counted after the marker's token
		adoptable(h, g)
		_, err := h.factory().ForceStopSlot(context.Background(), "i-wedge", true)
		if !errors.Is(err, ErrSlotStartedAgain) || len(h.ssmc.cancelled) != 0 || stopsOnBox(h) != 0 {
			t.Fatalf("err %v cancelled %v stops %v", err, h.ssmc.cancelled, h.ec2.stopForce)
		}
	})
	t.Run("a Start counted while the commands are listed", func(t *testing.T) {
		h := wedgedSlot(t)
		g, _ := h.rt.startGenNow(context.Background())
		adoptable(h, g)
		h.ssmc.onList = func() { h.ssmc.onList = nil; h.rt.generation().Add(1) }
		_, err := h.factory().ForceStopSlot(context.Background(), "i-wedge", true)
		if !errors.Is(err, ErrSlotStartedAgain) || len(h.ssmc.cancelled) != 0 || stopsOnBox(h) != 0 {
			t.Fatalf("err %v cancelled %v stops %v", err, h.ssmc.cancelled, h.ec2.stopForce)
		}
	})
}

// A wedged release that died after its token but before the scale-down leaves the service at
// 1: the takeover ends the Start as a Stop does (desired 0, claim dropped, count untouched).
func TestForceStopTakeoverPutsTheServiceAtZero(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}
	h.ci.registered["i-wedge"] = true
	h.ssmc.online["i-wedge"] = true
	h.ec2.setTag("vol-1", EC2TagClaim, "i-wedge")
	h.ec2.setTag("vol-1", ec2TagClaimAt, time.Now().UTC().Format(time.RFC3339))
	g, _ := h.rt.startGenNow(ctx)
	m := rescueMarker{Volume: "vol-1", Epoch: slotEpoch(h), Phase: rescuePhaseBegun, Gen: g, Wedged: true,
		Began: time.Now().Add(-time.Hour).UTC().Truncate(time.Second)}
	h.ec2.setInstanceTag("i-wedge", ec2TagRescue, m.String())
	if _, err := h.factory().ForceStopSlot(ctx, "i-wedge", true); err != nil {
		t.Fatalf("ForceStopSlot: %v", err)
	}
	if got := h.ecs.services["af-ws-acme-alice"].DesiredCount; got != 0 {
		t.Fatalf("service desired %d, want 0", got)
	}
	if now, _ := h.rt.startGenNow(ctx); now != g || stopsOnBox(h) != 1 {
		t.Fatalf("count %d (was %d) stops %v", now, g, h.ec2.stopForce)
	}
}

func TestOperatorMarkerRoundTripAndOldReaders(t *testing.T) {
	m := rescueFixture()
	m.Operator = true
	got, ok := parseRescueMarker(m.String())
	if !ok || !got.Operator || got.Wedged || !strings.HasSuffix(m.String(), "/operator") {
		t.Fatalf("round trip: %q -> %+v", m.String(), got)
	}
}

// An aged marker taken over is the operator's new attempt, young by its own clock: another
// CP's resume scan, running while the handler works, must leave it alone.
func TestForceStopTakeoverOfAnAgedMarkerIsNotFinalizedByAResume(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	g, _ := h.rt.startGenNow(ctx)
	m := rescueMarker{Volume: "vol-1", Epoch: slotEpoch(h), Phase: rescuePhaseBegun, Gen: g,
		Began: time.Now().Add(-time.Hour).UTC().Truncate(time.Second)}
	h.ec2.setInstanceTag("i-wedge", ec2TagRescue, m.String())
	f := h.factory()
	var during string
	hookAt("prepared", func() {
		resumer(h, f).resumeRescues(ctx)
		during = slotMarker(h)
	})
	defer func() { rescueStepHook = nil }()
	if _, err := f.ForceStopSlot(ctx, "i-wedge", true); err != nil {
		t.Fatalf("ForceStopSlot: %v", err)
	}
	cur, ok := parseRescueMarker(during)
	if !ok || !cur.Operator || time.Since(cur.Began) > time.Minute || !cur.BlockedSince.IsZero() {
		t.Fatalf("marker after the resume = %q: it must still be the operator's, begun now", during)
	}
	if stopsOnBox(h) != 1 || h.ec2.countCalls("DetachVolume") != 0 {
		t.Fatalf("stops %v calls %v", h.ec2.stopForce, h.ec2.calls)
	}
}

// Two operator requests do not share one token: a second press while the first works (even one
// that took over a blocked rescue) is refused, and nothing is cancelled or stopped twice.
func TestForceStopSecondOperatorWhileTheFirstWorksIsRefused(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ssmc.unfinished = []ssmtypes.Command{{CommandId: aws.String("cmd-1"), Comment: aws.String(slotVolumeCommandComment)}}
	g, _ := h.rt.startGenNow(ctx)
	m := rescueMarker{Volume: "vol-1", Epoch: slotEpoch(h), Phase: rescuePhaseDeregistering, Gen: g,
		Began: time.Now().Add(-time.Minute).UTC().Truncate(time.Second), BlockedSince: time.Now().UTC().Truncate(time.Second)}
	h.ec2.setInstanceTag("i-wedge", ec2TagRescue, m.String())
	f := h.factory()
	var second error
	hookAt("prepared", func() {
		_, second = f.ForceStopSlot(ctx, "i-wedge", true)
	})
	defer func() { rescueStepHook = nil }()
	if _, err := f.ForceStopSlot(ctx, "i-wedge", true); err != nil {
		t.Fatalf("first: %v", err)
	}
	if !errors.Is(second, ErrSlotRescueActive) {
		t.Fatalf("second press = %v, want ErrSlotRescueActive", second)
	}
	if stopsOnBox(h) != 1 || len(h.ssmc.cancelled) != 1 {
		t.Fatalf("stops %v cancelled %v: one stop and one cancel", h.ec2.stopForce, h.ssmc.cancelled)
	}
}

// A young operator marker is never taken over, even one that carries blocked_since.
func TestForceStopYoungBlockedOperatorMarkerIsRefused(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	g, _ := h.rt.startGenNow(ctx)
	m := rescueMarker{Volume: "vol-1", Epoch: slotEpoch(h), Phase: rescuePhaseDeregistering, Gen: g, Operator: true,
		Began: time.Now().UTC().Truncate(time.Second), BlockedSince: time.Now().UTC().Truncate(time.Second)}
	h.ec2.setInstanceTag("i-wedge", ec2TagRescue, m.String())
	if _, err := h.factory().ForceStopSlot(ctx, "i-wedge", true); !errors.Is(err, ErrSlotRescueActive) || stopsOnBox(h) != 0 {
		t.Fatalf("err %v stops %v", err, h.ec2.stopForce)
	}
}

// The request ends right after the deregistration (a closed Console page). The work belongs to
// the CP from the moment the token is taken, so the stop still goes out, the token is released
// and the episode recorded; the caller only learns it is in progress, and the final answer
// reaches the late callback that writes the audit outcome.
func TestForceStopFinishesWhenTheRequestEndsAfterTheDeregistration(t *testing.T) {
	h := wedgedSlot(t)
	h.ec2.giveTaskENI("i-wedge")
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	type late struct {
		res SlotForceStop
		err error
	}
	lateCh := make(chan late, 1)
	ctx := WithForceStopLate(reqCtx, func(r SlotForceStop, err error) { lateCh <- late{r, err} })
	// Hold the work at the hook until the caller has stopped waiting, so the order is certain.
	released := make(chan struct{})
	hookAt("deregistered", func() {
		cancelReq()
		<-released
	})
	defer func() { rescueStepHook = nil }()

	res, err := h.factory().ForceStopSlot(ctx, "i-wedge", false)
	if !errors.Is(err, ErrSlotForceStopInProgress) {
		t.Fatalf("ForceStopSlot after the request ended = %+v, %v; want ErrSlotForceStopInProgress", res, err)
	}
	close(released)
	var got late
	select {
	case got = <-lateCh:
	case <-time.After(10 * time.Second):
		t.Fatal("the force-stop never finished after its request ended")
	}
	if got.err != nil || !got.res.Deregistered || stopsOnBox(h) != 1 {
		t.Fatalf("late answer %+v, %v; stops %v", got.res, got.err, h.ec2.stopForce)
	}
	if slotMarker(h) != "" || latestEpisode(h.ec2.instances["i-wedge"].Tags) == "" {
		t.Fatalf("marker %q episode %q: the token must be released and the episode recorded",
			slotMarker(h), latestEpisode(h.ec2.instances["i-wedge"].Tags))
	}
}

// A request that ends before the token is taken refuses cleanly and touches nothing: no
// deregistration, no stop, and the caller gets the failure, not "in progress".
func TestForceStopRequestEndedBeforeTheTokenTouchesNothing(t *testing.T) {
	h := wedgedSlot(t)
	h.ec2.giveTaskENI("i-wedge")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rescueStepHook = func(c context.Context, step string) error {
		if step == "counted" { // inside the token's preparation, before it is complete
			cancel()
			return c.Err()
		}
		return nil
	}
	defer func() { rescueStepHook = nil }()

	res, err := h.factory().ForceStopSlot(ctx, "i-wedge", false)
	if err == nil || errors.Is(err, ErrSlotForceStopInProgress) {
		t.Fatalf("ForceStopSlot = %+v, %v; want a refusal", res, err)
	}
	if stopsOnBox(h) != 0 || res.Deregistered {
		t.Fatalf("a request that ended before the token touched the box: res %+v stops %v", res, h.ec2.stopForce)
	}
}
