package main

import (
	"context"
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
		eng := &ttsEngineECS{api: &fakeTTSECS{svc: c.svc}, cluster: "c", service: "voicevox"}
		v, err := eng.view(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if v.state != c.wantState || v.desired != c.wantDesired {
			t.Errorf("%s: state=%q desired=%d, want %q/%d", c.name, v.state, v.desired, c.wantState, c.wantDesired)
		}
	}

	// missing service → error (misconfiguration should be visible, not "none" silently)
	eng := &ttsEngineECS{api: &fakeTTSECS{}, cluster: "c", service: "voicevox"}
	if _, err := eng.view(t.Context()); err == nil {
		t.Error("missing service should return an error")
	}
}

// TestTTSEngineViewCache — DescribeServices was called once per /api/tts/status request,
// which is fine while only the admin panel polls and is not once every client polls to
// render "starting" (ADR 0070 decision 10). The cache also has to step aside the moment
// the desired count is written, or the answer to "did my click do anything" is stale.
func TestTTSEngineViewCache(t *testing.T) {
	f := &fakeTTSECS{svc: &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}}
	now := time.Now()
	eng := &ttsEngineECS{api: f, cluster: "c", service: "voicevox", now: func() time.Time { return now }}

	for range 5 {
		if _, err := eng.view(t.Context()); err != nil {
			t.Fatalf("view: %v", err)
		}
	}
	if f.describe != 1 {
		t.Errorf("DescribeServices calls = %d, want 1 (the rest served from the cache)", f.describe)
	}

	now = now.Add(ttsViewTTL + time.Second)
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

// TestTTSEngineViewDetail — the two fields the controller judges a start by: when this
// start began (the primary deployment) and what ECS says went wrong (the service events).
func TestTTSEngineViewDetail(t *testing.T) {
	created := time.Date(2026, 9, 6, 8, 30, 0, 0, time.UTC)
	f := &fakeTTSECS{svc: &ecstypes.Service{
		Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 0,
		Deployments: []ecstypes.Deployment{
			{Status: aws.String("ACTIVE"), CreatedAt: aws.Time(created.Add(-time.Hour))},
			{Status: aws.String("PRIMARY"), CreatedAt: aws.Time(created)},
		},
		Events: []ecstypes.ServiceEvent{
			{Message: aws.String("unable to place a task")},
			{Message: aws.String("  ")},
			{Message: aws.String("older")},
			{Message: aws.String("older still")},
		},
	}}
	eng := &ttsEngineECS{api: f, cluster: "c", service: "voicevox"}
	v, err := eng.view(t.Context())
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if !v.lastStart.Equal(created) {
		t.Errorf("lastStart = %v, want the PRIMARY deployment's %v", v.lastStart, created)
	}
	if len(v.events) != 2 || v.events[0] != "unable to place a task" {
		t.Errorf("events = %v, want the newest few, blanks dropped", v.events)
	}
}

func TestTTSEngineECSSetEnabled(t *testing.T) {
	f := &fakeTTSECS{}
	eng := &ttsEngineECS{api: f, cluster: "c", service: "voicevox"}
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
