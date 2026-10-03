package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
)

// fakeTasks is the home task's ECS port. A started task reports RUNNING for runningPolls
// reads and then STOPPED with exitCode (nil = it never ran the command).
type fakeTasks struct {
	inflight     []string // what ListTasks answers for the member's startedBy
	runs         []*ecs.RunTaskInput
	runFailure   string
	runningPolls int
	exitCode     *int32
	stoppedWhy   string
	lists        []*ecs.ListTasksInput
	// onRun sees the moment of the RunTask, for the ordering checks.
	onRun func()
}

func (f *fakeTasks) RunTask(_ context.Context, in *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	f.runs = append(f.runs, in)
	if f.onRun != nil {
		f.onRun()
	}
	if f.runFailure != "" {
		return &ecs.RunTaskOutput{Failures: []ecstypes.Failure{{Reason: aws.String(f.runFailure)}}}, nil
	}
	return &ecs.RunTaskOutput{Tasks: []ecstypes.Task{{TaskArn: aws.String("arn:task/home-1")}}}, nil
}

func (f *fakeTasks) DescribeTasks(_ context.Context, in *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	t := ecstypes.Task{TaskArn: aws.String(in.Tasks[0]), LastStatus: aws.String("RUNNING")}
	if f.runningPolls > 0 {
		f.runningPolls--
		return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{t}}, nil
	}
	t.LastStatus = aws.String("STOPPED")
	t.StoppedReason = aws.String(f.stoppedWhy)
	t.Containers = []ecstypes.Container{{Name: aws.String(ecsHomeTaskContainer), ExitCode: f.exitCode}}
	return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{t}}, nil
}

func (f *fakeTasks) ListTasks(_ context.Context, in *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	f.lists = append(f.lists, in)
	return &ecs.ListTasksOutput{TaskArns: f.inflight}, nil
}

func exit(code int32) *int32 { return &code }

// newHomeTaskECS is newTestECS on a stack that declares the home-ops task.
func newHomeTaskECS(fe *fakeECS, ff *fakeEFS, ft *fakeTasks) *ecsRuntime {
	rt := newTestECS(fe, ff, &fakeSSM{})
	rt.cfg.homeTask = "af-stack-home-ops"
	rt.tasks = ft
	rt.homeTaskPoll = time.Millisecond
	return rt
}

func runEnv(in *ecs.RunTaskInput) map[string]string {
	env := map[string]string{}
	for _, o := range in.Overrides.ContainerOverrides {
		for _, kv := range o.Environment {
			env[aws.ToString(o.Name)+"/"+aws.ToString(kv.Name)] = aws.ToString(kv.Value)
		}
	}
	return env
}

// The ports are the stack's to give: the same adapter without the task declared refuses
// every home operation, as it did before the task existed, and with it claims Wipe and
// Erase and says they take minutes.
func TestECSHomePortsFollowTheStack(t *testing.T) {
	without := &ecsFactory{}
	if got := HomeOperationsOf(without); got != (HomeOperations{}) {
		t.Errorf("ecs without the home task = %+v, want none", got)
	}
	with := &ecsFactory{cfg: ecsConfig{homeTask: "af-stack-home-ops"}, tasks: &fakeTasks{}}
	if got := HomeOperationsOf(with); got != (HomeOperations{Wipe: true, Erase: true, Background: true, DestroyBackground: true}) {
		t.Errorf("ecs with the home task = %+v, want Wipe, Erase, Background, DestroyBackground", got)
	}
	rt := newTestECS(&fakeECS{}, &fakeEFS{}, &fakeSSM{})
	if err := WipeHome(context.Background(), rt, HomeWipeRepos); !errors.Is(err, ErrHomeWipeUnsupported) {
		t.Errorf("WipeHome without the task = %v, want ErrHomeWipeUnsupported", err)
	}
}

// A member's wipe waits for the workspace's own task to go, then runs the stack's task with
// the operation and the membership, on the workspace network, and succeeds on exit 0.
func TestECSWipeHomeRunsTheTaskAfterTheWorkspaceTaskIsGone(t *testing.T) {
	fe, ft := &fakeECS{}, &fakeTasks{runningPolls: 2, exitCode: exit(0)}
	rt := newHomeTaskECS(fe, &fakeEFS{}, ft)
	fe.services[rt.name] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	fe.drainingPolls = 3
	ft.onRun = func() {
		if fe.drainingPolls != 0 {
			t.Errorf("the home task started while the workspace task was still draining (%d polls left)", fe.drainingPolls)
		}
	}
	for _, what := range []HomeWipe{HomeWipeRepos, HomeWipeClean} {
		fe.drainingPolls = 3
		ft.runs = nil
		if err := WipeHome(context.Background(), rt, what); err != nil {
			t.Fatalf("WipeHome(%s): %v", what, err)
		}
		if len(ft.runs) != 1 {
			t.Fatalf("WipeHome(%s) ran %d tasks, want 1", what, len(ft.runs))
		}
		in := ft.runs[0]
		if aws.ToString(in.TaskDefinition) != "af-stack-home-ops" || aws.ToString(in.Cluster) != "clu" ||
			aws.ToString(in.StartedBy) != "af-home/M-1" || in.LaunchType != ecstypes.LaunchTypeFargate {
			t.Errorf("RunTask = def %q cluster %q startedBy %q launch %q", aws.ToString(in.TaskDefinition),
				aws.ToString(in.Cluster), aws.ToString(in.StartedBy), in.LaunchType)
		}
		vpc := in.NetworkConfiguration.AwsvpcConfiguration
		if strings.Join(vpc.SecurityGroups, ",") != "sg-ws" || strings.Join(vpc.Subnets, ",") != "sub-1" ||
			vpc.AssignPublicIp != ecstypes.AssignPublicIpDisabled {
			t.Errorf("the task must run on the workspace network (the EFS SG admits only it): %+v", vpc)
		}
		env := runEnv(in)
		if env["home-ops/AF_HOME_OP"] != string(what) || env["home-ops/AF_HOME_MEMBERSHIP"] != "M-1" {
			t.Errorf("overrides = %v", env)
		}
	}
}

// Every way the task can end other than exit 0 is an error that says which: refused,
// failed part-way, or never ran at all.
func TestECSWipeHomeReportsTheTaskOutcome(t *testing.T) {
	for _, c := range []struct {
		name string
		ft   *fakeTasks
		want string
	}{
		{"refused", &fakeTasks{exitCode: exit(HomeOpExitRefused)}, "refused and removed nothing"},
		{"failed", &fakeTasks{exitCode: exit(HomeOpExitFailed)}, "may have removed part"},
		{"never ran", &fakeTasks{stoppedWhy: "CannotPullContainerError"}, "CannotPullContainerError"},
		{"not placed", &fakeTasks{runFailure: "RESOURCE:ENI"}, "RESOURCE:ENI"},
	} {
		rt := newHomeTaskECS(&fakeECS{}, &fakeEFS{}, c.ft)
		err := rt.WipeHome(context.Background(), HomeWipeClean)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: WipeHome = %v, want an error naming %q", c.name, err, c.want)
		}
	}
}

// A home task still running for this member — this CP's or one it lost across a restart —
// refuses a second operation and a start, and nothing is launched.
func TestECSHomeTaskInFlightRefusesWipeAndStart(t *testing.T) {
	fe, ft := &fakeECS{}, &fakeTasks{inflight: []string{"arn:task/earlier"}, exitCode: exit(0)}
	rt := newHomeTaskECS(fe, &fakeEFS{}, ft)
	if err := rt.HomeWipeBlocked(context.Background()); !errors.Is(err, ErrHomeTaskInFlight) {
		t.Errorf("HomeWipeBlocked = %v, want ErrHomeTaskInFlight", err)
	}
	if err := rt.WipeHome(context.Background(), HomeWipeRepos); !errors.Is(err, ErrHomeTaskInFlight) {
		t.Errorf("WipeHome = %v, want ErrHomeTaskInFlight", err)
	}
	if err := rt.Start(context.Background()); !errors.Is(err, ErrHomeTaskInFlight) {
		t.Errorf("Start = %v, want ErrHomeTaskInFlight", err)
	}
	if len(ft.runs) != 0 || len(fe.regCalls) != 0 || len(fe.createCalls) != 0 {
		t.Errorf("refused, but runs=%d registrations=%d creates=%d", len(ft.runs), len(fe.regCalls), len(fe.createCalls))
	}
	if len(ft.lists) == 0 || aws.ToString(ft.lists[0].StartedBy) != "af-home/M-1" || ft.lists[0].Family != nil {
		t.Errorf("ListTasks must filter on startedBy alone (ECS refuses it combined), got %+v", ft.lists)
	}
	ft.inflight = nil
	if err := rt.HomeWipeBlocked(context.Background()); err != nil {
		t.Errorf("HomeWipeBlocked with nothing in flight = %v", err)
	}
}

// The membership id becomes a path on the whole file system; one that is not a single
// path element never reaches RunTask.
func TestECSWipeHomeRefusesAMembershipIDThatIsNotOnePathElement(t *testing.T) {
	for _, id := range []string{"", "..", "a/b", "../M-2", "M 1"} {
		ft := &fakeTasks{exitCode: exit(0)}
		rt := newHomeTaskECS(&fakeECS{}, &fakeEFS{}, ft)
		rt.membershipID = id
		if err := rt.WipeHome(context.Background(), HomeWipeClean); err == nil {
			t.Errorf("WipeHome for membership %q succeeded", id)
		}
		if len(ft.runs) != 0 {
			t.Errorf("membership %q reached RunTask", id)
		}
	}
}

// While the background wipe is queued the workspace reads `starting` with the clearing
// phase, and goes back to `stopped` when it is released.
func TestECSQueuedHomeWipeReadsAsStarting(t *testing.T) {
	fe := &fakeECS{}
	rt := newHomeTaskECS(fe, &fakeEFS{}, &fakeTasks{})
	fe.services[rt.name] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	release := QueueHomeWipe(rt)
	if got := rt.State(context.Background()); got != "starting" {
		t.Errorf("State while queued = %q, want starting", got)
	}
	if got := rt.BootPhase(); got != "home: clearing" {
		t.Errorf("BootPhase while queued = %q", got)
	}
	release()
	release() // idempotent
	if got := rt.State(context.Background()); got != "stopped" {
		t.Errorf("State after release = %q, want stopped", got)
	}
	if rt.BootPhase() != "" {
		t.Errorf("BootPhase after release = %q", rt.BootPhase())
	}
}

// Destroy on a stack with the task: the service and the access points go first, then the
// task removes both directories, and nothing is left over. A failed task is an error, so
// the workspace row stays and the retry runs it again.
func TestECSDestroyRemovesTheEFSHomeThroughTheTask(t *testing.T) {
	fe, ff, ft := &fakeECS{}, &fakeEFS{}, &fakeTasks{exitCode: exit(0)}
	rt := newHomeTaskECS(fe, ff, ft)
	// Stopped a moment ago: its task is still draining for the next two reads.
	fe.services[rt.name] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	fe.drainingPolls = 2
	ff.aps = []efstypes.AccessPointDescription{
		{AccessPointId: aws.String("fsap-home"), RootDirectory: &efstypes.RootDirectory{Path: aws.String("/home/M-1")},
			Tags: []efstypes.Tag{{Key: aws.String("af-membership"), Value: aws.String("M-1")}}},
	}
	ft.onRun = func() {
		if fe.drainingPolls != 0 {
			t.Error("the destroy task ran while the workspace task was still draining")
		}
		if len(fe.deleteCalls) != 1 || len(ff.aps) != 0 {
			t.Errorf("the destroy task ran before the service (%d deletes) and access points (%d left) were gone",
				len(fe.deleteCalls), len(ff.aps))
		}
	}
	leftovers, err := rt.Destroy(context.Background())
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(leftovers) != 0 {
		t.Errorf("leftovers = %v, want none: the task removed them", leftovers)
	}
	if len(ft.runs) != 1 || runEnv(ft.runs[0])["home-ops/AF_HOME_OP"] != "destroy" {
		t.Fatalf("Destroy ran %d tasks (%v), want one destroy", len(ft.runs), ft.runs)
	}

	ft2 := &fakeTasks{exitCode: exit(HomeOpExitFailed)}
	rt2 := newHomeTaskECS(&fakeECS{}, &fakeEFS{}, ft2)
	if _, err := rt2.Destroy(context.Background()); err == nil {
		t.Error("Destroy with a failed home task succeeded; the row would go and the directories stay")
	}
}

// ecs-ec2 keeps the home on EBS, but the Claude state and the keep-list on EFS. With the
// stack's task its Destroy removes both directories through it, reports neither as a
// leftover, and says it takes minutes; an access point rooted anywhere else is still
// reported (#1536).
func TestECSEC2DestroyRemovesTheEFSDirectoriesThroughTheTask(t *testing.T) {
	h := newEC2Harness(t)
	if DestroyInBackground(h.rt) {
		t.Error("ecs-ec2 without the home task claims a background Destroy")
	}
	ft := &fakeTasks{exitCode: exit(0)}
	h.rt.base.cfg.homeTask = "af-stack-home-ops"
	h.rt.base.tasks = ft
	h.rt.base.homeTaskPoll = time.Millisecond
	if !DestroyInBackground(h.rt) {
		t.Error("ecs-ec2 with the home task must Destroy in the background: the task takes minutes")
	}
	if HomeWipeInBackground(h.rt) {
		t.Error("ecs-ec2's own wipe fits in the request; only its Destroy may move to the background")
	}
	ap := func(id, path string) efstypes.AccessPointDescription {
		return efstypes.AccessPointDescription{AccessPointId: aws.String(id),
			RootDirectory: &efstypes.RootDirectory{Path: aws.String(path)},
			Tags:          []efstypes.Tag{{Key: aws.String("af-membership"), Value: aws.String("M-1")}}}
	}
	h.efs.aps = []efstypes.AccessPointDescription{
		ap("fsap-claude", "/claude-config/M-1"), ap("fsap-keep", "/home-keep/M-1"), ap("fsap-odd", "/elsewhere/M-1"),
	}
	leftovers, err := h.rt.Destroy(context.Background())
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(ft.runs) != 1 || runEnv(ft.runs[0])["home-ops/AF_HOME_OP"] != "destroy" ||
		runEnv(ft.runs[0])["home-ops/AF_HOME_MEMBERSHIP"] != "M-1" {
		t.Fatalf("Destroy ran %d tasks, want one destroy for M-1", len(ft.runs))
	}
	if len(leftovers) != 1 || !strings.HasSuffix(leftovers[0], "/elsewhere/M-1") {
		t.Errorf("leftovers = %v, want only the directory the task does not remove", leftovers)
	}

	// A home task still running (a Destroy a restarted CP lost) refuses before anything.
	ft.inflight = []string{"arn:task/earlier"}
	if err := HomeWipeBlocked(context.Background(), h.rt); !errors.Is(err, ErrHomeTaskInFlight) {
		t.Errorf("HomeWipeBlocked with a home task in flight = %v, want ErrHomeTaskInFlight", err)
	}
}

// Fargate bills the home task like any other task, so it carries the cost tags of the
// member's workspace service — the same keys and values — and no af-tenant at all when the
// slug is unknown, never an empty one (#1538).
func TestECSHomeTaskCarriesTheWorkspaceCostTags(t *testing.T) {
	tags := func(in []ecstypes.Tag) map[string]string {
		out := map[string]string{}
		for _, tg := range in {
			out[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
		}
		return out
	}
	for _, slug := range []string{"acme", ""} {
		fe, ft := &fakeECS{}, &fakeTasks{exitCode: exit(0)}
		rt := newHomeTaskECS(fe, &fakeEFS{}, ft)
		rt.tenantSlug = slug
		if err := rt.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if len(fe.createCalls) != 1 {
			t.Fatalf("Start created %d services, want 1", len(fe.createCalls))
		}
		service := tags(fe.createCalls[0].Tags)
		// Stopped and drained, as the wipe requires.
		fe.services[rt.name] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
		if err := rt.WipeHome(context.Background(), HomeWipeRepos); err != nil {
			t.Fatalf("WipeHome: %v", err)
		}
		task := tags(ft.runs[0].Tags)
		if fmt.Sprint(task) != fmt.Sprint(service) {
			t.Errorf("slug %q: home task tags %v, want the workspace service's %v", slug, task, service)
		}
		if _, ok := task["af-tenant"]; ok != (slug != "") || task["af-membership"] != "M-1" {
			t.Errorf("slug %q: home task tags = %v", slug, task)
		}
	}
}
