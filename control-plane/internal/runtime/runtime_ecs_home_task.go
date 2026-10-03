// runtime_ecs_home_task.go — Recreate, Clean home and Destroy on Fargate (ADR 0045
// decision 31, #1260).
//
// The home is an EFS directory the CP cannot mount. The stack declares a one-shot task
// definition (deploy/aws/ecs/cfn/30-ingress.yaml, HomeOpsTaskDef) that mounts the file
// system and runs `af-cp efs-home-op` (home_task.go); the CP starts it with the operation
// and the membership in its environment and reads the exit code back with DescribeTasks.
//
// A Fargate task takes minutes from cold, so every caller runs these after its request has
// been answered (HomeWipeInBackground). ECS itself is the record of a task in flight: each
// one is started with startedBy=af-home/<membership>, and a Start or another operation on
// the same home is refused while ECS still lists one — including a task this CP lost
// track of across a restart.
package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// ecsTaskAPI is the narrow port for the home task. *ecs.Client satisfies it.
type ecsTaskAPI interface {
	RunTask(context.Context, *ecs.RunTaskInput, ...func(*ecs.Options)) (*ecs.RunTaskOutput, error)
	DescribeTasks(context.Context, *ecs.DescribeTasksInput, ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
	ListTasks(context.Context, *ecs.ListTasksInput, ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
}

// ecsHomeTaskContainer is the container name in HomeOpsTaskDef; the overrides and the exit
// code are addressed to it.
const ecsHomeTaskContainer = "home-ops"

// ecsHomeTaskPoll is how often a running home task and a draining workspace task are
// re-read. The task takes minutes; a poll every few seconds costs nothing against that.
const ecsHomeTaskPoll = 5 * time.Second

// homeClearing holds the workspaces whose member wipe is queued or running in the
// background, by name. Process-local like startPhase: the Runtime value is rebuilt per
// request. Another replica, or this one after a restart, sees `stopped` instead of
// `starting` — and its Start is still refused through ECS (homeTaskInFlight).
var homeClearing sync.Map // workspace name -> struct{}

func (e *ecsRuntime) homePortsReady() bool { return e.cfg.homeTask != "" && e.tasks != nil }

func (e *ecsRuntime) homeTaskStartedBy() string { return "af-home/" + e.membershipID }

// MarkHomeClearing satisfies backgroundWiper.
func (e *ecsRuntime) MarkHomeClearing() func() {
	homeClearing.Store(e.name, struct{}{})
	var once sync.Once
	return func() { once.Do(func() { homeClearing.Delete(e.name) }) }
}

func (e *ecsRuntime) clearingHome() bool {
	_, ok := homeClearing.Load(e.name)
	return ok
}

// BootPhase names the background wipe for the Console's starting dialog, with the phase
// the slot pool uses for the same removal (console/src/lib/bootPhase.ts).
func (e *ecsRuntime) BootPhase() string {
	if e.clearingHome() {
		return "home: clearing"
	}
	return ""
}

// homeTaskInFlight asks ECS whether a home task for this member has not stopped yet.
// startedBy has to be the only filter of a ListTasks; the default desired status RUNNING
// is what "not stopped yet" means.
func (e *ecsRuntime) homeTaskInFlight(ctx context.Context) (bool, error) {
	out, err := e.tasks.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:   aws.String(e.cfg.cluster),
		StartedBy: aws.String(e.homeTaskStartedBy()),
	})
	if err != nil {
		return false, fmt.Errorf("list home tasks: %w", err)
	}
	return len(out.TaskArns) > 0, nil
}

// HomeWipeBlocked refuses an operation on the home while a home task is still running.
func (e *ecsRuntime) HomeWipeBlocked(ctx context.Context) error {
	if !e.homePortsReady() {
		return nil
	}
	busy, err := e.homeTaskInFlight(ctx)
	if err != nil {
		return err
	}
	if busy {
		return ErrHomeTaskInFlight
	}
	return nil
}

// WipeHome — ecs: a member's Recreate or Clean home, run as the home task. Blocks until the
// task has stopped; the CP calls it in the background.
func (e *ecsRuntime) WipeHome(ctx context.Context, what HomeWipe) error {
	if what != HomeWipeRepos && what != HomeWipeClean {
		return fmt.Errorf("unknown home wipe %q", what)
	}
	return e.runHomeTask(ctx, what)
}

// EraseHome — ecs: an administrator's Clean home, the same removal as the member's.
func (e *ecsRuntime) EraseHome(ctx context.Context) error {
	return e.runHomeTask(ctx, HomeWipeClean)
}

// runHomeTask waits for the workspace's own task to be gone, starts the home task and
// waits for it to stop. nil only when the task exited 0.
func (e *ecsRuntime) runHomeTask(ctx context.Context, what HomeWipe) error {
	if !e.homePortsReady() {
		return ErrHomeWipeUnsupported
	}
	// The task would refuse it as well; refusing here keeps a bad id from costing a task.
	if !ValidMembershipID(e.membershipID) {
		return fmt.Errorf("membership id %q cannot name a home", e.membershipID)
	}
	busy, err := e.homeTaskInFlight(ctx)
	if err != nil {
		return err
	}
	if busy {
		return ErrHomeTaskInFlight
	}
	// Stop only set the desired count to 0. The old task holds the home until it exits,
	// and removing files under a workspace that is still writing them leaves a half-home.
	if err := e.waitServiceTasksGone(ctx); err != nil {
		return err
	}
	arn, err := e.startHomeTask(ctx, what)
	if err != nil {
		return err
	}
	return e.waitHomeTask(ctx, arn, what)
}

// waitServiceTasksGone returns once the workspace service runs no task. A service that is
// back at desired 1 has been started under the operation, which the lease should have made
// impossible; the home is left alone.
func (e *ecsRuntime) waitServiceTasksGone(ctx context.Context) error {
	for {
		s, ok, err := e.describeService(ctx)
		if err != nil {
			return fmt.Errorf("describe service %s: %w", e.name, err)
		}
		if !ok || (s.RunningCount == 0 && s.PendingCount == 0) {
			return nil
		}
		if s.DesiredCount > 0 {
			return fmt.Errorf("service %s is at desired %d; its home is left alone", e.name, s.DesiredCount)
		}
		if err := e.pause(ctx); err != nil {
			return err
		}
	}
}

func (e *ecsRuntime) startHomeTask(ctx context.Context, what HomeWipe) (string, error) {
	out, err := e.tasks.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(e.cfg.cluster),
		TaskDefinition: aws.String(e.cfg.homeTask),
		LaunchType:     ecstypes.LaunchTypeFargate,
		Count:          aws.Int32(1),
		StartedBy:      aws.String(e.homeTaskStartedBy()),
		// Billing only, as on the workspace's own service (upsertService): Fargate bills the
		// task, so without these its minutes land in nobody's share of the bill. The same
		// keys and af-role, so the per-member and per-role views count it as this member's
		// workspace (ADR 0048). ECS authorizes them as ecs:TagResource on the new task
		// (CpHomeOpsPolicy, 30-ingress).
		Tags: appendECSTenantTag(e.tenantSlug, []ecstypes.Tag{
			{Key: aws.String("af-membership"), Value: aws.String(e.membershipID)},
			{Key: aws.String("af-role"), Value: aws.String("workspace")},
		}),
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        e.cfg.subnets,
				SecurityGroups: []string{e.cfg.securityGroup},
				AssignPublicIp: ecstypes.AssignPublicIpDisabled,
			},
		},
		Overrides: &ecstypes.TaskOverride{
			ContainerOverrides: []ecstypes.ContainerOverride{{
				Name: aws.String(ecsHomeTaskContainer),
				Environment: []ecstypes.KeyValuePair{
					{Name: aws.String("AF_HOME_OP"), Value: aws.String(string(what))},
					{Name: aws.String("AF_HOME_MEMBERSHIP"), Value: aws.String(e.membershipID)},
				},
			}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("run the home task: %w", err)
	}
	for _, f := range out.Failures {
		// A RunTask that placed nothing answers 200 with a failure list; its reason is the
		// only useful sentence in it.
		return "", fmt.Errorf("ECS refused the home task: %s %s", aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	if len(out.Tasks) == 0 {
		return "", fmt.Errorf("ECS started no home task and gave no reason")
	}
	return aws.ToString(out.Tasks[0].TaskArn), nil
}

// waitHomeTask polls the task until it has stopped and reads its container's exit code.
// A task that stopped without one never ran the command — an image that would not pull, a
// mount that failed — and its stop reason is the sentence that says which.
func (e *ecsRuntime) waitHomeTask(ctx context.Context, arn string, what HomeWipe) error {
	for {
		out, err := e.tasks.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(e.cfg.cluster),
			Tasks:   []string{arn},
		})
		if err != nil {
			return fmt.Errorf("describe the home task: %w", err)
		}
		for _, f := range out.Failures {
			return fmt.Errorf("describe the home task %s: %s %s", arn, aws.ToString(f.Reason), aws.ToString(f.Detail))
		}
		if len(out.Tasks) == 0 {
			return fmt.Errorf("the home task %s is not known to ECS", arn)
		}
		t := out.Tasks[0]
		if aws.ToString(t.LastStatus) == string(ecstypes.DesiredStatusStopped) {
			return homeTaskOutcome(t, what)
		}
		if err := e.pause(ctx); err != nil {
			return err
		}
	}
}

func homeTaskOutcome(t ecstypes.Task, what HomeWipe) error {
	for _, c := range t.Containers {
		if aws.ToString(c.Name) != ecsHomeTaskContainer {
			continue
		}
		if c.ExitCode == nil {
			return fmt.Errorf("the home task (%s) stopped before it ran: %s",
				what, strings.TrimSpace(aws.ToString(t.StoppedReason)+" "+aws.ToString(c.Reason)))
		}
		switch code := aws.ToInt32(c.ExitCode); code {
		case HomeOpExitOK:
			return nil
		case HomeOpExitRefused:
			return fmt.Errorf("the home task (%s) refused and removed nothing (exit %d); see its log", what, code)
		default:
			return fmt.Errorf("the home task (%s) failed with exit %d and may have removed part of it; see its log", what, code)
		}
	}
	return fmt.Errorf("the home task (%s) stopped without a %s container: %s",
		what, ecsHomeTaskContainer, aws.ToString(t.StoppedReason))
}

func (e *ecsRuntime) pause(ctx context.Context) error {
	d := e.homeTaskPoll
	if d <= 0 {
		d = ecsHomeTaskPoll
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
