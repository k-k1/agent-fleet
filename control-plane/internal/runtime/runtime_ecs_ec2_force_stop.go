package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// slotVolumeCommandComment tags every SSM command the CP sends to a slot for a home volume
// (mount, umount, wipe). ForceStopSlot cancels exactly those and nothing else on the box.
const slotVolumeCommandComment = "agent-fleet slot volume"

// slotForceStopGrace is how long a graceful stop may take before the same stop is repeated
// with Force. EC2's own shutdown of an unresponsive instance can run for several minutes;
// three is long enough for a thrashing kernel to flush and unmount the home, and short
// enough that a box that will never answer ACPI does not sit in `stopping` for the hour.
const slotForceStopGrace = 3 * time.Minute

// Refusals ForceStopSlot adds to the ones TerminateQuarantinedSlot exports.
var (
	// ErrSlotHealthy: both agents answer, so this is not a wedged slot and a workspace may
	// be running on it. Overridable on purpose, by an operator who knows better.
	ErrSlotHealthy = errors.New("slot is healthy (its ECS agent is connected and SSM is online)")
	// ErrSlotNotRunning: nothing to stop. A stopped slot with a task still STOPPING is a
	// different job (start it once; SlotView.StoppingTask says so).
	ErrSlotNotRunning = errors.New("slot is not running")
)

// ssmSlotCommands is the part of the SSM client ForceStopSlot needs beyond
// ssmCommandAPI. It is asked for by type assertion, as ecsTaskLister is, so a client
// without it (a test double) makes the action refuse rather than half-run.
type ssmSlotCommands interface {
	ListCommands(context.Context, *ssm.ListCommandsInput, ...func(*ssm.Options)) (*ssm.ListCommandsOutput, error)
	CancelCommand(context.Context, *ssm.CancelCommandInput, ...func(*ssm.Options)) (*ssm.CancelCommandOutput, error)
	DescribeInstanceInformation(context.Context, *ssm.DescribeInstanceInformationInput, ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error)
}

// SlotForceStop is what ForceStopSlot reports, and what the audit line and the Console
// show. Every field is the state at the moment of the call.
type SlotForceStop struct {
	InstanceID    string `json:"instance_id"`
	Workspace     string `json:"workspace"` // the occupant whose Start was ended, "" = none
	PreviousState string `json:"previous_state"`
	// AgentConnected / SSMOnline are the two health signals the refusal is made on.
	AgentConnected bool `json:"agent_connected"`
	SSMOnline      bool `json:"ssm_online"`
	// OverrodeHealthy: the slot looked healthy and the operator said stop it anyway.
	OverrodeHealthy   bool `json:"overrode_healthy"`
	CancelledCommands int  `json:"cancelled_commands"`
	// CancelFailed counts commands that were listed but could not be cancelled.
	CancelFailed int `json:"cancel_failed"`
	// Quarantined is whether the af-role tag is now `quarantined`.
	Quarantined bool `json:"quarantined"`
	// Forced: this StopInstances carried Force. ForceAfterSec is the delay before the
	// background re-stop with Force, 0 when none is pending.
	Forced        bool `json:"forced"`
	ForceAfterSec int  `json:"force_after_sec"`
	// StoppingTask is the ECS status of a task that is still STOPPING on the slot ("" = none).
	StoppingTask string `json:"stopping_task,omitempty"`
}

// ForceStopSlot stops a slot that is wedged but not quarantined — running, status checks
// ok, yet its ECS agent and SSM no longer answer — because an operator said so.
//
// What it does, in this order, and why:
//
//  1. Refuse a slot that answers on both agents (ErrSlotHealthy) unless overrideHealthy:
//     a workspace may be running on it. An unreadable health signal refuses too, unless
//     overridden: "when in doubt, do not stop it".
//  2. Quarantine the box (markQuarantined) FIRST, because the tag is the fence against a
//     Start choosing this slot meanwhile. Decision: quarantine, not just stop. The box
//     failed in a way nobody has diagnosed, so it keeps its evidence and is never offered
//     again (ADR 0045 decisions 20 and 23); an operator terminates it from the pool screen
//     when done. placeHome treats a quarantined box as a mismatch, reserved or not, so the
//     member's next Start releases the home from it (releaseSlot skips the umount for a
//     stopped box) and places it elsewhere.
//  3. End the occupant's Start with the runtime's own Stop (abortStarts under the claim
//     lock, desiredCount 0, the claim dropped), reading the occupant after the fence. A
//     Start that chose the slot before the tag is overtaken; one after it saw the
//     quarantine. The member sees `stopped`.
//  4. Cancel the CP's own pending SSM commands (slotVolumeCommandComment). A queued
//     umount or mount holds the home's mount lock for as long as the command lives and no
//     agent will ever finish it; cancelling is what lets the lock holder see it end.
//  5. StopInstances. Graceful first, because a wedged box may still have the home mounted
//     and Force is a power cut with no unmount; a background re-stop with Force follows
//     after slotForceStopGrace if the box is not stopped by then. A slot already
//     `stopping` (the operator pressing again) is forced at once.
//
// The ECS container instance is NOT deregistered: a task still STOPPING on the slot is
// cleared by starting the instance once so its agent reports, and that stays a human
// decision (StoppingTask reports it).
func (f *ecsEC2Factory) ForceStopSlot(ctx context.Context, instanceID string, overrideHealthy bool) (SlotForceStop, error) {
	res := SlotForceStop{InstanceID: instanceID}
	cmds, ok := f.ssmc.(ssmSlotCommands)
	if !ok {
		return res, errors.New("the SSM client cannot list or cancel commands")
	}
	insts, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		// Filtered, not looked up by id, for the reason TerminateQuarantinedSlot gives:
		// "not ours" and "not there" are one reply.
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, f.pool.pool),
			{Name: aws.String("instance-id"), Values: []string{instanceID}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		return res, err
	}
	var inst *ec2types.Instance
	for _, r := range insts.Reservations {
		for i := range r.Instances {
			if aws.ToString(r.Instances[i].InstanceId) == instanceID {
				inst = &r.Instances[i]
			}
		}
	}
	if inst == nil {
		return res, ErrSlotNotFound
	}
	role := ec2TagValue(inst.Tags, EC2TagRole)
	if role != ec2RoleSlot && role != ec2RoleQuarantined {
		return res, ErrSlotNotFound // an engine box or something else in the pool tag
	}
	if inst.State != nil {
		res.PreviousState = string(inst.State.Name)
	}
	if inst.State == nil || inst.State.Name == ec2types.InstanceStateNameStopped {
		return res, ErrSlotNotRunning
	}
	probe := f.probeRuntime()

	// 1. health. A quarantined box is already out of the pool, so the gate is for the
	// working role only.
	registered, regErr := probe.registeredSlots(ctx)
	online, ssmErr := ssmOnline(ctx, cmds, instanceID)
	res.AgentConnected, res.SSMOnline = registered[instanceID], online
	if role == ec2RoleSlot && !overrideHealthy {
		if regErr != nil || ssmErr != nil {
			return res, fmt.Errorf("could not tell whether the slot is wedged (ECS: %v, SSM: %v); retry, or override", regErr, ssmErr)
		}
		if res.AgentConnected && res.SSMOnline {
			return res, ErrSlotHealthy
		}
	}
	res.OverrodeHealthy = overrideHealthy && role == ec2RoleSlot &&
		(regErr != nil || ssmErr != nil || (res.AgentConnected && res.SSMOnline))

	// 2. quarantine FIRST: it is the fence. A Start choosing a slot after this tag sees a
	// mismatch and releases the home; one that chose it before has claimed the home, which the
	// read below finds, and the Stop that follows overtakes it. Nothing has been touched yet
	// if the tag cannot be written.
	if role == ec2RoleSlot {
		cause := fmt.Errorf("force-stopped by an operator (ecs agent connected=%v, ssm online=%v)", res.AgentConnected, res.SSMOnline)
		res.Quarantined = probe.markQuarantined(ctx, instanceID, cause) == nil
		if !res.Quarantined {
			return res, fmt.Errorf("could not quarantine %s; nothing was changed", instanceID)
		}
	} else {
		res.Quarantined = true
	}

	// 3. the occupant, read AFTER the fence (see slotHolder's note on write-then-read).
	var occupant *ecsEC2Runtime
	if holder, err := probe.slotHolder(ctx, instanceID, ""); err != nil {
		return res, fmt.Errorf("find the home on %s: %w", instanceID, err)
	} else if holder != "" {
		vols, err := f.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{holder}})
		if err != nil {
			return res, fmt.Errorf("read the home %s: %w", holder, err)
		}
		if len(vols.Volumes) > 0 {
			res.Workspace = ec2TagValue(vols.Volumes[0].Tags, EC2TagWorkspace)
			occupant = f.runtimeForVolume(&vols.Volumes[0])
		}
	}
	log.Printf("ecs-ec2: FORCE-STOPPING slot %s (state %s, ecs agent=%v, ssm online=%v, healthy override=%v, occupant %q)",
		instanceID, res.PreviousState, res.AgentConnected, res.SSMOnline, res.OverrodeHealthy, res.Workspace)
	if occupant != nil {
		if err := occupant.Stop(ctx); err != nil {
			// The slot is quarantined but not stopped; pressing the button again retries.
			return res, fmt.Errorf("the slot is quarantined, but ending the start of %s failed: %w", res.Workspace, err)
		}
	}

	// 4. the CP's own pending commands.
	res.CancelledCommands, res.CancelFailed = cancelSlotVolumeCommands(ctx, cmds, instanceID)

	// 5. stop.
	res.Forced = inst.State.Name == ec2types.InstanceStateNameStopping
	if _, err := f.ec2.StopInstances(ctx, &ec2.StopInstancesInput{
		InstanceIds: []string{instanceID}, Force: aws.Bool(res.Forced),
	}); err != nil {
		return res, fmt.Errorf("stop %s (it is quarantined and its commands are cancelled): %w", instanceID, err)
	}
	if !res.Forced {
		res.ForceAfterSec = int(slotForceStopGrace.Seconds())
		launched := aws.ToTime(inst.LaunchTime)
		probe.bg(ctx, func(c context.Context) { probe.forceStopAfterGrace(c, instanceID, launched) })
	}
	res.StoppingTask, _ = probe.stoppingTaskOn(ctx, instanceID)
	log.Printf("ecs-ec2: force-stop of slot %s sent (forced=%v, cancelled %d commands, %d failed, stopping task %q)",
		instanceID, res.Forced, res.CancelledCommands, res.CancelFailed, res.StoppingTask)
	return res, nil
}

// forceStopAfterGrace repeats the stop with Force when THIS stop has not finished. It acts
// only on an instance still `stopping` from the same launch (LaunchTime moves on every
// start): one that finished stopping and was started again since, for example by an operator
// clearing a STOPPING task, must never be power-cut by a stale timer.
func (e *ecsEC2Runtime) forceStopAfterGrace(ctx context.Context, instanceID string, launched time.Time) {
	if err := e.sleep(ctx, slotForceStopGrace); err != nil {
		return
	}
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		return // unknown: leave it, the operator's second press forces
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			if aws.ToString(inst.InstanceId) != instanceID || inst.State == nil ||
				inst.State.Name != ec2types.InstanceStateNameStopping || !aws.ToTime(inst.LaunchTime).Equal(launched) {
				return
			}
			log.Printf("ecs-ec2: slot %s did not stop within %s; stopping it with Force", instanceID, slotForceStopGrace)
			if _, err := e.ec2.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{instanceID}, Force: aws.Bool(true)}); err != nil {
				log.Printf("ecs-ec2: force-stopping slot %s: %v", instanceID, err)
			}
			return
		}
	}
}

// ssmOnline reports whether SSM lists the instance with PingStatus Online. Not listed at
// all reads as offline: that is what a box whose agent died long ago looks like.
func ssmOnline(ctx context.Context, c ssmSlotCommands, instanceID string) (bool, error) {
	out, err := c.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{
		Filters: []ssmtypes.InstanceInformationStringFilter{{Key: aws.String("InstanceIds"), Values: []string{instanceID}}},
	})
	if err != nil {
		return false, err
	}
	for _, i := range out.InstanceInformationList {
		if aws.ToString(i.InstanceId) == instanceID {
			return i.PingStatus == ssmtypes.PingStatusOnline, nil
		}
	}
	return false, nil
}

// cancelSlotVolumeCommands cancels the CP's own commands that have not finished on the
// instance. Best effort: a command that cannot be cancelled is counted, not fatal, since
// the stop that follows ends it anyway.
func cancelSlotVolumeCommands(ctx context.Context, c ssmSlotCommands, instanceID string) (cancelled, failed int) {
	for _, status := range []string{"Pending", "InProgress"} {
		var next *string
		for {
			out, err := c.ListCommands(ctx, &ssm.ListCommandsInput{
				InstanceId: aws.String(instanceID),
				Filters:    []ssmtypes.CommandFilter{{Key: ssmtypes.CommandFilterKeyStatus, Value: aws.String(status)}},
				NextToken:  next,
			})
			if err != nil {
				log.Printf("ecs-ec2: listing %s SSM commands on %s: %v", status, instanceID, err)
				failed++
				break
			}
			for _, cmd := range out.Commands {
				if aws.ToString(cmd.Comment) != slotVolumeCommandComment {
					continue // somebody else's command, never ours to cancel
				}
				if _, err := c.CancelCommand(ctx, &ssm.CancelCommandInput{
					CommandId: cmd.CommandId, InstanceIds: []string{instanceID},
				}); err != nil {
					log.Printf("ecs-ec2: cancelling SSM command %s on %s: %v", aws.ToString(cmd.CommandId), instanceID, err)
					failed++
					continue
				}
				cancelled++
			}
			if next = out.NextToken; next == nil {
				break
			}
		}
	}
	return cancelled, failed
}

// stoppingTaskOn is the ECS status of a task asked to stop on the instance that has not
// reported it ("" = none), and how long ago it was asked. It reads only the container
// instance's own tasks, so it costs two calls for a slot ECS still knows, none otherwise.
func (e *ecsEC2Runtime) stoppingTaskOn(ctx context.Context, instanceID string) (status string, err error) {
	got, err := e.stoppingTasksOn(ctx, []string{instanceID})
	return got[instanceID].status, err
}

type stoppingTask struct {
	status string
	since  time.Time
}

func (e *ecsEC2Runtime) stoppingTasksOn(ctx context.Context, instanceIDs []string) (map[string]stoppingTask, error) {
	out := map[string]stoppingTask{}
	lister, ok := e.ci.(ecsTaskLister)
	if !ok || len(instanceIDs) == 0 {
		return out, nil
	}
	arns, err := e.listContainerInstanceARNs(ctx)
	if err != nil || len(arns) == 0 {
		return out, err
	}
	want := map[string]bool{}
	for _, id := range instanceIDs {
		want[id] = true
	}
	for _, chunk := range chunkStrings(arns, 100) {
		cis, err := e.ci.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
			Cluster: aws.String(e.base.cfg.cluster), ContainerInstances: chunk,
		})
		if err != nil {
			return out, err
		}
		for _, ci := range cis.ContainerInstances {
			id := aws.ToString(ci.Ec2InstanceId)
			if !want[id] || !isPoolContainerInstance(ci) {
				continue
			}
			listed, err := lister.ListTasks(ctx, &ecs.ListTasksInput{
				Cluster:           aws.String(e.base.cfg.cluster),
				ContainerInstance: ci.ContainerInstanceArn,
				DesiredStatus:     ecstypes.DesiredStatusStopped,
			})
			if err != nil {
				return out, err
			}
			if len(listed.TaskArns) == 0 {
				continue
			}
			described, err := lister.DescribeTasks(ctx, &ecs.DescribeTasksInput{
				Cluster: aws.String(e.base.cfg.cluster), Tasks: listed.TaskArns,
			})
			if err != nil {
				return out, err
			}
			for _, t := range described.Tasks {
				last := aws.ToString(t.LastStatus)
				if aws.ToString(t.ContainerInstanceArn) != aws.ToString(ci.ContainerInstanceArn) ||
					(last != "STOPPING" && last != "DEACTIVATING" && last != "DEPROVISIONING") {
					continue
				}
				out[id] = stoppingTask{status: last, since: aws.ToTime(t.StoppingAt)}
			}
		}
	}
	return out, nil
}
