package main

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// fakeTTSECS serves one service snapshot and records UpdateService desired counts.
type fakeTTSECS struct {
	svc     *ecstypes.Service // nil = not found
	desired []int32           // recorded UpdateService calls
}

func (f *fakeTTSECS) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
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
		st, desired, err := eng.state(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if st != c.wantState || desired != c.wantDesired {
			t.Errorf("%s: state=%q desired=%d, want %q/%d", c.name, st, desired, c.wantState, c.wantDesired)
		}
	}

	// missing service → error (misconfiguration should be visible, not "none" silently)
	eng := &ttsEngineECS{api: &fakeTTSECS{}, cluster: "c", service: "voicevox"}
	if _, _, err := eng.state(t.Context()); err == nil {
		t.Error("missing service should return an error")
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
