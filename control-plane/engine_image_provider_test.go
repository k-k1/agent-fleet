package main

// ADR 0083 decision 5: this build's images-provider vocabulary is {comfy, openai-compat}. A row
// naming anything else — `sdcpp`, most likely, retired the same ADR — must not fail silently:
// the admin panel marks the row, and the registry logs it once per (re)build.

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

func TestImageProviderServable(t *testing.T) {
	for _, tc := range []struct {
		api, provider string
		want          bool
	}{
		{engineAPIImages, "comfy", true},
		{engineAPIImages, "openai-compat", true},
		{engineAPIImages, "sdcpp", false},
		{engineAPIImages, "", false},
		// Off the images API this ADR does not touch the chat vocabulary at all.
		{engineAPIChat, "llamacpp", true},
		{engineAPIChat, "sdcpp", true},
	} {
		got := imageProviderServable(engineDef{API: tc.api, Provider: tc.provider})
		if got != tc.want {
			t.Errorf("imageProviderServable(api=%q, provider=%q) = %v, want %v", tc.api, tc.provider, got, tc.want)
		}
	}
}

// The panel's mark, on a row this build genuinely cannot generate from.
func TestEngineAdminRowMarksAnUnservableImageProvider(t *testing.T) {
	st := testSettingsStore(t)
	f := &fakeTTSECS{svc: &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0, RunningCount: 0}}
	e := newTestImageEngine(t, "http://127.0.0.1:1", f) // provider: "sdcpp" — retired by ADR 0083
	e.settings = st
	e.demand = newEngineDemand(st, engineSettingsFor("image").demandAt, 5*time.Minute)
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}

	row := a.row(t.Context(), e)
	if v, _ := row["provider_unserved"].(bool); !v {
		t.Errorf("provider_unserved = %v, want true for provider %q", row["provider_unserved"], e.def.Provider)
	}
}

// The neighbouring comfy row must not carry the mark — this is the completion condition that
// matters most: a build that flags one provider must not scare an operator off the other.
func TestEngineAdminRowLeavesAServableProviderUnmarked(t *testing.T) {
	st := testSettingsStore(t)
	f := &fakeTTSECS{svc: &ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0, RunningCount: 0}}
	e := newTestComfyEngine(t, "http://127.0.0.1:1", f)
	e.settings = st
	e.demand = newEngineDemand(st, engineSettingsFor("image").demandAt, 5*time.Minute)
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}

	row := a.row(t.Context(), e)
	if _, ok := row["provider_unserved"]; ok {
		t.Errorf("provider_unserved = %v, want absent for provider %q", row["provider_unserved"], e.def.Provider)
	}
}
