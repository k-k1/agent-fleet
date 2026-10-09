package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// Automatic replacement of a slot below the launch template's $Latest (#1934): the same move
// as a reservation, started by the workspace's own Start, never failing it for capacity.

// outdatedSlotHarness is reservedSlotHarness without the reservation: alice's home is on
// i-old (version 3 of 5), nobody asked for it to go.
func outdatedSlotHarness(t *testing.T) (*ec2Harness, *[]SlotAutoReplace) {
	t.Helper()
	autoReplaceBlocked.reset()
	h := reservedSlotHarness(t, false)
	i := h.ec2.instances["i-old"]
	var kept []ec2types.Tag
	for _, tag := range i.Tags {
		if *tag.Key != ec2TagSlotReplace {
			kept = append(kept, tag)
		}
	}
	i.Tags = kept
	var got []SlotAutoReplace
	h.rt.tenantID, h.rt.workspaceID = "T-1", "W-1"
	h.rt.onAutoReplace = func(_ context.Context, ev SlotAutoReplace) { got = append(got, ev) }
	return h, &got
}

func TestECSEC2OutdatedSlotIsReplacedOnTheNextStartAndAudited(t *testing.T) {
	ctx := context.Background()
	h, events := outdatedSlotHarness(t)

	if h.rt.SlotReplacePending(ctx) {
		t.Fatal("SlotReplacePending = true for an automatic replacement; the WS bar pill must stay off")
	}
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	run, detach, term := callIndex(h, "RunInstances"), callIndex(h, "DetachVolume vol-1"), callIndex(h, "TerminateInstances i-old")
	if run < 0 || detach < 0 || term < 0 || !(run < detach && detach < term) {
		t.Fatalf("calls = %v, want launch < detach < terminate", h.ec2.calls)
	}
	if len(*events) != 1 {
		t.Fatalf("audit events = %v, want exactly one", *events)
	}
	ev := (*events)[0]
	if ev.OldSlot != "i-old" || ev.NewSlot != "i-new1" || ev.OldVersion != "3" || ev.Latest != "5" || ev.TenantID != "T-1" {
		t.Fatalf("audit event = %+v", ev)
	}
}

func TestECSEC2OutdatedSlotThatCannotBeReplacedFallsBackAndBacksOff(t *testing.T) {
	ctx := context.Background()
	h, events := outdatedSlotHarness(t)
	h.ec2.runErr["sub-1a"] = errors.New("InsufficientInstanceCapacity: none left")

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start failed for a member over a hardening they did not ask for: %v", err)
	}
	if got := attachedInstance(h.ec2.volumes["vol-1"]); got != "i-old" {
		t.Fatalf("home on %q, want it left on i-old", got)
	}
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "DetachVolume") || strings.HasPrefix(c, "TerminateInstances") {
			t.Fatalf("%s after the replacement could not launch", c)
		}
	}
	if len(*events) != 0 {
		t.Fatalf("audit events = %v, want none for a deferred replacement", *events)
	}
	// The next Start inside the back-off does not launch (and fail) again.
	runs := countCalls(h, "RunInstances")
	forgetStart(h)
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if got := countCalls(h, "RunInstances"); got != runs {
		t.Fatalf("RunInstances calls %d → %d, want the back-off to hold the retry", runs, got)
	}
	// Past the back-off it tries again, and succeeds once capacity is back.
	autoReplaceBlocked.reset()
	delete(h.ec2.runErr, "sub-1a")
	forgetStart(h)
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("third Start: %v", err)
	}
	if callIndex(h, "TerminateInstances i-old") < 0 {
		t.Fatalf("calls = %v, want the old slot replaced once capacity returned", h.ec2.calls)
	}
}

func TestECSEC2ReservationStillNeverFallsBack(t *testing.T) {
	ctx := context.Background()
	h, _ := outdatedSlotHarness(t)
	h.ec2.setInstanceTag("i-old", ec2TagSlotReplace, time.Now().UTC().Format(time.RFC3339))
	h.ec2.runErr["sub-1a"] = errors.New("InsufficientInstanceCapacity: none left")
	if err := h.rt.Start(ctx); err == nil {
		t.Fatal("a reserved outdated slot fell back to the old box")
	}
}

func TestECSEC2AutoReplaceCanBeSwitchedOff(t *testing.T) {
	ctx := context.Background()
	h, _ := outdatedSlotHarness(t)
	h.rt.pool.noAutoReplace = true
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if callIndex(h, "RunInstances") >= 0 || callIndex(h, "TerminateInstances") >= 0 {
		t.Fatalf("calls = %v, want the outdated slot kept with the switch off", h.ec2.calls)
	}
}

func TestECSEC2CurrentOrUnjudgeableSlotIsNotReplaced(t *testing.T) {
	for name, mutate := range map[string]func(h *ec2Harness){
		"on $Latest":          func(h *ec2Harness) { h.ec2.setInstanceTag("i-old", ec2TagLaunchTemplateVersion, "5") },
		"no version stamp":    func(h *ec2Harness) { h.ec2.setInstanceTag("i-old", ec2TagLaunchTemplateVersion, "") },
		"template unreadable": func(h *ec2Harness) { h.ec2.ltLatest = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := outdatedSlotHarness(t)
			mutate(h)
			if err := h.rt.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if callIndex(h, "RunInstances") >= 0 || callIndex(h, "TerminateInstances") >= 0 {
				t.Fatalf("calls = %v, want the slot kept", h.ec2.calls)
			}
		})
	}
}

func TestECSEC2SweeperRetiresAFreeOutdatedSlotButNotAnOccupiedOne(t *testing.T) {
	for _, off := range []bool{false, true} {
		ctx := context.Background()
		h := freeSlotHarness(t, time.Minute)
		h.ec2.ltLatest = 5
		h.rt.pool.slotSleepAfter, h.rt.pool.slotTerminateAfter = 0, 0
		h.rt.pool.noAutoReplace = off
		for _, id := range []string{"i-free", "i-held"} {
			if id == "i-held" {
				h.ec2.addSlot("i-held", "ap-northeast-1a", "m7i.large", false, false)
				h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
				h.ec2.attach("vol-1", "i-held", time.Now())
			}
			h.ec2.setInstanceTag(id, ec2TagLaunchTemplateID, "lt-1")
			h.ec2.setInstanceTag(id, ec2TagLaunchTemplateVersion, "3")
		}
		h.factory().sweepFreeSlots(ctx, []ec2types.Volume{*h.ec2.volumes["vol-1"]})
		got := terminatedInstances(h)
		if off && len(got) != 0 {
			t.Fatalf("switch off: TerminateInstances = %v, want none", got)
		}
		if !off && (len(got) != 1 || got[0] != "i-free") {
			t.Fatalf("TerminateInstances = %v, want only the free outdated slot", got)
		}
	}
}

func TestEnvBoolDefault(t *testing.T) {
	for v, want := range map[string]bool{"": true, "true": true, "1": true, "false": false, "OFF": false, "0": false, "garbage": true} {
		t.Setenv("AF_TEST_BOOL", v)
		if got := envBoolDefault("AF_TEST_BOOL", true); got != want {
			t.Errorf("%q → %v, want %v", v, got, want)
		}
	}
}

func countCalls(h *ec2Harness, prefix string) int {
	n := 0
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// forgetStart puts the workspace back to "stopped" between two Starts: no claim, no service.
func forgetStart(h *ec2Harness) {
	h.ec2.setTag("vol-1", EC2TagClaim, "")
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
}

// Review round 1 of #1946.

// runWrap lets a test act around RunInstances.
type runWrap struct {
	ec2API
	run func(context.Context, *ec2.RunInstancesInput) (*ec2.RunInstancesOutput, error)
}

func (r runWrap) RunInstances(c context.Context, in *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	return r.run(c, in)
}

// A Start that places a home after the sweeper chose a free outdated slot must not end up on an
// instance the sweeper then terminates: the sweeper reserves the slot first, so placement skips it.
func TestECSEC2SweeperReservesAFreeOutdatedSlotBeforeItReadsOccupancy(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.ltLatest = 5
	h.ec2.addSlot("i-old", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.setInstanceTag("i-old", ec2TagLaunchTemplateID, "lt-1")
	h.ec2.setInstanceTag("i-old", ec2TagLaunchTemplateVersion, "3")
	h.ec2.addHomeVolume("vol-1", "M-1", h.rt.base.name, "ap-northeast-1a")
	h.ci.registered["i-old"] = true
	h.ec2.afterDescribeVolumes = func() {
		h.ec2.afterDescribeVolumes = nil
		// A Start arriving now: its candidate list is read after the sweeper's tag write.
		slots, err := h.rt.freeSlots(ctx, "ap-northeast-1a")
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range slots {
			if s.id == "i-old" {
				h.ec2.attach("vol-1", "i-old", time.Now())
			}
		}
	}
	h.factory().sweepFreeSlots(ctx, nil)
	if attachedInstance(h.ec2.volumes["vol-1"]) == "i-old" && len(terminatedInstances(h)) > 0 {
		t.Fatalf("a home was placed on the outdated slot and the slot terminated: %v", h.ec2.calls)
	}
	if ec2TagValue(h.ec2.instances["i-old"].Tags, ec2TagSlotReplace) == "" {
		t.Fatal("the sweeper did not reserve the outdated slot before acting on it")
	}
}

func TestECSEC2PendingReplacementStaysProtectedWhenTheTemplateCannotBeRead(t *testing.T) {
	h, _ := outdatedSlotHarness(t)
	h.ec2.addSlot("i-new", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.setInstanceTag("i-new", ec2TagReplacesHome, "vol-1")
	h.ec2.ltLatest = 0
	out, err := h.ec2.DescribeInstances(context.Background(), &ec2.DescribeInstancesInput{InstanceIds: []string{"i-new"}})
	if err != nil {
		t.Fatal(err)
	}
	other := *h.rt
	b := *h.rt.base
	b.name = "af-ws-other"
	other.base = &b
	if !other.pendingReplacementsForOthers(context.Background(), out)["i-new"] {
		t.Fatal("an unreadable template released the pending replacement to another workspace")
	}
}

func TestECSEC2ReservationDuringAFailingAutomaticLaunchDoesNotFallBack(t *testing.T) {
	h, _ := outdatedSlotHarness(t)
	h.rt.ec2 = runWrap{ec2API: h.ec2, run: func(context.Context, *ec2.RunInstancesInput) (*ec2.RunInstancesOutput, error) {
		h.ec2.setInstanceTag("i-old", ec2TagSlotReplace, time.Now().UTC().Format(time.RFC3339))
		return nil, errors.New("InsufficientInstanceCapacity")
	}}
	if err := h.rt.Start(context.Background()); err == nil {
		t.Fatalf("Start fell back to a slot reserved meanwhile: %v", h.ec2.calls)
	}
	if callIndex(h, "StartInstances i-old") >= 0 {
		t.Fatalf("the reserved slot was started: %v", h.ec2.calls)
	}
}

func TestECSEC2LostLaunchResponseIsAdoptedNotOrphaned(t *testing.T) {
	ctx := context.Background()
	h, _ := outdatedSlotHarness(t)
	h.rt.ec2 = runWrap{ec2API: h.ec2, run: func(c context.Context, in *ec2.RunInstancesInput) (*ec2.RunInstancesOutput, error) {
		if _, err := h.ec2.RunInstances(c, in); err != nil {
			return nil, err
		}
		return nil, errors.New("response lost after acceptance")
	}}
	if err := h.rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim); got != "i-new1" {
		t.Fatalf("claim = %q, want the launched-but-unanswered i-new1 adopted (calls %v)", got, h.ec2.calls)
	}
}
