package main

// Which box an engine buys, and who buys it (ADR 0075 P0, rebuilt on ADR 0077 P1). What is
// pinned here is what a reader of the panel — or of the bill — cannot check for themselves:
//
//   - a deployment that declares no offer, or no launch template, never asks EC2 anything. Every
//     claim of that shape carries its positive control, because "the fake was never called" and
//     "the fake cannot be called" look identical;
//   - an ADR 0074 ladder is an all-on-demand offer list, unchanged. That is the whole migration;
//   - one CreateFleet response separates "try the next row" from "this whole purchase option is
//     full" from "this row can never be bought";
//   - 🔴 THE DESIRED COUNT ONLY EVER GOES TO 1 AFTER A BOX HAS REGISTERED. That is the reversal
//     ADR 0077 exists for, and it is asserted twice: on the behaviour and on the tree, because a
//     guard in a function nobody has to go through is decoration;
//   - an `od` offer's overrides carry `Priority` in the declared order, because `prioritized`
//     reads that field and not the order of the list.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// --- the fakes -------------------------------------------------------------------------

// offerBox is one container instance as the cluster reports it.
type offerBox struct {
	role         string // the af-role attribute; "" is a workspace slot
	instanceType string
	status       string // "" reads as ACTIVE
	disconnected bool
	tasks        int32
}

// offerECS is the engine's ECS service plus the cluster's container instances. Every
// UpdateService is recorded verbatim: most of what follows is about WHAT was written — a desired
// count, and never a strategy — rather than that something was.
type offerECS struct {
	mu               sync.Mutex
	desired, running int32
	updates          []*ecs.UpdateServiceInput
	describes        int
	lists            int
	// boxes are the cluster's container instances, keyed by EC2 instance id.
	boxes        map[string]offerBox
	deregistered []string
}

func (f *offerECS) DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.describes++
	return &ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
		Status: aws.String("ACTIVE"), DesiredCount: f.desired, RunningCount: f.running,
		Deployments: []ecstypes.Deployment{{Status: aws.String("PRIMARY"), Id: aws.String("ecs-svc/1")}},
	}}}, nil
}

func (f *offerECS) UpdateService(_ context.Context, in *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, in)
	if in.DesiredCount != nil {
		f.desired = *in.DesiredCount
	}
	return &ecs.UpdateServiceOutput{}, nil
}

func (f *offerECS) ListContainerInstances(context.Context, *ecs.ListContainerInstancesInput, ...func(*ecs.Options)) (*ecs.ListContainerInstancesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	out := &ecs.ListContainerInstancesOutput{}
	for id := range f.boxes {
		out.ContainerInstanceArns = append(out.ContainerInstanceArns, "arn:ci/"+id)
	}
	return out, nil
}

func (f *offerECS) DescribeContainerInstances(_ context.Context, in *ecs.DescribeContainerInstancesInput, _ ...func(*ecs.Options)) (*ecs.DescribeContainerInstancesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &ecs.DescribeContainerInstancesOutput{}
	for _, arn := range in.ContainerInstances {
		id := strings.TrimPrefix(arn, "arn:ci/")
		b, ok := f.boxes[id]
		if !ok {
			continue
		}
		status := b.status
		if status == "" {
			status = "ACTIVE"
		}
		ci := ecstypes.ContainerInstance{
			ContainerInstanceArn: aws.String(arn), Ec2InstanceId: aws.String(id),
			Status: aws.String(status), AgentConnected: !b.disconnected,
			RunningTasksCount: b.tasks, RegisteredAt: aws.Time(time.Now().Add(-time.Hour)),
		}
		if b.role != "" {
			ci.Attributes = append(ci.Attributes, ecstypes.Attribute{
				Name: aws.String(engineBoxRoleAttr), Value: aws.String(b.role),
			})
		}
		if b.instanceType != "" {
			ci.Attributes = append(ci.Attributes, ecstypes.Attribute{
				Name: aws.String(engineBoxTypeAttr), Value: aws.String(b.instanceType),
			})
		}
		out.ContainerInstances = append(out.ContainerInstances, ci)
	}
	return out, nil
}

func (f *offerECS) DeregisterContainerInstance(_ context.Context, in *ecs.DeregisterContainerInstanceInput, _ ...func(*ecs.Options)) (*ecs.DeregisterContainerInstanceOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := strings.TrimPrefix(aws.ToString(in.ContainerInstance), "arn:ci/")
	f.deregistered = append(f.deregistered, id)
	delete(f.boxes, id)
	return &ecs.DeregisterContainerInstanceOutput{}, nil
}

// register puts a box into the cluster, which is what the launch template's user data does when
// the instance finishes booting.
func (f *offerECS) register(id, role, instanceType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.boxes == nil {
		f.boxes = map[string]offerBox{}
	}
	f.boxes[id] = offerBox{role: role, instanceType: instanceType}
}

func (f *offerECS) desiredCount() int32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.desired
}

// desiredWrites is every desired count this service was asked for, in order. The whole of ADR
// 0077 done item 3 is a statement about this list.
func (f *offerECS) desiredWrites() []int32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int32
	for _, u := range f.updates {
		if u.DesiredCount != nil {
			out = append(out, *u.DesiredCount)
		}
	}
	return out
}

// fleetAnswer is one CreateFleet response: an instance, or the error codes that say why not.
type fleetAnswer struct {
	instance string
	codes    []string
}

// fakeFleet is EC2 as this feature uses it. It behaves like the real API in the two ways that
// decide whether a test means anything: `DescribeInstances` applies the FILTERS it is given (a
// walk that forgot `af-role` would see every box in the pool), and an instance bought by
// CreateFleet appears in it afterwards, tagged with whatever the call asked for.
type fakeFleet struct {
	mu sync.Mutex
	// answers are handed out in order and the last one repeats. Empty = every purchase succeeds
	// with a generated id.
	answers []fleetAnswer
	// instance forces every purchase to answer with this id, for a test that wants to line it
	// up with a container instance.
	instance string

	creates    []*ec2.CreateFleetInput
	terminated []string
	describes  int
	// createErr is what the CALL itself answers with, for the top-level refusals (P0 measured
	// `SsmAccessDenied` when the role cannot resolve the AMI parameter).
	createErr error

	instances map[string]*ec2types.Instance
	next      int
}

// writes is every call that changes something: a purchase or a terminate. `calls` includes the
// reads as well, which the panel legitimately makes.
func (f *fakeFleet) writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates) + len(f.terminated)
}

func (f *fakeFleet) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates) + len(f.terminated) + f.describes
}

func (f *fakeFleet) CreateFleet(_ context.Context, in *ec2.CreateFleetInput, _ ...func(*ec2.Options)) (*ec2.CreateFleetOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = append(f.creates, in)
	if f.createErr != nil {
		return nil, f.createErr
	}
	ans := fleetAnswer{instance: f.instance}
	if len(f.answers) > 0 {
		ans = f.answers[min(f.next, len(f.answers)-1)]
	}
	f.next++
	out := &ec2.CreateFleetOutput{FleetId: aws.String(fmt.Sprintf("fleet-%d", len(f.creates)))}
	if len(ans.codes) > 0 {
		for _, c := range ans.codes {
			out.Errors = append(out.Errors, ec2types.CreateFleetError{
				ErrorCode: aws.String(c), ErrorMessage: aws.String("fake: " + c),
			})
		}
		return out, nil
	}
	id := ans.instance
	if id == "" {
		id = fmt.Sprintf("i-%d", len(f.creates))
	}
	inst := &ec2types.Instance{
		InstanceId: aws.String(id),
		State:      &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
		LaunchTime: aws.Time(time.Now().Add(-time.Hour)),
	}
	if len(in.LaunchTemplateConfigs) > 0 && len(in.LaunchTemplateConfigs[0].Overrides) > 0 {
		inst.InstanceType = in.LaunchTemplateConfigs[0].Overrides[0].InstanceType
	}
	for _, ts := range in.TagSpecifications {
		inst.Tags = append(inst.Tags, ts.Tags...)
	}
	if f.instances == nil {
		f.instances = map[string]*ec2types.Instance{}
	}
	f.instances[id] = inst
	out.Instances = []ec2types.CreateFleetInstance{{InstanceIds: []string{id}}}
	return out, nil
}

func (f *fakeFleet) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.describes++
	out := &ec2.DescribeInstancesOutput{}
	for _, inst := range f.instances {
		if !fakeFleetMatches(inst, in.Filters) {
			continue
		}
		out.Reservations = append(out.Reservations, ec2types.Reservation{Instances: []ec2types.Instance{*inst}})
	}
	return out, nil
}

// fakeFleetMatches applies the filters the way EC2 does — every filter must match, and a value
// list is an OR. Without this the fake would be more forgiving than the API and a walk that asked
// for the wrong things would pass.
func fakeFleetMatches(inst *ec2types.Instance, filters []ec2types.Filter) bool {
	for _, fl := range filters {
		name := aws.ToString(fl.Name)
		got := ""
		switch {
		case strings.HasPrefix(name, "tag:"):
			got = engineTagValue(inst.Tags, strings.TrimPrefix(name, "tag:"))
		case name == "instance-state-name":
			if inst.State != nil {
				got = string(inst.State.Name)
			}
		default:
			return false
		}
		ok := false
		for _, v := range fl.Values {
			if v == got {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func (f *fakeFleet) TerminateInstances(_ context.Context, in *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminated = append(f.terminated, in.InstanceIds...)
	for _, id := range in.InstanceIds {
		if inst := f.instances[id]; inst != nil {
			inst.State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameShuttingDown}
		}
	}
	return &ec2.TerminateInstancesOutput{}, nil
}

// addForeignBox puts somebody else's instance into the pool — a workspace slot — so that a walk
// which forgot to filter on the role would find it.
func (f *fakeFleet) addForeignBox(id, role string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.instances == nil {
		f.instances = map[string]*ec2types.Instance{}
	}
	f.instances[id] = &ec2types.Instance{
		InstanceId: aws.String(id),
		State:      &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
		LaunchTime: aws.Time(time.Now().Add(-time.Hour)),
		Tags: []ec2types.Tag{
			{Key: aws.String("af-pool"), Value: aws.String("cluster")},
			{Key: aws.String("af-role"), Value: aws.String(role)},
		},
	}
}

func (f *fakeFleet) tagsOf(id string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	if inst := f.instances[id]; inst != nil {
		for _, t := range inst.Tags {
			out[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
	}
	return out
}

// --- the engine under test ---------------------------------------------------------------

// newOfferTestEngine is the `image` role as newEngineRegistry wires it: the offer list parsed the
// way the registry parses it, a controller, and the ADR 0077 hooks attached through the one
// function that decides whether they exist at all.
func newOfferTestEngine(t *testing.T, api engineECSAPI, fleet engineFleetAPI, offers string, st store.Store) *engineRuntimeState {
	t.Helper()
	return newOfferTestEngineWith(t, api, fleet, offers, "lt-image", st)
}

func newOfferTestEngineWith(t *testing.T, api engineECSAPI, fleet engineFleetAPI, offers, template string, st store.Store) *engineRuntimeState {
	t.Helper()
	e := newTestImageEngine(t, "http://127.0.0.1:1", api)
	e.def.LaunchTemplate = template
	e.def.Offers = offers
	e.ecs.roleAttr = engineBoxRole(e.def)
	e.classes = parseEngineOffers("image", e.def.offersSpec())
	e.cluster = "cluster"
	e.settings = st
	e.catalog = newEngineCatalog(st, "image")
	e.offers = newEngineOfferRun(engineOfferBudgetDefault)
	e.audit = st
	cfg := engineControlCfgFor(e.def)
	e.demand = newEngineDemand(st, engineSettingsFor("image").demandAt, cfg.window)
	e.ctrl = newEngineController(e.ecs, engineSettingsFor("image"), nil, e.demand, st, st, cfg)
	e.wireOffers(newEngineFleet(fleet, "image", "cluster", template, []string{"subnet-a", "subnet-b"}))
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

const twoOffers = "l4|L4 24GB|22000|g6.xlarge|4-8|15000-65536|1.26;" +
	"l40s|L40S 48GB|44000|g6e.xlarge|4-8|30000-65536|2.91"

func seedOfferModel(t *testing.T, st store.Store, id string, vram int) {
	t.Helper()
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: id, Kind: "checkpoint", VramMiB: vram, Enabled: true,
	}); err != nil {
		t.Fatalf("seed model: %v", err)
	}
}

func offerIDs(list []engineClass) string {
	ids := make([]string, 0, len(list))
	for _, c := range list {
		ids = append(ids, c.ID)
	}
	return strings.Join(ids, ",")
}

func offerTrailResults(e *engineRuntimeState) string {
	parts := make([]string, 0, 4)
	for _, a := range e.offers.attempts() {
		parts = append(parts, a.ID+"="+a.Result)
	}
	return strings.Join(parts, ",")
}

// --- ADR 0077 done item 1: no offers, no EC2 ----------------------------------------------

// 🔴 ADR 0074 decision 3, which 0075 and 0077 both inherit: a deployment that declares no offer
// must not gain a single EC2 call. The same for one that declares offers and no launch template
// — a CP upgraded before its stack, which has to keep serving. The positive control is the last
// third, because a fake that was never called proves nothing on its own.
func TestADeploymentWithNoOffersNeverAsksEC2ForABox(t *testing.T) {
	st := testSettingsStore(t)

	t.Run("no offers at all", func(t *testing.T) {
		fleet := &fakeFleet{}
		api := &offerECS{}
		e := newOfferTestEngine(t, api, fleet, "", st)

		tickIntoAStart(t, e, st)

		if fleet.calls() != 0 {
			t.Errorf("EC2 was called %d time(s) for a role that declares no offer: %+v", fleet.calls(), fleet.creates)
		}
		if got := api.desiredWrites(); len(got) != 1 || got[0] != 1 {
			t.Fatalf("desired writes = %v, want the one plain start", got)
		}
		// One DescribeServices — the controller's own look — and no container-instance walk:
		// a role with no box of its own has nothing to recognise on the cluster.
		if api.lists != 0 {
			t.Errorf("the cluster was walked %d time(s) for a role that owns no box", api.lists)
		}
	})

	t.Run("offers but no launch template", func(t *testing.T) {
		fleet := &fakeFleet{}
		api := &offerECS{}
		e := newOfferTestEngineWith(t, api, fleet, twoOffers, "", st)

		tickIntoAStart(t, e, st)

		if fleet.calls() != 0 {
			t.Errorf("EC2 was called %d time(s) for a role with no launch template", fleet.calls())
		}
		if got := api.desiredWrites(); len(got) != 1 || got[0] != 1 {
			t.Fatalf("desired writes = %v — a CP upgraded before its stack still starts its engine", got)
		}
	})

	t.Run("positive control: one offer and a template, and the box is bought", func(t *testing.T) {
		fleet := &fakeFleet{}
		api := &offerECS{}
		e := newOfferTestEngine(t, api, fleet, "l4|L4|21000|g6.xlarge|4-8|15000-65536|1.26|od", st)

		tickIntoAStart(t, e, st)

		if len(fleet.creates) != 1 {
			t.Fatalf("%d CreateFleet call(s) with one offer declared — the two cases above prove nothing", len(fleet.creates))
		}
		if got := api.desiredWrites(); len(got) != 0 {
			t.Fatalf("desired writes = %v before the box registered", got)
		}
	})
}

// The hooks are the whole mechanism, so "inert" has to mean they are not there.
func TestWireOffersAttachesNothingWithoutAnOfferOrATemplate(t *testing.T) {
	st := testSettingsStore(t)
	for _, tc := range []struct{ name, offers, template string }{
		{"no offers", "", "lt-image"},
		{"no launch template", twoOffers, ""},
	} {
		e := newOfferTestEngineWith(t, &offerECS{}, &fakeFleet{}, tc.offers, tc.template, st)
		if e.fleet != nil || e.ctrl.startGate != nil || e.ctrl.startWith != nil || e.ctrl.boxStep != nil {
			t.Fatalf("%s: hooks attached: fleet=%v gate=%v start=%v box=%v",
				tc.name, e.fleet != nil, e.ctrl.startGate != nil, e.ctrl.startWith != nil, e.ctrl.boxStep != nil)
		}
	}
	// Positive control: one row and a template, and all of them are wired.
	e := newOfferTestEngine(t, &offerECS{}, &fakeFleet{}, "l4|L4|21000|g6.xlarge|4-8|15000-65536", st)
	if e.fleet == nil || e.ctrl.startGate == nil || e.ctrl.startWith == nil || e.ctrl.boxStep == nil {
		t.Fatal("a declared offer with a launch template left a hook unattached")
	}
}

// --- the offer list itself ----------------------------------------------------------------

// The existing `<role>InstanceClasses` string, character for character, is a list of on-demand
// offers. This is the entire migration story (ADR 0075), and it is what lets a stack move a
// ladder to `offers` without an edit.
func TestAnInstanceClassLadderReadsAsAllOnDemandOffers(t *testing.T) {
	const ladder = "l4|L4 24GB (g6.xlarge)|22000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26;" +
		"l40s|L40S 48GB (g6e.xlarge)|44000|g6e.xlarge|4-8|30000-65536|2.91"
	d := engineDef{Classes: ladder}
	if d.offersSpec() != ladder {
		t.Fatalf("offersSpec = %q, want the ladder verbatim", d.offersSpec())
	}
	list := parseEngineClasses(d.offersSpec())
	if len(list) != 2 {
		t.Fatalf("parsed %d offers", len(list))
	}
	for _, c := range list {
		if c.buy() != engineBuyOnDemand {
			t.Errorf("offer %s reads as %q, want on-demand", c.ID, c.buy())
		}
	}
	// And `offers` wins where both are declared.
	both := engineDef{Classes: ladder, Offers: "spot|S|22000|g6.xlarge|4-8|15000-65536|1.57|spot"}
	if got := parseEngineClasses(both.offersSpec()); len(got) != 1 || got[0].buy() != engineBuySpot {
		t.Fatalf("offers = %+v, want the declared list to win", got)
	}
}

func TestParseEngineOffersReadsTheBuyColumn(t *testing.T) {
	list := parseEngineClasses(
		"a|A|22000|g6.xlarge|4-8|15000-65536|1.26|spot;" +
			"b|B|22000|g6.xlarge|4-8|15000-65536|1.26|OD;" +
			"c|C|22000|g6.xlarge|4-8|15000-65536|1.26|;" +
			"d|D|22000|g6.xlarge|4-8|15000-65536|1.26|sport")
	if len(list) != 3 {
		t.Fatalf("parsed %d offers, want the misspelt one dropped: %s", len(list), offerIDs(list))
	}
	if list[0].buy() != engineBuySpot || list[1].buy() != engineBuyOnDemand || list[2].buy() != engineBuyOnDemand {
		t.Fatalf("buy column = %s/%s/%s", list[0].buy(), list[1].buy(), list[2].buy())
	}
}

// ADR 0077 decision 9: the llm role is on-demand only, and under 0075 that was guaranteed by
// there being no Spot capacity provider to address. One launch template serves both purchase
// options now, so nothing stands in the way except this refusal.
func TestTheLlmRoleRefusesASpotOffer(t *testing.T) {
	const offers = "spot|Spot|22000|g6.xlarge|4-8|15000-65536|1.57|spot;" +
		"od|OD|22000|g6.xlarge|4-8|15000-65536|1.26|od"
	if got := offerIDs(parseEngineOffers("llm", offers)); got != "od" {
		t.Fatalf("llm offers = %q, want the spot row dropped: a lost conversation is not a retry", got)
	}
	// Positive control: the same list for the image role keeps both, so the assertion above is
	// about the role and not about a parser that drops spot rows everywhere.
	if got := offerIDs(parseEngineOffers("image", offers)); got != "spot,od" {
		t.Fatalf("image offers = %q, want both rows", got)
	}
}

// Decision 2: an offer smaller than the largest enabled model is not a candidate.
func TestOffersAreFilteredByWhatTheModelsNeed(t *testing.T) {
	st := testSettingsStore(t)
	e := newOfferTestEngine(t, &offerECS{}, &fakeFleet{}, twoOffers, st)
	if got := offerIDs(e.candidateOffers(t.Context())); got != "l4,l40s" {
		t.Fatalf("candidates with no model = %q, want both in declaration order", got)
	}
	seedOfferModel(t, st, "big", 30000)
	e.catalog.invalidate()
	if got := offerIDs(e.candidateOffers(t.Context())); got != "l40s" {
		t.Fatalf("candidates = %q, want the 22 GB offer dropped for a 30 GB model", got)
	}
}

// Decision 2's refusal: every offer is smaller than the model, so there is no box to buy and the
// engine does not start on the biggest one either.
func TestNoCandidateMeansNoStart(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{}
	api := &offerECS{}
	e := newOfferTestEngine(t, api, fleet, twoOffers, st)
	seedOfferModel(t, st, "huge", 90000)
	e.catalog.invalidate()

	ok, why := e.startGate(t.Context())
	if ok || why != engineReasonNoOffer {
		t.Fatalf("gate = %v %q, want the start refused because no offer holds 90000 MiB", ok, why)
	}
	tickIntoAStart(t, e, st)
	if len(fleet.creates) != 0 || len(api.updates) != 0 {
		t.Fatalf("the engine was started anyway: %d purchase(s), %d update(s)", len(fleet.creates), len(api.updates))
	}

	// Positive control: a model that fits, and the same engine starts on the offer that holds it.
	seedOfferModel(t, st, "huge", 30000)
	e.catalog.invalidate()
	if ok, why := e.startGate(t.Context()); !ok {
		t.Fatalf("gate still refusing with a model that fits (%s)", why)
	}
}

func TestAPinnedOfferDoesNotFallThroughAndUnpinningRestoresTheChoice(t *testing.T) {
	st := testSettingsStore(t)
	e := newOfferTestEngine(t, &offerECS{}, &fakeFleet{}, twoOffers, st)

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

// --- ADR 0077 done item 6: the request one offer becomes ----------------------------------

// ⚠️ `prioritized` reads each override's OWN `Priority` field, not the order of the list. An
// implementation that relied on the order would be correct on the page and wrong on the invoice —
// EC2 would spread across the types instead of preferring the cheapest declared one — and nothing
// but the bill would ever say so.
func TestAnOnDemandOfferWritesPriorityInTheDeclaredOrder(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{}
	e := newOfferTestEngine(t, &offerECS{}, fleet,
		"l4|L4|22000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26|od", st)

	tickIntoAStart(t, e, st)

	if len(fleet.creates) != 1 {
		t.Fatalf("%d CreateFleet call(s), want one per offer row", len(fleet.creates))
	}
	in := fleet.creates[0]
	if in.Type != ec2types.FleetTypeInstant {
		t.Errorf("fleet type = %q, want instant: everything else is asynchronous", in.Type)
	}
	if aws.ToInt32(in.TargetCapacitySpecification.TotalTargetCapacity) != 1 {
		t.Errorf("target capacity = %v, want exactly one box", in.TargetCapacitySpecification.TotalTargetCapacity)
	}
	if in.TargetCapacitySpecification.DefaultTargetCapacityType != ec2types.DefaultTargetCapacityTypeOnDemand {
		t.Errorf("capacity type = %q", in.TargetCapacitySpecification.DefaultTargetCapacityType)
	}
	if in.OnDemandOptions == nil || in.OnDemandOptions.AllocationStrategy != ec2types.FleetOnDemandAllocationStrategyPrioritized {
		t.Fatalf("on-demand options = %+v, want prioritized", in.OnDemandOptions)
	}
	// Two types × two subnets, in the declared order, each carrying its own priority.
	var got []string
	for _, o := range in.LaunchTemplateConfigs[0].Overrides {
		if o.Priority == nil {
			t.Fatalf("override %s/%s carries no Priority — `prioritized` would ignore the declaration",
				o.InstanceType, aws.ToString(o.SubnetId))
		}
		got = append(got, fmt.Sprintf("%s/%s#%g", o.InstanceType, aws.ToString(o.SubnetId), *o.Priority))
	}
	want := "g6.xlarge/subnet-a#1 g6.xlarge/subnet-b#2 g5.xlarge/subnet-a#3 g5.xlarge/subnet-b#4"
	if strings.Join(got, " ") != want {
		t.Errorf("overrides = %q,\n want %q", strings.Join(got, " "), want)
	}
	if got := aws.ToString(in.LaunchTemplateConfigs[0].LaunchTemplateSpecification.LaunchTemplateId); got != "lt-image" {
		t.Errorf("launch template = %q", got)
	}
	if got := aws.ToString(in.LaunchTemplateConfigs[0].LaunchTemplateSpecification.Version); got != "$Latest" {
		t.Errorf("template version = %q, want $Latest — CloudFormation owns the template", got)
	}
}

// The Spot half of the same request, and the tags that make the box findable afterwards.
func TestASpotOfferAsksForPriceCapacityOptimizedAndTagsTheBox(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{}
	e := newOfferTestEngine(t, &offerECS{}, fleet,
		"spot3|Spot|22000|g6.xlarge,g5.xlarge,g6.2xlarge|4-8|15000-65536|1.57|spot", st)

	tickIntoAStart(t, e, st)

	in := fleet.creates[0]
	if in.TargetCapacitySpecification.DefaultTargetCapacityType != ec2types.DefaultTargetCapacityTypeSpot {
		t.Errorf("capacity type = %q, want spot", in.TargetCapacitySpecification.DefaultTargetCapacityType)
	}
	if in.SpotOptions == nil || in.SpotOptions.AllocationStrategy != ec2types.SpotAllocationStrategyPriceCapacityOptimized {
		// ADR 0075 run 3 bought a g6e.xlarge from a three-type Spot row because Managed Instances
		// has no allocation strategy at all. This is the field that fixes it.
		t.Fatalf("spot options = %+v, want price-capacity-optimized", in.SpotOptions)
	}
	if in.OnDemandOptions != nil {
		t.Errorf("a spot row carried on-demand options: %+v", in.OnDemandOptions)
	}
	for _, o := range in.LaunchTemplateConfigs[0].Overrides {
		if o.Priority != nil {
			t.Errorf("a spot override carries Priority %v — the allocation strategy chooses here", *o.Priority)
			break
		}
	}
	// 🔴 The tags are what lets a restarted CP find this box at all (ADR 0045 decision 29). They
	// go on at launch AND are written again afterwards: a box whose tags did not land is a GPU
	// nothing can enumerate.
	tags := fleet.tagsOf("i-1")
	for k, want := range map[string]string{
		"af-pool": "cluster", "af-role": "engine-image",
		"af-engine-offer": "spot3", "af-engine-buy": "spot", "af-managed-by": "agent-fleet",
	} {
		if tags[k] != want {
			t.Errorf("tag %s = %q, want %q", k, tags[k], want)
		}
	}
	// 🔴 And they arrive with the PURCHASE: `TagSpecifications` merges with the launch
	// template's own tags (measured, ADR 0077 P0), so there is no second call to lose.
	if len(fleet.creates[0].TagSpecifications) != 1 ||
		fleet.creates[0].TagSpecifications[0].ResourceType != ec2types.ResourceTypeInstance {
		t.Errorf("tag specifications = %+v, want one for the instance", fleet.creates[0].TagSpecifications)
	}
}

// --- ADR 0077 done item 2: the failure-code table -----------------------------------------

// The table itself, as a pure function: three answers, and an unrecognised code moves to the next
// row rather than giving up (EC2 may reword one at any time, and a table that fell through to
// "give up" would turn that into an engine that walks its whole list in a second and cools down
// for four hours).
func TestEngineFleetVerdict(t *testing.T) {
	for _, tc := range []struct {
		code, action, result string
		known                bool
	}{
		{"InsufficientInstanceCapacity", engineFleetNext, engineOfferInsufficient, true},
		{"SpotMaxPriceTooLow", engineFleetNext, engineOfferUnfulfillable, true},
		{"MaxSpotInstanceCountExceeded", engineFleetSkipBuy, engineOfferQuota, true},
		{"VcpuLimitExceeded", engineFleetSkipBuy, engineOfferQuota, true},
		{"InvalidFleetConfiguration", engineFleetUnusable, engineOfferUnusable, true},
		{"UnauthorizedOperation", engineFleetRefused, engineOfferUnusable, true},
		// Wrapped, the way ECS wrapped the Spot quota inside a ResourceInitializationError
		// (measured, ADR 0075).
		{"ResourceInitializationError: MaxSpotInstanceCountExceeded", engineFleetSkipBuy, engineOfferQuota, true},
		{"SomethingAwsRenamedYesterday", engineFleetNext, engineOfferUnfulfillable, false},
	} {
		got, known := engineFleetVerdict(tc.code)
		if got.action != tc.action || got.result != tc.result || known != tc.known {
			t.Errorf("%s = %+v/%v, want %s/%s/%v", tc.code, got, known, tc.action, tc.result, tc.known)
		}
	}
}

// 🔴 `Errors[]` carries one entry PER OVERRIDE (measured, ADR 0077 P0), so a three-type row can
// answer three different things at once. What the fold does with that is the part a reader cannot
// check from the table alone.
func TestEngineFleetResponseVerdict(t *testing.T) {
	for _, tc := range []struct {
		name           string
		codes          []string
		action, result string
	}{
		{"every override refused the configuration", []string{"InvalidFleetConfiguration", "InvalidFleetConfiguration"},
			engineFleetUnusable, engineOfferUnusable},
		// One override refused and the others had no stock: `InvalidFleetConfiguration` is one
		// word for "misspelt" AND for "not offered in that AZ" (P0 could not tell them apart),
		// so a minority of them must not condemn the row.
		{"only some overrides refused it", []string{"InvalidFleetConfiguration", "InsufficientInstanceCapacity"},
			engineFleetNext, engineOfferInsufficient},
		{"a quota anywhere in the list wins", []string{"InsufficientInstanceCapacity", "MaxSpotInstanceCountExceeded"},
			engineFleetSkipBuy, engineOfferQuota},
		// 🔴 The one that must never read as "no stock": it is in the same list, in the same
		// shape, and it means the deployment cannot launch anything at all.
		{"a missing grant stops the walk", []string{"InsufficientInstanceCapacity", "UnauthorizedOperation"},
			engineFleetRefused, engineOfferUnusable},
		{"nothing recognised", []string{"WhoKnows"}, engineFleetNext, engineOfferUnfulfillable},
		{"no errors at all", nil, engineFleetNext, engineOfferUnfulfillable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := engineFleetResponseVerdict(tc.codes)
			if got.action != tc.action || got.result != tc.result {
				t.Errorf("%v = %+v, want %s/%s", tc.codes, got, tc.action, tc.result)
			}
		})
	}
}

// And the same table where it matters: what the walk does with it.
func TestTheWalkBranchesOnTheFailureCode(t *testing.T) {
	// A Spot row, a second Spot row, and an on-demand one — the shape that makes "skip the rest
	// of this purchase option" different from "take the next row".
	const offers = "spotA|Spot A|22000|g6.xlarge|4-8|15000-65536|1.57|spot;" +
		"spotB|Spot B|22000|g5.xlarge|4-8|15000-65536|1.57|spot;" +
		"l4|L4|22000|g6.xlarge|4-8|15000-65536|1.26|od"

	t.Run("no stock takes the very next row", func(t *testing.T) {
		st := testSettingsStore(t)
		fleet := &fakeFleet{answers: []fleetAnswer{
			{codes: []string{"InsufficientInstanceCapacity"}}, {instance: "i-9"},
		}}
		e := newOfferTestEngine(t, &offerECS{}, fleet, offers, st)
		tickIntoAStart(t, e, st)

		if got := offerTrailResults(e); got != "spotA=insufficient,spotB=active" {
			t.Fatalf("trail = %q, want the very next row tried", got)
		}
		if len(fleet.creates) != 2 {
			t.Fatalf("%d purchases, want one per row tried", len(fleet.creates))
		}
	})

	t.Run("a quota skips the rest of that purchase option", func(t *testing.T) {
		st := testSettingsStore(t)
		fleet := &fakeFleet{answers: []fleetAnswer{
			{codes: []string{"MaxSpotInstanceCountExceeded"}}, {instance: "i-9"},
		}}
		e := newOfferTestEngine(t, &offerECS{}, fleet, offers, st)
		tickIntoAStart(t, e, st)

		// spotB is never asked for: the two quotas are separate, so another Spot row hits the
		// same wall, and asking would be a call that cannot succeed.
		if got := offerTrailResults(e); got != "spotA=quota,l4=active" {
			t.Fatalf("trail = %q, want the second Spot row skipped entirely", got)
		}
		cur, _ := e.offers.current()
		if cur.buy() != engineBuyOnDemand {
			t.Fatalf("moved to a %s offer after a quota error", cur.buy())
		}
		if len(fleet.creates) != 2 {
			t.Fatalf("%d purchases, want spotB not to have been asked for at all", len(fleet.creates))
		}
	})

	t.Run("a type EC2 refuses is unusable, and the walk steps over it", func(t *testing.T) {
		st := testSettingsStore(t)
		fleet := &fakeFleet{answers: []fleetAnswer{
			{codes: []string{"InvalidFleetConfiguration"}}, {instance: "i-9"},
		}}
		e := newOfferTestEngine(t, &offerECS{}, fleet, offers, st)
		tickIntoAStart(t, e, st)

		// 🔴 `unusable` rather than a failure of the whole start: under ADR 0075 a misspelt type
		// was refused by UpdateCapacityProvider, the CP logged it and started anyway, and the
		// provider still held the PREVIOUS offer's requirements when the box was bought.
		if got := offerTrailResults(e); got != "spotA=unusable,spotB=active" {
			t.Fatalf("trail = %q, want the refused row recorded rather than silently skipped", got)
		}
	})

	// 🔴 The failure that looks exactly like "there was no stock" and is not (measured, ADR 0077
	// P0): a missing `iam:PassRole` comes back as `UnauthorizedOperation` INSIDE a 200 response,
	// in `Errors[]`, where every capacity answer also lives. Read as "next", it would walk the
	// whole offer list against a deployment that cannot launch anything — once per demand, for
	// ever, with a log line per row and nothing naming the grant.
	t.Run("a missing grant stops the walk instead of spending it", func(t *testing.T) {
		st := testSettingsStore(t)
		fleet := &fakeFleet{answers: []fleetAnswer{{codes: []string{"UnauthorizedOperation"}}}}
		api := &offerECS{}
		e := newOfferTestEngine(t, api, fleet, offers, st)
		tickIntoAStart(t, e, st)

		if len(fleet.creates) != 1 {
			t.Fatalf("%d purchases, want the walk abandoned after the first refusal", len(fleet.creates))
		}
		if got := offerTrailResults(e); got != "spotA=unusable" {
			t.Fatalf("trail = %q, want the one row it got to", got)
		}
		// Still ONE failed start, so the cooldown holds the next demand off rather than a busy
		// loop asking the same impossible question.
		e.ctrl.mu.Lock()
		failures := e.ctrl.failures
		e.ctrl.mu.Unlock()
		if failures != 1 {
			t.Fatalf("failures = %d after a refusal, want 1", failures)
		}
		// Positive control: the identical fixture answering "no stock" instead DOES walk on.
		st2 := testSettingsStore(t)
		fleet2 := &fakeFleet{answers: []fleetAnswer{{codes: []string{"InsufficientInstanceCapacity"}}}}
		e2 := newOfferTestEngine(t, &offerECS{}, fleet2, offers, st2)
		tickIntoAStart(t, e2, st2)
		if len(fleet2.creates) < 2 {
			t.Fatalf("%d purchases for a capacity failure — the assertion above is about the code, not about the walk stopping", len(fleet2.creates))
		}
	})

	// The top-level spelling of the same fact: P0 measured `SsmAccessDenied` coming back as the
	// call's own error when the role cannot resolve the AMI parameter.
	t.Run("a denied call stops the walk too", func(t *testing.T) {
		st := testSettingsStore(t)
		fleet := &fakeFleet{createErr: fmt.Errorf("SsmAccessDenied: User is not authorized to perform ssm:GetParameters")}
		e := newOfferTestEngine(t, &offerECS{}, fleet, offers, st)
		tickIntoAStart(t, e, st)
		if len(fleet.creates) != 1 {
			t.Fatalf("%d purchases, want the walk abandoned after the first denial", len(fleet.creates))
		}
	})

	t.Run("going round the whole list is one failed start", func(t *testing.T) {
		st := testSettingsStore(t)
		fleet := &fakeFleet{answers: []fleetAnswer{{codes: []string{"InsufficientInstanceCapacity"}}}}
		api := &offerECS{}
		e := newOfferTestEngine(t, api, fleet, offers, st)
		tickIntoAStart(t, e, st)

		if got := offerTrailResults(e); got != "spotA=insufficient,spotB=insufficient,l4=insufficient" {
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
		if got := api.desiredWrites(); len(got) != 0 {
			t.Errorf("desired writes = %v after a start that bought nothing", got)
		}
	})
}

// --- ADR 0077 done item 3: desired 1 only after the box registered -------------------------

// 🔴 The reversal this whole ADR is for, as a NEGATIVE claim: there is no route that asks ECS for
// a task before the box is in the cluster. Under ADR 0075 the desired count was what sent ECS
// shopping, and everything the three hardware rounds found lives in that gap — two boxes bought,
// one offer's echo read as the next offer's answer, a start that waited 2 m 35 s for a
// deployment to settle.
//
// The positive control is at the end: the identical fixture with the box registered writes the
// count immediately. Removing the `registered()` guard in startOnOffer makes the first half fail.
func TestTheDesiredCountOnlyFollowsARegisteredBox(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{instance: "i-77"}
	api := &offerECS{}
	e := newOfferTestEngine(t, api, fleet, twoOffers, st)

	// Several ticks with the box bought and NOT registered — the seconds a real box spends
	// booting (21 s measured, ADR 0045 decision 22).
	for range 3 {
		tickIntoAStart(t, e, st)
	}
	if len(fleet.creates) != 1 {
		t.Fatalf("%d purchases for one start, want exactly one — every extra one is a GPU", len(fleet.creates))
	}
	if got := api.desiredWrites(); len(got) != 0 {
		t.Fatalf("desired writes = %v while no box had registered; that is a task ECS would have to place itself", got)
	}
	if !e.offers.startInFlight() {
		t.Fatal("the run does not know a box is on its way; a second start path would buy another")
	}

	// The box boots and joins the cluster. Positive control for everything above: the same tick
	// now writes the count.
	api.register("i-77", "engine-image", "g6.xlarge")
	e.ecs.invalidateBox()
	tickIntoAStart(t, e, st)
	if got := api.desiredWrites(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("desired writes = %v once the box registered, want one write of 1", got)
	}
	if len(fleet.creates) != 1 {
		t.Errorf("%d purchases in total, want the one", len(fleet.creates))
	}
	// The write carries the desired count and NOTHING else: no strategy to replace a running
	// task with, and no forced deployment (ADR 0077 decision 2).
	last := api.updates[len(api.updates)-1]
	if len(last.CapacityProviderStrategy) != 0 || last.ForceNewDeployment {
		t.Errorf("the start wrote %+v, want the desired count alone", last)
	}
}

// A box that is registered but whose agent is NOT connected is not a box ECS will place on. The
// slot pool waits for exactly this pair (ACTIVE and agentConnected), and asking for the task a
// moment early is a task that sits PENDING.
func TestABoxIsNotReadyUntilItsAgentIsConnected(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{instance: "i-77"}
	api := &offerECS{boxes: map[string]offerBox{"i-77": {role: "engine-image", disconnected: true}}}
	e := newOfferTestEngine(t, api, fleet, twoOffers, st)

	tickIntoAStart(t, e, st)
	tickIntoAStart(t, e, st)
	if got := api.desiredWrites(); len(got) != 0 {
		t.Fatalf("desired writes = %v while the box's agent was disconnected", got)
	}
	// Positive control: the agent connects and the next tick writes the count.
	api.register("i-77", "engine-image", "")
	e.ecs.invalidateBox()
	tickIntoAStart(t, e, st)
	if got := api.desiredWrites(); len(got) != 1 {
		t.Fatalf("desired writes = %v once the agent connected", got)
	}
}

// The other half of done item 3, on the TREE rather than on the behaviour: a guard inside a
// function nobody has to go through is decoration. Every route that asks this service for a task
// is in engine_offer.go, and the only one an engine with offers can reach is the one behind
// `registered()`.
func TestOnlyOneFileAsksForTheTask(t *testing.T) {
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
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "setEnabled(ctx, true)") {
				hits[name]++
			}
		}
	}
	// Positive control: the scanner really does find them. An empty result and a scanner that
	// never ran look identical.
	if hits["engine_offer.go"] == 0 {
		t.Fatalf("the scanner found no start at all in engine_offer.go — it is not looking at anything")
	}
	delete(hits, "engine_offer.go")
	if len(hits) != 0 {
		t.Fatalf("a second file asks for the task: %v — every start has to go through the walk that owns the box", hits)
	}
}

// --- the registration ceiling ------------------------------------------------------------

// The budget's whole remaining meaning (ADR 0077 decision 1): a box that never joins the cluster
// is terminated — it is ours and it is billing — and the next offer is tried. Under ADR 0075 this
// clock timed "can this provider produce a box", which the purchase call now answers outright.
func TestABoxThatNeverRegistersIsEndedAndTheNextOfferTried(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{}
	api := &offerECS{}
	e := newOfferTestEngine(t, api, fleet, twoOffers, st)
	clk := time.Now()
	e.offers.now = func() time.Time { return clk }

	tickIntoAStart(t, e, st)
	if len(fleet.creates) != 1 || len(fleet.terminated) != 0 {
		t.Fatalf("after the first tick: %d purchase(s), %d terminate(s)", len(fleet.creates), len(fleet.terminated))
	}
	// Inside the ceiling, nothing moves.
	clk = clk.Add(engineOfferBudgetDefault - time.Second)
	tickIntoAStart(t, e, st)
	if len(fleet.creates) != 1 {
		t.Fatalf("the walk moved on after %s, inside the ceiling", engineOfferBudgetDefault-time.Second)
	}
	// Past it: the box goes, and the next offer is bought.
	clk = clk.Add(2 * time.Second)
	tickIntoAStart(t, e, st)
	if len(fleet.terminated) != 1 || fleet.terminated[0] != "i-1" {
		t.Fatalf("terminated = %v, want the box that never registered", fleet.terminated)
	}
	if got := offerTrailResults(e); got != "l4=budget,l40s=active" {
		t.Fatalf("trail = %q", got)
	}
	if got := api.desiredWrites(); len(got) != 0 {
		t.Errorf("desired writes = %v — no box ever registered", got)
	}
}

// --- ADR 0077 decision 5: the box leaves when the service does -----------------------------

func TestTheBoxIsEndedOnceTheServiceIsStoppedAndEmpty(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{instance: "i-77"}
	api := &offerECS{}
	e := newOfferTestEngine(t, api, fleet, twoOffers, st)

	// Start, register, and let the task come up.
	tickIntoAStart(t, e, st)
	api.register("i-77", "engine-image", "g6.xlarge")
	e.ecs.invalidateBox()
	tickIntoAStart(t, e, st)
	api.mu.Lock()
	api.running, api.boxes["i-77"] = 1, offerBox{role: "engine-image", tasks: 1}
	api.mu.Unlock()
	e.ecs.invalidateBox()
	e.fleet.invalidate()

	// A running engine's box is never swept, whatever the tick sees.
	e.sweepBoxes(t.Context(), engineServiceView{state: "running", desired: 1, running: 1})
	if len(fleet.terminated) != 0 {
		t.Fatalf("the box of a RUNNING engine was terminated: %v", fleet.terminated)
	}

	// The idle window closes: desired 0, and the task goes.
	api.mu.Lock()
	api.desired, api.running = 0, 0
	api.boxes["i-77"] = offerBox{role: "engine-image"}
	api.mu.Unlock()
	e.ecs.invalidateBox()
	e.offers.dropBox() // the start is over; this is what the desired-count write leaves behind

	e.sweepBoxes(t.Context(), engineServiceView{state: "stopped"})

	// 🔴 Out of the cluster FIRST, then out of EC2 (ADR 0045 decision 3-2): a terminated instance
	// stays registered as ACTIVE with no agent, and a ghost that looks ACTIVE still satisfies
	// placement constraints.
	if len(api.deregistered) != 1 || api.deregistered[0] != "i-77" {
		t.Fatalf("deregistered = %v, want the container instance taken out first", api.deregistered)
	}
	if len(fleet.terminated) != 1 || fleet.terminated[0] != "i-77" {
		t.Fatalf("terminated = %v, want the box ended once nothing is running on it", fleet.terminated)
	}
}

// Direction (b) of the same sweep: ECS keeps a container instance whose EC2 instance is gone, and
// the slot pool's own ghost sweep no longer sees an engine box (decision 3). A ghost that looks
// ACTIVE still satisfies placement constraints, so the next task is aimed at a box that is not
// there.
func TestAGhostContainerInstanceIsDeregistered(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{}
	api := &offerECS{boxes: map[string]offerBox{"i-gone": {role: "engine-image"}}}
	e := newOfferTestEngine(t, api, fleet, twoOffers, st)

	e.sweepBoxes(t.Context(), engineServiceView{state: "stopped"})
	if len(api.deregistered) != 1 || api.deregistered[0] != "i-gone" {
		t.Fatalf("deregistered = %v, want the ghost taken out of the cluster", api.deregistered)
	}
	if len(fleet.terminated) != 0 {
		t.Errorf("an instance that does not exist was terminated: %v", fleet.terminated)
	}

	// Positive control: a container instance whose EC2 instance IS there, with the engine's task
	// on it, is left alone.
	fleet.addForeignBox("i-live", "engine-image")
	api.mu.Lock()
	api.boxes["i-live"] = offerBox{role: "engine-image", tasks: 1}
	api.mu.Unlock()
	e.ecs.invalidateBox()
	e.fleet.invalidate()
	before := len(api.deregistered)
	e.sweepBoxes(t.Context(), engineServiceView{state: "running", desired: 1, running: 1})
	if len(api.deregistered) != before {
		t.Fatalf("deregistered %v — a live box was read as a ghost", api.deregistered[before:])
	}
}

// The sweep must not touch a box it does not own, and the filter that keeps it out is the one
// sent to DescribeInstances. A workspace slot is in the same pool and carries the same af-pool
// tag; the only thing that separates them is af-role.
func TestTheSweepLeavesSomebodyElsesBoxAlone(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{}
	fleet.addForeignBox("i-slot", "slot")
	fleet.addForeignBox("i-llm", "engine-llm")
	api := &offerECS{}
	e := newOfferTestEngine(t, api, fleet, twoOffers, st)

	e.sweepBoxes(t.Context(), engineServiceView{state: "stopped"})
	if len(fleet.terminated) != 0 {
		t.Fatalf("terminated %v — a workspace slot and the other role's GPU are not this engine's", fleet.terminated)
	}
	// Positive control: the same sweep, with a box of THIS role in the same list.
	fleet.addForeignBox("i-mine", "engine-image")
	e.fleet.invalidate()
	e.sweepBoxes(t.Context(), engineServiceView{state: "stopped"})
	if len(fleet.terminated) != 1 || fleet.terminated[0] != "i-mine" {
		t.Fatalf("terminated = %v, want this role's box and only it", fleet.terminated)
	}
}

// --- one start, one box --------------------------------------------------------------------

// 🔴 The admin toggle starts the box itself because somebody is watching, and the controller's
// next tick agrees a second later. On the ADR 0075 deployment those two raced and `offer_trail`
// read `[spot3 active, spot3 active]` — a lie about the walk. Here the second one would be a
// second GPU.
func TestTheAdminToggleAndTheControllerBuyOneBoxBetweenThem(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{instance: "i-77"}
	api := &offerECS{}
	const offers = "spotA|Spot A|22000|g6.xlarge|4-8|15000-65536|1.57|spot;" + twoOffers
	e := newOfferTestEngine(t, api, fleet, offers, st)
	a := engineAdminAPI{memberAuth{&manager{store: st}}, &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}, st}

	if code, out := adminPut(t, a, "image", `{"mode":"on"}`); code != http.StatusOK {
		t.Fatalf("on = %d (%v) — a box on its way up is not a failed request", code, out)
	}
	if len(fleet.creates) != 1 {
		t.Fatalf("%d purchases from the toggle, want one", len(fleet.creates))
	}
	e.ctrl.tick(t.Context())
	if len(fleet.creates) != 1 {
		t.Fatalf("%d purchases after the controller's own tick: %+v", len(fleet.creates), fleet.creates)
	}
	if got := offerTrailResults(e); got != "spotA=active" {
		t.Fatalf("offer_trail = %q after both start paths ran, want one row for one demand", got)
	}
}

// --- contract B: what the panel is handed --------------------------------------------------

func TestTheAdminRowCarriesTheOffersTheTrailAndTheOfferOnTheBox(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{instance: "i-77"}
	api := &offerECS{}
	const offers = "spotA|Spot A|22000|g6.xlarge|4-8|15000-65536|1.57|spot;" +
		"l4|L4|22000|g6.xlarge|4-8|15000-65536|1.26|od"
	e := newOfferTestEngine(t, api, fleet, offers, st)
	a := engineAdminAPI{memberAuth{&manager{store: st}}, &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}, st}

	row := a.row(t.Context(), e)
	offerRows, _ := row["offers"].([]map[string]any)
	if len(offerRows) != 2 || offerRows[0]["buy"] != engineBuySpot || offerRows[1]["buy"] != engineBuyOnDemand {
		t.Fatalf("offers = %v", row["offers"])
	}
	// The ADR 0074 fields ride unchanged beside them: this row is read by a Console that may be
	// older than the CP, and ADR 0077 changes contract B in no way at all.
	for _, k := range []string{"classes", "class", "class_default", "class_is_default"} {
		if _, ok := row[k]; !ok {
			t.Fatalf("%q disappeared from the row: %v", k, row)
		}
	}
	if _, said := row["offer_trail"]; said {
		t.Errorf("a trail was reported before anything was tried: %v", row["offer_trail"])
	}
	if _, said := row["offer"]; said {
		t.Errorf("offer = %v with no box up", row["offer"])
	}

	tickIntoAStart(t, e, st)
	e.fleet.invalidate()

	row = a.row(t.Context(), e)
	trail, _ := row["offer_trail"].([]map[string]any)
	if len(trail) != 1 || trail[0]["id"] != "spotA" || trail[0]["result"] != engineOfferActive {
		t.Fatalf("offer_trail = %v", row["offer_trail"])
	}
	// 🔴 `offer` comes from THE BOX'S OWN TAGS (ADR 0077 decision 8). The service has no strategy
	// to read any more, and the tags were written by the call that paid for the hardware — so the
	// panel answers the same after a CP restart, which is what ADR 0075 decision 11 could not.
	cur, _ := row["offer"].(map[string]any)
	if cur == nil || cur["id"] != "spotA" || cur["buy"] != engineBuySpot {
		t.Fatalf("offer = %v, want the row the box's tags name", row["offer"])
	}
}

// Decision 8's contract with the Console: `{"class": ""}` takes the pin off. It is the only way
// back to automatic, so it cannot be a no-op.
func TestPutClassWithAnEmptyIdUnpins(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{}
	e := newOfferTestEngine(t, &offerECS{}, fleet, twoOffers, st)
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
	// Neither a pin nor an unpin buys anything: the choice reaches hardware at the next start.
	if fleet.writes() != 0 {
		t.Errorf("%d EC2 write(s) for a stored choice", fleet.writes())
	}
}

// The registration ceiling travels live, like the offer list and the launch template. Without it,
// raising it needs a Control Plane replacement and the panel's figure is not the one in force.
func TestTheOfferBudgetIsCarriedLive(t *testing.T) {
	st := testSettingsStore(t)
	e := newOfferTestEngine(t, &offerECS{}, &fakeFleet{}, twoOffers, st)
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	row := func(budget int) string {
		return `{"engines":[{"key":"image","service":"af-image","url":"http://127.0.0.1:1",` +
			`"health":"/v1/models","provider":"sdcpp","api":"images","idleSec":900,` +
			`"startDeadlineSec":900,"launchTemplate":"lt-image",` +
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

// --- the second CP pass: what the P1 hardware run found ------------------------------------

// 🔴 Decision 5's departure never ran once, and this is the line that let it.
//
// `startInFlight` means "a box is bought and the desired count is not written yet", and the
// sweep stands down while it is true — a box we bought seconds ago has no task on it by
// construction. But until the fix the only things that cleared it were the registration ceiling
// and the NEXT start, so a start that SUCCEEDED left it true for the rest of the demand: the idle
// window closed, the task went, and the sweep returned at its first line. Measured twice on
// hardware (ADR 0077 P1 run): `mode=off`, no task, and the box still running four minutes later.
func TestASuccessfulStartEndsTheWalkSoTheBoxCanLeave(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{instance: "i-77"}
	api := &offerECS{}
	e := newOfferTestEngine(t, api, fleet, twoOffers, st)

	tickIntoAStart(t, e, st)
	api.register("i-77", "engine-image", "g6.xlarge")
	e.ecs.invalidateBox()
	tickIntoAStart(t, e, st)
	if got := api.desiredWrites(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("desired writes = %v, want the start to have finished", got)
	}
	// 🔴 The claim: the walk is over the moment the count is written.
	if e.offers.startInFlight() {
		t.Fatal("the run still says a start is in flight after the desired count was written")
	}

	// The engine is switched off and the task goes. The very next sweep must end the box.
	api.mu.Lock()
	api.desired, api.running = 0, 0
	api.boxes["i-77"] = offerBox{role: "engine-image"}
	api.mu.Unlock()
	e.ecs.invalidateBox()
	e.fleet.invalidate()

	e.sweepBoxes(t.Context(), engineServiceView{state: "stopped"})
	if len(fleet.terminated) != 1 || fleet.terminated[0] != "i-77" {
		t.Fatalf("terminated = %v, want the box ended once the engine stopped", fleet.terminated)
	}

	// The positive control is the defect itself: with the box still remembered — which is what a
	// start that never cleared it leaves behind — the identical sweep does nothing at all.
	fleet2 := &fakeFleet{instance: "i-78"}
	api2 := &offerECS{boxes: map[string]offerBox{"i-78": {role: "engine-image"}}}
	e2 := newOfferTestEngine(t, api2, fleet2, twoOffers, st)
	tickIntoAStart(t, e2, st)
	if !e2.offers.startInFlight() {
		t.Fatal("the control fixture has no box in flight")
	}
	e2.sweepBoxes(t.Context(), engineServiceView{state: "stopped"})
	if len(fleet2.terminated) != 0 {
		t.Fatalf("terminated %v while a start was in flight — the sweep would end the box it just bought", fleet2.terminated)
	}
}

// The other half of forgetting the box: the admin toggle calls the start unconditionally on
// `mode=on`, so a press while the engine is ALREADY RUNNING must not begin the walk again. Under
// the fix above the run no longer says "in flight", so this is the guard that stops the second
// purchase — and a second purchase is a second GPU.
func TestAStartOnAnEngineThatIsAlreadyUpBuysNothing(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{instance: "i-77"}
	api := &offerECS{}
	e := newOfferTestEngine(t, api, fleet, twoOffers, st)
	a := engineAdminAPI{memberAuth{&manager{store: st}}, &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}, st}

	tickIntoAStart(t, e, st)
	api.register("i-77", "engine-image", "g6.xlarge")
	e.ecs.invalidateBox()
	tickIntoAStart(t, e, st)
	api.mu.Lock()
	api.running = 1
	api.boxes["i-77"] = offerBox{role: "engine-image", tasks: 1}
	api.mu.Unlock()
	e.ecs.invalidate()

	if code, out := adminPut(t, a, "image", `{"mode":"on"}`); code != http.StatusOK {
		t.Fatalf("on = %d (%v)", code, out)
	}
	if len(fleet.creates) != 1 {
		t.Fatalf("%d purchases after pressing ON on a running engine, want the one from the start", len(fleet.creates))
	}
}

// 🔴 `<Role>Enabled=true` recreates the service, and CloudFormation creates it at **desired 1**
// with no box to place on (measured: the stack update sits in stabilisation until somebody
// writes desired 0). The controller has to be the one that writes it — whoever set the count —
// or the migration's second half needs a human with a shell.
func TestModeOffTakesTheDesiredCountDownWhoeverWroteIt(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{}
	// desired 1, nothing running, no box anywhere: exactly what CloudFormation leaves behind.
	api := &offerECS{desired: 1}
	e := newOfferTestEngine(t, api, fleet, twoOffers, st)
	if err := st.SetSetting(t.Context(), engineSettingsFor("image").mode, engineModeOff); err != nil {
		t.Fatal(err)
	}
	e.demand.stamp(t.Context())

	e.ctrl.tick(t.Context())

	if got := api.desiredWrites(); len(got) != 1 || got[0] != 0 {
		t.Fatalf("desired writes = %v, want the count taken down to 0", got)
	}
	if len(fleet.creates) != 0 {
		t.Errorf("%d purchases while the mode is off", len(fleet.creates))
	}
	// Positive control: the same fixture with the mode ON leaves the count alone (it is already
	// where it wants it), so the assertion above is about `off` and not about a tick that always
	// writes 0.
	st2 := testSettingsStore(t)
	api2 := &offerECS{desired: 1}
	e2 := newOfferTestEngine(t, &offerECS{}, &fakeFleet{}, twoOffers, st2)
	e2.ecs.api = api2
	if err := st2.SetSetting(t.Context(), engineSettingsFor("image").mode, engineModeOn); err != nil {
		t.Fatal(err)
	}
	e2.demand.stamp(t.Context())
	e2.ctrl.tick(t.Context())
	if got := api2.desiredWrites(); len(got) != 0 {
		t.Fatalf("desired writes = %v with the mode on, want none", got)
	}
}

// 🔴 A `mode: on` pressed on an engine that is ALREADY RUNNING must not touch the trail.
//
// It never bought a box — the start's own "already asked for" guard holds (P1 hardware run 2
// confirmed that on the deployment) — but the admin toggle presses the start GATE first, and the
// gate's first act is `begin()`, which empties the trail. So the panel lost `offer_trail` for the
// demand it was serving: the operator's own click deleted the answer to "why is it on this box".
//
// The fix is an ordering one, so the positive control has to be about the order: the same press
// on a STOPPED engine does begin a new walk, which is what proves the gate still reaches begin().
func TestModeOnWhileRunningKeepsTheTrail(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{instance: "i-77"}
	api := &offerECS{}
	e := newOfferTestEngine(t, api, fleet, twoOffers, st)
	a := engineAdminAPI{memberAuth{&manager{store: st}}, &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}, st}

	// A start that got its box and its task.
	tickIntoAStart(t, e, st)
	api.register("i-77", "engine-image", "g6.xlarge")
	e.ecs.invalidateBox()
	tickIntoAStart(t, e, st)
	api.mu.Lock()
	api.running = 1
	api.boxes["i-77"] = offerBox{role: "engine-image", tasks: 1}
	api.mu.Unlock()
	e.ecs.invalidate()
	if got := offerTrailResults(e); got != "l4=active" {
		t.Fatalf("trail = %q before the press", got)
	}

	if code, out := adminPut(t, a, "image", `{"mode":"on"}`); code != http.StatusOK {
		t.Fatalf("on = %d (%v)", code, out)
	}

	if got := offerTrailResults(e); got != "l4=active" {
		t.Fatalf("trail = %q after pressing ON on a running engine — the panel lost the walk it is serving", got)
	}
	if row := a.row(t.Context(), e); row["offer_trail"] == nil {
		t.Errorf("offer_trail is absent from the panel row: %v", row)
	}
	if len(fleet.creates) != 1 {
		t.Errorf("%d purchases, want the one from the start", len(fleet.creates))
	}

	// Positive control: the engine is stopped, and the same press DOES begin a new walk. Without
	// this, a gate that had simply stopped calling begin() would pass the assertion above.
	api.mu.Lock()
	api.desired, api.running = 0, 0
	api.mu.Unlock()
	e.ecs.invalidate()
	e.offers.dropBox()
	if ok, why := e.startGate(t.Context()); !ok {
		t.Fatalf("the gate refused a start on a stopped engine (%s)", why)
	}
	if got := offerTrailResults(e); got != "" {
		t.Fatalf("trail = %q after the gate began a new walk, want it emptied", got)
	}
}

// --- ADR 0077 decision 4: the box was taken away -------------------------------------------

// interruptedEngine is an engine whose start succeeded: a box bought, registered, the task
// running, and the controller's previous observation recorded as `running`.
func interruptedEngine(t *testing.T, st store.Store, offers string) (*engineRuntimeState, *offerECS, *fakeFleet) {
	t.Helper()
	fleet := &fakeFleet{}
	api := &offerECS{}
	e := newOfferTestEngine(t, api, fleet, offers, st)
	tickIntoAStart(t, e, st)
	api.register("i-1", "engine-image", "g6.xlarge")
	e.ecs.invalidateBox()
	tickIntoAStart(t, e, st)
	api.mu.Lock()
	api.running = 1
	api.boxes["i-1"] = offerBox{role: "engine-image", tasks: 1}
	api.mu.Unlock()
	e.ecs.invalidate()
	e.ecs.invalidateBox()
	// The controller's previous observation. `running` → `starting` is the transition decision 4
	// is read from, and it exists nowhere but between two ticks.
	e.ctrl.tick(t.Context())
	return e, api, fleet
}

// interrupt takes the box away the way EC2 does: the instance goes, and the service falls back to
// `starting` with the desired count still 1.
func interrupt(t *testing.T, e *engineRuntimeState, api *offerECS, fleet *fakeFleet, id string) {
	t.Helper()
	fleet.mu.Lock()
	if inst := fleet.instances[id]; inst != nil {
		inst.State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameTerminated}
	}
	fleet.mu.Unlock()
	api.mu.Lock()
	api.running = 0
	delete(api.boxes, id)
	api.mu.Unlock()
	e.ecs.invalidate()
	e.ecs.invalidateBox()
	e.fleet.invalidate()
}

// offerBought is the offer id the nth purchase carried, read off the tags the call asked for —
// which is also what the box will be found by afterwards.
func offerBought(t *testing.T, fleet *fakeFleet, n int) string {
	t.Helper()
	fleet.mu.Lock()
	defer fleet.mu.Unlock()
	if n >= len(fleet.creates) {
		t.Fatalf("only %d purchase(s)", len(fleet.creates))
	}
	return engineTagValue(fleet.creates[n].TagSpecifications[0].Tags, engineTagOffer)
}

// 🔴 (i) The rebuild: the box is gone, the task is PENDING, and the CP buys again FROM THE TOP of
// the list without touching the desired count. ECS places the pending task on the new box the
// moment it registers, which is why the count must not be written: it is already 1, and ADR 0075
// decision 6's "write only when the first candidate differs" restriction existed to avoid racing
// ECS's own re-placement — and ECS buys nothing here.
func TestAnInterruptionRebuildsFromTheTopWithoutWritingTheDesiredCount(t *testing.T) {
	st := testSettingsStore(t)
	e, api, fleet := interruptedEngine(t, st, twoOffers)
	writes := len(api.desiredWrites())
	if got := offerTrailResults(e); got != "l4=active" {
		t.Fatalf("trail = %q before the interruption", got)
	}

	interrupt(t, e, api, fleet, "i-1")
	e.ctrl.tick(t.Context())

	if len(fleet.creates) != 2 {
		t.Fatalf("%d purchases, want a second one for the rebuild", len(fleet.creates))
	}
	// From the TOP: the first offer is what the operator asked for, and an interruption says
	// nothing about the offers above the one that was reclaimed.
	if got := offerBought(t, fleet, 1); got != "l4" {
		t.Errorf("the rebuild bought offer %q, want the top of the list", got)
	}
	if got := api.desiredWrites(); len(got) != writes {
		t.Fatalf("the rebuild wrote the desired count (%v); the task is already asked for", got)
	}
	if got := offerTrailResults(e); got != "l4=interrupted,l4=active" {
		t.Fatalf("trail = %q, want the interruption recorded and the rebuild in flight", got)
	}
	// Not a failure (rule 3: a Spot box dying suddenly is acceptable).
	e.ctrl.mu.Lock()
	failures := e.ctrl.failures
	e.ctrl.mu.Unlock()
	if failures != 0 {
		t.Errorf("failures = %d after an interruption, want 0", failures)
	}
	// And the new box finishes the walk without a desired write, so the departure sweep can
	// collect it after the next idle window (the #584 shape).
	api.register("i-2", "engine-image", "g6.xlarge")
	e.ecs.invalidateBox()
	e.ctrl.tick(t.Context())
	if e.offers.startInFlight() {
		t.Error("the rebuild's walk never ended; the departure sweep would stand down for ever")
	}
	if got := api.desiredWrites(); len(got) != writes {
		t.Errorf("desired writes = %v, want the count untouched throughout the rebuild", got)
	}
}

// 🔴 (ii) The positive control that matters most: a task replaced WITH THE BOX STILL THERE is not
// an interruption. An OOM kill and a failed health check produce the identical service transition
// (measured, ADR 0071 P0), and rebuilding then would buy a second GPU for a task ECS is already
// placing on the first one.
func TestATaskReplacedOnALiveBoxIsNotAnInterruption(t *testing.T) {
	st := testSettingsStore(t)
	e, api, fleet := interruptedEngine(t, st, twoOffers)

	// The task goes; the box does not.
	api.mu.Lock()
	api.running = 0
	api.boxes["i-1"] = offerBox{role: "engine-image"}
	api.mu.Unlock()
	e.ecs.invalidate()
	e.ecs.invalidateBox()
	e.fleet.invalidate()

	e.ctrl.tick(t.Context())

	if n := len(fleet.creates); n != 1 {
		t.Fatalf("%d purchases, want no rebuild while the box is still here", n)
	}
	if got := offerTrailResults(e); got != "l4=active" {
		t.Fatalf("trail = %q, want the walk untouched", got)
	}
}

// (iii) An offer interrupted TWICE IN A ROW is skipped for the rest of the demand: a Spot pool
// that is reclaiming this shape now will reclaim it again in a minute, and the operator's list
// has another row for exactly this.
func TestAnOfferInterruptedTwiceInARowIsSkipped(t *testing.T) {
	st := testSettingsStore(t)
	e, api, fleet := interruptedEngine(t, st, twoOffers)

	// First interruption: rebuilt on the same (top) offer.
	interrupt(t, e, api, fleet, "i-1")
	e.ctrl.tick(t.Context())
	if got := offerBought(t, fleet, 1); got != "l4" {
		t.Fatalf("the first rebuild bought %q", got)
	}
	api.register("i-2", "engine-image", "g6.xlarge")
	e.ecs.invalidateBox()
	e.ctrl.tick(t.Context())
	// The task comes up on it, so the controller sees running → starting again.
	api.mu.Lock()
	api.running = 1
	api.boxes["i-2"] = offerBox{role: "engine-image", tasks: 1}
	api.mu.Unlock()
	e.ecs.invalidate()
	e.ctrl.tick(t.Context())

	// Second interruption of the same offer.
	interrupt(t, e, api, fleet, "i-2")
	e.ctrl.tick(t.Context())

	if len(fleet.creates) != 3 {
		t.Fatalf("%d purchases, want a third", len(fleet.creates))
	}
	if got := offerBought(t, fleet, 2); got != "l40s" {
		t.Errorf("the second rebuild bought %q, want the NEXT offer — the top one has been taken twice", got)
	}
	if got := offerTrailResults(e); got != "l4=interrupted,l4=interrupted,l40s=active" {
		t.Fatalf("trail = %q", got)
	}
}

// (iv) An interruption is not a failure; a rebuild that cannot get a box is. The cooldown is what
// stands between "the Spot pool is empty right now" and a CreateFleet per tick for ever.
func TestARebuildThatGetsNoBoxIsAFailedStart(t *testing.T) {
	st := testSettingsStore(t)
	e, api, fleet := interruptedEngine(t, st, twoOffers)
	// Every purchase from here answers "no stock", so the rebuild walks the list and spends it.
	fleet.mu.Lock()
	fleet.answers = []fleetAnswer{{codes: []string{"InsufficientInstanceCapacity"}}}
	fleet.next = 0
	fleet.mu.Unlock()

	interrupt(t, e, api, fleet, "i-1")
	e.ctrl.tick(t.Context())

	e.ctrl.mu.Lock()
	failures := e.ctrl.failures
	e.ctrl.mu.Unlock()
	if failures != 1 {
		t.Fatalf("failures = %d after a rebuild that got no box, want exactly 1", failures)
	}
	if got := offerTrailResults(e); got != "l4=interrupted,l4=insufficient,l40s=insufficient" {
		t.Fatalf("trail = %q", got)
	}
	// The desired count is still 1 throughout: the rebuild never writes it, and giving up on the
	// list is the start deadline's business (it stops the service), not the rebuild's.
	if api.desiredCount() != 1 {
		t.Errorf("desired = %d", api.desiredCount())
	}
}

// A rebuild the operator switched off mid-flight has a box to end. Nothing would drive that walk
// again — the rebuild's premise is a task the service wants — so it is ended here rather than
// left for the sweep's ceiling backstop.
func TestARebuildAbandonedByAStopEndsItsBox(t *testing.T) {
	st := testSettingsStore(t)
	e, api, fleet := interruptedEngine(t, st, twoOffers)

	interrupt(t, e, api, fleet, "i-1")
	e.ctrl.tick(t.Context())
	if len(fleet.creates) != 2 || !e.offers.rebuilding() {
		t.Fatalf("the rebuild did not start: %d purchase(s)", len(fleet.creates))
	}

	// The engine is switched off while the replacement box is still booting.
	if err := st.SetSetting(t.Context(), engineSettingsFor("image").mode, engineModeOff); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	api.desired, api.running = 0, 0
	api.mu.Unlock()
	e.ecs.invalidate()

	e.ctrl.tick(t.Context())

	if len(fleet.terminated) != 1 || fleet.terminated[0] != "i-2" {
		t.Fatalf("terminated = %v, want the replacement box ended", fleet.terminated)
	}
	if e.offers.startInFlight() {
		t.Error("the abandoned walk is still in flight; the departure sweep would stand down")
	}
}

// A second box with no task on it, next to one that IS carrying the task, is the two-box leak ADR
// 0075 produced three times out of three — and the sweep collects it while the engine runs.
//
// 🔴 The second condition ("another box is busy") is what separates it from an ordinary task
// replacement, where the engine's ONLY box has zero tasks for a few seconds. The positive control
// is that shape: the same sweep, the same ages, one box, and nothing is terminated.
func TestASecondIdleBoxIsSweptWhileTheEngineRuns(t *testing.T) {
	st := testSettingsStore(t)
	fleet := &fakeFleet{}
	fleet.addForeignBox("i-busy", "engine-image")
	fleet.addForeignBox("i-idle", "engine-image")
	api := &offerECS{
		desired: 1, running: 1,
		boxes: map[string]offerBox{
			"i-busy": {role: "engine-image", tasks: 1},
			"i-idle": {role: "engine-image"},
		},
	}
	e := newOfferTestEngine(t, api, fleet, twoOffers, st)

	e.sweepBoxes(t.Context(), engineServiceView{state: "running", desired: 1, running: 1})

	if len(fleet.terminated) != 1 || fleet.terminated[0] != "i-idle" {
		t.Fatalf("terminated = %v, want the idle second box and only it", fleet.terminated)
	}

	// The positive control: no box is carrying the task (a replacement in flight), so the idle
	// one is the engine's own and must be left for ECS to place on.
	fleet2 := &fakeFleet{}
	fleet2.addForeignBox("i-only", "engine-image")
	api2 := &offerECS{desired: 1, boxes: map[string]offerBox{"i-only": {role: "engine-image"}}}
	e2 := newOfferTestEngine(t, api2, fleet2, twoOffers, st)
	e2.sweepBoxes(t.Context(), engineServiceView{state: "starting", desired: 1})
	if len(fleet2.terminated) != 0 {
		t.Fatalf("terminated %v — a task being re-placed is not a stray box", fleet2.terminated)
	}
}
