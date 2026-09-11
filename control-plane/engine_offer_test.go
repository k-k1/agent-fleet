package main

// Which box an engine buys, out of a list of offers (ADR 0075 P0). What is pinned here is the
// five things a reader of the panel — or of the bill — cannot check for themselves:
//
//   - a deployment that declares no offer never asks ECS about a box. Every claim of that shape
//     in this file carries its positive control, because "the fake was never called" and "the
//     fake cannot be called" look identical;
//   - an ADR 0074 ladder is an all-on-demand offer list, unchanged. That is the whole migration;
//   - an offer smaller than the models need is not a candidate, and no candidates means no start
//     rather than a start on the smallest box;
//   - THE SERVICE'S STRATEGY IS NEVER WRITTEN WHILE A TASK IS RUNNING. This is the one rule that
//     protects a generation in flight, and it is asserted twice: once on the guard, once on the
//     tree, because a guard in a function nobody has to go through is decoration;
//   - the three measured failure codes are three different answers — move now, wait out the
//     budget, change purchase option — and a code nobody recognises waits.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// offerECS records every UpdateService verbatim: most of what follows is about WHAT was written
// — a provider, a desired count, forceNewDeployment — rather than that something was.
type offerECS struct {
	desired, running int32
	events           []offerEventFixture
	// strategy is the capacity provider the service itself names, i.e. what DescribeServices
	// reports back. It moves when an update writes one, exactly as the real service does.
	strategy  string
	updates   []*ecs.UpdateServiceInput
	describes int
	lists     int
	// instances are the cluster's container instances, arn -> capacity provider.
	instances    map[string]string
	instanceType string
	// instanceStatus is what those instances report; empty is ACTIVE. DRAINING is a box on its
	// way out, which is a different answer to "has this offer produced one".
	instanceStatus string
	// instanceStatuses is the same per instance, for the case that matters most: the previous
	// offer's box draining while this offer's box comes up.
	instanceStatuses map[string]string
	// deployments counts the PRIMARY deployments ECS has created. A forced update REPLACES the
	// PRIMARY one — new id, new createdAt (measured, live test 0) — and the start path splits on
	// exactly that, so the fake has to move it or nothing here tests the split.
	deployments int
	// stuckDeployment keeps the PRIMARY id where it is however often a deployment is forced: the
	// service ECS has accepted a write for but not acted on yet.
	stuckDeployment bool
	// draining is the deployment the forced one replaced, still ACTIVE. 🔴 This is the state the
	// two-box start happened in (measured: 2 m 35 s of it), so the fake holds it until a test
	// says it has gone — `settled()` is the whole point of the second call's gate.
	draining bool
	rollout  ecstypes.DeploymentRolloutState
}

func (f *offerECS) deploymentID() string {
	return fmt.Sprintf("ecs-svc/%d", f.deployments)
}

// deploymentList is what `describe-services` reports: the PRIMARY one, plus the one it replaced
// while ECS is still winding that down.
func (f *offerECS) deploymentList() []ecstypes.Deployment {
	out := []ecstypes.Deployment{{
		Status: aws.String("PRIMARY"), Id: aws.String(f.deploymentID()), RolloutState: f.rollout,
	}}
	if f.draining {
		out = append(out, ecstypes.Deployment{
			Status: aws.String("ACTIVE"), Id: aws.String(f.deploymentID() + "-old"),
		})
	}
	return out
}

// offerEventFixture is one service event with the age ECS would report it at. The age matters as
// much as the text: rule 2 only believes an event that is newer than the offer it is judging.
type offerEventFixture struct {
	message string
	ago     time.Duration
}

// placementEvent is a capacity failure in the shape ECS actually writes it — the reason wrapped in
// the placement failure, WITH the capacity provider named. Measured for all three codes
// (ADR 0074's `UnfulfillableCapacity`, ADR 0075's `MaxSpotInstanceCountExceeded`).
func placementEvent(provider, reason string) offerEventFixture {
	return offerEventFixture{message: "(service af-stack-image) was unable to place a task. Reason: " +
		"ResourceInitializationError: Unable to launch instance(s) for capacity provider " +
		provider + ". " + reason}
}

// agedPlacementEvent is the same, written a while ago — i.e. about a previous offer.
func agedPlacementEvent(provider, reason string, ago time.Duration) offerEventFixture {
	e := placementEvent(provider, reason)
	e.ago = ago
	return e
}

// The reasons, verbatim from the deployment.
const (
	reasonUnfulfillable = "UnfulfillableCapacity: Unable to fulfill capacity due to your request configuration. Please adjust your request and try again."
	reasonInsufficient  = "InsufficientInstanceCapacity: There is not enough capacity."
	reasonSpotQuota     = "MaxSpotInstanceCountExceeded: Max spot instance count exceeded. RequestId: 3495893a-…"
	reasonVcpuLimit     = "VcpuLimitExceeded: You have requested more vCPU capacity than your current limit."
)

func (f *offerECS) DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	f.describes++
	s := ecstypes.Service{
		Status: aws.String("ACTIVE"), DesiredCount: f.desired, RunningCount: f.running,
		Deployments: f.deploymentList(),
	}
	for _, e := range f.events {
		s.Events = append(s.Events, ecstypes.ServiceEvent{
			Message: aws.String(e.message), CreatedAt: aws.Time(time.Now().Add(-e.ago)),
		})
	}
	if f.strategy != "" {
		s.CapacityProviderStrategy = []ecstypes.CapacityProviderStrategyItem{
			{CapacityProvider: aws.String(f.strategy), Weight: 1},
		}
	}
	return &ecs.DescribeServicesOutput{Services: []ecstypes.Service{s}}, nil
}

func (f *offerECS) UpdateService(_ context.Context, in *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
	// What the real API refuses, so that a test cannot pass against a fake that is more forgiving
	// than ECS: a strategy that CHANGES on a service already using one is a 400 without
	// `forceNewDeployment` (measured, both directions, at desired 0).
	if len(in.CapacityProviderStrategy) > 0 && !in.ForceNewDeployment && f.strategy != "" &&
		aws.ToString(in.CapacityProviderStrategy[0].CapacityProvider) != f.strategy {
		return nil, fmt.Errorf("InvalidParameterException: on a service that is already using one, you must force a new deployment.")
	}
	f.updates = append(f.updates, in)
	if in.DesiredCount != nil {
		f.desired = *in.DesiredCount
	}
	if len(in.CapacityProviderStrategy) > 0 {
		f.strategy = aws.ToString(in.CapacityProviderStrategy[0].CapacityProvider)
	}
	if in.ForceNewDeployment && !f.stuckDeployment {
		f.deployments++
	}
	return &ecs.UpdateServiceOutput{}, nil
}

func (f *offerECS) ListContainerInstances(context.Context, *ecs.ListContainerInstancesInput, ...func(*ecs.Options)) (*ecs.ListContainerInstancesOutput, error) {
	f.lists++
	out := &ecs.ListContainerInstancesOutput{}
	for arn := range f.instances {
		out.ContainerInstanceArns = append(out.ContainerInstanceArns, arn)
	}
	return out, nil
}

func (f *offerECS) DescribeContainerInstances(_ context.Context, in *ecs.DescribeContainerInstancesInput, _ ...func(*ecs.Options)) (*ecs.DescribeContainerInstancesOutput, error) {
	out := &ecs.DescribeContainerInstancesOutput{}
	for _, arn := range in.ContainerInstances {
		status := f.instanceStatus
		if s, ok := f.instanceStatuses[arn]; ok {
			status = s
		}
		if status == "" {
			status = "ACTIVE"
		}
		ci := ecstypes.ContainerInstance{
			ContainerInstanceArn: aws.String(arn), Status: aws.String(status),
			Ec2InstanceId:        aws.String("i-1"),
			CapacityProviderName: aws.String(f.instances[arn]),
		}
		if f.instanceType != "" {
			ci.Attributes = []ecstypes.Attribute{{Name: aws.String(engineBoxTypeAttr), Value: aws.String(f.instanceType)}}
		}
		out.ContainerInstances = append(out.ContainerInstances, ci)
	}
	return out, nil
}

// The two provider names one role has under ADR 0075 decision 3.
const (
	offerProviderOD   = "af-eng-image"
	offerProviderSpot = "af-eng-image-spot"
)

// offerCapacityAPI is the PAIR of capacity providers a role has under decision 3 — unlike
// fakeCapacityAPI, which is one. A test that walks from a Spot offer to an on-demand one writes
// rungs to both, and a fake holding only one would fail the second write for the wrong reason.
type offerCapacityAPI struct {
	describe int
	// updates holds the writes that LANDED. A refused one is deliberately absent: the real API
	// leaves the provider exactly as it was, and that is the state the CP has to deal with.
	updates  []*ecs.UpdateCapacityProviderInput
	attempts int
	// refuseType makes UpdateCapacityProvider answer the way ECS answers requirements no instance
	// satisfies: `400 ClientException: No instance types satisfy the instance requirements
	// specified in the Managed Instances capacity provider` (measured while building ADR 0075's
	// live positive control — a misspelt type is refused HERE, not when the box is bought).
	refuseType string
}

func (f *offerCapacityAPI) DescribeCapacityProviders(_ context.Context, in *ecs.DescribeCapacityProvidersInput, _ ...func(*ecs.Options)) (*ecs.DescribeCapacityProvidersOutput, error) {
	f.describe++
	out := &ecs.DescribeCapacityProvidersOutput{}
	for _, name := range in.CapacityProviders {
		out.CapacityProviders = append(out.CapacityProviders, testCapacityProvider(name))
	}
	return out, nil
}

func (f *offerCapacityAPI) UpdateCapacityProvider(_ context.Context, in *ecs.UpdateCapacityProviderInput, _ ...func(*ecs.Options)) (*ecs.UpdateCapacityProviderOutput, error) {
	f.attempts++
	if f.refuseType != "" && in.ManagedInstancesProvider != nil && in.ManagedInstancesProvider.InstanceLaunchTemplate != nil {
		if r := in.ManagedInstancesProvider.InstanceLaunchTemplate.InstanceRequirements; r != nil {
			for _, t := range r.AllowedInstanceTypes {
				if t == f.refuseType {
					return nil, fmt.Errorf("ClientException: No instance types satisfy the instance requirements specified in the Managed Instances capacity provider.")
				}
			}
		}
	}
	f.updates = append(f.updates, in)
	return &ecs.UpdateCapacityProviderOutput{}, nil
}

// providersWritten names, in order, the capacity providers a test's rungs actually reached.
func (f *offerCapacityAPI) providersWritten() []string {
	out := make([]string, 0, len(f.updates))
	for _, u := range f.updates {
		out = append(out, aws.ToString(u.Name))
	}
	return out
}

// newOfferTestEngine is the `image` role as newEngineRegistry wires it: the offer list parsed the
// way the registry parses it, a controller, and the ADR 0075 hooks attached through the one
// function that decides whether they exist at all.
func newOfferTestEngine(t *testing.T, api engineECSAPI, capacity engineCapacityAPI, offers string, st store.Store) *engineRuntimeState {
	t.Helper()
	e := newTestImageEngine(t, "http://127.0.0.1:1", api)
	e.def.CapacityProvider = offerProviderOD
	e.def.SpotCapacityProvider = offerProviderSpot
	e.def.Offers = offers
	e.ecs.capacityProvider = offerProviderOD
	e.ecs.spotProvider = offerProviderSpot
	e.classes = parseEngineClasses(e.def.offersSpec())
	e.cluster = "cluster"
	e.settings = st
	e.catalog = newEngineCatalog(st, "image")
	e.offers = newEngineOfferRun(engineOfferBudgetDefault)
	e.audit = st
	cfg := engineControlCfgFor(e.def)
	e.demand = newEngineDemand(st, engineSettingsFor("image").demandAt, cfg.window)
	e.ctrl = newEngineController(e.ecs, engineSettingsFor("image"), nil, e.demand, st, st, cfg)
	e.wireOffers(capacity)
	return e
}

// tickIntoAStart drives one controller tick with the engine pinned on, which is the shortest real
// path to "the controller decided to start".
func tickIntoAStart(t *testing.T, e *engineRuntimeState, st store.Store) {
	t.Helper()
	if err := st.SetSetting(t.Context(), engineSettingsFor("image").mode, engineModeOn); err != nil {
		t.Fatalf("mode: %v", err)
	}
	e.demand.stamp(t.Context())
	e.ctrl.tick(t.Context())
}

// --- decision 3 inherited: no offers, no calls ----------------------------------------

// 🔴 ADR 0074 decision 3, which ADR 0075 inherits: a deployment that declares no offer must not
// gain a single ECS call. The positive control is the second half — one declared row and the same
// start reaches the capacity provider AND writes a strategy — because a fake that was never
// called proves nothing on its own.
func TestADeploymentWithNoOffersNeverAsksECSAboutABox(t *testing.T) {
	st := testSettingsStore(t)
	capacity := &offerCapacityAPI{}
	api := &offerECS{}
	e := newOfferTestEngine(t, api, capacity, "", st)

	tickIntoAStart(t, e, st)

	if capacity.describe != 0 || len(capacity.updates) != 0 {
		t.Errorf("the capacity provider was read %d and written %d times for a role that declares no offer",
			capacity.describe, len(capacity.updates))
	}
	// One DescribeServices — the controller's own look — and no second one: reading the service
	// back before writing a strategy is a call this ADR adds, and a role with no offer must not
	// pay for it. (The container-instance read is NOT counted here: `draining` predates offers
	// and every Managed Instances engine has always made it — ADR 0071 decision 7.)
	if api.describes != 1 {
		t.Errorf("DescribeServices called %d times for a role that declares no offer, want 1", api.describes)
	}
	if len(api.updates) != 1 {
		t.Fatalf("%d UpdateService calls, want the one plain start: %+v", len(api.updates), api.updates)
	}
	if len(api.updates[0].CapacityProviderStrategy) != 0 {
		t.Errorf("a role with no offers had a strategy written: %+v", api.updates[0].CapacityProviderStrategy)
	}
	if aws.ToInt32(api.updates[0].DesiredCount) != 1 {
		t.Errorf("desired = %v, want the start to have happened at all", api.updates[0].DesiredCount)
	}

	// Positive control: ONE declared row and every one of those numbers moves.
	capacity2 := &offerCapacityAPI{}
	api2 := &offerECS{}
	e2 := newOfferTestEngine(t, api2, capacity2, "l4|L4|21000|g6.xlarge|4-8|15000-65536|1.26|od", st)
	tickIntoAStart(t, e2, st)
	if capacity2.describe == 0 || len(capacity2.updates) == 0 {
		t.Fatalf("with one offer declared the capacity provider was still not touched (%d/%d) — the test above proves nothing",
			capacity2.describe, len(capacity2.updates))
	}
	if len(api2.updates) != 2 || len(api2.updates[0].CapacityProviderStrategy) == 0 {
		t.Fatalf("with one offer declared the start was not the strategy-then-desired pair: %+v", api2.updates)
	}
	if api2.describes < 2 {
		t.Fatalf("DescribeServices called %d times with an offer declared; the guard's own read is missing", api2.describes)
	}
}

// The hooks are the whole mechanism, so "inert" has to mean they are not there.
func TestWireOffersAttachesNothingWithoutAnOffer(t *testing.T) {
	st := testSettingsStore(t)
	capacity := &offerCapacityAPI{}
	e := newOfferTestEngine(t, &offerECS{}, capacity, "", st)
	if e.capacity != nil || e.ctrl.startGate != nil || e.ctrl.startWith != nil || e.ctrl.offerStep != nil {
		t.Fatalf("hooks attached for a role with no offer: capacity=%v gate=%v start=%v step=%v",
			e.capacity != nil, e.ctrl.startGate != nil, e.ctrl.startWith != nil, e.ctrl.offerStep != nil)
	}
	// Positive control: one row and all four are wired.
	e2 := newOfferTestEngine(t, &offerECS{}, capacity, "l4|L4|21000|g6.xlarge|4-8|15000-65536", st)
	if e2.capacity == nil || e2.ctrl.startGate == nil || e2.ctrl.startWith == nil || e2.ctrl.offerStep == nil {
		t.Fatal("one declared offer left a hook unattached")
	}
}

// --- decision 1: the migration -------------------------------------------------------

// The existing `<role>InstanceClasses` string, character for character, is a list of on-demand
// offers. This is the entire migration story (ADR 0075, 移行の節), and it is what lets a stack
// move a ladder to `offers` without an edit.
func TestAnInstanceClassLadderReadsAsAllOnDemandOffers(t *testing.T) {
	const ladder = "l4|L4 24GB (g6.xlarge)|22000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26;" +
		"l40s|L40S 48GB (g6e.xlarge)|44000|g6e.xlarge|4-8|30000-65536|2.91"
	d := engineDef{Classes: ladder}
	if d.offersSpec() != ladder {
		t.Fatalf("a row with no `offers` must read its ladder, got %q", d.offersSpec())
	}
	list := parseEngineClasses(d.offersSpec())
	if len(list) != 2 {
		t.Fatalf("parsed %d offers, want 2: %+v", len(list), list)
	}
	for _, c := range list {
		if c.buy() != engineBuyOnDemand {
			t.Errorf("offer %s reads as %q, want on-demand", c.ID, c.buy())
		}
	}
	// And `offers`, when declared, wins: the two columns are not merged.
	d.Offers = "spot3|22GB+ Spot|22000|g6.xlarge|4-8|15000-65536|1.57|spot"
	got := parseEngineClasses(d.offersSpec())
	if len(got) != 1 || got[0].buy() != engineBuySpot {
		t.Fatalf("offers = %+v, want the declared Spot row alone", got)
	}
}

// The eighth column, and what an unreadable one costs. Unlike the price (a label), `buy` decides
// which of two wallets the box comes out of, so a word nobody recognises takes the row with it
// rather than being defaulted to on-demand.
func TestParseEngineOffersReadsTheBuyColumn(t *testing.T) {
	list := parseEngineClasses(
		"spot3|Spot|22000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.57|spot;" +
			"l4|L4|22000|g6.xlarge|4-8|15000-65536|1.26|od;" +
			"blank|Blank|22000|g6.xlarge|4-8|15000-65536|1.26|;" +
			"seven|Seven fields|22000|g6.xlarge|4-8|15000-65536|1.26;" +
			"typo|Typo|22000|g6.xlarge|4-8|15000-65536|1.26|sport")
	if len(list) != 4 {
		t.Fatalf("parsed %d offers, want 4 (the typo is dropped): %+v", len(list), list)
	}
	if list[0].Buy != engineBuySpot {
		t.Errorf("the spot row parsed as %q", list[0].Buy)
	}
	for _, c := range list[1:] {
		if c.buy() != engineBuyOnDemand {
			t.Errorf("offer %s = %q, want on-demand (an absent or empty column is `od`)", c.ID, c.buy())
		}
	}
	for _, c := range list {
		if c.ID == "typo" {
			t.Error("an unreadable purchase option must take the row with it, not default to on-demand")
		}
	}
	// The purchase option is part of the declaration, so a ladder that changed only there is a
	// change the reloader must carry.
	a := parseEngineClasses("l4|L4|22000|g6.xlarge|4-8|15000-65536|1.26|od")
	b := parseEngineClasses("l4|L4|22000|g6.xlarge|4-8|15000-65536|1.26|spot")
	if engineClassesEqual(a, b) {
		t.Error("two offers differing only in `buy` compared equal")
	}
}

// --- decision 2: the VRAM filters the candidates --------------------------------------

func seedOfferModel(t *testing.T, st store.Store, id string, vramMiB int) {
	t.Helper()
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: id, Kind: "checkpoint", Enabled: true, Selected: true, VramMiB: vramMiB,
		Files: []store.EngineModelFile{{S3Key: "image/" + id, Bytes: 1}},
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

const twoOffers = "l4|L4 24GB|22000|g6.xlarge|4-8|15000-65536|1.26|od;" +
	"l40s|L40S 48GB|44000|g6e.xlarge|4-8|30000-65536|2.91|od"

func TestOffersAreFilteredByWhatTheModelsNeed(t *testing.T) {
	st := testSettingsStore(t)
	capacity := &offerCapacityAPI{}
	e := newOfferTestEngine(t, &offerECS{}, capacity, twoOffers, st)

	// Nobody declared a demand: nothing is filtered, and the first offer is tried (decision 2 —
	// refusing here would make an unmeasured model into an engine that never starts).
	if got := offerIDs(e.candidateOffers(t.Context())); got != "l4,l40s" {
		t.Fatalf("with an unknown demand the candidates are %q, want the whole list", got)
	}

	seedOfferModel(t, st, "big", 30000)
	e.catalog.invalidate()
	if got := offerIDs(e.candidateOffers(t.Context())); got != "l40s" {
		t.Fatalf("candidates = %q, want the 22 GB offer dropped for a 30 GB model", got)
	}
}

// Decision 2's refusal: every offer is smaller than the model, so there is no box to buy and the
// engine does not start on the biggest one either. The positive control is the second half — the
// same engine, a model that fits, starts.
func TestNoCandidateMeansNoStart(t *testing.T) {
	st := testSettingsStore(t)
	capacity := &offerCapacityAPI{}
	api := &offerECS{}
	e := newOfferTestEngine(t, api, capacity, twoOffers, st)
	seedOfferModel(t, st, "huge", 90000)
	e.catalog.invalidate()

	ok, why := e.startGate(t.Context())
	if ok || why != engineReasonNoOffer {
		t.Fatalf("gate = %v %q, want the start refused because no offer holds 90000 MiB", ok, why)
	}
	tickIntoAStart(t, e, st)
	if len(api.updates) != 0 {
		t.Fatalf("the engine was started anyway: %+v", api.updates)
	}

	// Positive control: a model that fits, and the same engine starts on the offer that holds it.
	seedOfferModel(t, st, "huge", 30000)
	e.catalog.invalidate()
	if ok, why := e.startGate(t.Context()); !ok {
		t.Fatalf("gate still refusing with a model that fits (%s)", why)
	}
}

func offerIDs(list []engineClass) string {
	ids := make([]string, 0, len(list))
	for _, c := range list {
		ids = append(ids, c.ID)
	}
	return strings.Join(ids, ",")
}

// --- decision 8: pinned or automatic --------------------------------------------------

func TestAPinnedOfferDoesNotFallThroughAndUnpinningRestoresTheChoice(t *testing.T) {
	st := testSettingsStore(t)
	capacity := &offerCapacityAPI{}
	e := newOfferTestEngine(t, &offerECS{}, capacity, twoOffers, st)

	if err := st.SetSetting(t.Context(), engineClassSettingKey("image"), "l40s"); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if got := offerIDs(e.candidateOffers(t.Context())); got != "l40s" {
		t.Fatalf("a pinned offer left %q as candidates, want it alone — a pin does not fall through", got)
	}
	// Even a pin that is too small for the models stays: an explicit intent is not overruled
	// (the panel warns; ADR 0074 decision 6 does not refuse).
	seedOfferModel(t, st, "big", 30000)
	e.catalog.invalidate()
	if got := offerIDs(e.candidateOffers(t.Context())); got != "l40s" {
		t.Fatalf("candidates = %q", got)
	}
	if err := st.SetSetting(t.Context(), engineClassSettingKey("image"), ""); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if got := offerIDs(e.candidateOffers(t.Context())); got != "l40s" {
		t.Fatalf("unpinned candidates = %q, want the filtered list", got)
	}
}

// --- decision 4: the one door, and its guard ------------------------------------------

// 🔴 The whole safety of this ADR. ADR 0074 refused to let the CP touch a service's strategy
// because re-applying one kills the generation in flight; that reason holds exactly while there
// is a task to kill, so the rule is RUNNING == 0.
func TestTheStrategyIsNeverWrittenWhileATaskIsRunning(t *testing.T) {
	st := testSettingsStore(t)
	capacity := &offerCapacityAPI{}
	api := &offerECS{desired: 1, running: 1}
	e := newOfferTestEngine(t, api, capacity, twoOffers, st)

	err := e.ecs.setStrategy(t.Context(), offerProviderSpot, false)
	if err == nil {
		t.Fatal("the strategy was written while a task was running")
	}
	if !strings.Contains(err.Error(), "running") {
		t.Errorf("refusal = %v, want it to name what it refused over", err)
	}
	if len(api.updates) != 0 {
		t.Fatalf("refused, and yet %d UpdateService call(s) went out: %+v", len(api.updates), api.updates)
	}

	// Positive control: the identical call with the task gone goes through. Without this, a
	// setStrategy that always failed would pass the assertion above.
	api.running = 0
	if err := e.ecs.setStrategy(t.Context(), offerProviderSpot, false); err != nil {
		t.Fatalf("with nothing running the same call failed: %v", err)
	}
	if len(api.updates) != 1 {
		t.Fatalf("%d updates after the allowed call", len(api.updates))
	}
}

// A guard inside a function nobody has to go through is decoration. This is the other half of the
// claim above: `capacityProviderStrategy` is written in ONE place in the whole Control Plane.
func TestOnlyOneFunctionWritesAServiceStrategy(t *testing.T) {
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	hits := map[string]int{}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		// Writes only: `CapacityProviderStrategy =` (into an UpdateServiceInput being built) and
		// `CapacityProviderStrategy:` (a struct literal). Reading the service's own strategy back
		// — which decision 11 does on every tick — is not a write and does not count.
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "CapacityProviderStrategy =") || strings.Contains(line, "CapacityProviderStrategy:") {
				hits[name]++
			}
		}
	}
	// Positive control: the scanner really does find the one write. An empty result and a
	// scanner that never ran look identical.
	if hits["engine_ecs.go"] != 1 {
		t.Fatalf("engine_ecs.go writes a strategy %d times, want exactly the one in setStrategy", hits["engine_ecs.go"])
	}
	delete(hits, "engine_ecs.go")
	if len(hits) != 0 {
		t.Fatalf("a second path writes a service's capacity provider strategy: %v — every write has to go through setStrategy's running==0 guard", hits)
	}
}

// 🔴 Decision 4 (a) as the deployment corrected it: a start that also changes the strategy is TWO
// calls, and the desired count is the second one.
//
// Measured twice on af-sandbox (ADR 0075 live run 3): one UpdateService carrying both makes ECS
// place the task against the OLD strategy first, and that provider buys a box — $0.51 of billing
// for a g6e nobody used, and its deregistration then held the next start for six minutes through
// the ADR 0074 swap wait. `forceNewDeployment` rides with the strategy because without it ECS
// answers 400 on a service that already has one (live test 0, both directions, at desired 0).
func TestTheStartWritesTheStrategyFirstAndTheDesiredCountAfter(t *testing.T) {
	st := testSettingsStore(t)
	capacity := &offerCapacityAPI{}
	// The service comes out of CloudFormation pointing at the on-demand provider (decision 12),
	// so the first Spot offer is a real change.
	api := &offerECS{strategy: offerProviderOD}
	e := newOfferTestEngine(t, api, capacity, "spot3|Spot|22000|g6.xlarge|4-8|15000-65536|1.57|spot;"+twoOffers, st)

	tickIntoAStart(t, e, st)

	if len(api.updates) != 2 {
		t.Fatalf("%d UpdateService calls, want the strategy and then the desired count: %+v", len(api.updates), api.updates)
	}
	first, second := api.updates[0], api.updates[1]
	if got := aws.ToString(first.CapacityProviderStrategy[0].CapacityProvider); got != offerProviderSpot {
		t.Errorf("started on %q, want the first offer's Spot provider", got)
	}
	if !first.ForceNewDeployment {
		t.Error("a strategy CHANGE without forceNewDeployment is a 400 on the real API (measured)")
	}
	if first.DesiredCount != nil {
		t.Errorf("the strategy call carried desired = %v; that is the call that buys the second box", first.DesiredCount)
	}
	if aws.ToInt32(second.DesiredCount) != 1 || len(second.CapacityProviderStrategy) != 0 {
		t.Errorf("the second call = %+v, want the desired count alone", second)
	}
	// The rung went to the Spot provider, because that is where the box will come from.
	if got := capacity.providersWritten(); len(got) != 1 || got[0] != offerProviderSpot {
		t.Errorf("the class was applied to %v, want the offer's own provider", got)
	}
}

// 🔴 The two-box start, at the layer it actually lives in: the desired count waits until the
// deployment the strategy write REPLACED has gone, not merely until a new one exists.
//
// Measured (ADR 0075 re-run 3): eight seconds after the new PRIMARY appeared, a `desiredCount: 1`
// was placed by the OLD deployment — `describe-tasks` named it in `startedBy` — and the strategy
// this engine had just moved away from bought a second box: 13 minutes of billing, 79% of that
// run's GPU spend.
func TestTheDesiredCountWaitsForTheOldDeploymentToGo(t *testing.T) {
	st := testSettingsStore(t)
	capacity := &offerCapacityAPI{}
	// The service starts on the on-demand provider (CloudFormation's declaration), so the Spot
	// offer is a real change — and the fake keeps the replaced deployment ACTIVE, as ECS does.
	api := &offerECS{strategy: offerProviderOD, draining: true}
	e := newOfferTestEngine(t, api, capacity, "spot3|Spot|22000|g6.xlarge|4-8|15000-65536|1.57|spot;"+twoOffers, st)

	// The controller's start: the gate chooses the offer, the strategy goes out, and the desired
	// count does NOT.
	tickIntoAStart(t, e, st)
	if api.desired != 0 {
		t.Fatalf("desired = %d while the replaced deployment is still ACTIVE — that is the second box",
			api.desired)
	}
	if len(api.updates) != 1 || len(api.updates[0].CapacityProviderStrategy) == 0 {
		t.Fatalf("the strategy half did not go out: %+v", api.updates)
	}
	// Asked directly, the answer is the sentinel rather than an error: nothing failed.
	if err := e.startEngine(t.Context()); !errors.Is(err, errEngineStrategySettling) {
		t.Fatalf("the retry = %v, want the settling sentinel", err)
	}
	if api.desired != 0 || len(api.updates) != 1 {
		t.Fatalf("the retry wrote something: desired=%d updates=%d", api.desired, len(api.updates))
	}
	// And the controller's own retry does not re-run the gate while this is going on: the offer
	// is chosen and its rung is written, and thirty more UpdateCapacityProvider calls per start
	// is what re-running it would cost.
	attempts := capacity.attempts
	e.ctrl.tick(t.Context())
	if capacity.attempts != attempts {
		t.Errorf("the rung was re-applied %d time(s) while the start was settling", capacity.attempts-attempts)
	}
	if api.desired != 0 {
		t.Fatalf("desired = %d on a retry while the old deployment is still there", api.desired)
	}

	// Positive control: the old deployment goes, and the very next attempt writes the count —
	// with no strategy, because the service already names the provider this offer buys from.
	api.draining = false
	e.ctrl.tick(t.Context())
	if api.desired != 1 {
		t.Fatalf("desired = %d, want the count written once the service settled", api.desired)
	}
	if last := api.updates[len(api.updates)-1]; len(last.CapacityProviderStrategy) != 0 || last.ForceNewDeployment {
		t.Errorf("the second half = %+v, want the desired count alone", last)
	}
}

// A rollout that is still IN_PROGRESS is the same "not yet", even with one deployment: the
// service is placing something, and a desired count written into that is how the previous defect
// started.
func TestTheDesiredCountWaitsForARollingDeployment(t *testing.T) {
	st := testSettingsStore(t)
	api := &offerECS{strategy: offerProviderOD, rollout: ecstypes.DeploymentRolloutStateInProgress}
	e := newOfferTestEngine(t, api, &offerCapacityAPI{}, twoOffers, st)

	if ok, why := e.startGate(t.Context()); !ok {
		t.Fatalf("gate = %q", why)
	}
	if err := e.startEngine(t.Context()); !errors.Is(err, errEngineStrategySettling) {
		t.Fatalf("start = %v, want the settling sentinel while the rollout is in progress", err)
	}
	if api.desired != 0 || len(api.updates) != 0 {
		t.Fatalf("desired = %d after %d call(s)", api.desired, len(api.updates))
	}
	// Positive control: COMPLETED, and the same call goes through.
	api.rollout = ecstypes.DeploymentRolloutStateCompleted
	if err := e.startEngine(t.Context()); err != nil {
		t.Fatalf("start against a completed rollout = %v", err)
	}
	if api.desired != 1 {
		t.Fatalf("desired = %d", api.desired)
	}
}

// The other half of the same rule: a start onto the provider the service ALREADY names writes no
// strategy at all. It is the ordinary case after a release — CloudFormation has just pointed the
// service at the on-demand provider — and sending a strategy that changes nothing would buy a
// forced deployment, and the 400, for no reason.
func TestAStartOnTheProviderTheServiceAlreadyNamesWritesNoStrategy(t *testing.T) {
	st := testSettingsStore(t)
	capacity := &offerCapacityAPI{}
	api := &offerECS{strategy: offerProviderOD}
	e := newOfferTestEngine(t, api, capacity, twoOffers, st)

	tickIntoAStart(t, e, st)

	if len(api.updates) != 1 {
		t.Fatalf("%d UpdateService calls: %+v", len(api.updates), api.updates)
	}
	in := api.updates[0]
	if len(in.CapacityProviderStrategy) != 0 {
		t.Errorf("a strategy was written for a provider the service already names: %+v", in.CapacityProviderStrategy)
	}
	if in.ForceNewDeployment {
		t.Error("nothing changed, so nothing had to be redeployed")
	}
	if aws.ToInt32(in.DesiredCount) != 1 {
		t.Errorf("desired = %v, want the plain start", in.DesiredCount)
	}
}

// The budget is measured from the deployment ECS created, not from the moment the CP wrote the
// strategy: a deployment takes about 89 seconds to complete with no task to replace (measured),
// and charging that to the offer would move on before it had been asked for capacity.
func TestTheOfferBudgetRunsFromTheNewDeployment(t *testing.T) {
	clk := &offerClock{t: time.Now()}
	run := newEngineOfferRun(engineOfferBudgetDefault)
	run.now = clk.now
	offer := parseEngineClasses("l4|L4|22000|g6.xlarge|4-8|15000-65536")[0]
	run.begin([]engineClass{offer})
	wroteAt := clk.t
	run.took(offer)
	clk.t = wroteAt.Add(120 * time.Second)

	if d, ok := run.waited(time.Time{}); !ok || d != 120*time.Second {
		t.Fatalf("with no deployment reported: %v %v, want the CP's own write to be the clock", d, ok)
	}
	// A deployment that started LATER — ECS rolled again — restarts the budget.
	if d, _ := run.waited(wroteAt.Add(60 * time.Second)); d != 60*time.Second {
		t.Fatalf("waited = %v, want the clock to run from the newer deployment", d)
	}
	// A deployment from BEFORE our write belongs to the previous offer and must not shorten this
	// one's budget.
	if d, _ := run.waited(wroteAt.Add(-300 * time.Second)); d != 120*time.Second {
		t.Fatalf("waited = %v, want the previous offer's deployment ignored", d)
	}
	// No walk in progress is not "waited 0 seconds": a CP replaced mid-start adopts nothing.
	if _, ok := newEngineOfferRun(engineOfferBudgetDefault).waited(time.Now()); ok {
		t.Error("a run that never took an offer reported a budget")
	}
}

// --- decision 5: rule 2 ---------------------------------------------------------------

// The table itself, as a pure function: four measured codes, three different answers, and an
// unrecognised message waits rather than giving up (AWS may reword one at any time).
//
// 🔴 Every case also exercises the filter, because the filter is what decides whether a code is
// read at all: an event counts when it NAMES this offer's capacity provider and was written after
// the offer was taken. The last four cases are that rule on its own.
func TestEngineOfferVerdict(t *testing.T) {
	const budget = 180 * time.Second
	took := time.Now()
	// after and before are an event's age relative to the moment the offer was taken.
	after := func(provider, reason string) engineServiceEvent {
		return engineServiceEvent{at: took.Add(time.Second), message: placementEvent(provider, reason).message}
	}
	before := func(provider, reason string) engineServiceEvent {
		return engineServiceEvent{at: took.Add(-20 * time.Second), message: placementEvent(provider, reason).message}
	}
	for _, tc := range []struct {
		name       string
		events     []engineServiceEvent
		waited     time.Duration
		wantResult string
		wantMove   bool
		wantMatch  bool
	}{
		{"unfulfillable moves at once", []engineServiceEvent{after(offerProviderSpot, reasonUnfulfillable)}, time.Second, engineOfferUnfulfillable, true, true},
		{"a quota moves at once", []engineServiceEvent{after(offerProviderSpot, reasonVcpuLimit)}, time.Second, engineOfferQuota, true, true},
		// 🔴 The Spot quota, as the deployment actually reports it: a different word, inside a
		// different error. Anchored matching read this as "no known code" and waited out a
		// 15-minute budget (ADR 0075 live run 4).
		{"the Spot quota is a quota, wrapped", []engineServiceEvent{after(offerProviderSpot, reasonSpotQuota)}, time.Second, engineOfferQuota, true, true},
		{"a shortage waits out the budget", []engineServiceEvent{after(offerProviderSpot, reasonInsufficient)}, time.Second, engineOfferInsufficient, false, true},
		{"a shortage moves when the budget is spent", []engineServiceEvent{after(offerProviderSpot, reasonInsufficient)}, budget, engineOfferInsufficient, true, true},
		{"silence waits", nil, time.Second, engineOfferBudget, false, false},
		{"silence moves when the budget is spent", nil, budget, engineOfferBudget, true, false},
		{"an unknown message is silence", []engineServiceEvent{{at: took.Add(time.Second), message: "has begun draining connections"}}, time.Second, engineOfferBudget, false, false},
		{"the newest recognised code wins", []engineServiceEvent{
			after(offerProviderSpot, reasonInsufficient), after(offerProviderSpot, reasonVcpuLimit),
		}, time.Second, engineOfferInsufficient, false, true},
		// 🔴 The re-run's defect, as a unit: the other provider's quota is not this offer's answer.
		{"another provider's failure is not evidence", []engineServiceEvent{after(offerProviderOD, reasonSpotQuota)}, time.Second, engineOfferBudget, false, false},
		{"this provider's failure from BEFORE the offer was taken is not evidence", []engineServiceEvent{before(offerProviderSpot, reasonSpotQuota)}, time.Second, engineOfferBudget, false, false},
		{"an event with no timestamp cannot be placed either side of the move", []engineServiceEvent{
			{message: placementEvent(offerProviderSpot, reasonSpotQuota).message},
		}, time.Second, engineOfferBudget, false, false},
		{"a failure that names no provider waits out the budget", []engineServiceEvent{
			{at: took.Add(time.Second), message: "(service af-stack-image) was unable to place a task."},
		}, time.Second, engineOfferBudget, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, move, matched := engineOfferVerdict(tc.events, offerProviderSpot, took, tc.waited, budget)
			if result != tc.wantResult || move != tc.wantMove || matched != tc.wantMatch {
				t.Fatalf("got %q move=%v matched=%v, want %q %v %v", result, move, matched, tc.wantResult, tc.wantMove, tc.wantMatch)
			}
		})
	}
}

// offerClock is a hand-wound clock for the budget, so the three branches below are about the
// rule rather than about waiting 180 real seconds.
type offerClock struct{ t time.Time }

func (c *offerClock) now() time.Time { return c.t }

// startWalking puts the engine on its first offer and returns the clock its budget runs on.
func startWalking(t *testing.T, e *engineRuntimeState, st store.Store) *offerClock {
	t.Helper()
	clk := &offerClock{t: time.Now()}
	e.offers.now = clk.now
	tickIntoAStart(t, e, st)
	return clk
}

// The three measured codes, through the machinery: move now, wait, or change purchase option.
func TestRuleTwoBranchesOnTheFailureCode(t *testing.T) {
	// A Spot row, a second Spot row, and an on-demand one — the shape that makes "skip the rest
	// of this purchase option" different from "take the next row".
	const offers = "spotA|Spot A|22000|g6.xlarge|4-8|15000-65536|1.57|spot;" +
		"spotB|Spot B|22000|g5.xlarge|4-8|15000-65536|1.57|spot;" +
		"l4|L4|22000|g6.xlarge|4-8|15000-65536|1.26|od"

	t.Run("UnfulfillableCapacity moves to the next offer without waiting", func(t *testing.T) {
		st := testSettingsStore(t)
		capacity := &offerCapacityAPI{}
		api := &offerECS{}
		e := newOfferTestEngine(t, api, capacity, offers, st)
		startWalking(t, e, st)
		api.running = 0
		api.events = []offerEventFixture{placementEvent(offerProviderSpot, reasonUnfulfillable)}

		if spent := e.stepOffers(t.Context(), mustView(t, e)); spent {
			t.Fatal("the list was declared spent after one offer")
		}
		cur, _ := e.offers.current()
		if cur.ID != "spotB" {
			t.Fatalf("now on %q, want the next row without waiting for the budget", cur.ID)
		}
		in := api.updates[len(api.updates)-1]
		if !in.ForceNewDeployment {
			t.Error("moving to the next offer must carry forceNewDeployment: the PENDING task has to be placed again")
		}
		if in.DesiredCount != nil {
			t.Errorf("the move wrote a desired count (%v); it is already 1", in.DesiredCount)
		}
		// spotA and spotB are bought from the SAME provider, so nothing about the strategy
		// changed — what changed is the provider's instance requirements, and the forced
		// deployment above is what makes ECS ask for capacity against them again.
		if len(in.CapacityProviderStrategy) != 0 {
			t.Errorf("the strategy was rewritten for a move within one purchase option: %+v", in.CapacityProviderStrategy)
		}
	})

	t.Run("InsufficientInstanceCapacity waits out the budget", func(t *testing.T) {
		st := testSettingsStore(t)
		capacity := &offerCapacityAPI{}
		api := &offerECS{}
		e := newOfferTestEngine(t, api, capacity, offers, st)
		clk := startWalking(t, e, st)
		api.events = []offerEventFixture{placementEvent(offerProviderSpot, reasonInsufficient)}

		writes := len(api.updates)
		e.stepOffers(t.Context(), mustView(t, e))
		if len(api.updates) != writes {
			t.Fatalf("moved on before the budget was spent: %+v", api.updates[writes:])
		}
		if cur, _ := e.offers.current(); cur.ID != "spotA" {
			t.Fatalf("now on %q, want to still be waiting on spotA", cur.ID)
		}
		// Positive control on the clock: past the budget, the same events move it.
		clk.t = clk.t.Add(engineOfferBudgetDefault + time.Second)
		e.stepOffers(t.Context(), mustView(t, e))
		if cur, _ := e.offers.current(); cur.ID != "spotB" {
			t.Fatalf("after the budget it is on %q, want spotB", cur.ID)
		}
	})

	t.Run("VcpuLimitExceeded skips the rest of that purchase option", func(t *testing.T) {
		st := testSettingsStore(t)
		capacity := &offerCapacityAPI{}
		api := &offerECS{}
		e := newOfferTestEngine(t, api, capacity, offers, st)
		startWalking(t, e, st)
		api.events = []offerEventFixture{placementEvent(offerProviderSpot, reasonVcpuLimit)}

		e.stepOffers(t.Context(), mustView(t, e))
		cur, _ := e.offers.current()
		if cur.ID != "l4" {
			t.Fatalf("now on %q, want the on-demand offer: the quotas are separate, so another Spot row hits the same wall", cur.ID)
		}
		if cur.buy() != engineBuyOnDemand {
			t.Fatalf("moved to a %s offer after a quota error", cur.buy())
		}
		if got := aws.ToString(api.updates[len(api.updates)-1].CapacityProviderStrategy[0].CapacityProvider); got != offerProviderOD {
			t.Errorf("the service was pointed at %q, want the on-demand provider", got)
		}
	})

	t.Run("the wrapped Spot quota skips the rest of that purchase option too", func(t *testing.T) {
		st := testSettingsStore(t)
		capacity := &offerCapacityAPI{}
		api := &offerECS{}
		e := newOfferTestEngine(t, api, capacity, offers, st)
		startWalking(t, e, st)
		// Verbatim from the deployment: the quota word arrives inside a ResourceInitializationError.
		api.events = []offerEventFixture{placementEvent(offerProviderSpot, reasonSpotQuota)}

		e.stepOffers(t.Context(), mustView(t, e))
		cur, _ := e.offers.current()
		if cur.ID != "l4" || cur.buy() != engineBuyOnDemand {
			t.Fatalf("now on %q (%s), want the on-demand offer: the Spot quota is a wall every Spot row shares",
				cur.ID, cur.buy())
		}
		if got := offerTrailResults(e); got != "spotA=quota,l4=active" {
			t.Fatalf("trail = %q, want the Spot row recorded as a quota rather than a budget", got)
		}
	})

	// 🔴 The re-run's second defect: after a move, the service's newest events still hold the
	// PREVIOUS offer's failure. Reading them as this offer's answer made `l4` "answer quota after
	// 5s" and the engine gave up on a list that had a working offer left in it — a quota takes a
	// whole purchase option off the table, so ONE misreading invalidates everything.
	t.Run("the previous offer's failure is not the next offer's answer", func(t *testing.T) {
		st := testSettingsStore(t)
		api := &offerECS{}
		e := newOfferTestEngine(t, api, &offerCapacityAPI{}, offers, st)
		startWalking(t, e, st)
		api.events = []offerEventFixture{placementEvent(offerProviderSpot, reasonSpotQuota)}

		// The Spot quota moves the walk to the on-demand offer, as it should.
		e.stepOffers(t.Context(), mustView(t, e))
		if cur, _ := e.offers.current(); cur.ID != "l4" {
			t.Fatalf("first step went to %q, want l4", cur.ID)
		}
		// Five seconds later ECS has written nothing new, and the Spot event from before the move
		// is still the newest thing on the service.
		api.events = []offerEventFixture{agedPlacementEvent(offerProviderSpot, reasonSpotQuota, 20*time.Second)}
		if spent := e.stepOffers(t.Context(), mustView(t, e)); spent {
			t.Fatal("the list was declared spent on the previous offer's event")
		}
		if cur, _ := e.offers.current(); cur.ID != "l4" {
			t.Fatalf("moved off %q on an event about another provider", cur.ID)
		}
		if got := offerTrailResults(e); got != "spotA=quota,l4=active" {
			t.Fatalf("trail = %q, want l4 still being tried", got)
		}

		// Positive control: an event about THIS offer's provider, written after the move, does
		// move it — so the assertion above is about the filter and not about a walk that stopped.
		api.events = []offerEventFixture{placementEvent(offerProviderOD, reasonUnfulfillable)}
		e.stepOffers(t.Context(), mustView(t, e))
		if got := offerTrailResults(e); got != "spotA=quota,l4=unfulfillable" {
			t.Fatalf("trail = %q, want l4's own failure to be read", got)
		}
	})

	t.Run("going round the whole list is one failed start", func(t *testing.T) {
		st := testSettingsStore(t)
		capacity := &offerCapacityAPI{}
		api := &offerECS{}
		e := newOfferTestEngine(t, api, capacity, offers, st)
		// Driven through the controller rather than through stepOffers directly: "one failure"
		// is a statement about what the CONTROLLER counts, and the cooldown is what acts on it.
		startWalking(t, e, st)
		api.events = []offerEventFixture{placementEvent(offerProviderSpot, reasonUnfulfillable)}
		for i := 0; i < 3; i++ {
			e.ctrl.tick(t.Context())
		}

		if got := offerTrailResults(e); got != "spotA=unfulfillable,spotB=unfulfillable,l4=unfulfillable" {
			t.Fatalf("trail = %q", got)
		}
		// One failure, not three: the cooldown doubles per consecutive failure, and counting per
		// offer would take a three-row list to the four-hour cooldown three times as fast.
		e.ctrl.mu.Lock()
		failures := e.ctrl.failures
		e.ctrl.mu.Unlock()
		if failures != 1 {
			t.Fatalf("failures = %d after the list was spent, want exactly 1", failures)
		}
		if api.desired != 0 {
			t.Errorf("desired = %d after giving up, want the service taken back to 0", api.desired)
		}
	})
}

// 🔴 The defect that made the default budget unusable (ADR 0075 live run 3): the box arrived in
// 26 seconds, and the CP moved on at 180 anyway — because what it was really timing was the TASK.
// A Spot offer can then never succeed: every start walks the whole list, buying a box per offer
// and running none of them.
func TestTheBudgetEndsWhenTheBoxArrivesNotWhenTheTaskRuns(t *testing.T) {
	const offers = "spotA|Spot A|22000|g6.xlarge|4-8|15000-65536|1.57|spot;" +
		"l4|L4|22000|g6.xlarge|4-8|15000-65536|1.26|od"

	t.Run("a box on the offer's provider stops the clock", func(t *testing.T) {
		st := testSettingsStore(t)
		api := &offerECS{strategy: offerProviderOD}
		e := newOfferTestEngine(t, api, &offerCapacityAPI{}, offers, st)
		clk := startWalking(t, e, st)
		// The box the offer asked for, registered on the offer's own capacity provider.
		api.instances = map[string]string{"arn:ci/i-1": offerProviderSpot}
		e.ecs.invalidateBox()
		// Far past the budget, with the silence that would otherwise move the walk on.
		clk.t = clk.t.Add(10 * engineOfferBudgetDefault)

		if spent := e.stepOffers(t.Context(), mustView(t, e)); spent {
			t.Fatal("the list was declared spent while a box was up")
		}
		if cur, _ := e.offers.current(); cur.ID != "spotA" {
			t.Fatalf("moved to %q with the box already registered; from here the start deadline owns the clock", cur.ID)
		}
	})

	t.Run("no box, and the same budget moves it on", func(t *testing.T) {
		// The positive control: identical fixture, identical clock, no container instance.
		st := testSettingsStore(t)
		api := &offerECS{strategy: offerProviderOD}
		e := newOfferTestEngine(t, api, &offerCapacityAPI{}, offers, st)
		clk := startWalking(t, e, st)
		clk.t = clk.t.Add(10 * engineOfferBudgetDefault)

		e.stepOffers(t.Context(), mustView(t, e))
		if cur, _ := e.offers.current(); cur.ID != "l4" {
			t.Fatalf("still on %q with no box and the budget spent", cur.ID)
		}
	})

	t.Run("the previous offer's box draining does not hide this offer's box", func(t *testing.T) {
		// The shape the deployment actually produced: two boxes registered at once, seventeen
		// seconds apart. Asked without naming the provider, ECS answers with whichever it lists
		// first — and if that is the one going away, rule 2 walks off a perfectly good box.
		st := testSettingsStore(t)
		api := &offerECS{strategy: offerProviderOD}
		e := newOfferTestEngine(t, api, &offerCapacityAPI{}, offers, st)
		clk := startWalking(t, e, st)
		api.instances = map[string]string{
			"arn:ci/i-old": offerProviderOD,   // the previous start's box, on its way out
			"arn:ci/i-new": offerProviderSpot, // this offer's
		}
		api.instanceStatuses = map[string]string{"arn:ci/i-old": "DRAINING"}
		e.ecs.invalidateBox()
		clk.t = clk.t.Add(10 * engineOfferBudgetDefault)

		e.stepOffers(t.Context(), mustView(t, e))
		if cur, _ := e.offers.current(); cur.ID != "spotA" {
			t.Fatalf("moved to %q while this offer's box was registered next to a draining one", cur.ID)
		}
	})

	t.Run("a DRAINING box is the previous offer leaving, not an arrival", func(t *testing.T) {
		st := testSettingsStore(t)
		api := &offerECS{strategy: offerProviderOD, instanceStatus: "DRAINING"}
		e := newOfferTestEngine(t, api, &offerCapacityAPI{}, offers, st)
		clk := startWalking(t, e, st)
		api.instances = map[string]string{"arn:ci/i-1": offerProviderSpot}
		e.ecs.invalidateBox()
		clk.t = clk.t.Add(10 * engineOfferBudgetDefault)

		e.stepOffers(t.Context(), mustView(t, e))
		if cur, _ := e.offers.current(); cur.ID != "l4" {
			t.Fatalf("still on %q: a box on its way out is not this offer's box", cur.ID)
		}
	})
}

// 🔴 An offer whose rung the capacity provider refuses cannot be started on. Measured while
// building the live positive control: `UpdateCapacityProvider` answers 400 "No instance types
// satisfy the instance requirements", and the CP used to log it and start anyway — so the
// provider still held the PREVIOUS offer's requirements when the box was bought.
func TestAnOfferThatCannotBeAppliedIsSkipped(t *testing.T) {
	// The middle offer asks for a type this fake refuses, exactly as ECS refuses a misspelt one.
	const offers = "spotA|Spot A|22000|g6.xlarge|4-8|15000-65536|1.57|spot;" +
		"l40s|L40S|44000|g6e.xlarge|4-8|30000-65536|2.91|od;" +
		"l4|L4|22000|g6.xlarge|4-8|15000-65536|1.26|od"

	t.Run("the walk steps over it", func(t *testing.T) {
		st := testSettingsStore(t)
		capacity := &offerCapacityAPI{refuseType: "g6e.xlarge"}
		api := &offerECS{strategy: offerProviderOD}
		e := newOfferTestEngine(t, api, capacity, offers, st)
		startWalking(t, e, st)
		api.events = []offerEventFixture{placementEvent(offerProviderSpot, reasonUnfulfillable)}

		e.stepOffers(t.Context(), mustView(t, e))
		if cur, _ := e.offers.current(); cur.ID != "l4" {
			t.Fatalf("moved to %q, want the offer past the one whose rung was refused", cur.ID)
		}
		if got := offerTrailResults(e); got != "spotA=unfulfillable,l40s=unusable,l4=active" {
			t.Fatalf("trail = %q, want the refused offer recorded rather than silently skipped", got)
		}
		// And the service is pointed at the on-demand provider, whose rung DID land.
		if got := api.strategy; got != offerProviderOD {
			t.Errorf("service strategy = %q", got)
		}
	})

	t.Run("positive control: nothing refused, and the walk stops at it", func(t *testing.T) {
		st := testSettingsStore(t)
		api := &offerECS{strategy: offerProviderOD}
		e := newOfferTestEngine(t, api, &offerCapacityAPI{}, offers, st)
		startWalking(t, e, st)
		api.events = []offerEventFixture{placementEvent(offerProviderSpot, reasonUnfulfillable)}

		e.stepOffers(t.Context(), mustView(t, e))
		if cur, _ := e.offers.current(); cur.ID != "l40s" {
			t.Fatalf("moved to %q, want the very next offer when its rung applies", cur.ID)
		}
	})

	t.Run("the start itself skips it", func(t *testing.T) {
		st := testSettingsStore(t)
		capacity := &offerCapacityAPI{refuseType: "g6.xlarge"} // the FIRST offer's type
		api := &offerECS{strategy: offerProviderOD}
		e := newOfferTestEngine(t, api, capacity, offers, st)

		ok, why := e.startGate(t.Context())
		if !ok {
			t.Fatalf("gate = %q, want the start allowed on an offer that does apply", why)
		}
		if cur, _ := e.offers.current(); cur.ID != "l40s" {
			t.Fatalf("the start chose %q, want the first offer whose rung the provider accepted", cur.ID)
		}
		if got := offerTrailResults(e); got != "spotA=unusable" {
			t.Fatalf("trail = %q", got)
		}
	})

	t.Run("no offer applies, and there is no start", func(t *testing.T) {
		st := testSettingsStore(t)
		capacity := &offerCapacityAPI{refuseType: "g6.xlarge"}
		api := &offerECS{strategy: offerProviderOD}
		e := newOfferTestEngine(t, api, capacity, "l4|L4|22000|g6.xlarge|4-8|15000-65536|1.26|od", st)

		if ok, why := e.startGate(t.Context()); ok || why != engineReasonClassApply {
			t.Fatalf("gate = %v %q, want the start refused", ok, why)
		}
		if len(api.updates) != 0 {
			t.Fatalf("the service was written to anyway: %+v", api.updates)
		}
	})
}

// The per-offer budget travels live, like the offer list and the provider names. Without it,
// raising it — which the deployment had to do to make Spot work at all — needs a Control Plane
// replacement, and the panel's figure is not the one in force.
func TestTheOfferBudgetIsCarriedLive(t *testing.T) {
	st := testSettingsStore(t)
	e := newOfferTestEngine(t, &offerECS{}, &offerCapacityAPI{}, twoOffers, st)
	e.def.CapacityProvider = offerProviderOD
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	row := func(budget int) string {
		return `{"engines":[{"key":"image","service":"af-image","url":"http://127.0.0.1:1",` +
			`"health":"/v1/models","provider":"sdcpp","api":"images","idleSec":900,` +
			`"startDeadlineSec":900,"capacityProvider":"` + offerProviderOD + `",` +
			`"spotCapacityProvider":"` + offerProviderSpot + `",` +
			`"offers":"` + twoOffers + `","offerBudgetSec":` + fmt.Sprint(budget) + `}]}`
	}
	ssmc := &fakePendingSSM{value: row(900)}
	r := newEngineTableReloader(ssmc, "/af-ws/engines", reg, "")

	if got := e.offers.budget(); got != engineOfferBudgetDefault {
		t.Fatalf("the fixture starts at %s, want the default", got)
	}
	if !r.tick(t.Context()) {
		t.Fatal("a table with a new offer budget changed nothing")
	}
	if got := e.offers.budget(); got != 900*time.Second {
		t.Fatalf("budget = %s after the reload, want 900s", got)
	}
	// Positive control for the poll: the same reloader on an unchanged value reports no change,
	// so the assertion above is about the new figure and not about tick() always answering true.
	ssmc.value = row(900)
	if r.tick(t.Context()) {
		t.Error("an unchanged table reported a change")
	}
}

func mustView(t *testing.T, e *engineRuntimeState) engineServiceView {
	t.Helper()
	e.ecs.invalidate()
	v, err := e.ecs.view(t.Context())
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	return v
}

func offerTrailResults(e *engineRuntimeState) string {
	parts := make([]string, 0, 4)
	for _, a := range e.offers.attempts() {
		parts = append(parts, a.ID+"="+a.Result)
	}
	return strings.Join(parts, ",")
}

// --- decision 3: either provider's box is this engine's --------------------------------

// The failure this prevents is measured: during the 0074 rename the panel reported `box: null`
// while a box was up, because the one name it matched on was the wrong one.
func TestABoxOnEitherProviderIsThisEnginesBox(t *testing.T) {
	st := testSettingsStore(t)
	capacity := &offerCapacityAPI{}
	api := &offerECS{instances: map[string]string{"arn:ci/i-1": offerProviderSpot}, instanceType: "g6.xlarge"}
	e := newOfferTestEngine(t, api, capacity, twoOffers, st)

	b, ok := e.ecs.box(t.Context())
	if !ok {
		t.Fatal("a box bought on the Spot provider was not recognised as this engine's")
	}
	if b.provider != offerProviderSpot {
		t.Errorf("box provider = %q", b.provider)
	}
	// The on-demand one too, and nothing else on the cluster.
	api.instances = map[string]string{"arn:ci/i-1": offerProviderOD}
	e.ecs.invalidateBox()
	if _, ok := e.ecs.box(t.Context()); !ok {
		t.Error("the on-demand provider's box was not recognised")
	}
	api.instances = map[string]string{"arn:ci/i-1": "af-ws-slots"}
	e.ecs.invalidateBox()
	if _, ok := e.ecs.box(t.Context()); ok {
		t.Error("a workspace slot's instance was claimed as the engine's box")
	}
}

// --- contract B: what the panel is handed ----------------------------------------------

func TestTheAdminRowCarriesTheOffersTheTrailAndTheOfferInUse(t *testing.T) {
	st := testSettingsStore(t)
	capacity := &offerCapacityAPI{}
	api := &offerECS{}
	const offers = "spotA|Spot A|22000|g6.xlarge|4-8|15000-65536|1.57|spot;" +
		"l4|L4|22000|g6.xlarge|4-8|15000-65536|1.26|od"
	e := newOfferTestEngine(t, api, capacity, offers, st)
	a := engineAdminAPI{memberAuth{&manager{store: st}}, &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}, st}

	row := a.row(t.Context(), e)
	offerRows, _ := row["offers"].([]map[string]any)
	if len(offerRows) != 2 || offerRows[0]["buy"] != engineBuySpot || offerRows[1]["buy"] != engineBuyOnDemand {
		t.Fatalf("offers = %v", row["offers"])
	}
	// The ADR 0074 fields ride unchanged beside them: this row is read by a Console that may be
	// older than the CP.
	for _, k := range []string{"classes", "class", "class_default", "class_is_default"} {
		if _, ok := row[k]; !ok {
			t.Fatalf("%q disappeared from the row: %v", k, row)
		}
	}
	if row["class_is_default"] != true {
		t.Errorf("class_is_default = %v with nothing pinned, want true (`not pinned`)", row["class_is_default"])
	}
	if _, said := row["offer_trail"]; said {
		t.Errorf("a trail was reported before anything was tried: %v", row["offer_trail"])
	}

	startWalking(t, e, st)
	api.events = []offerEventFixture{placementEvent(offerProviderSpot, reasonUnfulfillable)}
	e.stepOffers(t.Context(), mustView(t, e))

	row = a.row(t.Context(), e)
	trail, _ := row["offer_trail"].([]map[string]any)
	if len(trail) != 2 || trail[0]["id"] != "spotA" || trail[0]["result"] != engineOfferUnfulfillable ||
		trail[1]["id"] != "l4" || trail[1]["result"] != engineOfferActive {
		t.Fatalf("offer_trail = %v", row["offer_trail"])
	}
	// `offer` comes from the SERVICE's own strategy, not from what this process remembers
	// choosing (decision 11): the fake moved its strategy when the update was written.
	cur, _ := row["offer"].(map[string]any)
	if cur == nil || cur["id"] != "l4" || cur["buy"] != engineBuyOnDemand {
		t.Fatalf("offer = %v, want the row the service's strategy points at", row["offer"])
	}
	// And when the service names a provider this role does not declare — a CloudFormation
	// rewrite, somebody's console edit — the panel says nothing rather than guessing.
	api.strategy = "af-something-else"
	e.ecs.invalidate()
	if off, said := a.row(t.Context(), e)["offer"]; said {
		t.Errorf("offer = %v for a provider this role does not declare", off)
	}
}

// The admin toggle starts the box itself, so it has to buy the same box the controller would.
// A second start path that moved the desired count alone would be a way round decision 4 (a) —
// and the box it bought would be whichever provider the service was last pointed at, which after
// a release is always the on-demand one (decision 12).
func TestTheAdminOnToggleStartsOnTheChosenOffer(t *testing.T) {
	st := testSettingsStore(t)
	capacity := &offerCapacityAPI{}
	api := &offerECS{strategy: offerProviderOD}
	const offers = "spotA|Spot A|22000|g6.xlarge|4-8|15000-65536|1.57|spot;" + twoOffers
	e := newOfferTestEngine(t, api, capacity, offers, st)
	a := engineAdminAPI{memberAuth{&manager{store: st}}, &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}, st}

	if code, out := adminPut(t, a, "image", `{"mode":"on"}`); code != http.StatusOK {
		t.Fatalf("on = %d (%v)", code, out)
	}
	if len(api.updates) != 2 {
		t.Fatalf("%d UpdateService calls, want the strategy and then the desired count: %+v", len(api.updates), api.updates)
	}
	in := api.updates[0]
	if len(in.CapacityProviderStrategy) == 0 ||
		aws.ToString(in.CapacityProviderStrategy[0].CapacityProvider) != offerProviderSpot {
		t.Fatalf("the toggle started on %+v, want the first offer's Spot provider", in.CapacityProviderStrategy)
	}
	if aws.ToInt32(api.updates[1].DesiredCount) != 1 {
		t.Errorf("desired = %v", api.updates[1].DesiredCount)
	}

	// 🔴 And the controller's own tick, a second later, is the SAME start — not a second attempt.
	// On the deployment the two paths raced and `offer_trail` read `[spotA active, spotA active]`,
	// which is a lie about the walk and redraws the budget clock into the bargain.
	e.ctrl.tick(t.Context())
	if got := offerTrailResults(e); got != "spotA=active" {
		t.Fatalf("offer_trail = %q after both start paths ran, want one row for one demand", got)
	}
	if len(api.updates) != 2 {
		t.Errorf("%d UpdateService calls after the second start path: %+v", len(api.updates), api.updates)
	}
	if _, started := e.offers.waited(time.Time{}); !started {
		t.Error("the budget clock was cleared by the second start path; rule 2 would wait for ever")
	}
}

// Decision 8's contract with the Console: `{"class": ""}` takes the pin off. It is the only way
// back to automatic, so it cannot be a no-op.
func TestPutClassWithAnEmptyIdUnpins(t *testing.T) {
	st := testSettingsStore(t)
	capacity := &offerCapacityAPI{}
	e := newOfferTestEngine(t, &offerECS{}, capacity, twoOffers, st)
	a := engineAdminAPI{memberAuth{&manager{store: st}}, &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}, st}

	if code, out := putClass(t, a, `{"class":"l40s"}`); code != http.StatusOK {
		t.Fatalf("pin = %d (%v)", code, out)
	}
	if v, _ := st.GetSetting(t.Context(), engineClassSettingKey("image")); v != "l40s" {
		t.Fatalf("stored = %q", v)
	}
	code, out := putClass(t, a, `{"class":""}`)
	if code != http.StatusOK {
		t.Fatalf("unpin = %d (%v)", code, out)
	}
	if v, _ := st.GetSetting(t.Context(), engineClassSettingKey("image")); v != "" {
		t.Fatalf("stored = %q after unpinning, want nothing", v)
	}
	if out["class_is_default"] != true {
		t.Errorf("class_is_default = %v after unpinning", out["class_is_default"])
	}
	// The rung of the offer that would be chosen now was applied, so a missing grant is
	// discovered here rather than at the next cold start.
	if len(capacity.updates) != 2 {
		t.Errorf("%d capacity provider updates, want one per request", len(capacity.updates))
	}
}

// A response body helper mirroring the class tests', so the two read alike.
var _ = json.Marshal

var _ = httptest.NewRequest
