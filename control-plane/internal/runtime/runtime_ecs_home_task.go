// runtime_ecs_home_task.go — Recreate, Clean home and Destroy on Fargate (ADR 0045
// decision 31, #1260).
//
// The home is an EFS directory the CP cannot mount. The stack declares a one-shot task
// definition (deploy/aws/ecs/cfn/30-ingress.yaml, HomeOpsTaskDef) that mounts the file
// system and runs `af-cp efs-home-op` (home_task.go); the CP starts it with the operation
// and the membership in its environment and reads the exit code back with DescribeTasks.
//
// A Fargate task takes minutes from cold, so every caller runs these after its request has
// been answered (HomeWipeInBackground). A Start or another operation on the same home is
// refused while a task may still be running. The record of that is an SSM parameter,
// /af-ws/<workspace>/home-task, written before RunTask and holding the task's ARN after
// it: GetParameter answers consistently and survives a CP restart, where ECS's own
// ListTasks and DescribeTasks are eventually consistent and can miss a task RunTask has
// just returned. ListTasks (startedBy=af-home/<membership>) is asked as well, for a task
// whose marker is gone.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
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

const (
	// homeTaskMissingGrace is how long a task RunTask returned may stay unknown to
	// DescribeTasks before it counts as gone. ECS documents RunTask as eventually
	// consistent; past this a task that is still MISSING has stopped and been forgotten.
	homeTaskMissingGrace = 2 * time.Minute
	// homeTaskPendingGrace bounds a marker written before a RunTask whose answer was lost
	// (a CP that died, a timeout after the request was sent). ListTasks has long caught up
	// by then, so it alone decides after this.
	homeTaskPendingGrace = 15 * time.Minute
	// homeTaskMarkerPending is the marker's value until RunTask has returned an ARN.
	homeTaskMarkerPending = "pending"
	// homeTaskDescribeRetries is how many failed DescribeTasks in a row end a wait. The
	// marker stays, so the home stays refused until a later check can read the task.
	homeTaskDescribeRetries = 12
)

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

func (e *ecsRuntime) homeTaskMarker() string { return fmt.Sprintf("/af-ws/%s/home-task", e.name) }

func (e *ecsRuntime) markHomeTask(ctx context.Context, value string) error {
	if _, err := e.ssm.PutParameter(ctx, &ssm.PutParameterInput{
		Name: aws.String(e.homeTaskMarker()), Value: aws.String(value),
		Type: ssmtypes.ParameterTypeString, Overwrite: aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("record the home task: %w", err)
	}
	return nil
}

func (e *ecsRuntime) clearHomeTaskMarker(ctx context.Context) {
	if _, err := e.ssm.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(e.homeTaskMarker())}); err != nil && !isAWSNotFound(err) {
		// Left behind it only keeps the home refused until its task is seen stopped.
		log.Printf("ecs: drop the home task marker of %s: %v", e.name, err)
	}
}

// homeTaskInFlight reports whether a home task for this member may not have stopped yet:
// the marker first, then ECS's own listing.
func (e *ecsRuntime) homeTaskInFlight(ctx context.Context) (bool, error) {
	if busy, err := e.markedHomeTaskBusy(ctx); err != nil || busy {
		return busy, err
	}
	// startedBy has to be the only filter of a ListTasks; the default desired status
	// RUNNING is what "not stopped yet" means.
	out, err := e.tasks.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:   aws.String(e.cfg.cluster),
		StartedBy: aws.String(e.homeTaskStartedBy()),
	})
	if err != nil {
		return false, fmt.Errorf("list home tasks: %w", err)
	}
	return len(out.TaskArns) > 0, nil
}

// markedHomeTaskBusy reads the marker. A marker whose task has stopped, or that has
// outlived its grace, is dropped and reads as not busy.
func (e *ecsRuntime) markedHomeTaskBusy(ctx context.Context) (bool, error) {
	out, err := e.ssm.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(e.homeTaskMarker())})
	if isAWSNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the home task marker: %w", err)
	}
	value := aws.ToString(out.Parameter.Value)
	age := time.Since(aws.ToTime(out.Parameter.LastModifiedDate))
	if value == homeTaskMarkerPending {
		if age < homeTaskPendingGrace {
			return true, nil
		}
		e.clearHomeTaskMarker(ctx)
		return false, nil
	}
	t, known, err := e.describeHomeTask(ctx, value)
	if err != nil {
		return false, err
	}
	switch {
	case known && aws.ToString(t.LastStatus) == string(ecstypes.DesiredStatusStopped):
	case !known && age >= homeTaskMissingGrace:
	default:
		return true, nil
	}
	e.clearHomeTaskMarker(ctx)
	return false, nil
}

// describeHomeTask reads one task. known=false when ECS answers MISSING or nothing, which
// right after RunTask is eventual consistency, not proof the task is gone.
func (e *ecsRuntime) describeHomeTask(ctx context.Context, arn string) (ecstypes.Task, bool, error) {
	out, err := e.tasks.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(e.cfg.cluster),
		Tasks:   []string{arn},
	})
	if err != nil {
		return ecstypes.Task{}, false, fmt.Errorf("describe the home task: %w", err)
	}
	if len(out.Tasks) == 0 {
		return ecstypes.Task{}, false, nil
	}
	return out.Tasks[0], true, nil
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
	// The marker goes first: a CP that dies between RunTask and its answer still leaves a
	// record that something may be running.
	if err := e.markHomeTask(ctx, homeTaskMarkerPending); err != nil {
		return err
	}
	arn, err := e.startHomeTask(ctx, what)
	if err != nil {
		// A refusal ECS answered (an API error, a failure list) started nothing. Anything
		// else — a timeout after the request was sent — may have, so the pending marker
		// stays and keeps the home refused for its grace.
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) || errors.Is(err, errHomeTaskNotPlaced) {
			e.clearHomeTaskMarker(ctx)
		}
		return err
	}
	if err := e.markHomeTask(ctx, arn); err != nil {
		// The pending marker is still there and blocks for its grace; the wait goes on.
		log.Printf("ecs: %v", err)
	}
	stopped, err := e.waitHomeTask(ctx, arn, what)
	if stopped {
		e.clearHomeTaskMarker(ctx)
	}
	return err
}

// errHomeTaskNotPlaced marks a RunTask that ECS answered without starting a task.
var errHomeTaskNotPlaced = errors.New("ECS did not place the home task")

// waitServiceTasksGone returns once every task of the workspace has reached STOPPED. A
// service that is back at desired 1 has been started under the operation, which the lease
// should have made impossible; the home is left alone.
//
// The service's running count is not that proof: it drops when a task leaves RUNNING, and
// a STOPPING task is still inside its stop timeout, its processes still writing the home.
// The tasks are found by the workspace's task definition family (registerTaskDef names it
// after the workspace), which works while the service drains and after it is INACTIVE.
func (e *ecsRuntime) waitServiceTasksGone(ctx context.Context) error {
	for {
		s, ok, err := e.describeService(ctx)
		if err != nil {
			return fmt.Errorf("describe service %s: %w", e.name, err)
		}
		if ok && s.DesiredCount > 0 {
			return fmt.Errorf("service %s is at desired %d; its home is left alone", e.name, s.DesiredCount)
		}
		alive, err := e.workspaceTasksAlive(ctx)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		if err := e.pause(ctx); err != nil {
			return err
		}
	}
}

// workspaceTasksAlive reports whether any task of the workspace's family has not reached
// STOPPED. Both desired statuses are listed: a stopping task is desired STOPPED already.
func (e *ecsRuntime) workspaceTasksAlive(ctx context.Context) (bool, error) {
	var arns []string
	for _, ds := range []ecstypes.DesiredStatus{ecstypes.DesiredStatusRunning, ecstypes.DesiredStatusStopped} {
		var token *string
		for {
			out, err := e.tasks.ListTasks(ctx, &ecs.ListTasksInput{
				Cluster: aws.String(e.cfg.cluster), Family: aws.String(e.name),
				DesiredStatus: ds, NextToken: token,
			})
			if err != nil {
				return false, fmt.Errorf("list the tasks of %s: %w", e.name, err)
			}
			arns = append(arns, out.TaskArns...)
			if aws.ToString(out.NextToken) == "" {
				break
			}
			token = out.NextToken
		}
	}
	for len(arns) > 0 {
		n := min(len(arns), 100) // DescribeTasks takes at most 100
		out, err := e.tasks.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(e.cfg.cluster), Tasks: arns[:n],
		})
		if err != nil {
			return false, fmt.Errorf("describe the tasks of %s: %w", e.name, err)
		}
		for _, t := range out.Tasks {
			if aws.ToString(t.LastStatus) != string(ecstypes.DesiredStatusStopped) {
				return true, nil
			}
		}
		arns = arns[n:]
	}
	return false, nil
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
		return "", fmt.Errorf("%w: %s %s", errHomeTaskNotPlaced, aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	if len(out.Tasks) == 0 {
		return "", fmt.Errorf("%w and gave no reason", errHomeTaskNotPlaced)
	}
	return aws.ToString(out.Tasks[0].TaskArn), nil
}

// waitHomeTask polls the task until it has stopped and reads its container's exit code.
// stopped says the outcome is known — the task reached STOPPED, or stayed unknown to ECS
// past homeTaskMissingGrace. Anything else (the context ended, DescribeTasks kept failing)
// leaves the task possibly running, and the caller keeps its marker.
//
// A task that stopped without an exit code never ran the command — an image that would not
// pull, a mount that failed — and its stop reason is the sentence that says which.
func (e *ecsRuntime) waitHomeTask(ctx context.Context, arn string, what HomeWipe) (stopped bool, err error) {
	started, failures := time.Now(), 0
	for {
		t, known, err := e.describeHomeTask(ctx, arn)
		switch {
		case err != nil:
			if failures++; failures >= homeTaskDescribeRetries {
				return false, err
			}
		case !known:
			// Eventual consistency right after RunTask; past the grace it is gone.
			if time.Since(started) >= e.missingGrace() {
				return true, fmt.Errorf("the home task (%s) %s is not known to ECS; its outcome is unknown", what, arn)
			}
		case aws.ToString(t.LastStatus) == string(ecstypes.DesiredStatusStopped):
			return true, homeTaskOutcome(t, what)
		default:
			failures = 0
		}
		if err := e.pause(ctx); err != nil {
			return false, err
		}
	}
}

func (e *ecsRuntime) missingGrace() time.Duration {
	if e.homeTaskMissingGrace > 0 {
		return e.homeTaskMissingGrace
	}
	return homeTaskMissingGrace
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
