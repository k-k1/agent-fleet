package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
)

// fakeTasks is the home task's ECS port. A started task is MISSING for missingPolls reads
// (RunTask's eventual consistency), RUNNING for runningPolls, then STOPPED with exitCode
// (nil = it never ran the command). The workspace's own task, listed by its family, is
// STOPPING for wsStoppingPolls reads and STOPPED after.
type fakeTasks struct {
	inflight        []string // what ListTasks answers for the member's startedBy
	runs            []*ecs.RunTaskInput
	runFailure      string
	missingPolls    int
	runningPolls    int
	exitCode        *int32
	stoppedWhy      string
	wsStoppingPolls int
	wsRunningListed bool // the workspace task is listed as desired RUNNING (before a Stop)
	lists           []*ecs.ListTasksInput
	// onRun sees the moment of the RunTask, for the ordering checks.
	onRun func()
}

const fakeWSTask = "arn:task/ws-1"

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
	if in.Tasks[0] == fakeWSTask {
		t := ecstypes.Task{TaskArn: aws.String(fakeWSTask), LastStatus: aws.String("STOPPED")}
		if f.wsStoppingPolls > 0 {
			f.wsStoppingPolls--
			t.LastStatus = aws.String("DEACTIVATING")
		}
		return &ecs.DescribeTasksOutput{Tasks: []ecstypes.Task{t}}, nil
	}
	if f.missingPolls > 0 {
		f.missingPolls--
		return &ecs.DescribeTasksOutput{Failures: []ecstypes.Failure{{Arn: aws.String(in.Tasks[0]), Reason: aws.String("MISSING")}}}, nil
	}
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
	if in.Family != nil {
		// The workspace's own task: listed under desired STOPPED once it is being stopped.
		if in.DesiredStatus == ecstypes.DesiredStatusStopped || f.wsRunningListed {
			return &ecs.ListTasksOutput{TaskArns: []string{fakeWSTask}}, nil
		}
		return &ecs.ListTasksOutput{}, nil
	}
	return &ecs.ListTasksOutput{TaskArns: f.inflight}, nil
}

func exit(code int32) *int32 { return &code }

// newHomeTaskECS is newTestECS on a stack that declares the home-ops task.
func newHomeTaskECS(fe *fakeECS, ff *fakeEFS, ft *fakeTasks) *ecsRuntime {
	return newHomeTaskECSWith(fe, ff, &fakeSSM{}, ft)
}

func newHomeTaskECSWith(fe *fakeECS, ff *fakeEFS, fs *fakeSSM, ft *fakeTasks) *ecsRuntime {
	rt := newTestECS(fe, ff, fs)
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
	if got := HomeOperationsOf(with); got != (HomeOperations{Wipe: true, Erase: true, Background: true}) {
		t.Errorf("ecs with the home task = %+v, want Wipe, Erase, Background", got)
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
	// The service already counts no running task, but the old task is still STOPPING (its
	// stop timeout): only its own LastStatus says when it has let go of the home.
	fe.services[rt.name] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	ft.onRun = func() {
		if ft.wsStoppingPolls != 0 {
			t.Errorf("the home task started while the workspace task was still stopping (%d polls left)", ft.wsStoppingPolls)
		}
	}
	for _, what := range []HomeWipe{HomeWipeRepos, HomeWipeClean} {
		ft.wsStoppingPolls = 3
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
	byStarter := 0
	for _, l := range ft.lists {
		if l.StartedBy == nil {
			continue
		}
		byStarter++
		if aws.ToString(l.StartedBy) != "af-home/M-1" || l.Family != nil || l.ServiceName != nil || l.DesiredStatus != "" {
			t.Errorf("ListTasks must filter on startedBy alone (ECS refuses it combined), got %+v", l)
		}
	}
	if byStarter == 0 {
		t.Error("no ListTasks by startedBy")
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
	// Stopped a moment ago: its task is still stopping for the next two reads.
	fe.services[rt.name] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	ft.wsStoppingPolls = 2
	ff.aps = []efstypes.AccessPointDescription{
		{AccessPointId: aws.String("fsap-home"), RootDirectory: &efstypes.RootDirectory{Path: aws.String("/home/M-1")},
			Tags: []efstypes.Tag{{Key: aws.String("af-membership"), Value: aws.String("M-1")}}},
	}
	ft.onRun = func() {
		if ft.wsStoppingPolls != 0 {
			t.Error("the destroy task ran while the workspace task was still stopping")
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

// R2: right after RunTask, DescribeTasks may not know the task yet. That is not the end of
// it: the wait keeps polling, the marker keeps the home refused meanwhile, and only the
// task's own STOPPED ends it.
func TestECSHomeTaskMissingRightAfterRunTaskKeepsWaiting(t *testing.T) {
	fs, ft := &fakeSSM{}, &fakeTasks{missingPolls: 3, runningPolls: 1, exitCode: exit(0)}
	rt := newHomeTaskECSWith(&fakeECS{}, &fakeEFS{}, fs, ft)
	marker := rt.homeTaskMarker()
	ft.onRun = func() {
		if fs.values[marker] != homeTaskMarkerPending {
			t.Errorf("RunTask before the marker was written (marker = %q)", fs.values[marker])
		}
	}
	if err := rt.WipeHome(context.Background(), HomeWipeRepos); err != nil {
		t.Fatalf("WipeHome with an eventually consistent DescribeTasks: %v", err)
	}
	if _, ok := fs.values[marker]; ok {
		t.Error("the marker outlived a task seen STOPPED")
	}
}

// The marker is what a restarted CP (or another replica) finds. While its task is not
// seen STOPPED — MISSING, RUNNING, MISSING again, however old the marker — Start and a
// second operation are refused even though ListTasks lists nothing. Only STOPPED releases.
func TestECSHomeTaskMarkerRefusesUntilItsTaskStops(t *testing.T) {
	fs := &fakeSSM{}
	ft := &fakeTasks{missingPolls: 1, runningPolls: 1, exitCode: exit(0)} // ListTasks: nothing
	fe := &fakeECS{}
	rt := newHomeTaskECSWith(fe, &fakeEFS{}, fs, ft)
	marker := rt.homeTaskMarker()
	fs.values = map[string]string{marker: "arn:task/home-1"}
	// An old marker: elapsed time is no evidence either.
	fs.at = map[string]time.Time{marker: time.Now().Add(-time.Hour)}
	for i, want := range []error{ErrHomeTaskInFlight, ErrHomeTaskInFlight, nil} { // MISSING, RUNNING, STOPPED
		if err := rt.Start(context.Background()); !errors.Is(err, want) || (want == nil && err != nil) {
			t.Fatalf("Start #%d = %v, want %v", i+1, err, want)
		}
		if want != nil && len(fe.createCalls) != 0 {
			t.Fatalf("Start #%d created the service while the home task was not seen stopped", i+1)
		}
	}
	if _, ok := fs.values[marker]; ok {
		t.Error("a marker whose task stopped was kept")
	}

	// RUNNING seen, then a transient MISSING: still refused, marker kept.
	fs.values[marker] = "arn:task/home-2"
	ft.runningPolls, ft.missingPolls = 1, 0
	if err := rt.HomeWipeBlocked(context.Background()); !errors.Is(err, ErrHomeTaskInFlight) {
		t.Errorf("RUNNING = %v, want ErrHomeTaskInFlight", err)
	}
	ft.missingPolls = 1
	if err := rt.HomeWipeBlocked(context.Background()); !errors.Is(err, ErrHomeTaskInFlight) {
		t.Errorf("MISSING after RUNNING = %v, want ErrHomeTaskInFlight", err)
	}
	if _, ok := fs.values[marker]; !ok {
		t.Error("MISSING dropped the marker")
	}

	// A marker written before a RunTask whose answer never came names no task that could be
	// seen stopped: it blocks however old it is, until the operator clears it.
	fs.values[marker] = homeTaskMarkerPending
	fs.at[marker] = time.Now().Add(-24 * time.Hour)
	if err := rt.HomeWipeBlocked(context.Background()); !errors.Is(err, ErrHomeTaskInFlight) {
		t.Errorf("an old pending marker = %v, want ErrHomeTaskInFlight", err)
	}
	if _, ok := fs.values[marker]; !ok {
		t.Error("a pending marker was dropped without an operator")
	}
}

// A wait that never sees its task (MISSING past the wait's bound) ends without claiming
// the outcome: the marker stays, so the home stays refused.
func TestECSHomeTaskUnknownOutcomeKeepsTheMarker(t *testing.T) {
	fs, ft := &fakeSSM{}, &fakeTasks{missingPolls: 1 << 30, exitCode: exit(0)}
	rt := newHomeTaskECSWith(&fakeECS{}, &fakeEFS{}, fs, ft)
	rt.homeTaskMissingGrace = 20 * time.Millisecond
	if err := rt.WipeHome(context.Background(), HomeWipeRepos); err == nil {
		t.Fatal("a wait that never saw its task reported success")
	}
	if fs.values[rt.homeTaskMarker()] != "arn:task/home-1" {
		t.Errorf("marker = %q, want the task's ARN kept", fs.values[rt.homeTaskMarker()])
	}
	if err := rt.HomeWipeBlocked(context.Background()); !errors.Is(err, ErrHomeTaskInFlight) {
		t.Errorf("after an unknown outcome HomeWipeBlocked = %v, want ErrHomeTaskInFlight", err)
	}
}

// fakeTasksNoListing hides every workspace task from ListTasks: the listing lags.
type fakeTasksNoListing struct{ fakeTasks }

func (f *fakeTasksNoListing) ListTasks(ctx context.Context, in *ecs.ListTasksInput, opts ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	if in.Family != nil {
		f.lists = append(f.lists, in)
		return &ecs.ListTasksOutput{}, nil
	}
	return f.fakeTasks.ListTasks(ctx, in, opts...)
}

// An empty listing is not proof the workspace task is gone: while the service still counts
// one running, the wait goes on and no home task starts.
func TestECSDrainWaitTrustsTheServiceCountOverAnEmptyListing(t *testing.T) {
	fe := &fakeECS{}
	ft := &fakeTasksNoListing{fakeTasks{exitCode: exit(0)}}
	rt := newHomeTaskECSWith(fe, &fakeEFS{}, &fakeSSM{}, &fakeTasks{})
	rt.tasks = ft
	fe.services[rt.name] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0, RunningCount: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := rt.WipeHome(ctx, HomeWipeClean); err == nil {
		t.Error("the wipe went ahead while the service still counts a running task")
	}
	if len(ft.runs) != 0 {
		t.Error("the home task started while the workspace task was unresolved")
	}
}

// A task Stop saw running must be seen STOPPED, even once no listing shows it any more and
// DescribeTasks answers MISSING for it.
func TestECSDrainWaitNeedsEveryTaskStopSawToBeSeenStopped(t *testing.T) {
	fe := &fakeECS{}
	ft := &fakeTasksNoListing{fakeTasks{exitCode: exit(0), missingPolls: 1 << 30}}
	rt := newHomeTaskECSWith(fe, &fakeEFS{}, &fakeSSM{}, &fakeTasks{})
	rt.tasks = ft
	fe.services[rt.name] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	stoppedTasks.Store(rt.name, []string{"arn:task/ws-vanished"})
	t.Cleanup(func() { stoppedTasks.Delete(rt.name) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := rt.WipeHome(ctx, HomeWipeClean); err == nil {
		t.Error("the wipe went ahead although a task Stop saw was never seen STOPPED")
	}
	if len(ft.runs) != 0 {
		t.Error("the home task started while a stopped workspace task was unconfirmed")
	}
}

// A task ECS never placed leaves no marker behind to block the next attempt.
func TestECSHomeTaskNotPlacedDropsTheMarker(t *testing.T) {
	fs, ft := &fakeSSM{}, &fakeTasks{runFailure: "RESOURCE:ENI"}
	rt := newHomeTaskECSWith(&fakeECS{}, &fakeEFS{}, fs, ft)
	if err := rt.WipeHome(context.Background(), HomeWipeClean); err == nil {
		t.Fatal("WipeHome with a refused RunTask succeeded")
	}
	if _, ok := fs.values[rt.homeTaskMarker()]; ok {
		t.Error("a RunTask that placed nothing left the home refused")
	}
}

// The stack hands AF_ECS_HOME_TASK to ecs-ec2 as well, and ecs-ec2's Destroy runs the base
// adapter's. The base it builds must not pick the Fargate task up: that Destroy is waited
// for inside the request, and the task does not know ecs-ec2's /home-keep directory, so it
// would report "nothing left" while the keep files stayed on EFS.
func TestECSEC2DoesNotUseTheFargateHomeTask(t *testing.T) {
	h := newEC2Harness(t)
	f := h.factory()
	f.base.cfg.homeTask = "af-stack-home-ops"
	f.base.tasks = &fakeTasks{exitCode: exit(0)}
	rt := f.New(Workspace{ContainerName: "af-ws-acme-alice", MembershipID: "M-1"}, "", nil).(*ecsEC2Runtime)
	if rt.base.homePortsReady() {
		t.Fatal("the ecs-ec2 runtime's base claims the Fargate home task")
	}
	if got := HomeOperationsOf(f); got.Background {
		t.Errorf("ecs-ec2 home operations = %+v; none of them run in the background", got)
	}
	h.efs.aps = []efstypes.AccessPointDescription{
		{AccessPointId: aws.String("fsap-keep"), RootDirectory: &efstypes.RootDirectory{Path: aws.String("/home-keep/M-1")},
			Tags: []efstypes.Tag{{Key: aws.String("af-membership"), Value: aws.String("M-1")}}},
	}
	ft := f.base.tasks.(*fakeTasks)
	leftovers, err := rt.Destroy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ft.runs) != 0 {
		t.Errorf("ecs-ec2 Destroy ran %d Fargate home tasks", len(ft.runs))
	}
	if len(leftovers) != 1 || !strings.HasSuffix(leftovers[0], "/home-keep/M-1") {
		t.Errorf("leftovers = %v, want the keep directory reported", leftovers)
	}
}

// Stop records the tasks it is stopping, for the wait that follows (only where the stack
// declares the home task).
func TestECSStopCapturesTheTasksItStops(t *testing.T) {
	fe, ft := &fakeECS{}, &fakeTasks{wsRunningListed: true}
	rt := newHomeTaskECS(fe, &fakeEFS{}, ft)
	fe.services[rt.name] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}
	t.Cleanup(func() { stoppedTasks.Delete(rt.name) })
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	v, ok := stoppedTasks.Load(rt.name)
	if !ok || len(v.([]string)) != 1 || v.([]string)[0] != fakeWSTask {
		t.Errorf("captured = %v, want the running workspace task", v)
	}
}

// fakeTasksRunErr fails RunTask with err.
type fakeTasksRunErr struct {
	fakeTasks
	err error
}

func (f *fakeTasksRunErr) RunTask(_ context.Context, in *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	f.runs = append(f.runs, in)
	return nil, f.err
}

// Only a RunTask known to have started nothing drops the pending marker: a client fault
// (4xx). A server fault (5xx, even after the SDK's retries) or an error with no fault may
// have placed a task, so the marker stays and Start stays refused.
func TestECSRunTaskFailureKeepsTheMarkerUnlessNothingStarted(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		kept bool
	}{
		{"server fault", &ecstypes.ServerException{Message: aws.String("internal server error")}, true},
		{"no fault (transport)", errors.New("dial tcp: i/o timeout"), true},
		{"client fault", &ecstypes.InvalidParameterException{Message: aws.String("TaskDefinition is inactive")}, false},
		{"access denied", &ecstypes.AccessDeniedException{Message: aws.String("not authorized")}, false},
	} {
		fe, fs := &fakeECS{}, &fakeSSM{}
		rt := newHomeTaskECSWith(fe, &fakeEFS{}, fs, &fakeTasks{})
		rt.tasks = &fakeTasksRunErr{err: c.err}
		if err := rt.WipeHome(context.Background(), HomeWipeClean); err == nil {
			t.Fatalf("%s: the RunTask error was lost", c.name)
		}
		_, kept := fs.values[rt.homeTaskMarker()]
		if kept != c.kept {
			t.Errorf("%s: marker kept = %v, want %v", c.name, kept, c.kept)
		}
		startErr := rt.Start(context.Background())
		if c.kept && (!errors.Is(startErr, ErrHomeTaskInFlight) || len(fe.createCalls) != 0) {
			t.Errorf("%s: Start = %v (creates %d), want ErrHomeTaskInFlight and no service", c.name, startErr, len(fe.createCalls))
		}
	}
}
