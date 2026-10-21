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
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// wedgedSlot is the slot of #2449: running, its home attached, ECS and SSM both dark.
func wedgedSlot(t *testing.T) *ec2Harness {
	t.Helper()
	h := newEC2Harness(t)
	h.ec2.addSlot("i-wedge", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.attach("vol-1", "i-wedge", time.Now())
	h.ci.registered["i-wedge"] = false
	h.ssmc.online = map[string]bool{"i-wedge": false}
	return h
}

func TestForceStopSlotRefusesAHealthySlotUnlessOverridden(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ci.registered["i-wedge"] = true
	h.ssmc.online["i-wedge"] = true
	f := h.factory()

	res, err := f.ForceStopSlot(ctx, "i-wedge", false)
	if !errors.Is(err, ErrSlotHealthy) {
		t.Fatalf("ForceStopSlot on a healthy slot = %+v, %v; want ErrSlotHealthy", res, err)
	}
	if len(h.ec2.stopForce) != 0 || ec2TagValue(h.ec2.instances["i-wedge"].Tags, EC2TagRole) != ec2RoleSlot {
		t.Fatalf("a refused force-stop touched the slot: stops=%v calls=%v", h.ec2.stopForce, h.ec2.calls)
	}
	// Half a signal is still healthy: ECS answers, SSM does not.
	h.ssmc.online["i-wedge"] = false
	if _, err := f.ForceStopSlot(ctx, "i-wedge", false); err != nil {
		t.Fatalf("ECS connected but SSM dark is wedged enough: %v", err)
	}
}

func TestForceStopSlotOverrideStopsAHealthySlot(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ci.registered["i-wedge"] = true
	h.ssmc.online["i-wedge"] = true
	res, err := h.factory().ForceStopSlot(ctx, "i-wedge", true)
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if !res.OverrodeHealthy || !res.Quarantined || len(h.ec2.stopForce) != 1 {
		t.Fatalf("result %+v stops %v", res, h.ec2.stopForce)
	}
}

func TestForceStopSlotUnreadableHealthRefusesWithoutOverride(t *testing.T) {
	h := wedgedSlot(t)
	h.ssmc.infoErr = errors.New("throttled")
	if _, err := h.factory().ForceStopSlot(context.Background(), "i-wedge", false); err == nil || errors.Is(err, ErrSlotHealthy) {
		t.Fatalf("an unreadable SSM state must refuse with its own error, got %v", err)
	}
	if len(h.ec2.stopForce) != 0 {
		t.Fatal("stopped a slot whose health could not be read")
	}
}

func TestForceStopSlotWedgedFlow(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ssmc.unfinished = []ssmtypes.Command{
		{CommandId: aws.String("cmd-umount"), Comment: aws.String(slotVolumeCommandComment)},
		{CommandId: aws.String("cmd-foreign"), Comment: aws.String("somebody else")},
	}
	h.ci.stopping = []ecstypes.Task{{
		TaskArn: aws.String("arn:task/1"), ContainerInstanceArn: aws.String("arn:ci/i-wedge"),
		LastStatus: aws.String("STOPPING"), StoppingAt: aws.Time(time.Now().Add(-time.Hour)),
	}}
	// The member's Start is converging in the background on this slot.
	gen, err := h.rt.startGenNow(ctx)
	if err != nil {
		t.Fatal(err)
	}

	res, err := h.factory().ForceStopSlot(ctx, "i-wedge", false)
	if err != nil {
		t.Fatalf("ForceStopSlot: %v", err)
	}
	if res.Workspace != "af-ws-acme-alice" || res.CancelledCommands != 1 || res.CancelFailed != 0 {
		t.Fatalf("result %+v", res)
	}
	if got := h.ssmc.cancelled; len(got) != 1 || got[0] != "cmd-umount" {
		t.Fatalf("cancelled %v, want only the CP's own command", got)
	}
	inst := h.ec2.instances["i-wedge"]
	if ec2TagValue(inst.Tags, EC2TagRole) != ec2RoleQuarantined || !res.Quarantined ||
		!strings.Contains(ec2TagValue(inst.Tags, ec2TagQuarantineReason), "force-stopped by an operator") {
		t.Fatalf("slot not quarantined with a reason: %+v", inst.Tags)
	}
	if len(h.ec2.stopForce) != 1 || h.ec2.stopForce[0] || res.Forced || res.ForceAfterSec == 0 {
		t.Fatalf("first stop must be graceful with an escalation pending: %v %+v", h.ec2.stopForce, res)
	}
	if moved, err := h.rt.startedSince(ctx, gen); err != nil || !moved {
		t.Fatalf("the occupant's Start was not overtaken (moved=%v err=%v)", moved, err)
	}
	if res.StoppingTask != "STOPPING" {
		t.Fatalf("stopping task = %q, want STOPPING", res.StoppingTask)
	}
	// The order that matters: quarantine and cancel before the stop is sent.
	if len(h.ec2.calls) == 0 || !strings.HasPrefix(h.ec2.calls[len(h.ec2.calls)-1], "StopInstances") {
		t.Fatalf("calls %v", h.ec2.calls)
	}
	// The home is still on the box; the member's next Start must not wake it.
	matches, _, _, err := h.rt.slotTypeMatches(ctx, "i-wedge")
	if err != nil || matches {
		t.Fatalf("a quarantined slot must not match its workspace's Start (matches=%v err=%v)", matches, err)
	}
}

func TestForceStopSlotEscalatesToForceWhenTheGracefulStopHangs(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ec2.stopHold = true
	f := h.factory()
	if _, err := f.ForceStopSlot(ctx, "i-wedge", false); err != nil {
		t.Fatal(err)
	}
	if len(h.deferred) != 1 {
		t.Fatalf("no escalation scheduled")
	}
	h.runDeferred(ctx)
	if got := h.ec2.stopForce; len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("stops %v, want graceful then forced", got)
	}
	// Pressing again while it is stopping forces at once and schedules nothing.
	h.ec2.instances["i-wedge"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopping}
	res, err := f.ForceStopSlot(ctx, "i-wedge", false)
	if err != nil || !res.Forced || len(h.deferred) != 0 {
		t.Fatalf("second press = %+v, %v (deferred %d)", res, err, len(h.deferred))
	}
}

func TestForceStopSlotRefusals(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ec2.addSlot("i-off", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.addSlot("i-theirs", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.setInstanceTag("i-theirs", EC2TagPool, "another-deployment")
	h.ec2.addSlot("i-engine", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.setInstanceTag("i-engine", EC2TagRole, "engine")
	f := h.factory()
	for id, want := range map[string]error{
		"i-off": ErrSlotNotRunning, "i-theirs": ErrSlotNotFound, "i-engine": ErrSlotNotFound, "i-nope": ErrSlotNotFound,
	} {
		if _, err := f.ForceStopSlot(ctx, id, true); !errors.Is(err, want) {
			t.Errorf("ForceStopSlot(%s) = %v, want %v", id, err, want)
		}
	}
	if len(h.ec2.stopForce) != 0 {
		t.Fatalf("a refused request stopped something: %v", h.ec2.stopForce)
	}
}

func TestPoolStatusReportsATaskStoppingOnADarkSlot(t *testing.T) {
	h := wedgedSlot(t)
	h.ec2.addSlot("i-ok", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-ok"] = true
	h.ci.stopping = []ecstypes.Task{{
		TaskArn: aws.String("arn:task/1"), ContainerInstanceArn: aws.String("arn:ci/i-wedge"),
		LastStatus: aws.String("STOPPING"), StoppingAt: aws.Time(time.Now().Add(-30 * time.Minute)),
	}}
	st, err := h.factory().PoolStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range st.Slots {
		switch s.InstanceID {
		case "i-wedge":
			if s.StoppingTask != "STOPPING" || s.StoppingTaskMinutes < 29 {
				t.Fatalf("i-wedge = %+v", s)
			}
		case "i-ok":
			if s.StoppingTask != "" {
				t.Fatalf("a connected slot reports a stopping task: %+v", s)
			}
		}
	}
}

// A box still shutting down has its home mounted until the shutdown ends. A Start that
// releases the home in that window must wait, not detach.
func TestReleaseSlotWaitsForAStoppingSlot(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ec2.instances["i-wedge"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopping}
	if err := h.rt.releaseSlot(ctx); err == nil || !strings.Contains(err.Error(), "finish stopping") {
		t.Fatalf("releaseSlot on a stopping slot = %v, want a wait that gave up", err)
	}
	if attachedInstance(h.ec2.volumes["vol-1"]) != "i-wedge" {
		t.Fatal("the home was detached from a slot that had not finished stopping")
	}
	// Once it is stopped the same release goes through.
	h.ec2.instances["i-wedge"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped}
	if err := h.rt.releaseSlot(ctx); err != nil {
		t.Fatalf("releaseSlot after the stop: %v", err)
	}
	if attachedInstance(h.ec2.volumes["vol-1"]) != "" {
		t.Fatal("the home is still attached to the stopped slot")
	}
}

// The delayed Force belongs to the stop that scheduled it. An instance that finished
// stopping and was started again (an operator clearing a STOPPING task, say) must not be
// power-cut three minutes later.
func TestForceStopEscalationLeavesARevivedInstanceAlone(t *testing.T) {
	for _, state := range []ec2types.InstanceStateName{
		ec2types.InstanceStateNameRunning, ec2types.InstanceStateNameStopped, ec2types.InstanceStateNamePending,
		ec2types.InstanceStateNameStopping, // a later stop, from a later launch
	} {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			h := wedgedSlot(t)
			launch := time.Now().Add(-time.Hour)
			h.ec2.instances["i-wedge"].LaunchTime = &launch
			if _, err := h.factory().ForceStopSlot(ctx, "i-wedge", false); err != nil {
				t.Fatal(err)
			}
			// It stopped within seconds, and somebody started it again.
			relaunch := time.Now()
			h.ec2.instances["i-wedge"].LaunchTime = &relaunch
			h.ec2.instances["i-wedge"].State = &ec2types.InstanceState{Name: state}
			h.runDeferred(ctx)
			if got := h.ec2.stopForce; len(got) != 1 {
				t.Fatalf("stops %v: the stale timer stopped the %s instance again", got, state)
			}
		})
	}
}

// A quarantined slot that also carries a replacement reservation must not reach
// replaceReservedSlot, which terminates the old box and with it the evidence.
func TestForceStoppedReservedSlotIsReleasedNotTerminated(t *testing.T) {
	ctx := context.Background()
	h := reservedSlotHarness(t, true)
	h.ci.registered["i-old"] = false
	h.ssmc.online = map[string]bool{"i-old": false}
	if _, err := h.factory().ForceStopSlot(ctx, "i-old", false); err != nil {
		t.Fatal(err)
	}
	matches, reserved, outdated, err := h.rt.slotTypeMatches(ctx, "i-old")
	if err != nil || matches || reserved || outdated {
		t.Fatalf("quarantined slot read as matches=%v reserved=%v outdated=%v err=%v; want all false", matches, reserved, outdated, err)
	}
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start after the force-stop: %v", err)
	}
	if i := callIndex(h, "TerminateInstances"); i >= 0 {
		t.Fatalf("a Start terminated the quarantined slot: %v", h.ec2.calls)
	}
}

// ec2AfterQuarantine runs fire once, right after the quarantine tag lands.
type ec2AfterQuarantine struct {
	ec2API
	fire func()
}

func (w *ec2AfterQuarantine) CreateTags(ctx context.Context, in *ec2.CreateTagsInput, opt ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {
	out, err := w.ec2API.CreateTags(ctx, in, opt...)
	for _, tag := range in.Tags {
		if aws.ToString(tag.Key) == EC2TagRole && aws.ToString(tag.Value) == ec2RoleQuarantined && w.fire != nil {
			f := w.fire
			w.fire = nil
			f()
		}
	}
	return out, err
}

// A member's Start that lands between the quarantine fence and the occupant's Stop either
// sees the quarantine or is overtaken by the Stop: it must not outlive the force-stop with
// its claim on the stopped slot.
func TestForceStopOvertakesAStartThatRacesTheFence(t *testing.T) {
	ctx := context.Background()
	h := memberWipeHarness(t)
	h.ci.registered["i-hot"] = false
	h.ssmc.online = map[string]bool{"i-hot": false}
	gen, err := h.rt.startGenNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f := h.factory()
	f.ec2 = &ec2AfterQuarantine{ec2API: h.ec2, fire: func() {
		if err := h.rt.Start(ctx); err != nil {
			t.Errorf("racing Start: %v", err)
		}
	}}
	if _, err := f.ForceStopSlot(ctx, "i-hot", false); err != nil {
		t.Fatal(err)
	}
	if moved, err := h.rt.startedSince(ctx, gen); err != nil || !moved {
		t.Fatalf("start count did not move (moved=%v err=%v)", moved, err)
	}
	if ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim) != "" {
		t.Fatal("the racing Start's claim survived the force-stop")
	}
	if scaledUp(h) && h.ecs.services["af-ws-acme-alice"].DesiredCount != 0 {
		t.Fatal("the service was left at desired 1 on the stopped slot")
	}
}

// ec2BeforeAttach runs fire once, just before the first AttachVolume.
type ec2BeforeAttach struct {
	ec2API
	fire func()
}

func (w *ec2BeforeAttach) AttachVolume(ctx context.Context, in *ec2.AttachVolumeInput, opt ...func(*ec2.Options)) (*ec2.AttachVolumeOutput, error) {
	if f := w.fire; f != nil {
		w.fire = nil
		f()
	}
	return w.ec2API.AttachVolume(ctx, in, opt...)
}

// A placement that listed its free slots before the force-stop and attaches after it must
// notice the quarantine on its post-attach re-check and step off, as it does for a
// reservation, instead of living on as a Start on a stopped slot.
func TestForceStopFencesAPlacementThatListedTheSlotEarlier(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addSlot("i-wedge", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ci.registered["i-wedge"] = false
	h.ssmc.online = map[string]bool{"i-wedge": false}
	f := h.factory()
	h.rt.ec2 = &ec2BeforeAttach{ec2API: h.ec2, fire: func() {
		if _, err := f.ForceStopSlot(ctx, "i-wedge", false); err != nil {
			t.Errorf("ForceStopSlot: %v", err)
		}
	}}
	_ = h.rt.Start(ctx)
	if v := h.ec2.volumes["vol-1"]; v != nil && attachedInstance(v) == "i-wedge" {
		t.Fatalf("the home stayed on the force-stopped slot: %v", v.Attachments)
	}
	if v := h.ec2.volumes["vol-1"]; v != nil && ec2TagValue(v.Tags, EC2TagClaim) == "i-wedge" {
		t.Fatal("a claim on the force-stopped slot survived")
	}
}
