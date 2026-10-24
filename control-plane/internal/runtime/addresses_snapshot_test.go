package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// clusterTasksFake is an ECS cluster with many services, paged listings and counted
// requests. Tasks live in byStatus by desired status; each carries its service group.
type clusterTasksFake struct {
	fakeECS
	byStatus map[ecstypes.DesiredStatus][]ecstypes.Task
	pageSize int
	missing  map[string]bool

	listCalls, describeCalls int
	// failList / failDescribe fail the nth (1-based) call of that kind.
	failList, failDescribe int
}

func (f *clusterTasksFake) ListTasks(_ context.Context, in *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	f.listCalls++
	if f.failList == f.listCalls {
		return nil, errors.New("throttled")
	}
	var arns []string
	for _, t := range f.byStatus[in.DesiredStatus] {
		if in.ServiceName != nil && aws.ToString(t.Group) != "service:"+aws.ToString(in.ServiceName) {
			continue
		}
		arns = append(arns, aws.ToString(t.TaskArn))
	}
	start := 0
	if in.NextToken != nil {
		fmt.Sscanf(*in.NextToken, "%d", &start)
	}
	out := &ecs.ListTasksOutput{}
	end := len(arns)
	if f.pageSize > 0 && start+f.pageSize < end {
		end = start + f.pageSize
		out.NextToken = aws.String(fmt.Sprint(end))
	}
	out.TaskArns = arns[start:end]
	return out, nil
}

func (f *clusterTasksFake) DescribeTasks(_ context.Context, in *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	f.describeCalls++
	if f.failDescribe == f.describeCalls {
		return nil, errors.New("throttled")
	}
	if len(in.Tasks) > 100 {
		return nil, errors.New("too many tasks")
	}
	out := &ecs.DescribeTasksOutput{}
	for _, arn := range in.Tasks {
		if f.missing[arn] {
			out.Failures = append(out.Failures, ecstypes.Failure{Arn: aws.String(arn), Reason: aws.String("MISSING")})
			continue
		}
		for _, ts := range f.byStatus {
			for _, t := range ts {
				if aws.ToString(t.TaskArn) == arn {
					out.Tasks = append(out.Tasks, t)
				}
			}
		}
	}
	return out, nil
}

func (f *clusterTasksFake) requests() int { return f.listCalls + f.describeCalls }

func groupedTask(svc, arn, ip string, created time.Time, stopped *time.Time) ecstypes.Task {
	t := eniTask(arn, ip, created, stopped)
	t.Group = aws.String("service:" + svc)
	return t
}

// manyWorkspaces builds n workspaces with one running and one stopped task each, plus a
// cluster task that belongs to no workspace.
func manyWorkspaces(n int) (*clusterTasksFake, []*ecsRuntime) {
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	stop := t0.Add(time.Hour)
	f := &clusterTasksFake{byStatus: map[ecstypes.DesiredStatus][]ecstypes.Task{}, pageSize: 40}
	var rts []*ecsRuntime
	for i := range n {
		svc := fmt.Sprintf("ws-%03d", i)
		f.byStatus[ecstypes.DesiredStatusRunning] = append(f.byStatus[ecstypes.DesiredStatusRunning],
			groupedTask(svc, "arn:task/"+svc+"/new", fmt.Sprintf("10.0.%d.9", i), t0.Add(2*time.Hour), nil))
		f.byStatus[ecstypes.DesiredStatusStopped] = append(f.byStatus[ecstypes.DesiredStatusStopped],
			groupedTask(svc, "arn:task/"+svc+"/old", fmt.Sprintf("10.1.%d.9", i), t0, &stop))
		rts = append(rts, &ecsRuntime{cfg: ecsConfig{cluster: "c"}, ecs: f, name: svc})
	}
	other := eniTask("arn:task/cp", "10.9.9.9", t0, nil)
	other.Group = aws.String("family:cp")
	f.byStatus[ecstypes.DesiredStatusRunning] = append(f.byStatus[ecstypes.DesiredStatusRunning], other)
	return f, rts
}

func sortedRefs(r AddressReport) []string {
	var refs []string
	for _, h := range r.Held {
		refs = append(refs, h.Ref+"@"+h.Address)
	}
	sort.Strings(refs)
	return refs
}

// The point of the snapshot: the requests per sweep stop growing with the workspaces, and
// every workspace gets the answer its own listing would have given.
func TestECSAddressSnapshotRequestCount(t *testing.T) {
	const n = 150
	ctx := context.Background()
	f, rts := manyWorkspaces(n)
	perWorkspace := map[string][]string{}
	for _, e := range rts {
		r, err := e.HeldAddresses(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		perWorkspace[e.name] = sortedRefs(r)
	}
	before := f.requests()
	if before < 3*n {
		t.Fatalf("baseline should be about 3 requests per workspace, got %d for %d", before, n)
	}

	f.listCalls, f.describeCalls = 0, 0
	snap := (&ecsFactory{}).AddressSnapshot(ctx)
	for _, e := range rts {
		rep, ok := snap.ReporterFor(e)
		if !ok {
			t.Fatal("an ECS runtime must be served from the snapshot")
		}
		r, err := rep.HeldAddresses(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, want := sortedRefs(r), perWorkspace[e.name]
		if len(got) != 2 || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s: snapshot %v, per-workspace %v", e.name, got, want)
		}
	}
	t.Logf("requests for %d workspaces: per-workspace %d, snapshot %d (list %d, describe %d)",
		n, before, f.requests(), f.listCalls, f.describeCalls)
	if f.describeCalls != 4 {
		t.Errorf("301 tasks are described in 4 batches of at most 100, got %d calls", f.describeCalls)
	}
	if f.listCalls != 4+4 {
		t.Errorf("150 running + 1 other and 150 stopped, paged by 40: want 8 list calls, got %d", f.listCalls)
	}
}

// A task the listing left out but the caller still has open is described by ARN, exactly as
// the per-workspace path does; MISSING is unresolved.
func TestECSAddressSnapshotDescribesOpenRefsMissingFromTheListing(t *testing.T) {
	ctx := context.Background()
	f, rts := manyWorkspaces(2)
	late := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	// Known to DescribeTasks only, in no listing.
	f.byStatus[ecstypes.DesiredStatus("HIDDEN")] = []ecstypes.Task{groupedTask("ws-000", "arn:task/ws-000/unlisted", "10.0.0.7", late.Add(-time.Hour), &late)}
	f.missing = map[string]bool{"arn:task/ws-000/aged-out": true}
	snap := (&ecsFactory{}).AddressSnapshot(ctx)
	rep, _ := snap.ReporterFor(rts[0])
	open := []string{"arn:task/ws-000/new", "arn:task/ws-000/unlisted", "arn:task/ws-000/aged-out"}
	f.listCalls, f.describeCalls = 0, 0
	got, err := rep.HeldAddresses(ctx, open)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := heldByRef(got)["arn:task/ws-000/unlisted"]; !ok || !h.Until.Equal(late) {
		t.Errorf("an open ref missing from the listing must be described: %+v", got)
	}
	if len(got.Unresolved) != 1 || got.Unresolved[0] != "arn:task/ws-000/aged-out" {
		t.Errorf("MISSING must come back unresolved: %+v", got)
	}
	// Two listings (one page each), one batch for the snapshot, one for the open refs.
	if f.listCalls != 2 || f.describeCalls != 2 {
		t.Errorf("want 2 list and 2 describe calls, got %d and %d", f.listCalls, f.describeCalls)
	}
}

// One page or one DescribeTasks batch failing fails the read of every workspace the
// snapshot covers: a partial snapshot would read them as holding nothing.
func TestECSAddressSnapshotPartialFailureReportsNothing(t *testing.T) {
	ctx := context.Background()
	for name, set := range map[string]func(*clusterTasksFake){
		"second running page": func(f *clusterTasksFake) { f.failList = 2 },
		"stopped listing":     func(f *clusterTasksFake) { f.failList = 5 },
		"second describe":     func(f *clusterTasksFake) { f.failDescribe = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			f, rts := manyWorkspaces(120)
			set(f)
			snap := (&ecsFactory{}).AddressSnapshot(ctx)
			for _, e := range rts {
				rep, _ := snap.ReporterFor(e)
				if r, err := rep.HeldAddresses(ctx, []string{"arn:task/" + e.name + "/old"}); err == nil {
					t.Fatalf("%s: a partial snapshot answered %+v", e.name, r)
				}
			}
			if name != "second running page" && f.requests() > 20 {
				t.Errorf("a failed read must not be retried per workspace: %d requests", f.requests())
			}
		})
	}
}

func TestECSAddressSnapshotCoversEC2AndSkipsOthers(t *testing.T) {
	snap := (&ecsEC2Factory{}).AddressSnapshot(context.Background())
	_, rts := manyWorkspaces(1)
	if _, ok := snap.ReporterFor(&ecsEC2Runtime{base: rts[0]}); !ok {
		t.Error("an ecs-ec2 runtime reads through its base")
	}
	if _, ok := snap.ReporterFor(&dockerRuntime{}); ok {
		t.Error("a docker runtime is not in the snapshot")
	}
}

// blockingTasks never answers a listing until its context ends.
type blockingTasks struct {
	fakeECS
	listCalls int
}

func (f *blockingTasks) ListTasks(ctx context.Context, _ *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	f.listCalls++
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *blockingTasks) DescribeTasks(ctx context.Context, _ *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A cluster read that hangs ends on its own budget, well before the sweep's deadline, and
// the failure is shared: the next workspace fails at once instead of waiting again.
func TestECSAddressSnapshotReadHasItsOwnBudget(t *testing.T) {
	f := &blockingTasks{}
	snap := newECSAddressSnapshot(context.Background())
	snap.timeout = 50 * time.Millisecond
	a := &ecsRuntime{cfg: ecsConfig{cluster: "c"}, ecs: f, name: "a"}
	b := &ecsRuntime{cfg: ecsConfig{cluster: "c"}, ecs: f, name: "b"}
	start := time.Now()
	for _, e := range []*ecsRuntime{a, b} {
		rep, _ := snap.ReporterFor(e)
		if _, err := rep.HeldAddresses(context.Background(), nil); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s: want the read's own deadline, got %v", e.name, err)
		}
	}
	if d := time.Since(start); d > 5*time.Second || f.listCalls != 1 {
		t.Errorf("one bounded read for the cluster: took %s, %d listings", d, f.listCalls)
	}
}

// The sweep's own cancellation still ends the read.
func TestECSAddressSnapshotStopsWithTheSweep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	snap := newECSAddressSnapshot(ctx)
	e := &ecsRuntime{cfg: ecsConfig{cluster: "c"}, ecs: &blockingTasks{}, name: "a"}
	rep, _ := snap.ReporterFor(e)
	time.AfterFunc(20*time.Millisecond, cancel)
	if _, err := rep.HeldAddresses(context.Background(), nil); !errors.Is(err, context.Canceled) {
		t.Errorf("want the sweep's cancellation, got %v", err)
	}
}
