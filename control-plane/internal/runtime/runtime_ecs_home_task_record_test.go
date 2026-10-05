package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// The bound home task carries the operation's id as its RunTask clientToken, and the ARN
// RunTask answers goes back to the record before the wait (#1544).
func TestECSBoundHomeTaskCarriesTheTokenAndReportsItsTask(t *testing.T) {
	ft := &fakeTasks{exitCode: exit(0)}
	rt := newHomeTaskECS(&fakeECS{}, &fakeEFS{}, ft)
	var started []string
	if !BindHomeTask(rt, HomeTaskBinding{Token: "op-1", Started: func(arn string) { started = append(started, arn) }}) {
		t.Fatal("ecs with the home task refused the binding")
	}
	if err := rt.WipeHome(context.Background(), HomeWipeRepos); err != nil {
		t.Fatalf("WipeHome: %v", err)
	}
	if len(ft.runs) != 1 || aws.ToString(ft.runs[0].ClientToken) != "op-1" {
		t.Fatalf("RunTask calls = %d, token %q; want one under the operation's id", len(ft.runs), aws.ToString(ft.runs[0].ClientToken))
	}
	if len(started) != 1 || started[0] != "arn:task/home-1" {
		t.Errorf("Started = %v, want the task's ARN once", started)
	}
	if BindHomeTask(newTestECS(&fakeECS{}, &fakeEFS{}, &fakeSSM{}), HomeTaskBinding{Token: "x"}) {
		t.Error("a stack without the home task accepted a binding")
	}
}

// A RunTask whose answer was lost leaves the pending marker. Unbound, that refuses every
// later run until an operator clears it; the reconciler's resumed run asks RunTask again
// under the same token instead, and finishes the operation.
func TestECSResumedHomeTaskAsksAgainUnderTheSameToken(t *testing.T) {
	fs, ft := &fakeSSM{}, &fakeTasks{exitCode: exit(0)}
	rt := newHomeTaskECSWith(&fakeECS{}, &fakeEFS{}, fs, ft)
	fs.values = map[string]string{rt.homeTaskMarker(): markerValue(homeTaskMarkerPending, "op-7")}

	if err := rt.WipeHome(context.Background(), HomeWipeClean); !errors.Is(err, ErrHomeTaskInFlight) {
		t.Fatalf("an unbound run over a pending marker = %v, want ErrHomeTaskInFlight", err)
	}
	var started string
	BindHomeTask(rt, HomeTaskBinding{Token: "op-7", Resume: true, SentAt: time.Now().Add(-time.Hour),
		Started: func(arn string) { started = arn }})
	if err := rt.WipeHome(context.Background(), HomeWipeClean); err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if len(ft.runs) != 1 || aws.ToString(ft.runs[0].ClientToken) != "op-7" {
		t.Fatalf("RunTask calls = %d; want one, under the operation's token", len(ft.runs))
	}
	if started != "arn:task/home-1" {
		t.Errorf("Started = %q, want the task RunTask answered", started)
	}
	if _, ok := fs.values[rt.homeTaskMarker()]; ok {
		t.Error("the marker of a task seen STOPPED was kept")
	}
}

// A resumed run whose record (or marker) names a task ECS still reports waits for that
// task and reads its outcome: no second RunTask.
func TestECSResumedHomeTaskAdoptsTheRecordedTask(t *testing.T) {
	for _, c := range []struct {
		name, recorded, marker string
	}{
		{"recorded on the operation", "arn:task/home-9", "arn:task/home-9 op-2"},
		{"only in the marker", "", "arn:task/home-9 op-2"},
	} {
		fs, ft := &fakeSSM{}, &fakeTasks{runningPolls: 2, exitCode: exit(1)}
		rt := newHomeTaskECSWith(&fakeECS{}, &fakeEFS{}, fs, ft)
		fs.values = map[string]string{rt.homeTaskMarker(): c.marker}
		var started string
		BindHomeTask(rt, HomeTaskBinding{Token: "op-2", TaskARN: c.recorded, Resume: true, Started: func(arn string) { started = arn }})
		err := rt.WipeHome(context.Background(), HomeWipeRepos)
		if err == nil || errors.Is(err, ErrHomeTaskUnresolved) {
			t.Errorf("%s: outcome = %v, want the task's exit 1 as a definite failure", c.name, err)
		}
		if len(ft.runs) != 0 {
			t.Errorf("%s: a task already running was started again (%d RunTask)", c.name, len(ft.runs))
		}
		if started != "arn:task/home-9" {
			t.Errorf("%s: Started = %q, want the adopted task", c.name, started)
		}
		if _, ok := fs.values[rt.homeTaskMarker()]; ok {
			t.Errorf("%s: the marker of a task seen STOPPED was kept", c.name)
		}
	}
}

// fakeTasksConflict answers RunTask as ECS does when the token is known with other
// parameters (a stack update moved the task definition's revision).
type fakeTasksConflict struct{ fakeTasks }

func (f *fakeTasksConflict) RunTask(_ context.Context, in *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	f.runs = append(f.runs, in)
	return nil, &ecstypes.ConflictException{Message: aws.String("token in use"), ResourceIds: []string{"arn:task/home-first"}}
}

func TestECSRunTaskConflictNamesTheTaskOfTheToken(t *testing.T) {
	fs := &fakeSSM{}
	rt := newHomeTaskECSWith(&fakeECS{}, &fakeEFS{}, fs, &fakeTasks{})
	ft := &fakeTasksConflict{fakeTasks{exitCode: exit(0)}}
	rt.tasks = ft
	var started string
	BindHomeTask(rt, HomeTaskBinding{Token: "op-3", Resume: true, Started: func(arn string) { started = arn }})
	if err := rt.WipeHome(context.Background(), HomeWipeRepos); err != nil {
		t.Fatalf("WipeHome after a ConflictException: %v", err)
	}
	if started != "arn:task/home-first" {
		t.Errorf("Started = %q, want the task the token already names", started)
	}
}

// What the CP may finish and what it must leave open: a task seen STOPPED is an outcome,
// whatever its exit code; a RunTask that may have started something, or a wait that never
// saw the task stop, is not.
func TestECSHomeTaskOutcomeIsUnresolvedOnlyWhileATaskMayRun(t *testing.T) {
	ctx := context.Background()
	exit1 := newHomeTaskECS(&fakeECS{}, &fakeEFS{}, &fakeTasks{exitCode: exit(1)})
	if err := exit1.WipeHome(ctx, HomeWipeRepos); err == nil || errors.Is(err, ErrHomeTaskUnresolved) {
		t.Errorf("exit 1 = %v, want a definite failure", err)
	}
	notPlaced := newHomeTaskECS(&fakeECS{}, &fakeEFS{}, &fakeTasks{runFailure: "RESOURCE:ENI"})
	if err := notPlaced.WipeHome(ctx, HomeWipeRepos); err == nil || errors.Is(err, ErrHomeTaskUnresolved) {
		t.Errorf("not placed = %v, want a definite failure", err)
	}
	fault := newHomeTaskECS(&fakeECS{}, &fakeEFS{}, &fakeTasks{})
	fault.tasks = &fakeTasksRunErr{err: &ecstypes.ServerException{Message: aws.String("boom")}}
	if err := fault.WipeHome(ctx, HomeWipeRepos); !errors.Is(err, ErrHomeTaskUnresolved) {
		t.Errorf("RunTask server fault = %v, want ErrHomeTaskUnresolved", err)
	}
	missing := newHomeTaskECS(&fakeECS{}, &fakeEFS{}, &fakeTasks{missingPolls: 1 << 30})
	missing.homeTaskMissingGrace = 10 * time.Millisecond
	if err := missing.WipeHome(ctx, HomeWipeRepos); !errors.Is(err, ErrHomeTaskUnresolved) {
		t.Errorf("never seen = %v, want ErrHomeTaskUnresolved", err)
	}
	// Under a resumed operation an earlier attempt may have started a task, so even a
	// client fault proves nothing; a failure list under the token is ECS's own answer.
	resumed := newHomeTaskECSWith(&fakeECS{}, &fakeEFS{}, &fakeSSM{values: map[string]string{}}, &fakeTasks{})
	resumed.tasks = &fakeTasksRunErr{err: &ecstypes.AccessDeniedException{Message: aws.String("no")}}
	resumed.ssm.(*fakeSSM).values[resumed.homeTaskMarker()] = markerValue(homeTaskMarkerPending, "op-4")
	BindHomeTask(resumed, HomeTaskBinding{Token: "op-4", Resume: true, SentAt: time.Now().Add(-time.Hour)})
	if err := resumed.WipeHome(ctx, HomeWipeRepos); !errors.Is(err, ErrHomeTaskUnresolved) {
		t.Errorf("resumed client fault = %v, want ErrHomeTaskUnresolved", err)
	}
	fs := &fakeSSM{}
	resumedNP := newHomeTaskECSWith(&fakeECS{}, &fakeEFS{}, fs, &fakeTasks{runFailure: "RESOURCE:ENI"})
	fs.values = map[string]string{resumedNP.homeTaskMarker(): markerValue(homeTaskMarkerPending, "op-5")}
	BindHomeTask(resumedNP, HomeTaskBinding{Token: "op-5", Resume: true, SentAt: time.Now().Add(-time.Hour)})
	if err := resumedNP.WipeHome(ctx, HomeWipeRepos); err == nil || errors.Is(err, ErrHomeTaskUnresolved) {
		t.Errorf("resumed, not placed = %v, want a definite failure", err)
	}
	if _, ok := fs.values[resumedNP.homeTaskMarker()]; ok {
		t.Error("a token ECS placed nothing for kept its marker")
	}
}

// A RunTask answer lost long enough ago that the token may have expired (ECS keeps one for
// at most 24 hours) is not asked for again: the same token could start a second task while
// the first still removes files. Nor is any RunTask sent while a task started for this
// member is listed running, whatever the token's age.
func TestECSResumedHomeTaskDoesNotOutliveItsToken(t *testing.T) {
	for _, c := range []struct {
		name     string
		sentAgo  time.Duration
		inflight []string
		runs     int
	}{
		{"recent, nothing listed", time.Hour, nil, 1},
		{"recent, the first task still listed", time.Hour, []string{"arn:task/home-A"}, 0},
		{"past the token, the first task still listed", 25 * time.Hour, []string{"arn:task/home-A"}, 0},
		{"past the token, nothing listed", 25 * time.Hour, nil, 0},
	} {
		fs, ft := &fakeSSM{}, &fakeTasks{exitCode: exit(0), inflight: c.inflight}
		rt := newHomeTaskECSWith(&fakeECS{}, &fakeEFS{}, fs, ft)
		fs.values = map[string]string{rt.homeTaskMarker(): markerValue(homeTaskMarkerPending, "op-8")}
		BindHomeTask(rt, HomeTaskBinding{Token: "op-8", Resume: true, SentAt: time.Now().Add(-c.sentAgo)})
		err := rt.WipeHome(context.Background(), HomeWipeRepos)
		if len(ft.runs) != c.runs {
			t.Errorf("%s: %d RunTask, want %d", c.name, len(ft.runs), c.runs)
		}
		if c.runs == 0 {
			if !errors.Is(err, ErrHomeTaskUnresolved) {
				t.Errorf("%s: outcome = %v, want unresolved", c.name, err)
			}
			if _, ok := fs.values[rt.homeTaskMarker()]; !ok {
				t.Errorf("%s: the marker was dropped although the first task may still run", c.name)
			}
		} else if err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// The operator's release: with the marker deleted after checking ECS, the operation
	// runs again.
	fs, ft := &fakeSSM{}, &fakeTasks{exitCode: exit(0)}
	rt := newHomeTaskECSWith(&fakeECS{}, &fakeEFS{}, fs, ft)
	BindHomeTask(rt, HomeTaskBinding{Token: "op-8", Resume: true, SentAt: time.Now().Add(-25 * time.Hour)})
	if err := rt.WipeHome(context.Background(), HomeWipeRepos); err != nil || len(ft.runs) != 1 {
		t.Errorf("released by the operator: %v, %d RunTask; want it run again", err, len(ft.runs))
	}
}

// Only the operation's own marker names its task. A marker without a token (a CP before
// #1544) or with another operation's is a home task like any other: running or pending it
// keeps this operation waiting, and once seen STOPPED it is cleared and this operation
// starts its own task — it never takes the stranger's exit code as its outcome.
func TestECSResumedHomeTaskNeverAdoptsAnotherOperationsMarker(t *testing.T) {
	for _, c := range []struct {
		name, marker string
		running      int
		runs         int
	}{
		{"legacy, stopped", "arn:task/old-recreate", 0, 1},
		{"another operation's, stopped", "arn:task/old-recreate op-other", 0, 1},
		{"legacy, pending", homeTaskMarkerPending, 0, 0},
		{"another operation's, running", "arn:task/old-recreate op-other", 1 << 30, 0},
	} {
		fs, ft := &fakeSSM{}, &fakeTasks{exitCode: exit(0), runningPolls: c.running}
		rt := newHomeTaskECSWith(&fakeECS{}, &fakeEFS{}, fs, ft)
		fs.values = map[string]string{rt.homeTaskMarker(): c.marker}
		var started string
		BindHomeTask(rt, HomeTaskBinding{Token: "op-destroy", Resume: true, Started: func(arn string) { started = arn }})
		err := rt.WipeHome(context.Background(), HomeWipeClean)
		if len(ft.runs) != c.runs {
			t.Errorf("%s: %d RunTask, want %d", c.name, len(ft.runs), c.runs)
		}
		if started == "arn:task/old-recreate" {
			t.Errorf("%s: adopted another operation's task", c.name)
		}
		if c.runs == 0 && !errors.Is(err, ErrHomeTaskUnresolved) {
			t.Errorf("%s: outcome = %v, want unresolved", c.name, err)
		}
		if c.runs == 1 && (err != nil || aws.ToString(ft.runs[0].ClientToken) != "op-destroy") {
			t.Errorf("%s: %v, token %q; want its own task under its own token", c.name, err, aws.ToString(ft.runs[0].ClientToken))
		}
	}
}

// A recorded task ECS no longer describes is no proof it stopped: past the token's safe
// age, with the operation's own marker still there, RunTask is not sent again however
// MISSING the task reads; inside it, RunTask under the token answers that task again.
func TestECSKnownButMissingTaskDoesNotOutliveItsToken(t *testing.T) {
	for _, c := range []struct {
		name    string
		sentAgo time.Duration
		runs    int
	}{
		{"past the token", 25 * time.Hour, 0},
		{"inside the token", time.Hour, 1},
	} {
		fs, ft := &fakeSSM{}, &fakeTasks{exitCode: exit(0), missingPolls: 1}
		rt := newHomeTaskECSWith(&fakeECS{}, &fakeEFS{}, fs, ft)
		fs.values = map[string]string{rt.homeTaskMarker(): markerValue("arn:task/old-A", "op-9")}
		BindHomeTask(rt, HomeTaskBinding{Token: "op-9", TaskARN: "arn:task/old-A", Resume: true,
			SentAt: time.Now().Add(-c.sentAgo)})
		err := rt.WipeHome(context.Background(), HomeWipeRepos)
		if len(ft.runs) != c.runs {
			t.Errorf("%s: %d RunTask, want %d", c.name, len(ft.runs), c.runs)
		}
		if c.runs == 0 && (!errors.Is(err, ErrHomeTaskUnresolved) || fs.values[rt.homeTaskMarker()] == "") {
			t.Errorf("%s: %v, marker %q; want unresolved with the marker kept", c.name, err, fs.values[rt.homeTaskMarker()])
		}
	}
}

// A first RunTask that could not be recorded as sent is not sent, and the marker written
// for it goes: nothing runs, and nothing would ever resolve it.
func TestECSUnrecordedFirstSendLeavesNoMarker(t *testing.T) {
	fs, ft := &fakeSSM{}, &fakeTasks{exitCode: exit(0)}
	rt := newHomeTaskECSWith(&fakeECS{}, &fakeEFS{}, fs, ft)
	BindHomeTask(rt, HomeTaskBinding{Token: "op-10", Sending: func() error { return errors.New("database temporarily unavailable") }})
	err := rt.WipeHome(context.Background(), HomeWipeRepos)
	if err == nil || errors.Is(err, ErrHomeTaskUnresolved) || len(ft.runs) != 0 {
		t.Errorf("= %v with %d RunTask; want a definite failure and none sent", err, len(ft.runs))
	}
	if v, ok := fs.values[rt.homeTaskMarker()]; ok {
		t.Errorf("marker %q left behind", v)
	}
}

// ECS may answer a ListTasks page with fewer tasks than it has, even none, and a NextToken.
// A member's task still running behind that token refuses a wipe and a start, and keeps a
// resumed operation from sending RunTask again beside it.
func TestECSRunningHomeTaskBehindAnEmptyPageStillRefuses(t *testing.T) {
	fe, fs := &fakeECS{}, &fakeSSM{}
	ft := &fakeTasks{inflightNextPage: []string{"arn:task/still-running"}, exitCode: exit(0)}
	rt := newHomeTaskECSWith(fe, &fakeEFS{}, fs, ft)
	if err := rt.HomeWipeBlocked(context.Background()); !errors.Is(err, ErrHomeTaskInFlight) {
		t.Errorf("HomeWipeBlocked = %v, want ErrHomeTaskInFlight", err)
	}
	if err := rt.Start(context.Background()); !errors.Is(err, ErrHomeTaskInFlight) {
		t.Errorf("Start = %v, want ErrHomeTaskInFlight", err)
	}
	if len(ft.runs) != 0 || len(fe.createCalls) != 0 {
		t.Errorf("refused, but runs=%d creates=%d", len(ft.runs), len(fe.createCalls))
	}

	fs = &fakeSSM{}
	ft = &fakeTasks{inflightNextPage: []string{"arn:task/still-running"}, exitCode: exit(0)}
	rt = newHomeTaskECSWith(&fakeECS{}, &fakeEFS{}, fs, ft)
	fs.values = map[string]string{rt.homeTaskMarker(): markerValue(homeTaskMarkerPending, "op-9")}
	BindHomeTask(rt, HomeTaskBinding{Token: "op-9", Resume: true, SentAt: time.Now().Add(-time.Hour)})
	if err := rt.WipeHome(context.Background(), HomeWipeClean); !errors.Is(err, ErrHomeTaskUnresolved) {
		t.Errorf("resumed run = %v, want it unresolved while the task runs", err)
	}
	if len(ft.runs) != 0 {
		t.Errorf("a resumed run sent RunTask %d times beside a running task", len(ft.runs))
	}
}
