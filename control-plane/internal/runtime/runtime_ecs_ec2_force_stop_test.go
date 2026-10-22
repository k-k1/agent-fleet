package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
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
	if !res.OverrodeHealthy || len(h.ec2.stopForce) != 1 {
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
	// Not quarantined: the slot stays in service and keeps its home.
	if got := ec2TagValue(h.ec2.instances["i-wedge"].Tags, EC2TagRole); got != ec2RoleSlot {
		t.Fatalf("role = %q, want the slot left in service", got)
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
	if len(h.ec2.calls) == 0 || !strings.HasPrefix(h.ec2.calls[len(h.ec2.calls)-1], "StopInstances") {
		t.Fatalf("the stop must come last: %v", h.ec2.calls)
	}
	// The member's next Start wakes this same, stopped slot through the ordinary path.
	p, err := h.rt.placeHome(ctx)
	if err != nil || p.instanceID != "i-wedge" || !p.wake {
		t.Fatalf("placeHome after the force-stop = %+v, %v; want the same slot, to be woken", p, err)
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
	h.ec2.addSlot("i-bad", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.setInstanceTag("i-bad", EC2TagRole, ec2RoleQuarantined)
	h.ec2.addSlot("i-engine", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.setInstanceTag("i-engine", EC2TagRole, "engine")
	f := h.factory()
	for id, want := range map[string]error{
		"i-off": ErrSlotNotRunning, "i-bad": ErrSlotQuarantined, "i-theirs": ErrSlotNotFound, "i-engine": ErrSlotNotFound, "i-nope": ErrSlotNotFound,
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
