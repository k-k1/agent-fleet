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

// fakeTasksHistory adds what ECS still lists under the home task's family with desired
// status STOPPED: the tasks of operations whose answers were lost.
type fakeTasksHistory struct {
	fakeTasks
	history []ecstypes.Task
}

const fakeHomeFamily = "af-stack-home-ops"

func (f *fakeTasksHistory) ListTasks(ctx context.Context, in *ecs.ListTasksInput, opts ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	if aws.ToString(in.Family) == fakeHomeFamily {
		f.lists = append(f.lists, in)
		if in.DesiredStatus != ecstypes.DesiredStatusStopped {
			return &ecs.ListTasksOutput{}, nil
		}
		var arns []string
		for _, t := range f.history {
			arns = append(arns, aws.ToString(t.TaskArn))
		}
		return &ecs.ListTasksOutput{TaskArns: arns}, nil
	}
	return f.fakeTasks.ListTasks(ctx, in, opts...)
}

func (f *fakeTasksHistory) DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, opts ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	var out []ecstypes.Task
	for _, arn := range in.Tasks {
		for _, t := range f.history {
			if aws.ToString(t.TaskArn) == arn {
				out = append(out, t)
			}
		}
	}
	if len(out) == len(in.Tasks) {
		return &ecs.DescribeTasksOutput{Tasks: out}, nil
	}
	return f.fakeTasks.DescribeTasks(ctx, in, opts...)
}

func homeTaskAt(arn, startedBy, status string, created time.Time, code *int32) ecstypes.Task {
	return ecstypes.Task{TaskArn: aws.String(arn), StartedBy: aws.String(startedBy), LastStatus: aws.String(status),
		CreatedAt:  aws.Time(created),
		Containers: []ecstypes.Container{{Name: aws.String(ecsHomeTaskContainer), ExitCode: code}}}
}

// A pending marker no operation record covers — the CP that wrote it was replaced between
// RunTask and its answer — is released by the CP itself once ECS shows the one task started
// for the member after the marker stopped with exit 0, and nothing of the member's running.
// Every doubt keeps the refusal (#1603).
func TestECSPendingMarkerIsReleasedOnlyOnECSsProof(t *testing.T) {
	writtenAt := time.Now().Add(-10 * time.Minute)
	after := writtenAt.Add(3 * time.Second)
	const me = "af-home/M-1"
	for _, c := range []struct {
		name     string
		value    string
		at       time.Time
		history  []ecstypes.Task
		inflight []string
		release  bool
	}{
		{name: "the task stopped with exit 0", value: "pending", at: writtenAt, release: true,
			history: []ecstypes.Task{homeTaskAt("arn:task/lost", me, "STOPPED", after, exit(0))}},
		{name: "the marker carries the operation's token", value: "pending tok-1", at: writtenAt, release: true,
			history: []ecstypes.Task{homeTaskAt("arn:task/lost", me, "STOPPED", after, exit(0))}},
		{name: "another task of the member is running", value: "pending", at: writtenAt, inflight: []string{"arn:task/other"},
			history: []ecstypes.Task{homeTaskAt("arn:task/lost", me, "STOPPED", after, exit(0))}},
		{name: "only an earlier operation's task is listed", value: "pending", at: writtenAt,
			history: []ecstypes.Task{homeTaskAt("arn:task/last-bake", me, "STOPPED", writtenAt.Add(-time.Minute), exit(0))}},
		{name: "the task failed", value: "pending", at: writtenAt,
			history: []ecstypes.Task{homeTaskAt("arn:task/lost", me, "STOPPED", after, exit(1))}},
		{name: "the task never ran its command", value: "pending", at: writtenAt,
			history: []ecstypes.Task{homeTaskAt("arn:task/lost", me, "STOPPED", after, nil)}},
		{name: "the task is still stopping", value: "pending", at: writtenAt,
			history: []ecstypes.Task{homeTaskAt("arn:task/lost", me, "DEACTIVATING", after, nil)}},
		{name: "two tasks after the marker", value: "pending", at: writtenAt,
			history: []ecstypes.Task{homeTaskAt("arn:task/a", me, "STOPPED", after, exit(0)),
				homeTaskAt("arn:task/b", me, "STOPPED", after.Add(time.Second), exit(0))}},
		{name: "only another member's task", value: "pending", at: writtenAt,
			history: []ecstypes.Task{homeTaskAt("arn:task/lost", "af-home/M-2", "STOPPED", after, exit(0))}},
		{name: "ECS no longer lists the task", value: "pending", at: writtenAt},
		{name: "the marker is too young for the listing", value: "pending", at: time.Now().Add(-30 * time.Second),
			history: []ecstypes.Task{homeTaskAt("arn:task/lost", me, "STOPPED", time.Now().Add(-20*time.Second), exit(0))}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs := &fakeSSM{}
			ft := &fakeTasksHistory{fakeTasks: fakeTasks{inflight: c.inflight}, history: c.history}
			rt := newTestECS(&fakeECS{}, &fakeEFS{}, fs)
			rt.cfg.homeTask = fakeHomeFamily
			rt.tasks = ft
			rt.homeTaskPoll = time.Millisecond
			marker := rt.homeTaskMarker()
			fs.values = map[string]string{marker: c.value}
			fs.at = map[string]time.Time{marker: c.at}
			var told []HomeMarkerRelease
			OnHomeMarkerReleased(func(r HomeMarkerRelease) { told = append(told, r) })
			t.Cleanup(func() { OnHomeMarkerReleased(nil) })

			err := rt.HomeWipeBlocked(context.Background())
			_, kept := fs.values[marker]
			if c.release {
				if err != nil || kept {
					t.Fatalf("HomeWipeBlocked = %v, marker kept = %v; want the proven marker released", err, kept)
				}
				if len(told) != 1 || told[0].TaskARN != "arn:task/lost" || told[0].Value != c.value || told[0].MembershipID != "M-1" {
					t.Errorf("told %+v, want one release naming the task", told)
				}
				return
			}
			if !errors.Is(err, ErrHomeTaskInFlight) || !kept {
				t.Errorf("HomeWipeBlocked = %v, marker kept = %v; want the refusal kept", err, kept)
			}
			if len(told) != 0 {
				t.Errorf("told of a release that did not happen: %+v", told)
			}
		})
	}
}

// The symptom of #1603: the golden seed's Start was refused every minute by a marker whose
// task had long finished. Once ECS proves it, the Start goes ahead.
func TestECSStartGoesAheadOnceThePendingMarkersTaskIsProvenStopped(t *testing.T) {
	fs, fe := &fakeSSM{}, &fakeECS{}
	written := time.Now().Add(-5 * time.Minute)
	ft := &fakeTasksHistory{history: []ecstypes.Task{homeTaskAt("arn:task/lost", "af-home/M-1", "STOPPED",
		written.Add(time.Second), exit(0))}}
	rt := newTestECS(fe, &fakeEFS{}, fs)
	rt.cfg.homeTask = fakeHomeFamily
	rt.tasks = ft
	rt.homeTaskPoll = time.Millisecond
	fs.values = map[string]string{rt.homeTaskMarker(): "pending"}
	fs.at = map[string]time.Time{rt.homeTaskMarker(): written}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start = %v, want it to go ahead", err)
	}
	if len(fe.createCalls) == 0 {
		t.Error("Start created no service")
	}
}
