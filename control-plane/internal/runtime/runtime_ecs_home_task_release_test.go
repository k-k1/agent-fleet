package runtime

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// fakeTasksHistory adds what ECS still lists under the home task's family with desired
// status STOPPED: the tasks of operations whose answers were lost. pageSize splits the
// listing into pages; failARN is answered as a DescribeTasks failure and dropARN left out
// of the answer without one.
type fakeTasksHistory struct {
	fakeTasks
	history          []ecstypes.Task
	pageSize         int
	failARN, dropARN string
	describedBatches []int
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
		from := 0
		if in.NextToken != nil {
			from, _ = strconv.Atoi(aws.ToString(in.NextToken))
		}
		to := len(arns)
		if f.pageSize > 0 && from+f.pageSize < to {
			to = from + f.pageSize
		}
		out := &ecs.ListTasksOutput{TaskArns: arns[from:to]}
		if to < len(arns) {
			out.NextToken = aws.String(strconv.Itoa(to))
		}
		return out, nil
	}
	return f.fakeTasks.ListTasks(ctx, in, opts...)
}

func (f *fakeTasksHistory) DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, opts ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	out := &ecs.DescribeTasksOutput{}
	inHistory := false
	for _, arn := range in.Tasks {
		for _, t := range f.history {
			if aws.ToString(t.TaskArn) != arn {
				continue
			}
			inHistory = true
			switch arn {
			case f.failARN:
				out.Failures = append(out.Failures, ecstypes.Failure{Arn: aws.String(arn), Reason: aws.String("MISSING")})
			case f.dropARN:
			default:
				out.Tasks = append(out.Tasks, t)
			}
		}
	}
	if inHistory {
		f.describedBatches = append(f.describedBatches, len(in.Tasks))
		return out, nil
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
	earlier := writtenAt.Add(-3 * time.Second)
	lost := homeTaskAt("arn:task/lost", me, "STOPPED", after, exit(0))
	noStartedBy := homeTaskAt("arn:task/manual", "", "STOPPED", after, exit(0))
	noStartedBy.StartedBy = nil
	noCreated := homeTaskAt("arn:task/lost", me, "STOPPED", after, exit(0))
	noCreated.CreatedAt = nil
	// 150 tasks of other members around one of ours: two listing pages, two describe batches.
	var crowd []ecstypes.Task
	for i := range 150 {
		crowd = append(crowd, homeTaskAt(fmt.Sprintf("arn:task/other-%d", i), fmt.Sprintf("af-home/M-%d", i+2), "STOPPED", after, exit(0)))
	}
	crowd = append(crowd[:120], append([]ecstypes.Task{lost}, crowd[120:]...)...)
	for _, c := range []struct {
		name     string
		value    string
		at       time.Time
		history  []ecstypes.Task
		inflight []string
		pageSize int
		fail     string
		drop     string
		release  bool
	}{
		{name: "the task stopped with exit 0", value: "pending", at: writtenAt, release: true,
			history: []ecstypes.Task{lost}},
		{name: "an earlier operation long before the marker", value: "pending", at: writtenAt, release: true,
			history: []ecstypes.Task{homeTaskAt("arn:task/last-bake", me, "STOPPED", writtenAt.Add(-time.Hour), exit(0)), lost}},
		{name: "past one page and one describe batch", value: "pending", at: writtenAt, release: true,
			history: crowd, pageSize: 100},
		{name: "the marker carries an operation's token", value: "pending tok-1", at: writtenAt,
			history: []ecstypes.Task{lost}},
		{name: "another task of the member is running", value: "pending", at: writtenAt, inflight: []string{"arn:task/other"},
			history: []ecstypes.Task{lost}},
		{name: "only an earlier operation's task is listed", value: "pending", at: writtenAt,
			history: []ecstypes.Task{homeTaskAt("arn:task/last-bake", me, "STOPPED", writtenAt.Add(-time.Hour), exit(0))}},
		{name: "an earlier operation's task just before the marker, ours not listed yet", value: "pending", at: writtenAt,
			history: []ecstypes.Task{homeTaskAt("arn:task/last-op", me, "STOPPED", earlier, exit(0))}},
		{name: "an earlier operation's task just before the marker beside ours", value: "pending", at: writtenAt,
			history: []ecstypes.Task{homeTaskAt("arn:task/last-op", me, "STOPPED", earlier, exit(0)), lost}},
		{name: "an older task of the member still stopping", value: "pending", at: writtenAt,
			history: []ecstypes.Task{homeTaskAt("arn:task/old", me, "DEACTIVATING", writtenAt.Add(-time.Hour), nil), lost}},
		{name: "the task failed", value: "pending", at: writtenAt,
			history: []ecstypes.Task{homeTaskAt("arn:task/lost", me, "STOPPED", after, exit(1))}},
		{name: "the task never ran its command", value: "pending", at: writtenAt,
			history: []ecstypes.Task{homeTaskAt("arn:task/lost", me, "STOPPED", after, nil)}},
		{name: "the task is still stopping", value: "pending", at: writtenAt,
			history: []ecstypes.Task{homeTaskAt("arn:task/lost", me, "DEACTIVATING", after, nil)}},
		{name: "two tasks after the marker", value: "pending", at: writtenAt,
			history: []ecstypes.Task{homeTaskAt("arn:task/a", me, "STOPPED", after, exit(0)),
				homeTaskAt("arn:task/b", me, "STOPPED", after.Add(time.Second), exit(0))}},
		{name: "a listed task answered as a failure", value: "pending", at: writtenAt, fail: "arn:task/b",
			history: []ecstypes.Task{lost, homeTaskAt("arn:task/b", me, "STOPPED", after, exit(0))}},
		{name: "a listed task missing from the answer", value: "pending", at: writtenAt, drop: "arn:task/b",
			history: []ecstypes.Task{lost, homeTaskAt("arn:task/b", me, "STOPPED", after, exit(0))}},
		{name: "a listed task without startedBy", value: "pending", at: writtenAt,
			history: []ecstypes.Task{lost, noStartedBy}},
		{name: "the member's task without a creation time", value: "pending", at: writtenAt,
			history: []ecstypes.Task{noCreated}},
		{name: "only another member's task", value: "pending", at: writtenAt,
			history: []ecstypes.Task{homeTaskAt("arn:task/lost", "af-home/M-2", "STOPPED", after, exit(0))}},
		{name: "ECS no longer lists the task", value: "pending", at: writtenAt},
		{name: "the marker is too young for the listing", value: "pending", at: time.Now().Add(-30 * time.Second),
			history: []ecstypes.Task{homeTaskAt("arn:task/lost", me, "STOPPED", time.Now().Add(-20*time.Second), exit(0))}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs := &fakeSSM{}
			ft := &fakeTasksHistory{fakeTasks: fakeTasks{inflight: c.inflight}, history: c.history,
				pageSize: c.pageSize, failARN: c.fail, dropARN: c.drop}
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
			if c.pageSize > 0 && len(ft.describedBatches) != 2 {
				t.Errorf("described in batches %v, want two (150 tasks, 100 a call)", ft.describedBatches)
			}
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
