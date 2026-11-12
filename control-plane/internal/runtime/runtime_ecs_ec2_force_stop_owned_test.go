package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
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
	if slotMarker(h) != "" || ec2TagValue(h.ec2.instances["i-wedge"].Tags, ec2TagWedgeAt) == "" {
		t.Fatalf("marker %q wedge-at %q: the episode must be recorded and the token released",
			slotMarker(h), ec2TagValue(h.ec2.instances["i-wedge"].Tags, ec2TagWedgeAt))
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
