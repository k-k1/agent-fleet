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
	byStatus  map[ecstypes.DesiredStatus][]ecstypes.Task
	failures  []ecstypes.Failure
	listErr   error
	described int
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
	f.described++
	out := &ecs.DescribeTasksOutput{Failures: f.failures}
	for _, ts := range f.byStatus {
		for _, t := range ts {
			for _, arn := range in.Tasks {
				if arn == aws.ToString(t.TaskArn) {
					out.Tasks = append(out.Tasks, t)
				}
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

func TestECSHeldAddressesReadsTheTaskENI(t *testing.T) {
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	stop := t0.Add(time.Hour)
	f := &addressTasks{byStatus: map[ecstypes.DesiredStatus][]ecstypes.Task{
		ecstypes.DesiredStatusRunning: {eniTask("arn:task/new", "10.0.1.9", t0.Add(2*time.Hour), nil)},
		ecstypes.DesiredStatusStopped: {eniTask("arn:task/old", "10.0.1.5", t0, &stop)},
	}}
	e := &ecsRuntime{cfg: ecsConfig{cluster: "c"}, ecs: f, name: "ws"}
	got, err := e.HeldAddresses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]HeldAddress{
		"arn:task/new": {Address: "10.0.1.9", Ref: "arn:task/new", From: t0.Add(2 * time.Hour)},
		"arn:task/old": {Address: "10.0.1.5", Ref: "arn:task/old", From: t0, Until: stop},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for _, h := range got {
		if w := want[h.Ref]; h.Address != w.Address || !h.From.Equal(w.From) || !h.Until.Equal(w.Until) {
			t.Errorf("%s: got %+v, want %+v", h.Ref, h, w)
		}
	}

	// A task that could not be described must fail the read, not shorten the list: a
	// missing task reads as one that stopped holding its address.
	f.failures = []ecstypes.Failure{{Arn: aws.String("arn:task/new"), Reason: aws.String("INTERNAL")}}
	if _, err := e.HeldAddresses(context.Background()); err == nil {
		t.Error("a describe failure must be an error")
	}
	f.failures = nil
	f.listErr = errors.New("throttled")
	if _, err := e.HeldAddresses(context.Background()); err == nil {
		t.Error("a list failure must be an error")
	}
	f.listErr = &ecstypes.ServiceNotFoundException{}
	if got, err := e.HeldAddresses(context.Background()); err != nil || len(got) != 0 {
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
	got, err := d.HeldAddresses(context.Background())
	if err != nil || len(got) != 1 || got[0].Ref != "abc123" || got[0].Address != "172.19.0.2" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if want := `index .NetworkSettings.Networks "af-u1"`; !strings.Contains(asked, want) {
		t.Errorf("the address must be read on the per-user network: format %q", asked)
	}
	// Stopped: no address on the network, nothing held.
	d.inspect = func(context.Context, string, string, string) string { return "abc123|2026-10-24T09:00:00Z|" }
	if got, err := d.HeldAddresses(context.Background()); err != nil || len(got) != 0 {
		t.Errorf("stopped container: %+v, %v", got, err)
	}
	// Gone: nothing held, not an error.
	d.inspect = func(context.Context, string, string, string) string { return "" }
	if got, err := d.HeldAddresses(context.Background()); err != nil || len(got) != 0 {
		t.Errorf("missing container: %+v, %v", got, err)
	}
	if _, err := dockerHeldAddresses("garbage"); err == nil {
		t.Error("unparseable output must be an error, not an empty list")
	}
}
