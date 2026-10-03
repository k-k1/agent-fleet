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
//
// The marker stays although the CP now keeps a record of each operation in its database
// (#1544): the record is the CP's, and this adapter has no database (ADR 0012), while every
// Start passes through here. The marker is the guard; the record is what finishes the
// operation and resolves a marker that would otherwise wait for an operator.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
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

// Only a task seen STOPPED releases its home. MISSING, an empty answer, an error or time
// passing prove nothing — ECS does not bound how long a task RunTask returned stays
// invisible — so each leaves the marker, and the home refused, in place. The CP's record
// of the operation (HomeTaskBinding) is what resolves a marker that never would on its
// own: its reconciler asks RunTask again under the operation's clientToken, which names the
// task a lost answer started, or starts the removal again once ECS has forgotten both. A
// pending marker without a token is released when ECS proves its task stopped
// (releaseProvenPending). Only such a marker ECS cannot prove, and one whose
// task was not seen stopped within the time the token is sure to last (mayRunAgain), are
// the operator's to delete (guide/ref/deploy-targets.md, note 7).
const (
	// homeTaskMissingGrace bounds how long one wait keeps polling a task ECS does not know.
	// Ending the wait keeps the marker; it only stops this CP from waiting.
	homeTaskMissingGrace = 2 * time.Minute
	// homeTaskMarkerPending is the marker's value until RunTask has returned an ARN. A
	// marker left pending (a RunTask whose answer was lost) names no task that could ever
	// be seen STOPPED; the operation's record and its clientToken find that task.
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

// markedHomeTaskBusy reads the marker. Only a marker whose task is seen STOPPED is dropped
// and reads as not busy.
func (e *ecsRuntime) markedHomeTaskBusy(ctx context.Context) (bool, error) {
	out, err := e.ssm.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(e.homeTaskMarker())})
	if isAWSNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the home task marker: %w", err)
	}
	value := aws.ToString(out.Parameter.Value)
	target, _ := parseMarker(value)
	if target == homeTaskMarkerPending {
		if e.releaseProvenPending(ctx, value, aws.ToTime(out.Parameter.LastModifiedDate)) {
			return false, nil
		}
		e.logStuckMarker(value, out.Parameter.LastModifiedDate, "RunTask's answer was never recorded")
		return true, nil
	}
	t, known, err := e.describeHomeTask(ctx, target)
	if err != nil {
		return false, err
	}
	if !known {
		e.logStuckMarker(value, out.Parameter.LastModifiedDate, "ECS does not know the task")
		return true, nil
	}
	if aws.ToString(t.LastStatus) != string(ecstypes.DesiredStatusStopped) {
		return true, nil
	}
	e.clearHomeTaskMarker(ctx)
	return false, nil
}

// logStuckMarker names the marker an operator may have to clear, once it is old enough that
// propagation no longer explains it.
func (e *ecsRuntime) logStuckMarker(value string, at *time.Time, why string) {
	if time.Since(aws.ToTime(at)) < homeTaskMissingGrace {
		return
	}
	log.Printf("ecs: the home of %s stays refused: %s (marker %s = %q). The CP resolves it while a "+
		"home operation record is open for the workspace, or while ECS lists its task stopped; with neither, "+
		"and no task started by %s running, delete the marker to release it", e.name, why, e.homeTaskMarker(), value, e.homeTaskStartedBy())
}

// homeTaskProofMargin is how far before a pending marker's write a task of the same member
// makes the proof ambiguous. The marker is written before RunTask, so its own task was
// created after it. A task created shortly before it is an earlier operation's, and
// SSM's and ECS's clocks are not exact enough to tell the two apart by seconds.
const homeTaskProofMargin = time.Minute

// errHomeTaskProofIncomplete is an ECS answer that does not account for every task listed.
var errHomeTaskProofIncomplete = errors.New("ECS did not describe every listed home task")

// releaseProvenPending drops a pending marker whose task ECS shows finished, and reports
// whether it did. A CP lost between RunTask and its answer leaves one. A marker that carries
// an operation's token is that operation's record's to resolve (the reconciler asks RunTask
// again under the token, which names the task) and is never released here. A marker
// without one (a CP before #1544 wrote it, or a path that bound no record) otherwise waited
// for an operator.
//
// The proof is all of: the marker is older than homeTaskMissingGrace (listings lag), no
// task started for this member is listed running, every task listed under the home task's
// family with desired status STOPPED is described with its startedBy and creation time,
// every one of this member's reads STOPPED, none was created within homeTaskProofMargin
// before the marker, and exactly one was created after it — and it exited 0. Anything else
// is ambiguous, and the marker stays: none (ECS forgets a stopped task after about an hour),
// two, one still stopping, one that failed, an incomplete answer. Creation time is what
// separates an earlier operation's task, which matters for the golden seed: its membership
// is reused by every bake.
//
// Every caller holds the member's lifecycle lease, and a CP that wrote a marker without a
// token is gone or still holds that lease, so no operation of this member writes a marker in
// between; it is read again before the delete all the same.
func (e *ecsRuntime) releaseProvenPending(ctx context.Context, value string, at time.Time) bool {
	if _, token := parseMarker(value); token != "" {
		return false
	}
	if at.IsZero() || time.Since(at) < homeTaskMissingGrace {
		return false
	}
	running, err := e.tasks.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:   aws.String(e.cfg.cluster),
		StartedBy: aws.String(e.homeTaskStartedBy()),
	})
	if err != nil || len(running.TaskArns) > 0 {
		return false
	}
	mine, err := e.stoppedHomeTasks(ctx)
	if err != nil {
		log.Printf("ecs: look for the task behind the pending home marker of %s: %v", e.name, err)
		return false
	}
	var found []ecstypes.Task
	for _, t := range mine {
		created := aws.ToTime(t.CreatedAt)
		switch {
		case aws.ToString(t.LastStatus) != string(ecstypes.DesiredStatusStopped):
			return false // still stopping: it may be writing the home
		case !created.Before(at):
			found = append(found, t)
		case !created.Before(at.Add(-homeTaskProofMargin)):
			return false // too close to the marker to say whose it is
		}
	}
	if len(found) != 1 || homeTaskOutcome(found[0], "") != nil {
		return false
	}
	t := found[0]
	cur, err := e.ssm.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(e.homeTaskMarker())})
	if err != nil || aws.ToString(cur.Parameter.Value) != value || !aws.ToTime(cur.Parameter.LastModifiedDate).Equal(at) {
		return false
	}
	if _, err := e.ssm.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(e.homeTaskMarker())}); err != nil && !isAWSNotFound(err) {
		log.Printf("ecs: drop the home task marker of %s: %v", e.name, err)
		return false
	}
	rel := HomeMarkerRelease{Workspace: e.name, MembershipID: e.membershipID, Marker: e.homeTaskMarker(),
		Value: value, TaskARN: aws.ToString(t.TaskArn)}
	log.Printf("ecs: released the pending home marker of %s (%s = %q): its task %s, started by %s after the "+
		"marker was written, stopped with exit 0 and none is running", e.name, rel.Marker, value, rel.TaskARN,
		e.homeTaskStartedBy())
	if f := homeMarkerReleased.Load(); f != nil {
		(*f)(rel)
	}
	return true
}

// stoppedHomeTasks describes every task ECS lists under the home task's family with desired
// status STOPPED, and returns this member's. A ListTasks filtered by startedBy takes no other
// filter, so the family is listed and startedBy read from each. An answer that leaves any
// listed task unaccounted for — a failure entry, a task missing from the answer, one without
// startedBy or, for this member's, without its creation time — is errHomeTaskProofIncomplete:
// the task it hides could be this member's, still running.
func (e *ecsRuntime) stoppedHomeTasks(ctx context.Context) ([]ecstypes.Task, error) {
	var arns []string
	var token *string
	for {
		out, err := e.tasks.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster: aws.String(e.cfg.cluster), Family: aws.String(e.cfg.homeTask),
			DesiredStatus: ecstypes.DesiredStatusStopped, NextToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list stopped home tasks: %w", err)
		}
		arns = append(arns, out.TaskArns...)
		if aws.ToString(out.NextToken) == "" {
			break
		}
		token = out.NextToken
	}
	var mine []ecstypes.Task
	for len(arns) > 0 {
		n := min(len(arns), 100) // DescribeTasks takes at most 100
		batch := arns[:n]
		arns = arns[n:]
		out, err := e.tasks.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(e.cfg.cluster), Tasks: batch})
		if err != nil {
			return nil, fmt.Errorf("describe stopped home tasks: %w", err)
		}
		if len(out.Failures) > 0 {
			return nil, fmt.Errorf("%w: %s %s", errHomeTaskProofIncomplete,
				aws.ToString(out.Failures[0].Arn), aws.ToString(out.Failures[0].Reason))
		}
		described := map[string]bool{}
		for _, t := range out.Tasks {
			described[aws.ToString(t.TaskArn)] = true
			if t.StartedBy == nil {
				return nil, fmt.Errorf("%w: %s has no startedBy", errHomeTaskProofIncomplete, aws.ToString(t.TaskArn))
			}
			if aws.ToString(t.StartedBy) != e.homeTaskStartedBy() {
				continue
			}
			if t.CreatedAt == nil {
				return nil, fmt.Errorf("%w: %s has no creation time", errHomeTaskProofIncomplete, aws.ToString(t.TaskArn))
			}
			mine = append(mine, t)
		}
		for _, arn := range batch {
			if !described[arn] {
				return nil, fmt.Errorf("%w: %s is missing from the answer", errHomeTaskProofIncomplete, arn)
			}
		}
	}
	return mine, nil
}

// HomeMarkerRelease is a pending home marker the adapter released on ECS's evidence alone
// (releaseProvenPending), for the CP's audit log.
type HomeMarkerRelease struct {
	Workspace, MembershipID, Marker, Value, TaskARN string
}

var homeMarkerReleased atomic.Pointer[func(HomeMarkerRelease)]

// OnHomeMarkerReleased sets what is told of each such release. The adapter keeps no
// database (ADR 0012); the CP writes the audit entry.
func OnHomeMarkerReleased(f func(HomeMarkerRelease)) {
	if f == nil {
		homeMarkerReleased.Store(nil)
		return
	}
	homeMarkerReleased.Store(&f)
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
//
// Bound to an operation record (BindHomeTask), RunTask carries the record's id as its
// clientToken, and an error that leaves a task possibly running wraps
// ErrHomeTaskUnresolved: the record stays open and the CP's reconciler finishes it. With
// Resume set, the run is that reconciler's: its own marker is no refusal, and a task an
// earlier attempt started is adopted rather than started again.
func (e *ecsRuntime) runHomeTask(ctx context.Context, what HomeWipe) error {
	if !e.homePortsReady() {
		return ErrHomeWipeUnsupported
	}
	// The task would refuse it as well; refusing here keeps a bad id from costing a task.
	if !ValidMembershipID(e.membershipID) {
		return fmt.Errorf("membership id %q cannot name a home", e.membershipID)
	}
	b := e.homeBinding
	mayRun := false // a task of this operation may already exist
	if b.Resume {
		arn, sent, marked, err := e.resumeTarget(ctx, b)
		if err != nil {
			return unresolved(err)
		}
		if arn != "" {
			_, known, err := e.describeHomeTask(ctx, arn)
			if err != nil {
				return unresolved(err)
			}
			if known {
				b.started(arn)
				return e.finishHomeTask(ctx, arn, what)
			}
		}
		mayRun = sent || arn != ""
		if mayRun {
			if err := e.mayRunAgain(ctx, b, arn, marked); err != nil {
				return unresolved(err)
			}
		}
	} else {
		busy, err := e.homeTaskInFlight(ctx)
		if err != nil {
			return err
		}
		if busy {
			return ErrHomeTaskInFlight
		}
	}
	fail := func(err error) error {
		if mayRun {
			return unresolved(err)
		}
		return err
	}
	// Stop only set the desired count to 0. The old task holds the home until it exits,
	// and removing files under a workspace that is still writing them leaves a half-home.
	if err := e.waitServiceTasksGone(ctx); err != nil {
		return fail(err)
	}
	// The marker goes first: a CP that dies between RunTask and its answer still leaves a
	// record that something may be running.
	if err := e.markHomeTask(ctx, markerValue(homeTaskMarkerPending, b.Token)); err != nil {
		return fail(err)
	}
	if err := b.sending(); err != nil {
		// Nothing was sent: the marker this attempt wrote names no task, and left behind
		// it would refuse the home with no record to resolve it. An earlier attempt's
		// marker stays.
		if !mayRun {
			e.clearHomeTaskMarker(ctx)
		}
		return fail(fmt.Errorf("record the RunTask: %w", err))
	}
	arn, err := e.startHomeTask(ctx, what, b.Token)
	if err != nil {
		// A failure list is ECS's answer for this token: the token is sent again only while
		// it surely names the first call (mayRunAgain), whose answer this then is, or once
		// that call's task is gone. A client fault after an earlier attempt proves less —
		// that attempt may have started a task before whatever is refused now.
		if errors.Is(err, errHomeTaskNotPlaced) || (runTaskStartedNothing(err) && !mayRun) {
			e.clearHomeTaskMarker(ctx)
			return err
		}
		return unresolved(err)
	}
	b.started(arn)
	if err := e.markHomeTask(ctx, markerValue(arn, b.Token)); err != nil {
		// The pending marker is still there and keeps the home refused; the wait goes on,
		// and it is dropped once the task is seen STOPPED.
		log.Printf("ecs: %v", err)
	}
	return e.finishHomeTask(ctx, arn, what)
}

// homeTokenSafeFor is how long after an operation's first RunTask the same token surely
// still names that call's task. ECS keeps a RunTask token for 24 hours or the task's
// lifetime plus one hour, whichever is shorter; past 24 hours a repeat may start a second
// task while the first still runs.
const homeTokenSafeFor = 23 * time.Hour

// mayRunAgain decides whether a resumed operation, whose earlier attempt may have started
// a task (arn, or a RunTask sent with no answer kept), may send RunTask again. It may not
// while any task started for this member is listed running: that is the earlier one, or a
// stranger's, and either is still removing files. Past that, an empty listing and a
// MISSING task prove nothing (ECS bounds neither), so RunTask goes out again only where
// it cannot start a second task beside a first one still running: while the token surely
// still names the first call, which ECS then answers again, or once the operation's own
// marker is gone — this CP drops it only when nothing of it can run, and deleting it is
// the operator's release after checking ECS.
func (e *ecsRuntime) mayRunAgain(ctx context.Context, b HomeTaskBinding, arn string, marked bool) error {
	out, err := e.tasks.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:   aws.String(e.cfg.cluster),
		StartedBy: aws.String(e.homeTaskStartedBy()),
	})
	if err != nil {
		return fmt.Errorf("list home tasks: %w", err)
	}
	if len(out.TaskArns) > 0 {
		return fmt.Errorf("a home task (%s) is still running for %s; waiting for it to stop", out.TaskArns[0], e.membershipID)
	}
	if !marked || b.SentAt.IsZero() || time.Since(b.SentAt) < homeTokenSafeFor {
		return nil
	}
	task := arn
	if task == "" {
		task = "(answer lost)"
	}
	log.Printf("ecs: the home operation %s on %s sent RunTask %s ago and has not seen its task %s stop; its "+
		"token may start a second task now, so it is not sent again. If no task started by %s is running, "+
		"delete the marker %s to let it run again", b.Token, e.name, time.Since(b.SentAt).Round(time.Minute),
		task, e.homeTaskStartedBy(), e.homeTaskMarker())
	return fmt.Errorf("RunTask went out over %s ago and its task %s was never seen stopped; its token no longer "+
		"proves which task it names", homeTokenSafeFor, task)
}

// finishHomeTask waits for arn to stop and drops the marker once it has. An outcome the
// wait could not read is unresolved: the task may still be running.
func (e *ecsRuntime) finishHomeTask(ctx context.Context, arn string, what HomeWipe) error {
	stopped, err := e.waitHomeTask(ctx, arn, what)
	if stopped {
		e.clearHomeTaskMarker(ctx)
		return err
	}
	return unresolved(err)
}

// markerValue is what the marker holds for target (homeTaskMarkerPending or a task ARN)
// under an operation's token: the token follows after a space, so a resumed operation adopts
// only its own task. Without a token the value is target alone, as a CP before #1544 wrote.
func markerValue(target, token string) string {
	if token == "" {
		return target
	}
	return target + " " + token
}

// parseMarker splits a marker value into its target and token ("" for a marker without one).
func parseMarker(value string) (target, token string) {
	target, token, _ = strings.Cut(value, " ")
	return target, token
}

// resumeTarget is the task a resumed operation looks for: the one its record names, else
// the one its own marker names (an earlier attempt wrote the marker and died before its
// record). sent says a RunTask of this operation may have gone out. A marker of another
// operation, or one without a token, is never this operation's task: it is a home task in
// flight like any other, and while it refuses the home this resume waits.
func (e *ecsRuntime) resumeTarget(ctx context.Context, b HomeTaskBinding) (arn string, sent, marked bool, err error) {
	sent = !b.SentAt.IsZero()
	out, err := e.ssm.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(e.homeTaskMarker())})
	switch {
	case isAWSNotFound(err):
		return b.TaskARN, sent, false, nil
	case err != nil:
		return "", false, false, fmt.Errorf("read the home task marker: %w", err)
	}
	target, token := parseMarker(aws.ToString(out.Parameter.Value))
	if token != b.Token || b.Token == "" {
		busy, err := e.markedHomeTaskBusy(ctx)
		if err != nil {
			return "", false, false, err
		}
		if busy {
			return "", false, false, ErrHomeTaskInFlight
		}
		return b.TaskARN, sent, false, nil
	}
	if b.TaskARN != "" {
		return b.TaskARN, true, true, nil
	}
	if target != homeTaskMarkerPending {
		return target, true, true, nil
	}
	return "", true, true, nil
}

// ErrHomeTaskUnresolved wraps an error after which a home task may still be running, or
// may have run with an outcome nobody read: a RunTask whose answer was lost, a wait that
// ended before the task was seen STOPPED. The CP keeps the operation's record open and its
// reconciler finishes it (#1544); it is never the operation's outcome.
var ErrHomeTaskUnresolved = errors.New("the home task's outcome is not known yet")

func unresolved(err error) error {
	if err == nil || errors.Is(err, ErrHomeTaskUnresolved) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrHomeTaskUnresolved, err)
}

// HomeTaskBinding ties the home task of one operation to the CP's durable record of it
// (#1544). The adapter keeps no database (ADR 0012), so the record's id and the task's ARN
// come and go through this.
type HomeTaskBinding struct {
	// Token is the RunTask clientToken: the record's id, the same on every attempt, so
	// RunTask asked again answers the task the first call started. ECS keeps a token for
	// 24 hours or the task's lifetime plus one hour, whichever is shorter; after that the
	// same token starts a new task, which repeats the removal on a home nothing could
	// start in between (the record and the marker keep it refused).
	Token string
	// TaskARN is the task an earlier attempt recorded, if any.
	TaskARN string
	// Resume marks the reconciler's attempt at an operation an earlier one left open.
	Resume bool
	// Started records the task's ARN as soon as RunTask has answered it.
	Started func(arn string)
	// SentAt is when an attempt first sent RunTask under Token (zero: none was sent).
	SentAt time.Time
	// Sending records, before RunTask goes out, that it is about to; an error keeps it
	// from going out. Its time is the one SentAt carries to a later attempt.
	Sending func() error
}

func (b HomeTaskBinding) sending() error {
	if b.Sending == nil {
		return nil
	}
	return b.Sending()
}

func (b HomeTaskBinding) started(arn string) {
	if b.Started != nil {
		b.Started(arn)
	}
}

// homeTaskBinder is claimed by an adapter that may run the stack's home task.
var (
	_ homeTaskBinder = (*ecsRuntime)(nil)
	_ homeTaskBinder = (*ecsEC2Runtime)(nil)
)

type homeTaskBinder interface {
	BindHomeTask(HomeTaskBinding) bool
}

// BindHomeTask binds rt's next home task to an operation record. false where rt runs no
// home task, and the operation then needs no record.
func BindHomeTask(rt Runtime, b HomeTaskBinding) bool {
	h, ok := rt.(homeTaskBinder)
	return ok && h.BindHomeTask(b)
}

// RunsHomeTask reports whether an operation on rt's home may run the stack's home task,
// without binding anything.
func RunsHomeTask(rt Runtime) bool {
	h, ok := rt.(homeTaskBinder)
	return ok && h.BindHomeTask(HomeTaskBinding{})
}

// BindHomeTask satisfies homeTaskBinder.
func (e *ecsRuntime) BindHomeTask(b HomeTaskBinding) bool {
	if !e.homePortsReady() {
		return false
	}
	e.homeBinding = b
	return true
}

// runTaskStartedNothing reports whether a failed RunTask is known to have started no task:
// ECS answered with a failure list (errHomeTaskNotPlaced), or refused the request as the
// caller's fault (a 4xx: invalid parameters, AccessDenied, a missing task definition).
// Everything else — a server fault (5xx, ServerException) even after the SDK's retries, an
// error with no fault, a timeout after the request was sent — may have placed one, so the
// pending marker stays and only an operator clears it.
func runTaskStartedNothing(err error) bool {
	if errors.Is(err, errHomeTaskNotPlaced) {
		return true
	}
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorFault() == smithy.FaultClient
}

// errHomeTaskNotPlaced marks a RunTask that ECS answered without starting a task.
var errHomeTaskNotPlaced = errors.New("ECS did not place the home task")

// stoppedTasks holds, by workspace name, the tasks Stop saw running: the wait that follows
// must see each of them STOPPED even if a listing no longer shows it. Process-local like
// homeClearing; another replica's wait relies on the service counts and its own listings.
var stoppedTasks sync.Map // workspace name -> []string

// waitServiceTasksGone returns once every task of the workspace is known to be STOPPED. A
// service that is back at desired 1 has been started under the operation, which the lease
// should have made impossible; the home is left alone.
//
// No single read is proof. The service's counts drop when a task leaves RUNNING, while a
// STOPPING task is still inside its stop timeout, its processes still writing the home; a
// listing, being eventually consistent, can miss a task. So the wait needs all of: the
// service counting nothing running or pending, and every task it has ever seen — what Stop
// captured and what any listing returned — described as STOPPED. A task that vanishes from
// a listing, or that DescribeTasks answers MISSING for, is not confirmed and keeps the
// wait going; the caller's budget ends it with the home left alone. The tasks are listed by
// the workspace's task definition family (registerTaskDef names it after the workspace),
// which works while the service drains and after it is INACTIVE.
func (e *ecsRuntime) waitServiceTasksGone(ctx context.Context) error {
	pending := map[string]bool{} // arn -> seen STOPPED
	if v, ok := stoppedTasks.Load(e.name); ok {
		for _, arn := range v.([]string) {
			pending[arn] = false
		}
	}
	for {
		s, ok, err := e.describeService(ctx)
		if err != nil {
			return fmt.Errorf("describe service %s: %w", e.name, err)
		}
		if ok && s.DesiredCount > 0 {
			return fmt.Errorf("service %s is at desired %d; its home is left alone", e.name, s.DesiredCount)
		}
		counted := ok && (s.RunningCount > 0 || s.PendingCount > 0)
		if err := e.confirmWorkspaceTasks(ctx, pending); err != nil {
			return err
		}
		if !counted && allTrue(pending) {
			stoppedTasks.Delete(e.name)
			return nil
		}
		if err := e.pause(ctx); err != nil {
			return err
		}
	}
}

func allTrue(m map[string]bool) bool {
	for _, v := range m {
		if !v {
			return false
		}
	}
	return true
}

// captureWorkspaceTasks records the tasks running before a Stop, for waitServiceTasksGone.
func (e *ecsRuntime) captureWorkspaceTasks(ctx context.Context) error {
	arns, err := e.listWorkspaceTasks(ctx, ecstypes.DesiredStatusRunning)
	if err != nil {
		return err
	}
	if len(arns) > 0 {
		stoppedTasks.Store(e.name, arns)
	}
	return nil
}

// confirmWorkspaceTasks adds every task a listing returns to pending and marks those
// described as STOPPED. Both desired statuses are listed: a stopping task is desired
// STOPPED already. A mark once set stays: a task seen STOPPED has let go of the home.
func (e *ecsRuntime) confirmWorkspaceTasks(ctx context.Context, pending map[string]bool) error {
	for _, ds := range []ecstypes.DesiredStatus{ecstypes.DesiredStatusRunning, ecstypes.DesiredStatusStopped} {
		arns, err := e.listWorkspaceTasks(ctx, ds)
		if err != nil {
			return err
		}
		for _, arn := range arns {
			if _, seen := pending[arn]; !seen {
				pending[arn] = false
			}
		}
	}
	var open []string
	for arn, stopped := range pending {
		if !stopped {
			open = append(open, arn)
		}
	}
	for len(open) > 0 {
		n := min(len(open), 100) // DescribeTasks takes at most 100
		out, err := e.tasks.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(e.cfg.cluster), Tasks: open[:n],
		})
		if err != nil {
			return fmt.Errorf("describe the tasks of %s: %w", e.name, err)
		}
		for _, t := range out.Tasks {
			if aws.ToString(t.LastStatus) == string(ecstypes.DesiredStatusStopped) {
				pending[aws.ToString(t.TaskArn)] = true
			}
		}
		open = open[n:]
	}
	return nil
}

func (e *ecsRuntime) listWorkspaceTasks(ctx context.Context, ds ecstypes.DesiredStatus) ([]string, error) {
	var arns []string
	var token *string
	for {
		out, err := e.tasks.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster: aws.String(e.cfg.cluster), Family: aws.String(e.name),
			DesiredStatus: ds, NextToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list the tasks of %s: %w", e.name, err)
		}
		arns = append(arns, out.TaskArns...)
		if aws.ToString(out.NextToken) == "" {
			return arns, nil
		}
		token = out.NextToken
	}
}

// startHomeTask runs the task. token, when set, is the RunTask clientToken: asked again
// with the same parameters, ECS answers the task the first call started. A ConflictException
// (the same token with other parameters: a stack update changed the task definition's
// revision in between) names that task as well.
func (e *ecsRuntime) startHomeTask(ctx context.Context, what HomeWipe, token string) (string, error) {
	var clientToken *string
	if token != "" {
		clientToken = aws.String(token)
	}
	out, err := e.tasks.RunTask(ctx, &ecs.RunTaskInput{
		ClientToken:    clientToken,
		Cluster:        aws.String(e.cfg.cluster),
		TaskDefinition: aws.String(e.cfg.homeTask),
		LaunchType:     ecstypes.LaunchTypeFargate,
		Count:          aws.Int32(1),
		StartedBy:      aws.String(e.homeTaskStartedBy()),
		// Billing only, as on the workspace's own service (upsertService): Fargate bills the
		// task, so without these its minutes land in nobody's share of the bill. The same
		// keys and af-role, so the per-member and per-role views count it as this member's
		// workspace (ADR 0048). ECS authorizes them as ecs:TagResource on the new task
		// (CpHomeOpsManagedPolicy, 30-ingress).
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
	var conflict *ecstypes.ConflictException
	if errors.As(err, &conflict) && len(conflict.ResourceIds) > 0 {
		return conflict.ResourceIds[0], nil
	}
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
// stopped says the task was seen STOPPED. Anything else — the context ended, DescribeTasks
// kept failing, the task stayed unknown for homeTaskMissingGrace — leaves it possibly
// running, and the caller keeps its marker.
//
// A task that stopped without an exit code never ran the command — an image that would not
// pull, a mount that failed — and its stop reason is the sentence that says which.
func (e *ecsRuntime) waitHomeTask(ctx context.Context, arn string, what HomeWipe) (stopped bool, err error) {
	failures := 0
	var unknownSince time.Time
	for {
		t, known, err := e.describeHomeTask(ctx, arn)
		switch {
		case err != nil:
			if failures++; failures >= homeTaskDescribeRetries {
				return false, err
			}
		case !known:
			// Eventual consistency, right after RunTask or in between two reads.
			if unknownSince.IsZero() {
				unknownSince = time.Now()
			} else if time.Since(unknownSince) >= e.missingGrace() {
				return false, fmt.Errorf("the home task (%s) %s is not known to ECS; its outcome is unknown "+
					"and the home stays refused until it is seen stopped", what, arn)
			}
		case aws.ToString(t.LastStatus) == string(ecstypes.DesiredStatusStopped):
			return true, homeTaskOutcome(t, what)
		default:
			failures, unknownSince = 0, time.Time{}
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
