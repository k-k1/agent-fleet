package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// fakeTTSECS serves one service snapshot and records UpdateService desired counts.
type fakeTTSECS struct {
	svc      *ecstypes.Service // nil = not found
	desired  []int32           // recorded UpdateService calls
	describe int               // DescribeServices calls, for the TTL cache test
	// instances are the cluster's container instances, keyed arn -> capacity provider.
	// Only an engine that declares a capacity provider ever reads them (ADR 0071).
	instances map[string]string
	// registered is the registeredAt the box reports, i.e. when it joined the cluster. Zero
	// leaves it unset, which is the shape of a cluster that has no box at all.
	registered time.Time
	// instanceType is reported the way ECS reports it — as an ATTRIBUTE, not a field — because
	// that is the only place a Managed Instances box's type can be read (ADR 0074). Empty
	// leaves the attribute off entirely, which is a real answer and not a match.
	instanceType string
	listCalls    int
}

func (f *fakeTTSECS) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	f.describe++
	out := &ecs.DescribeServicesOutput{}
	if f.svc != nil {
		out.Services = []ecstypes.Service{*f.svc}
	}
	return out, nil
}

func (f *fakeTTSECS) UpdateService(_ context.Context, in *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
	f.desired = append(f.desired, aws.ToInt32(in.DesiredCount))
	return &ecs.UpdateServiceOutput{}, nil
}

func (f *fakeTTSECS) ListContainerInstances(_ context.Context, _ *ecs.ListContainerInstancesInput, _ ...func(*ecs.Options)) (*ecs.ListContainerInstancesOutput, error) {
	f.listCalls++
	out := &ecs.ListContainerInstancesOutput{}
	for arn := range f.instances {
		out.ContainerInstanceArns = append(out.ContainerInstanceArns, arn)
	}
	return out, nil
}

func (f *fakeTTSECS) DescribeContainerInstances(_ context.Context, in *ecs.DescribeContainerInstancesInput, _ ...func(*ecs.Options)) (*ecs.DescribeContainerInstancesOutput, error) {
	out := &ecs.DescribeContainerInstancesOutput{}
	for _, arn := range in.ContainerInstances {
		ci := ecstypes.ContainerInstance{
			ContainerInstanceArn: aws.String(arn),
			Ec2InstanceId:        aws.String("i-" + strings.TrimPrefix(arn, "arn:ci/i-")),
			Status:               aws.String("ACTIVE"),
		}
		if !f.registered.IsZero() {
			ci.RegisteredAt = aws.Time(f.registered)
		}
		if cp := f.instances[arn]; cp != "" {
			ci.CapacityProviderName = aws.String(cp)
		}
		if f.instanceType != "" {
			ci.Attributes = []ecstypes.Attribute{
				{Name: aws.String("ecs.availability-zone"), Value: aws.String("ap-northeast-1a")},
				{Name: aws.String(engineBoxTypeAttr), Value: aws.String(f.instanceType)},
			}
		}
		out.ContainerInstances = append(out.ContainerInstances, ci)
	}
	return out, nil
}

// When the BOX started, which is the question `lastStart` cannot answer: that one is the
// service's primary deployment timestamp and moves on a stack update or a replaced task
// without a new box being bought. An operator looking at a $1.26/hour GPU means the box.
//
// ⚠️ The lookup goes through ECS and not through `ec2 describe-instances`. A Managed Instances
// box does not appear in an unfiltered EC2 listing at all — measured on af-sandbox while the
// task was RUNNING (ADR 0071, P1 の実測 2) — so an EC2-side implementation would report
// "no box" for a GPU that is running and billing.
func TestEngineECSBoxReportsWhenTheInstanceStarted(t *testing.T) {
	at := time.Date(2026, 9, 8, 4, 3, 0, 0, time.UTC)
	f := &fakeTTSECS{
		svc:        &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1},
		instances:  map[string]string{"arn:ci/i-08a9": "af-engines-image"},
		registered: at,
	}
	eng := &engineECS{api: f, key: "image", cluster: "c", service: "image", capacityProvider: "af-engines-image"}

	b, ok := eng.box(t.Context())
	if !ok {
		t.Fatal("no box found while one is registered under this engine's capacity provider")
	}
	if !b.since.Equal(at) || b.instanceID != "i-08a9" {
		t.Errorf("box = %+v, want i-08a9 registered at %v", b, at)
	}

	// Cached: the admin panel polls every five seconds while an engine is moving, and a box
	// takes minutes to appear and 427-477 s to go away. Nothing here changes inside the TTL.
	before := f.listCalls
	for range 4 {
		_, _ = eng.box(t.Context())
	}
	if f.listCalls != before {
		t.Errorf("%d extra cluster walks inside the TTL, want 0", f.listCalls-before)
	}

	// A Fargate engine declares no capacity provider and must never pay for the lookup.
	f2 := &fakeTTSECS{svc: f.svc, instances: f.instances, registered: at}
	fargate := &engineECS{api: f2, key: "tts", cluster: "c", service: "voicevox"}
	if _, ok := fargate.box(t.Context()); ok || f2.listCalls != 0 {
		t.Errorf("fargate: found=%v calls=%d, want false/0", ok, f2.listCalls)
	}
}

func TestTTSEngineECSState(t *testing.T) {
	cases := []struct {
		name        string
		svc         *ecstypes.Service
		wantState   string
		wantDesired int32
	}{
		{"running", &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}, "running", 1},
		{"starting", &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 0}, "starting", 1},
		{"stopped", &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0, RunningCount: 0}, "stopped", 0},
		{"inactive→none", &ecstypes.Service{Status: aws.String("INACTIVE"), DesiredCount: 1}, "none", 0},
	}
	for _, c := range cases {
		eng := &engineECS{api: &fakeTTSECS{svc: c.svc}, cluster: "c", service: "voicevox"}
		v, err := eng.view(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if v.state != c.wantState || v.desired != c.wantDesired {
			t.Errorf("%s: state=%q desired=%d, want %q/%d", c.name, v.state, v.desired, c.wantState, c.wantDesired)
		}
	}

	// missing service → error (misconfiguration should be visible, not "none" silently)
	eng := &engineECS{api: &fakeTTSECS{}, cluster: "c", service: "voicevox"}
	if _, err := eng.view(t.Context()); err == nil {
		t.Error("missing service should return an error")
	}
}

// A Managed Instances engine is "stopped" from the moment the task goes, and keeps billing
// for the several minutes AWS then takes to terminate the box (measured 427-463 s on a GPU
// box). The controller has to be able to see that: the idle window cannot be tuned against
// a cost that is already spent, and a start that lands on a box still holding the model
// file skips the whole S3 fetch (ADR 0071 decision 7). A Fargate engine declares no capacity
// provider and must never pay for the lookup, let alone report the state.
func TestEngineECSDrainingIsOnlyForManagedInstances(t *testing.T) {
	stopped := func() *ecstypes.Service {
		return &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0, RunningCount: 0}
	}
	f := &fakeTTSECS{svc: stopped(), instances: map[string]string{"arn:ci/i-1": "af-engines-llm"}}
	eng := &engineECS{api: f, key: "llm", cluster: "c", service: "llm", capacityProvider: "af-engines-llm"}
	v, err := eng.view(t.Context())
	if err != nil || v.state != "draining" {
		t.Fatalf("state=%q err=%v, want draining", v.state, err)
	}

	// Somebody else's box on the same cluster (a workspace slot) is not this engine draining.
	f = &fakeTTSECS{svc: stopped(), instances: map[string]string{"arn:ci/i-2": ""}}
	eng = &engineECS{api: f, key: "llm", cluster: "c", service: "llm", capacityProvider: "af-engines-llm"}
	if v, _ := eng.view(t.Context()); v.state != "stopped" {
		t.Fatalf("state=%q, want stopped — a pool slot is not an engine box", v.state)
	}

	// Fargate: no provider declared, so the cluster is never walked at all.
	f = &fakeTTSECS{svc: stopped(), instances: map[string]string{"arn:ci/i-1": "af-engines-llm"}}
	eng = &engineECS{api: f, key: "tts", cluster: "c", service: "voicevox"}
	if v, _ := eng.view(t.Context()); v.state != "stopped" {
		t.Fatalf("state=%q, want stopped — a Fargate engine has no box to drain", v.state)
	}
}

// TestTTSEngineViewCache — DescribeServices was called once per /api/tts/status request,
// which is fine while only the admin panel polls and is not once every client polls to
// render "starting" (ADR 0070 decision 10). The cache also has to step aside the moment
// the desired count is written, or the answer to "did my click do anything" is stale.
func TestTTSEngineViewCache(t *testing.T) {
	f := &fakeTTSECS{svc: &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}}
	now := time.Now()
	eng := &engineECS{api: f, cluster: "c", service: "voicevox", now: func() time.Time { return now }}

	for range 5 {
		if _, err := eng.view(t.Context()); err != nil {
			t.Fatalf("view: %v", err)
		}
	}
	if f.describe != 1 {
		t.Errorf("DescribeServices calls = %d, want 1 (the rest served from the cache)", f.describe)
	}

	now = now.Add(engineViewTTL + time.Second)
	if _, err := eng.view(t.Context()); err != nil {
		t.Fatalf("view after the TTL: %v", err)
	}
	if f.describe != 2 {
		t.Errorf("DescribeServices calls after the TTL = %d, want 2", f.describe)
	}

	// A write invalidates it, whatever the TTL says.
	if err := eng.setEnabled(t.Context(), true); err != nil {
		t.Fatalf("setEnabled: %v", err)
	}
	if _, err := eng.view(t.Context()); err != nil {
		t.Fatalf("view after a write: %v", err)
	}
	if f.describe != 3 {
		t.Errorf("DescribeServices calls after a write = %d, want 3 (the cache must not survive it)", f.describe)
	}
}

// TestTTSEngineViewDetail — the two fields the controller judges a start by: when the
// current transition began (the primary deployment) and what ECS says went wrong (the
// service events).
//
// `updatedAt`, never `createdAt`: measured on a real service, `createdAt` is when the
// deployment was created — hours or days before any of this — and it does not move when the
// desired count goes 0 → 1. Judging the start deadline by it declares every start on an
// older service failed the instant it begins, so the engine can never come up at all.
func TestTTSEngineViewDetail(t *testing.T) {
	created := time.Date(2026, 9, 6, 8, 30, 0, 0, time.UTC)
	updated := created.Add(3 * time.Hour)
	f := &fakeTTSECS{svc: &ecstypes.Service{
		Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 0,
		Deployments: []ecstypes.Deployment{
			{Status: aws.String("ACTIVE"), CreatedAt: aws.Time(created.Add(-time.Hour))},
			{
				Status: aws.String("PRIMARY"), CreatedAt: aws.Time(created), UpdatedAt: aws.Time(updated),
				RolloutState: ecstypes.DeploymentRolloutStateInProgress,
			},
		},
		Events: []ecstypes.ServiceEvent{
			{Message: aws.String("unable to place a task")},
			{Message: aws.String("  ")},
			{Message: aws.String("older")},
			{Message: aws.String("older still")},
		},
	}}
	eng := &engineECS{api: f, cluster: "c", service: "voicevox"}
	v, err := eng.view(t.Context())
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if !v.lastStart.Equal(updated) {
		t.Errorf("lastStart = %v, want the PRIMARY deployment's updatedAt %v (createdAt is the deployment, not the start)", v.lastStart, updated)
	}
	if v.rollout != string(ecstypes.DeploymentRolloutStateInProgress) {
		t.Errorf("rollout = %q, want IN_PROGRESS", v.rollout)
	}
	if got := v.eventMessages(); len(got) != 2 || got[0] != "unable to place a task" {
		t.Errorf("events = %v, want the newest few, blanks dropped", got)
	}
}

func TestTTSEngineECSSetEnabled(t *testing.T) {
	f := &fakeTTSECS{}
	eng := &engineECS{api: f, cluster: "c", service: "voicevox"}
	if err := eng.setEnabled(t.Context(), true); err != nil {
		t.Fatalf("on: %v", err)
	}
	if err := eng.setEnabled(t.Context(), false); err != nil {
		t.Fatalf("off: %v", err)
	}
	if len(f.desired) != 2 || f.desired[0] != 1 || f.desired[1] != 0 {
		t.Errorf("desired calls = %v, want [1 0]", f.desired)
	}
}

// With AF_TTS_ECS_SERVICE unset, newTTSEngineFromEnv returns nil: the engine is not
// ours to manage.
func TestTTSEngineFromEnvUnset(t *testing.T) {
	t.Setenv("AF_TTS_ECS_SERVICE", "")
	if eng := newTTSEngineFromEnv(); eng != nil {
		t.Error("engine control should be nil without AF_TTS_ECS_SERVICE")
	}
}

// Which EC2 type the box actually is (ADR 0074). It decides whether a start has to wait for a
// box of the previous instance class to leave, so reading it from the wrong place — or reading
// nothing and calling that a match — buys the old card for another cold start.
//
// ECS reports it as an ATTRIBUTE among others, never as a field, and a Managed Instances box is
// absent from `ec2 describe-instances` altogether: this is the only source there is.
func TestEngineECSBoxReadsTheInstanceType(t *testing.T) {
	f := &fakeTTSECS{
		svc:          &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1},
		instances:    map[string]string{"arn:ci/i-08a9": "af-engines-llm"},
		instanceType: "g6e.2xlarge",
	}
	eng := &engineECS{api: f, key: "llm", cluster: "c", service: "llm", capacityProvider: "af-engines-llm"}
	b, ok := eng.box(t.Context())
	if !ok {
		t.Fatal("no box found")
	}
	// Picked out of a list that holds other attributes too — matching on position rather than
	// on the name would read the availability zone as an instance type.
	if b.instanceType != "g6e.2xlarge" {
		t.Fatalf("instanceType = %q, want g6e.2xlarge", b.instanceType)
	}

	// 🔴 And the negative: no attribute means the type is UNKNOWN, which startGate must not
	// treat as "it matches the chosen class".
	f2 := &fakeTTSECS{
		svc:       &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1},
		instances: map[string]string{"arn:ci/i-08a9": "af-engines-llm"},
	}
	eng2 := &engineECS{api: f2, key: "llm", cluster: "c", service: "llm", capacityProvider: "af-engines-llm"}
	if b2, ok := eng2.box(t.Context()); !ok || b2.instanceType != "" {
		t.Fatalf("box = %+v %v, want a box with no type rather than an invented one", b2, ok)
	}
}
