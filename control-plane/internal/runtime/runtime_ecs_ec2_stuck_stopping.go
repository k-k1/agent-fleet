package runtime

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// ecsTaskLister is the part of the ECS client that finds a service's previous task. It is
// asked for by type assertion on the container-instance client, so a runtime built on a
// client without it simply never reports the condition.
type ecsTaskLister interface {
	ListTasks(context.Context, *ecs.ListTasksInput, ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
	DescribeTasks(context.Context, *ecs.DescribeTasksInput, ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
}

// stuckStoppingGrace is how long a task may sit in STOPPING before a disconnected agent is
// taken as the reason. A healthy slot finishes a stop in seconds; this only has to outlast
// the window in which a freshly stopped slot's agent is still reconnecting.
const stuckStoppingGrace = 2 * time.Minute

// stuckStoppingEvery bounds the extra API calls: State() is polled every few seconds, and
// the answer only changes when an operator acts.
const stuckStoppingEvery = 30 * time.Second

type stuckStoppingCacheEntry struct {
	at  time.Time
	why string
}

var stuckStoppingCache sync.Map // workspace name -> stuckStoppingCacheEntry

// stuckStoppingReason explains a start that can never converge because the service's
// PREVIOUS task is still stopping on a slot nobody can finish the stop on. The workspace
// services run with maximumPercent 100, so the new deployment waits for that task to be
// gone — and the agent that would report it is disconnected, the instance stopped, or
// gone. ECS emits no service event for this ("unable to place" never appears), which is
// why it needs its own look. why "" with known true means no such task, or not provably
// stuck: when in doubt, say nothing. known false means the lookup failed, and says nothing
// about the task either way (and is not cached).
//
// Detect-and-surface only. Starting the old instance, or force-deregistering it, is a
// decision about a slot that may hold someone else's data.
func (e *ecsEC2Runtime) stuckStoppingReason(ctx context.Context, s ecstypes.Service) (why string, known bool) {
	if s.DesiredCount < 1 || s.RunningCount != 0 || s.PendingCount != 0 {
		return "", true
	}
	if v, ok := stuckStoppingCache.Load(e.base.name); ok {
		if c := v.(stuckStoppingCacheEntry); e.now().Sub(c.at) < stuckStoppingEvery {
			return c.why, true
		}
	}
	why, err := e.findStuckStopping(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("ecs-ec2 state: looking for a stuck stopping task of %s: %v", e.base.name, err)
		}
		return "", false
	}
	stuckStoppingCache.Store(e.base.name, stuckStoppingCacheEntry{at: e.now(), why: why})
	return why, true
}

func (e *ecsEC2Runtime) findStuckStopping(ctx context.Context) (string, error) {
	tasks, ok := e.ci.(ecsTaskLister)
	if !ok {
		return "", nil
	}
	// A task asked to stop has desiredStatus STOPPED while its lastStatus is still earlier.
	// Every page is read: the stuck task is not promised to be on the first one.
	var next *string
	for {
		listed, err := tasks.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster:       aws.String(e.base.cfg.cluster),
			ServiceName:   aws.String(e.base.name),
			DesiredStatus: ecstypes.DesiredStatusStopped,
			NextToken:     next,
		})
		if err != nil {
			return "", err
		}
		if len(listed.TaskArns) > 0 {
			described, err := tasks.DescribeTasks(ctx, &ecs.DescribeTasksInput{
				Cluster: aws.String(e.base.cfg.cluster),
				Tasks:   listed.TaskArns,
			})
			if err != nil {
				return "", err
			}
			// MISSING is a task that has left the listing since: a known absence. Anything
			// else is a task that was not read, which must not become "nothing is stuck".
			for _, f := range described.Failures {
				if aws.ToString(f.Reason) != "MISSING" {
					return "", fmt.Errorf("describe task %s: %s", aws.ToString(f.Arn), aws.ToString(f.Reason))
				}
			}
			for _, t := range described.Tasks {
				last := aws.ToString(t.LastStatus)
				if last != "STOPPING" && last != "DEACTIVATING" && last != "DEPROVISIONING" {
					continue
				}
				// Without a stop time there is no way to tell a stuck stop from a young one.
				if t.StoppingAt == nil || e.now().Sub(*t.StoppingAt) < stuckStoppingGrace {
					continue
				}
				why, err := e.slotCannotFinishStop(ctx, t, last)
				if err != nil {
					return "", err
				}
				if why != "" {
					return why, nil
				}
			}
		}
		if listed.NextToken == nil {
			return "", nil
		}
		next = listed.NextToken
	}
}

// slotCannotFinishStop is "" unless the task's container instance is provably unable to
// report the stop: its agent is disconnected, or it is no longer registered. A failed
// lookup is an error, never "". Absence is only what ECS says it is: a MISSING failure.
func (e *ecsEC2Runtime) slotCannotFinishStop(ctx context.Context, t ecstypes.Task, last string) (string, error) {
	arn := aws.ToString(t.ContainerInstanceArn)
	if arn == "" {
		return "", nil
	}
	out, err := e.ci.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
		Cluster:            aws.String(e.base.cfg.cluster),
		ContainerInstances: []string{arn},
	})
	if err != nil {
		return "", err
	}
	slot, how := "", ""
	switch {
	case len(out.ContainerInstances) > 0:
		ci := out.ContainerInstances[0]
		if ci.AgentConnected && aws.ToString(ci.Status) == "ACTIVE" {
			return "", nil
		}
		slot = aws.ToString(ci.Ec2InstanceId)
		how = "has its ECS agent disconnected"
		if aws.ToString(ci.Status) != "ACTIVE" {
			how = "is no longer an active container instance"
		}
	case len(out.Failures) > 0 && aws.ToString(out.Failures[0].Reason) == "MISSING":
		how = "is no longer registered with the cluster"
	default:
		return "", fmt.Errorf("describe container instance %s: no answer", arn)
	}
	state := "unknown"
	if slot != "" {
		state = e.slotStateName(ctx, slot)
	}
	since := e.now().Sub(aws.ToTime(t.StoppingAt)).Round(time.Minute)
	return fmt.Sprintf("the previous task of this workspace is still %s (for %s) on a slot that %s (instance state: %s, slot %s). "+
		"The new task cannot start until that slot reports the stop; an administrator has to bring that slot back or retire it.",
		last, since, how, state, orUnknown(slot)), nil
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// slotStateName is the EC2 state of an instance, "gone" when EC2 no longer knows it.
func (e *ecsEC2Runtime) slotStateName(ctx context.Context, instanceID string) string {
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		if isAWSNotFound(err) {
			return "gone"
		}
		return "unknown"
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			return string(inst.State.Name)
		}
	}
	return "gone"
}
