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
	fs.values = map[string]string{rt.homeTaskMarker(): homeTaskMarkerPending}

	if err := rt.WipeHome(context.Background(), HomeWipeClean); !errors.Is(err, ErrHomeTaskInFlight) {
		t.Fatalf("an unbound run over a pending marker = %v, want ErrHomeTaskInFlight", err)
	}
	var started string
	BindHomeTask(rt, HomeTaskBinding{Token: "op-7", Resume: true, Started: func(arn string) { started = arn }})
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
		{"recorded on the operation", "arn:task/home-9", "arn:task/home-9"},
		{"only in the marker", "", "arn:task/home-9"},
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
	resumed.ssm.(*fakeSSM).values[resumed.homeTaskMarker()] = homeTaskMarkerPending
	BindHomeTask(resumed, HomeTaskBinding{Token: "op-4", Resume: true})
	if err := resumed.WipeHome(ctx, HomeWipeRepos); !errors.Is(err, ErrHomeTaskUnresolved) {
		t.Errorf("resumed client fault = %v, want ErrHomeTaskUnresolved", err)
	}
	fs := &fakeSSM{}
	resumedNP := newHomeTaskECSWith(&fakeECS{}, &fakeEFS{}, fs, &fakeTasks{runFailure: "RESOURCE:ENI"})
	fs.values = map[string]string{resumedNP.homeTaskMarker(): homeTaskMarkerPending}
	BindHomeTask(resumedNP, HomeTaskBinding{Token: "op-5", Resume: true})
	if err := resumedNP.WipeHome(ctx, HomeWipeRepos); err == nil || errors.Is(err, ErrHomeTaskUnresolved) {
		t.Errorf("resumed, not placed = %v, want a definite failure", err)
	}
	if _, ok := fs.values[resumedNP.homeTaskMarker()]; ok {
		t.Error("a token ECS placed nothing for kept its marker")
	}
}
