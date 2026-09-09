package main

// The GPU an engine buys (ADR 0074). What is pinned here is what a reader of the panel cannot
// check for themselves:
//
//   - a deployment that declares no ladder never calls ECS about a capacity provider. That is
//     decision 3, and it is the only reason it is safe to ship this without an IAM change
//     reaching every existing deployment first;
//   - applying a rung replaces FOUR fields and returns every other one exactly as it was read.
//     A dropped constraint here does not fail — it buys a slightly different box, once, at some
//     future cold start;
//   - the demand a class is compared against is a MAXIMUM (one model is in VRAM at a time), and
//     "nobody measured it" never reads as "it fits".

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestParseEngineClasses(t *testing.T) {
	list := parseEngineClasses(
		"l4|L4 24GB|21000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26;" +
			"l40s|L40S 48GB|44000|g6e.xlarge|4|30000-65536;" +
			"broken|no types|1000||4-8|1000-2000;" +
			"nonum|bad vram|lots|g6.xlarge|4-8|1000-2000;" +
			"l4|duplicate|21000|g6.xlarge|4-8|15000-65536")
	if len(list) != 2 {
		t.Fatalf("parsed %d rungs, want 2 (the malformed and the duplicate are dropped): %+v", len(list), list)
	}
	l4 := list[0]
	if l4.ID != "l4" || l4.VramMiB != 21000 || l4.UsdPerHour != 1.26 {
		t.Errorf("first rung = %+v", l4)
	}
	if got := strings.Join(l4.Types, ","); got != "g6.xlarge,g5.xlarge" {
		t.Errorf("types = %q", got)
	}
	if l4.VCpuMin != 4 || l4.VCpuMax != 8 || l4.MemMinMiB != 15000 || l4.MemMaxMiB != 65536 {
		t.Errorf("bounds = %+v", l4)
	}
	// A single number is both ends, and an absent price is 0 — never a made-up figure, because
	// the panel prints a price only when the operator declared one.
	if list[1].VCpuMin != 4 || list[1].VCpuMax != 4 {
		t.Errorf("single-number vcpu range = %d-%d", list[1].VCpuMin, list[1].VCpuMax)
	}
	if list[1].UsdPerHour != 0 {
		t.Errorf("undeclared price = %v, want 0", list[1].UsdPerHour)
	}
	if parseEngineClasses("") != nil {
		t.Error("an empty ladder must parse to nothing at all")
	}
}

// A price that does not parse must not cost the rung: it is a label, and dropping the hardware
// to protect a number nothing computes with is the wrong trade.
func TestParseEngineClassesKeepsTheRungWhenOnlyThePriceIsWrong(t *testing.T) {
	list := parseEngineClasses("l4|L4|21000|g6.xlarge|4-8|15000-65536|free")
	if len(list) != 1 || list[0].UsdPerHour != 0 {
		t.Fatalf("got %+v, want the rung kept with no price", list)
	}
}

func TestEngineClassByID(t *testing.T) {
	list := parseEngineClasses("a|A|1|t|1-2|1-2;b|B|2|t|1-2|1-2")
	// "" is the default and the default is the FIRST rung — that is what lets the stack declare
	// one without a second parameter naming it.
	if c, ok := engineClassByID(list, ""); !ok || c.ID != "a" {
		t.Errorf("default = %+v %v", c, ok)
	}
	if c, ok := engineClassByID(list, "b"); !ok || c.ID != "b" {
		t.Errorf("by id = %+v %v", c, ok)
	}
	if _, ok := engineClassByID(list, "zzz"); ok {
		t.Error("an id nobody declared must not resolve")
	}
	if _, ok := engineClassByID(nil, ""); ok {
		t.Error("no ladder must answer no rung")
	}
}

func TestEngineVramDemandIsTheMaximumAndKnowsHowWellItKnows(t *testing.T) {
	mib := func(n int64) []store.EngineModelFile {
		return []store.EngineModelFile{{S3Key: "k", Bytes: n * 1024 * 1024}}
	}
	t.Run("declared beats the file size, and the largest wins", func(t *testing.T) {
		need, source, id := engineVramDemand([]store.EngineModel{
			{ID: "small", Enabled: true, VramMiB: 7000},
			{ID: "big", Enabled: true, VramMiB: 21000},
			{ID: "off", Enabled: false, VramMiB: 40000},
		})
		if need != 21000 || source != engineVramDeclared || id != "big" {
			t.Fatalf("got %d %s %s, want the largest ENABLED model's own measurement", need, source, id)
		}
	})
	t.Run("a sum would be wrong", func(t *testing.T) {
		// Five 8 GB models are not a 40 GB demand: the router holds one at a time
		// (`--models-max 1`) and sd-server holds one checkpoint.
		rows := make([]store.EngineModel, 5)
		for i := range rows {
			rows[i] = store.EngineModel{ID: "m", Enabled: true, VramMiB: 8000}
		}
		if need, _, _ := engineVramDemand(rows); need != 8000 {
			t.Fatalf("need = %d, want 8000", need)
		}
	})
	t.Run("file bytes are a floor, and say so", func(t *testing.T) {
		need, source, _ := engineVramDemand([]store.EngineModel{
			{ID: "gguf", Enabled: true, Files: mib(18000)},
		})
		if need != 18000 || source != engineVramFloor {
			t.Fatalf("got %d %s, want a floor of 18000", need, source)
		}
	})
	t.Run("one unmeasured model weakens the whole answer", func(t *testing.T) {
		// The largest KNOWN demand is still reported — a panel needs a number — but it is a
		// floor, because the model nobody measured could be bigger.
		need, source, _ := engineVramDemand([]store.EngineModel{
			{ID: "known", Enabled: true, VramMiB: 9000},
			{ID: "mystery", Enabled: true},
		})
		if need != 9000 || source != engineVramFloor {
			t.Fatalf("got %d %s, want 9000 as a floor", need, source)
		}
	})
	t.Run("nothing declared is unknown, not zero", func(t *testing.T) {
		_, source, _ := engineVramDemand([]store.EngineModel{{ID: "m", Enabled: true}})
		if source != engineVramUnknown {
			t.Fatalf("source = %s, want unknown", source)
		}
	})
	t.Run("a LoRA is not what has to fit", func(t *testing.T) {
		_, source, _ := engineVramDemand([]store.EngineModel{
			{ID: "l", Enabled: true, Kind: engineModelKindLora, VramMiB: 99000},
		})
		if source != engineVramUnknown {
			t.Fatalf("source = %s: a LoRA is an accessory, not the model in VRAM", source)
		}
	})
}

// The unknown must not be turned into a refusal: a deployment where nobody has measured
// anything would ask for a confirmation on every single model, which teaches people to click
// through the one that matters.
func TestEngineClassFits(t *testing.T) {
	c := engineClass{VramMiB: 21000}
	for _, tc := range []struct {
		need int
		want bool
	}{{0, true}, {20000, true}, {21000, true}, {21001, false}} {
		if got := engineClassFits(c, tc.need); got != tc.want {
			t.Errorf("fits(%d) = %v", tc.need, got)
		}
	}
	if !engineClassFits(engineClass{}, 99999) {
		t.Error("a rung that declares no VRAM cannot say anything about fit")
	}
}

// --- applying a rung -----------------------------------------------------------------

// fakeCapacityAPI is one Managed Instances capacity provider, described and updated.
type fakeCapacityAPI struct {
	provider ecstypes.CapacityProvider
	describe int
	updates  []*ecs.UpdateCapacityProviderInput
	descErr  error
	updErr   error
}

func (f *fakeCapacityAPI) DescribeCapacityProviders(_ context.Context, in *ecs.DescribeCapacityProvidersInput, _ ...func(*ecs.Options)) (*ecs.DescribeCapacityProvidersOutput, error) {
	f.describe++
	// The real API refuses both at once, and says so with an InvalidParameterException — which
	// is how ADR 0074 P1 found it on the deployment, after every unit test here had passed
	// against a fake that accepted anything. The fake now refuses what ECS refuses.
	if in.Cluster != nil && len(in.CapacityProviders) > 0 {
		return nil, fmt.Errorf("InvalidParameterException: Cannot specify both capacity providers and cluster in the same request")
	}
	if f.descErr != nil {
		return nil, f.descErr
	}
	return &ecs.DescribeCapacityProvidersOutput{CapacityProviders: []ecstypes.CapacityProvider{f.provider}}, nil
}

func (f *fakeCapacityAPI) UpdateCapacityProvider(_ context.Context, in *ecs.UpdateCapacityProviderInput, _ ...func(*ecs.Options)) (*ecs.UpdateCapacityProviderOutput, error) {
	f.updates = append(f.updates, in)
	if f.updErr != nil {
		return nil, f.updErr
	}
	return &ecs.UpdateCapacityProviderOutput{}, nil
}

// testCapacityProvider is a provider carrying the things 60-engines actually declares, so that
// "everything else came back unchanged" is a claim about the real shape.
func testCapacityProvider(name string) ecstypes.CapacityProvider {
	return ecstypes.CapacityProvider{
		Name: aws.String(name),
		ManagedInstancesProvider: &ecstypes.ManagedInstancesProvider{
			InfrastructureRoleArn: aws.String("arn:aws:iam::1:role/infra"),
			PropagateTags:         ecstypes.PropagateMITagsCapacityProvider,
			InfrastructureOptimization: &ecstypes.InfrastructureOptimization{
				ScaleInAfter: aws.Int32(600),
			},
			InstanceLaunchTemplate: &ecstypes.InstanceLaunchTemplate{
				Ec2InstanceProfileArn: aws.String("arn:aws:iam::1:instance-profile/p"),
				CapacityOptionType:    ecstypes.CapacityOptionTypeOnDemand,
				NetworkConfiguration: &ecstypes.ManagedInstancesNetworkConfiguration{
					Subnets: []string{"subnet-a", "subnet-b"}, SecurityGroups: []string{"sg-1"},
				},
				LocalStorageConfiguration: &ecstypes.ManagedInstancesLocalStorageConfiguration{
					UseLocalStorage: true,
				},
				InstanceRequirements: &ecstypes.InstanceRequirementsRequest{
					VCpuCount:                 &ecstypes.VCpuCountRangeRequest{Min: aws.Int32(4), Max: aws.Int32(8)},
					MemoryMiB:                 &ecstypes.MemoryMiBRequest{Min: aws.Int32(15000), Max: aws.Int32(65536)},
					AllowedInstanceTypes:      []string{"g6.xlarge", "g5.xlarge"},
					BurstablePerformance:      ecstypes.BurstablePerformanceExcluded,
					AcceleratorCount:          &ecstypes.AcceleratorCountRequest{Min: aws.Int32(1), Max: aws.Int32(1)},
					AcceleratorTypes:          []ecstypes.AcceleratorType{ecstypes.AcceleratorTypeGpu},
					AcceleratorManufacturers:  []ecstypes.AcceleratorManufacturer{ecstypes.AcceleratorManufacturerNvidia},
					AcceleratorTotalMemoryMiB: &ecstypes.AcceleratorTotalMemoryMiBRequest{Min: aws.Int32(21000)},
				},
			},
		},
	}
}

// The name is asked for without a cluster (the API allows only one of the two), so the cluster
// the answer names is the only thing that says this provider is the engine's. A name that
// resolved elsewhere must not be written to — the update re-declares subnets and security
// groups, so writing to the wrong provider is not a read-only mistake.
func TestApplyEngineClassRefusesAProviderInAnotherCluster(t *testing.T) {
	p := testCapacityProvider("af-eng-llm")
	p.Cluster = aws.String("arn:aws:ecs:ap-northeast-1:1:cluster/somebody-elses")
	f := &fakeCapacityAPI{provider: p}
	err := applyEngineClass(t.Context(), f, "af-af-ecs-platform", "af-eng-llm", engineClass{ID: "x", VramMiB: 1, Types: []string{"g6.xlarge"}})
	if err == nil {
		t.Fatal("a provider in another cluster was written to")
	}
	if len(f.updates) != 0 {
		t.Errorf("refused, yet %d update(s) were sent", len(f.updates))
	}
}

// The same provider named by ARN rather than by name is the same provider.
func TestApplyEngineClassAcceptsTheClusterByArn(t *testing.T) {
	p := testCapacityProvider("af-eng-llm")
	p.Cluster = aws.String("arn:aws:ecs:ap-northeast-1:1:cluster/af-af-ecs-platform")
	f := &fakeCapacityAPI{provider: p}
	c := parseEngineClasses("l4|L4|21000|g6.xlarge|4-8|15000-65536")[0]
	if err := applyEngineClass(t.Context(), f, "af-af-ecs-platform", "af-eng-llm", c); err != nil {
		t.Fatalf("applying: %v", err)
	}
	if len(f.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(f.updates))
	}
}

func TestApplyEngineClassReplacesFourFieldsAndKeepsTheRest(t *testing.T) {
	f := &fakeCapacityAPI{provider: testCapacityProvider("af-eng-llm")}
	c := parseEngineClasses("l40s|L40S|44000|g6e.xlarge,g6e.2xlarge|4-8|30000-65536")[0]
	if err := applyEngineClass(t.Context(), f, "cluster", "af-eng-llm", c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(f.updates) != 1 {
		t.Fatalf("%d updates, want 1", len(f.updates))
	}
	up := f.updates[0]
	mi := up.ManagedInstancesProvider
	if aws.ToString(up.Name) != "af-eng-llm" || aws.ToString(up.Cluster) != "cluster" {
		t.Errorf("update addressed %v in %v", up.Name, up.Cluster)
	}
	// The required members, carried across rather than invented. Losing the infrastructure role
	// is a provider that cannot launch anything.
	if aws.ToString(mi.InfrastructureRoleArn) != "arn:aws:iam::1:role/infra" {
		t.Errorf("infrastructure role = %v", mi.InfrastructureRoleArn)
	}
	if mi.PropagateTags != ecstypes.PropagateMITagsCapacityProvider || mi.InfrastructureOptimization == nil {
		t.Errorf("provider-level fields were dropped: %+v", mi)
	}
	tpl := mi.InstanceLaunchTemplate
	if tpl.NetworkConfiguration == nil || len(tpl.NetworkConfiguration.Subnets) != 2 {
		t.Errorf("network configuration = %+v — the box would land in the wrong subnets", tpl.NetworkConfiguration)
	}
	if tpl.LocalStorageConfiguration == nil || !tpl.LocalStorageConfiguration.UseLocalStorage {
		t.Errorf("local storage = %+v — losing it doubles the cold start", tpl.LocalStorageConfiguration)
	}
	if aws.ToString(tpl.Ec2InstanceProfileArn) != "arn:aws:iam::1:instance-profile/p" {
		t.Errorf("instance profile = %v", tpl.Ec2InstanceProfileArn)
	}
	req := tpl.InstanceRequirements
	if got := strings.Join(req.AllowedInstanceTypes, ","); got != "g6e.xlarge,g6e.2xlarge" {
		t.Errorf("allowed types = %q", got)
	}
	if aws.ToInt32(req.AcceleratorTotalMemoryMiB.Min) != 44000 {
		t.Errorf("VRAM floor = %v", req.AcceleratorTotalMemoryMiB.Min)
	}
	if aws.ToInt32(req.MemoryMiB.Min) != 30000 || aws.ToInt32(req.VCpuCount.Max) != 8 {
		t.Errorf("bounds = %+v %+v", req.MemoryMiB, req.VCpuCount)
	}
	// 🔴 The fields nobody looks at are the ones a rewrite loses. `excluded` here is what keeps
	// a burstable instance out of a GPU role, and its absence would never be noticed.
	if req.BurstablePerformance != ecstypes.BurstablePerformanceExcluded {
		t.Errorf("BurstablePerformance = %q, want it carried across", req.BurstablePerformance)
	}
	if len(req.AcceleratorTypes) != 1 || req.AcceleratorCount == nil {
		t.Errorf("the GPU requirement was dropped: %+v", req)
	}
	// The read must not be mutated in place: the same describe answer is what the next call
	// starts from, and editing it would make a failed update look applied.
	if orig := f.provider.ManagedInstancesProvider.InstanceLaunchTemplate.InstanceRequirements; orig.AllowedInstanceTypes[0] != "g6.xlarge" {
		t.Errorf("the described provider was mutated in place: %+v", orig)
	}
}

// A rung's VRAM floor is only expressible where the role asks for an accelerator at all: ECS
// refuses AcceleratorTotalMemoryMiB without the accelerator fields (measured, ADR 0071), so a
// CPU-only engine must come back without one rather than with a filter no instance passes.
func TestEngineClassRequirementsOmitsTheVramFloorWithoutAGpu(t *testing.T) {
	cur := &ecstypes.InstanceRequirementsRequest{
		VCpuCount: &ecstypes.VCpuCountRangeRequest{Min: aws.Int32(2), Max: aws.Int32(2)},
	}
	got := engineClassRequirements(cur, engineClass{VramMiB: 44000, Types: []string{"m7i.large"}, VCpuMin: 2, VCpuMax: 2, MemMinMiB: 1, MemMaxMiB: 2})
	if got.AcceleratorTotalMemoryMiB != nil {
		t.Fatalf("a CPU role asked for %v of VRAM", got.AcceleratorTotalMemoryMiB.Min)
	}
}

func TestApplyEngineClassRefusesANonManagedInstancesProvider(t *testing.T) {
	f := &fakeCapacityAPI{provider: ecstypes.CapacityProvider{Name: aws.String("af-eng-llm")}}
	err := applyEngineClass(t.Context(), f, "c", "af-eng-llm", engineClass{ID: "x"})
	if err == nil || len(f.updates) != 0 {
		t.Fatalf("err = %v, updates = %d — writing an MI configuration onto something else is worse than refusing", err, len(f.updates))
	}
}

// 🔴 The copier is written by hand, and the SDK grows fields. This walks both structs so that
// the day a field appears the test fails here rather than the constraint disappearing from a
// live capacity provider. The same device as wiremap_convert_test.go.
func TestInstanceLaunchTemplateUpdateCarriesEveryFieldItCan(t *testing.T) {
	// The two fields the UPDATE type simply does not have. Whether ECS preserves them or
	// resets them is undocumented and unmeasured (ADR 0074 open question 1) — listing them
	// here is how that stays a known gap instead of a silent one.
	cannotCarry := map[string]string{
		"CapacityOptionType": "no counterpart in InstanceLaunchTemplateUpdate (ON_DEMAND / SPOT)",
		"FipsEnabled":        "no counterpart in InstanceLaunchTemplateUpdate",
	}
	in := reflect.TypeOf(ecstypes.InstanceLaunchTemplate{})
	out := reflect.TypeOf(ecstypes.InstanceLaunchTemplateUpdate{})
	// A populated template, so a field the copier forgot to assign shows up as a zero value.
	src := &ecstypes.InstanceLaunchTemplate{}
	sv := reflect.ValueOf(src).Elem()
	for i := 0; i < in.NumField(); i++ {
		f := in.Field(i)
		if !f.IsExported() {
			continue
		}
		fv := sv.Field(i)
		switch f.Type.Kind() {
		case reflect.Ptr:
			fv.Set(reflect.New(f.Type.Elem()))
		case reflect.String:
			fv.SetString("x")
		case reflect.Slice:
			fv.Set(reflect.MakeSlice(f.Type, 1, 1))
		}
	}
	got := reflect.ValueOf(instanceLaunchTemplateUpdate(src)).Elem()
	for i := 0; i < in.NumField(); i++ {
		f := in.Field(i)
		if !f.IsExported() {
			continue
		}
		if _, ok := out.FieldByName(f.Name); !ok {
			if _, known := cannotCarry[f.Name]; known {
				continue
			}
			t.Errorf("InstanceLaunchTemplate.%s has no counterpart in the update type and is not listed as uncarriable — decide what happens to it before it silently stops being declared", f.Name)
			continue
		}
		if got.FieldByName(f.Name).IsZero() {
			t.Errorf("instanceLaunchTemplateUpdate drops %s — a re-declared launch template would lose it", f.Name)
		}
	}
	// And the other direction: a field the update type gains that the read type also has is a
	// field this copier should be carrying.
	for i := 0; i < out.NumField(); i++ {
		f := out.Field(i)
		if !f.IsExported() {
			continue
		}
		if _, ok := in.FieldByName(f.Name); ok && got.FieldByName(f.Name).IsZero() {
			t.Errorf("the update type has %s and the copier does not set it", f.Name)
		}
	}
}

// --- the start gate ------------------------------------------------------------------

func newClassTestEngine(t *testing.T, api engineECSAPI, cap engineCapacityAPI, ladder string, st store.Store) *engineRuntimeState {
	t.Helper()
	e := newTestImageEngine(t, "http://127.0.0.1:1", api)
	e.def.CapacityProvider = "af-eng-image"
	e.def.Classes = ladder
	e.ecs.capacityProvider = "af-eng-image"
	e.classes = parseEngineClasses(ladder)
	e.cluster = "cluster"
	e.settings = st
	e.ctrl = nil
	if len(e.classes) > 0 {
		e.capacity = cap
	}
	return e
}

// Decision 3, and the reason this can ship before any deployment has the IAM grant: with no
// ladder there is no path from the start path to a capacity provider at all.
func TestStartGateIsInertWithoutALadder(t *testing.T) {
	f := &fakeCapacityAPI{provider: testCapacityProvider("af-eng-image")}
	e := newClassTestEngine(t, &engineTestECS{}, f, "", testSettingsStore(t))
	if ok, why := e.startGate(t.Context()); !ok {
		t.Fatalf("gate refused a start with no ladder (%s)", why)
	}
	if f.describe != 0 || len(f.updates) != 0 {
		t.Fatalf("ECS was called %d/%d times for a deployment that declares no classes", f.describe, len(f.updates))
	}
}

// Decision 5: the rung is re-applied before every start, because a CloudFormation update puts
// the stack's declaration back and tells nobody.
func TestStartGateReAppliesTheChosenClass(t *testing.T) {
	st := testSettingsStore(t)
	f := &fakeCapacityAPI{provider: testCapacityProvider("af-eng-image")}
	e := newClassTestEngine(t, &engineTestECS{}, f, "l4|L4|21000|g6.xlarge|4-8|15000-65536;l40s|L40S|44000|g6e.xlarge|4-8|30000-65536", st)
	if err := st.SetSetting(t.Context(), engineClassSettingKey("image"), "l40s"); err != nil {
		t.Fatal(err)
	}
	if ok, why := e.startGate(t.Context()); !ok {
		t.Fatalf("gate refused (%s)", why)
	}
	if len(f.updates) != 1 {
		t.Fatalf("%d updates, want the class written before the start", len(f.updates))
	}
	req := f.updates[0].ManagedInstancesProvider.InstanceLaunchTemplate.InstanceRequirements
	if req.AllowedInstanceTypes[0] != "g6e.xlarge" {
		t.Errorf("started on %v — the stored choice must win over the stack's first rung", req.AllowedInstanceTypes)
	}
}

// Decision 4: a box of the PREVIOUS rung is still registered, so a start now either exceeds the
// vCPU quota or lands the task straight back on the old card.
func TestStartGateWaitsForTheOldBoxToLeave(t *testing.T) {
	st := testSettingsStore(t)
	f := &fakeCapacityAPI{provider: testCapacityProvider("af-eng-image")}
	api := &engineTestECS{instance: "af-eng-image", instanceType: "g6.xlarge"}
	e := newClassTestEngine(t, api, f, "l40s|L40S|44000|g6e.xlarge|4-8|30000-65536", st)
	ok, why := e.startGate(t.Context())
	if ok || why != engineReasonClassSwapWait {
		t.Fatalf("gate = %v %q, want the start held back", ok, why)
	}
	// The box goes away; the same gate now lets the start through.
	api.instance = ""
	e.ecs.invalidateBox()
	if ok, why := e.startGate(t.Context()); !ok {
		t.Fatalf("gate still refusing after the box left (%s)", why)
	}
}

// A box of the SELECTED rung is not something to wait for — that is the ordinary
// stopped-but-still-draining case ADR 0071 decision 7 calls the cheap start.
func TestStartGateDoesNotWaitForABoxOfTheSameClass(t *testing.T) {
	st := testSettingsStore(t)
	f := &fakeCapacityAPI{provider: testCapacityProvider("af-eng-image")}
	api := &engineTestECS{instance: "af-eng-image", instanceType: "g6.xlarge"}
	e := newClassTestEngine(t, api, f, "l4|L4|21000|g6.xlarge,g5.xlarge|4-8|15000-65536", st)
	if ok, why := e.startGate(t.Context()); !ok {
		t.Fatalf("gate = %v %q, want the start allowed", ok, why)
	}
}

// Decision 5's failure rule. The distinction is the whole point: refusing when the provider may
// still hold the wrong rung, allowing when this process already put the right one there.
func TestStartGateRefusesOnlyWhenTheClassWasNeverApplied(t *testing.T) {
	st := testSettingsStore(t)
	f := &fakeCapacityAPI{provider: testCapacityProvider("af-eng-image"), descErr: context.DeadlineExceeded}
	e := newClassTestEngine(t, &engineTestECS{}, f, "l4|L4|21000|g6.xlarge|4-8|15000-65536", st)
	if ok, why := e.startGate(t.Context()); ok || why != engineReasonClassApply {
		t.Fatalf("gate = %v %q, want a refusal: the provider may still say something else", ok, why)
	}
	// Once this process HAS applied it, a later failure is a transient API error in front of a
	// provider that already says the right thing, and taking the engine away over it would be
	// an outage caused by a check.
	e.noteAppliedClass("l4")
	if ok, why := e.startGate(t.Context()); !ok {
		t.Fatalf("gate = false %q after the class was applied", why)
	}
}

// --- the admin routes ----------------------------------------------------------------

func classAdminAPI(t *testing.T, e *engineRuntimeState, st store.Store) engineAdminAPI {
	t.Helper()
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	return engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
}

func putClass(t *testing.T, a engineAdminAPI, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/api/admin/engines/image/class", strings.NewReader(body))
	r.SetPathValue("key", "image")
	a.putClass(rec, r, store.Identity{ID: "u1"})
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestPutClassStoresAndApplies(t *testing.T) {
	st := testSettingsStore(t)
	f := &fakeCapacityAPI{provider: testCapacityProvider("af-eng-image")}
	api := &engineTestECS{instance: "af-eng-image", instanceType: "g6.xlarge"}
	e := newClassTestEngine(t, api, f, "l4|L4|21000|g6.xlarge|4-8|15000-65536;l40s|L40S 48GB|44000|g6e.xlarge|4-8|30000-65536", st)
	a := classAdminAPI(t, e, st)

	code, out := putClass(t, a, `{"class":"l40s"}`)
	if code != http.StatusOK {
		t.Fatalf("put = %d (%v)", code, out)
	}
	if v, _ := st.GetSetting(t.Context(), engineClassSettingKey("image")); v != "l40s" {
		t.Fatalf("stored class = %q — the stored setting is what wins over the stack", v)
	}
	if len(f.updates) != 1 {
		t.Errorf("%d capacity provider updates, want the choice applied at once", len(f.updates))
	}
	// A box of the old rung is up, so the change has not reached anything yet and the panel has
	// to say so rather than reporting success.
	if out["class_replace_pending"] != true {
		t.Errorf("class_replace_pending = %v while a %s box is running", out["class_replace_pending"], api.instanceType)
	}
	if cls, _ := out["class"].(map[string]any); cls == nil || cls["id"] != "l40s" {
		t.Errorf("class in the answer = %v", out["class"])
	}
	if out["class_is_default"] != false {
		t.Errorf("class_is_default = %v — running on a non-default rung must be visible", out["class_is_default"])
	}

	// A rung nobody declared does not exist. This is also what keeps arbitrary instance
	// requirements from reaching the capacity provider.
	if code, _ = putClass(t, a, `{"class":"h100"}`); code != http.StatusBadRequest {
		t.Errorf("undeclared class = %d, want 400", code)
	}
	if len(f.updates) != 1 {
		t.Errorf("a refused class still wrote to ECS (%d updates)", len(f.updates))
	}
}

func TestPutClassIsNotFoundWithoutALadder(t *testing.T) {
	st := testSettingsStore(t)
	e := newClassTestEngine(t, &engineTestECS{}, nil, "", st)
	code, _ := putClass(t, classAdminAPI(t, e, st), `{"class":"l4"}`)
	if code != http.StatusNotFound {
		t.Fatalf("put on a deployment with no ladder = %d, want 404", code)
	}
}

// Decision 6, at the one moment a human is choosing: enabling a model that does not fit the
// chosen card asks a question, and the same call with confirm_vram goes through.
func TestPutModelAsksBeforeEnablingAModelThatDoesNotFit(t *testing.T) {
	st := testSettingsStore(t)
	f := &fakeCapacityAPI{provider: testCapacityProvider("af-eng-image")}
	e := newClassTestEngine(t, &engineTestECS{}, f, "l4|L4|21000|g6.xlarge|4-8|15000-65536", st)
	e.catalog = newEngineCatalog(st, "image")
	ctx := t.Context()
	var models store.EngineModelStore = st
	if err := models.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "flux-dev", Kind: "checkpoint", VramMiB: 40000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := models.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "sdxl", Kind: "checkpoint", VramMiB: 7379,
	}); err != nil {
		t.Fatal(err)
	}
	a := classAdminAPI(t, e, st)

	code, out := putModelReq(t, a, "flux-dev", `{"enabled":true}`)
	if code != http.StatusConflict {
		t.Fatalf("enable = %d (%v), want a question", code, out)
	}
	// The row must be untouched: a refusal that had already written would leave the catalogue
	// saying yes while the answer said no.
	rows, _ := models.ListEngineModels(ctx, "image")
	for _, m := range rows {
		if m.ID == "flux-dev" && m.Enabled {
			t.Fatal("the model was enabled by the call that refused it")
		}
	}
	if code, out = putModelReq(t, a, "flux-dev", `{"enabled":true,"confirm_vram":true}`); code != http.StatusOK {
		t.Fatalf("confirmed enable = %d (%v) — this warns, it does not forbid", code, out)
	}
	// A model that fits is never asked about.
	if code, out = putModelReq(t, a, "sdxl", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("enabling a model that fits = %d (%v)", code, out)
	}
	// And neither is a model nobody has measured: `unknown` is not "too big".
	if err := models.PutEngineModel(ctx, store.EngineModel{Role: "image", ID: "mystery", Kind: "checkpoint"}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()
	if code, out = putModelReq(t, a, "mystery", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("enabling an unmeasured model = %d (%v)", code, out)
	}
}

func putModelReq(t *testing.T, a engineAdminAPI, id, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/api/admin/engines/image/models/"+id, strings.NewReader(body))
	r.SetPathValue("key", "image")
	r.SetPathValue("id", id)
	a.putModel(rec, r, store.Identity{ID: "u1"})
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// The controller asks the gate and does not start when it says no. Without this the wait is a
// log line and the box is bought anyway.
func TestControllerHoldsTheStartBackWhenTheGateRefuses(t *testing.T) {
	f := &engineTestECS{desired: 0}
	eng := &engineECS{api: f, key: "image", cluster: "c", service: "s", now: time.Now}
	c := newEngineController(eng, engineSettingsFor("image"), nil, nil, nil, nil, engineControlCfg{
		interval: time.Minute, window: time.Minute, startUnits: 1, idle: time.Hour, deadline: time.Hour,
	})
	c.demand = newEngineDemand(nil, "x", time.Minute)
	c.demand.record(context.Background(), 5)
	c.startGate = func(context.Context) (bool, string) { return false, engineReasonClassSwapWait }
	c.tick(context.Background())
	if f.updates != 0 {
		t.Fatalf("the controller started the engine %d time(s) while the gate said wait", f.updates)
	}
	c.startGate = func(context.Context) (bool, string) { return true, "" }
	c.tick(context.Background())
	if f.updates != 1 {
		t.Fatalf("the controller made %d update(s) once the gate allowed it", f.updates)
	}
}

// The gateway's own start path (a request arriving at a stopped engine) is the third way a box
// gets bought, and it needs the same gate: without it, one request after a class change buys
// the previous rung's box.
//
// It must not fail the request either. The caller is a wait loop, so "not yet" is a nil error
// and the request ends in the retryable 503 the provider already handles.
func TestGatewayStartPathRespectsTheClassGate(t *testing.T) {
	st := testSettingsStore(t)
	f := &fakeCapacityAPI{provider: testCapacityProvider("af-eng-image")}
	api := &engineTestECS{instance: "af-eng-image", instanceType: "g6.xlarge"}
	e := newClassTestEngine(t, api, f, "l40s|L40S|44000|g6e.xlarge|4-8|30000-65536", st)
	if err := e.ensureStarted(t.Context()); err != nil {
		t.Fatalf("ensureStarted = %v, want the wait to continue rather than the request to fail", err)
	}
	if api.updates != 0 {
		t.Fatalf("the gateway started the engine %d time(s) while a %s box was still registered",
			api.updates, api.instanceType)
	}
}
