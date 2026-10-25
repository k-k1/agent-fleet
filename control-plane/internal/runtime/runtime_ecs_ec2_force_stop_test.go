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

// A wedged slot whose task ENI ECS never cleaned up: the force-stop deregisters the
// container instance (its agent is disconnected, so only force can) and stops the box only
// once the ENI is gone.
func TestForceStopSlotDeregistersAndWaitsForTheTaskENI(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ec2.giveTaskENI("i-wedge")
	res, err := h.factory().ForceStopSlot(ctx, "i-wedge", false)
	if err != nil {
		t.Fatalf("ForceStopSlot: %v", err)
	}
	if !res.Deregistered || len(h.ci.deregistered) != 1 || h.ci.deregistered[0] != "arn:ci/i-wedge" {
		t.Fatalf("result %+v, deregistered %v; want the container instance force-deregistered once", res, h.ci.deregistered)
	}
	if len(h.ec2.stopForce) != 1 {
		t.Fatalf("stops %v, want one once the ENI is gone", h.ec2.stopForce)
	}
	if out, _ := h.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{"i-wedge"}}); instancesHoldTaskENI(out) {
		t.Fatal("the task ENI is still attached")
	}
}

// ECS acknowledges the deregistration and keeps the ENI: the stop is refused, with a
// reason the operator can act on, rather than leaving a multi-ENI box behind.
func TestForceStopSlotRefusesWhileTheTaskENIStaysAttached(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ec2.giveTaskENI("i-wedge")
	h.ci.stickyENI = true
	res, err := h.factory().ForceStopSlot(ctx, "i-wedge", false)
	if !errors.Is(err, ErrSlotTaskENIHeld) {
		t.Fatalf("ForceStopSlot = %+v, %v; want ErrSlotTaskENIHeld", res, err)
	}
	if len(h.ec2.stopForce) != 0 {
		t.Fatalf("stopped a slot that still held a task ENI: %v", h.ec2.stopForce)
	}
	if !res.Deregistered {
		t.Fatalf("result %+v: the deregistration was attempted and must be reported", res)
	}
	// The same press again, once ECS has let go, goes through.
	h.ec2.dropTaskENIs("i-wedge")
	if _, err := h.factory().ForceStopSlot(ctx, "i-wedge", false); err != nil || len(h.ec2.stopForce) != 1 {
		t.Fatalf("retry after the ENI went: err=%v stops=%v", err, h.ec2.stopForce)
	}
}

// No task ENI, nothing to release: the container instance is left registered.
func TestForceStopSlotWithoutATaskENIDoesNotDeregister(t *testing.T) {
	h := wedgedSlot(t)
	res, err := h.factory().ForceStopSlot(context.Background(), "i-wedge", false)
	if err != nil || res.Deregistered || len(h.ci.deregistered) != 0 || len(h.ec2.stopForce) != 1 {
		t.Fatalf("res=%+v err=%v deregistered=%v stops=%v", res, err, h.ci.deregistered, h.ec2.stopForce)
	}
}

// A Start accepted while the force-stop waits for the task ENI owns the slot now: the
// stop is dropped instead of powering off the box that Start is booting on.
func TestForceStopSlotLeavesASlotAloneWhenAStartLandsDuringTheENIWait(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ec2.giveTaskENI("i-wedge")
	h.ci.stickyENI = true
	f := h.factory()
	started := false
	f.sleep = func(context.Context, time.Duration) error {
		if !started {
			started = true
			if err := h.rt.Start(ctx); err != nil {
				t.Errorf("Start during the wait: %v", err)
			}
			h.ec2.dropTaskENIs("i-wedge")
		}
		return nil
	}
	_, err := f.ForceStopSlot(ctx, "i-wedge", false)
	if !errors.Is(err, ErrSlotStartedAgain) {
		t.Fatalf("ForceStopSlot = %v, want ErrSlotStartedAgain", err)
	}
	if len(h.ec2.stopForce) != 0 {
		t.Fatalf("stopped a slot a new Start had taken: %v", h.ec2.stopForce)
	}
}

// The same, one step earlier: the Start is accepted before the deregistration, which
// would otherwise cut the new task's ENI.
func TestForceStopSlotDoesNotDeregisterAfterAStartLanded(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ec2.giveTaskENI("i-wedge")
	h.ssmc.onList = func() {
		h.ssmc.onList = nil
		if err := h.rt.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}
	_, err := h.factory().ForceStopSlot(ctx, "i-wedge", false)
	if !errors.Is(err, ErrSlotStartedAgain) {
		t.Fatalf("ForceStopSlot = %v, want ErrSlotStartedAgain", err)
	}
	if len(h.ci.deregistered) != 0 || len(h.ec2.stopForce) != 0 {
		t.Fatalf("deregistered %v, stops %v: acted on a slot a new Start owns", h.ci.deregistered, h.ec2.stopForce)
	}
}

// The ENI wait spends the request's wall clock, not a count of sleeps: slow AWS calls
// must end it at the deadline with a refusal, long before the 15 polls would.
func TestForceStopSlotENIWaitStopsAtItsDeadlineWhenAWSCallsAreSlow(t *testing.T) {
	ctx := context.Background()
	old := forceStopENIBudget
	forceStopENIBudget = 400 * time.Millisecond
	defer func() { forceStopENIBudget = old }()
	h := wedgedSlot(t)
	h.ec2.giveTaskENI("i-wedge")
	h.ci.stickyENI = true
	h.ssmc.onList = func() { h.ec2.describeDelay = 150 * time.Millisecond }
	f := h.factory()
	f.sleep = func(c context.Context, d time.Duration) error { return sleepCtx(c, 10*time.Millisecond) }
	begin := time.Now()
	_, err := f.ForceStopSlot(ctx, "i-wedge", false)
	if !errors.Is(err, ErrSlotTaskENIHeld) {
		t.Fatalf("ForceStopSlot = %v, want ErrSlotTaskENIHeld", err)
	}
	if took := time.Since(begin); took > 1500*time.Millisecond {
		t.Fatalf("took %s with a 400ms ENI budget", took)
	}
	if len(h.ec2.stopForce) != 0 {
		t.Fatalf("stopped despite the held ENI: %v", h.ec2.stopForce)
	}
}

// Pre-steps that already used the budget leave no time for the ENI stage: refuse without
// deregistering anything.
func TestForceStopSlotRefusesWithoutDeregisteringWhenNoTimeIsLeft(t *testing.T) {
	old := forceStopBudget
	forceStopBudget = 5 * time.Second // below forceStopStopReserve: no ENI time at all
	defer func() { forceStopBudget = old }()
	h := wedgedSlot(t)
	h.ec2.giveTaskENI("i-wedge")
	_, err := h.factory().ForceStopSlot(context.Background(), "i-wedge", false)
	if !errors.Is(err, ErrSlotTaskENIHeld) {
		t.Fatalf("ForceStopSlot = %v, want ErrSlotTaskENIHeld", err)
	}
	if len(h.ci.deregistered) != 0 || len(h.ec2.stopForce) != 0 {
		t.Fatalf("deregistered %v, stops %v", h.ci.deregistered, h.ec2.stopForce)
	}
}
