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
)

// addressTasks is an ECS client that lists a fixed set of tasks per desired status.
type addressTasks struct {
	fakeECS
	byStatus map[ecstypes.DesiredStatus][]ecstypes.Task
	// unlisted are tasks DescribeTasks knows but ListTasks leaves out, as an eventually
	// consistent listing can.
	unlisted []ecstypes.Task
	// missing are ARNs DescribeTasks answers MISSING for.
	missing map[string]bool
	listErr error
}

func (f *addressTasks) ListTasks(_ context.Context, in *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var arns []string
	for _, t := range f.byStatus[in.DesiredStatus] {
		arns = append(arns, aws.ToString(t.TaskArn))
	}
	return &ecs.ListTasksOutput{TaskArns: arns}, nil
}

func (f *addressTasks) DescribeTasks(_ context.Context, in *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	out := &ecs.DescribeTasksOutput{}
	all := append([]ecstypes.Task{}, f.unlisted...)
	for _, ts := range f.byStatus {
		all = append(all, ts...)
	}
	for _, arn := range in.Tasks {
		if f.missing[arn] {
			out.Failures = append(out.Failures, ecstypes.Failure{Arn: aws.String(arn), Reason: aws.String("MISSING")})
			continue
		}
		for _, t := range all {
			if arn == aws.ToString(t.TaskArn) {
				out.Tasks = append(out.Tasks, t)
			}
		}
	}
	return out, nil
}

func eniTask(arn, ip string, created time.Time, stopped *time.Time) ecstypes.Task {
	return ecstypes.Task{
		TaskArn:   aws.String(arn),
		CreatedAt: aws.Time(created),
		StoppedAt: stopped,
		Attachments: []ecstypes.Attachment{
			{Type: aws.String("Service Connect")},
			{Type: aws.String("ElasticNetworkInterface"), Details: []ecstypes.KeyValuePair{
				{Name: aws.String("subnetId"), Value: aws.String("subnet-1")},
				{Name: aws.String("privateIPv4Address"), Value: aws.String(ip)},
			}},
		},
	}
}

func heldByRef(r AddressReport) map[string]HeldAddress {
	out := map[string]HeldAddress{}
	for _, h := range r.Held {
		out[h.Ref] = h
	}
	return out
}

func TestECSHeldAddressesReadsTheTaskENI(t *testing.T) {
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	stop := t0.Add(time.Hour)
	f := &addressTasks{byStatus: map[ecstypes.DesiredStatus][]ecstypes.Task{
		ecstypes.DesiredStatusRunning: {eniTask("arn:task/new", "10.0.1.9", t0.Add(2*time.Hour), nil)},
		ecstypes.DesiredStatusStopped: {eniTask("arn:task/old", "10.0.1.5", t0, &stop)},
	}}
	e := &ecsRuntime{cfg: ecsConfig{cluster: "c"}, ecs: f, name: "ws"}
	got, err := e.HeldAddresses(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]HeldAddress{
		"arn:task/new": {Address: "10.0.1.9", Ref: "arn:task/new", From: t0.Add(2 * time.Hour)},
		"arn:task/old": {Address: "10.0.1.5", Ref: "arn:task/old", From: t0, Until: stop},
	}
	if len(got.Held) != len(want) || len(got.Unresolved) != 0 {
		t.Fatalf("got %+v", got)
	}
	for ref, h := range heldByRef(got) {
		if w := want[ref]; h.Address != w.Address || !h.From.Equal(w.From) || !h.Until.Equal(w.Until) {
			t.Errorf("%s: got %+v, want %+v", ref, h, w)
		}
	}

	// A task the caller still has open, left out of the listing: described by ARN, and its
	// stop time reported, rather than read as gone.
	late := t0.Add(3 * time.Hour)
	f.unlisted = []ecstypes.Task{eniTask("arn:task/unlisted", "10.0.1.7", t0, &late)}
	got, err = e.HeldAddresses(context.Background(), []string{"arn:task/new", "arn:task/unlisted"})
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := heldByRef(got)["arn:task/unlisted"]; !ok || !h.Until.Equal(late) {
		t.Errorf("an open task missing from the listing must be described: %+v", got)
	}

	// An open task ECS no longer knows (aged out, or not indexed yet) next to a live one:
	// the live one is still reported, the old one comes back unresolved, and the read
	// does not fail.
	f.missing = map[string]bool{"arn:task/aged-out": true}
	got, err = e.HeldAddresses(context.Background(), []string{"arn:task/aged-out"})
	if err != nil {
		t.Fatalf("MISSING must not fail the read: %v", err)
	}
	if _, ok := heldByRef(got)["arn:task/new"]; !ok || len(got.Unresolved) != 1 || got.Unresolved[0] != "arn:task/aged-out" {
		t.Errorf("want the live task held and the aged-out one unresolved, got %+v", got)
	}
	f.missing = nil
	f.listErr = errors.New("throttled")
	if _, err := e.HeldAddresses(context.Background(), nil); err == nil {
		t.Error("a list failure must be an error")
	}
	f.listErr = &ecstypes.ServiceNotFoundException{}
	if got, err := e.HeldAddresses(context.Background(), nil); err != nil || len(got.Held) != 0 {
		t.Errorf("no service holds nothing: got %+v, %v", got, err)
	}
}

func TestPodHeldAddresses(t *testing.T) {
	pods := []kPod{
		{Metadata: kObjectMeta{UID: "u1", CreationTimestamp: "2026-10-24T08:59:00Z"}, Status: kPodStatus{PodIP: "10.4.0.7", StartTime: "2026-10-24T09:00:00Z"}},
		{Metadata: kObjectMeta{UID: "u2", CreationTimestamp: "2026-10-24T09:01:00Z"}, Status: kPodStatus{PodIP: "10.4.0.8"}},
		{Metadata: kObjectMeta{UID: "u3"}, Status: kPodStatus{}}, // pending, no address yet
	}
	got := podHeldAddresses(pods)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Ref != "u1" || got[0].Address != "10.4.0.7" || !got[0].From.Equal(time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("pod with a start time: %+v", got[0])
	}
	if got[1].Ref != "u2" || !got[1].From.Equal(time.Date(2026, 10, 24, 9, 1, 0, 0, time.UTC)) {
		t.Errorf("pod without a start time falls back to creation: %+v", got[1])
	}
}

func TestDockerHeldAddresses(t *testing.T) {
	var asked string
	d := &dockerRuntime{name: "ws", network: "af-u1", inspect: func(_ context.Context, _, _, format string) string {
		asked = format
		return "abc123|2026-10-24T09:00:00.123456789Z|172.19.0.2"
	}}
	got, err := d.HeldAddresses(context.Background(), nil)
	if err != nil || len(got.Held) != 1 || got.Held[0].Ref != "abc123" || got.Held[0].Address != "172.19.0.2" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if want := `index .NetworkSettings.Networks "af-u1"`; !strings.Contains(asked, want) {
		t.Errorf("the address must be read on the per-user network: format %q", asked)
	}
	// Stopped: no address on the network, nothing held.
	d.inspect = func(context.Context, string, string, string) string { return "abc123|2026-10-24T09:00:00Z|" }
	if got, err := d.HeldAddresses(context.Background(), nil); err != nil || len(got.Held) != 0 {
		t.Errorf("stopped container: %+v, %v", got, err)
	}
	// Gone: nothing held, not an error.
	d.inspect = func(context.Context, string, string, string) string { return "" }
	if got, err := d.HeldAddresses(context.Background(), nil); err != nil || len(got.Held) != 0 {
		t.Errorf("missing container: %+v, %v", got, err)
	}
	if _, err := dockerHeldAddresses("garbage"); err == nil {
		t.Error("unparseable output must be an error, not an empty list")
	}
}

// A docker Stop removes the container and its address with it, so the address is handed to
// the hook before the stop runs: a workspace started and stopped between two recorder
// sweeps is still on record.
func TestDockerStopHandsTheAddressToTheReleaseHook(t *testing.T) {
	var gotM, gotW string
	var got []HeldAddress
	d := &dockerRuntime{
		name: "ws", network: "af-u1", dataDir: t.TempDir(), membership: "m1", wsID: "w1",
		inspect: func(context.Context, string, string, string) string {
			return "abc123|2026-10-24T09:00:00Z|172.19.0.2"
		},
		onRelease: func(_ context.Context, m, w string, held []HeldAddress) { gotM, gotW, got = m, w, held },
	}
	_ = d.Stop(context.Background()) // no docker CLI here; the hook runs before it is needed
	if gotM != "m1" || gotW != "w1" || len(got) != 1 || got[0].Address != "172.19.0.2" {
		t.Errorf("hook got %q %q %+v", gotM, gotW, got)
	}
}

// The Kubernetes Stop deletes the pod, so its address is handed to the hook before the
// StatefulSet is scaled to zero.
func TestKubeStopHandsThePodAddressToTheReleaseHook(t *testing.T) {
	rt, f := fakeKubeRuntime(t)
	var got []HeldAddress
	rt.cfg.onRelease = func(_ context.Context, _, _ string, held []HeldAddress) { got = held }
	f.set(stsPathX, 200, stsJSON(1, 4, 4, 1, "af-ws-x-7f8c9d6b4", "2"))
	f.set(podsPathX, 200, podListJSON(`{"metadata":{"name":"af-ws-x-0","uid":"pod-uid-1"},`+
		`"status":{"phase":"Running","podIP":"10.4.0.7","startTime":"2026-10-24T09:00:00Z"}}`))
	_ = rt.Stop(context.Background()) // the scale-down itself is not served here
	if len(got) != 1 || got[0].Ref != "pod-uid-1" || got[0].Address != "10.4.0.7" {
		t.Errorf("hook got %+v", got)
	}
}
