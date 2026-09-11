// engine_ecs.go — the ECS side of an on-demand engine (ADR 0070 for VOICEVOX, ADR 0071 for
// the inference engines, which is what made this general).
//
// An engine is an ECS service whose desired count moves between 0 and 1, so a stopped
// engine costs nothing. Addressing is a fixed Cloud Map DNS name, which leaves every caller
// (the synthesis handler, the /engine/* gateway) pointing at one URL. The service, task
// definition and Cloud Map entry are owned by IaC (deploy/aws); CP only calls
// DescribeServices and UpdateService, plus — for a Managed Instances engine — the
// container-instance reads it already has. A small adapter independent of the workspace ECS
// adapter (runtime_ecs.go).
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// engineECSAPI is the narrow ECS port, so tests can pass a fake. The real *ecs.Client
// satisfies it. The two container-instance calls are only ever made for an engine that
// declares a capacity provider, i.e. an ADR 0071 Managed Instances engine; a Fargate engine
// leaves them unused.
type engineECSAPI interface {
	DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	UpdateService(context.Context, *ecs.UpdateServiceInput, ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
	ListContainerInstances(context.Context, *ecs.ListContainerInstancesInput, ...func(*ecs.Options)) (*ecs.ListContainerInstancesOutput, error)
	DescribeContainerInstances(context.Context, *ecs.DescribeContainerInstancesInput, ...func(*ecs.Options)) (*ecs.DescribeContainerInstancesOutput, error)
}

type engineECS struct {
	api     engineECSAPI
	key     string // "tts" / "llm" — what this engine is called in logs
	cluster string
	service string
	mu      sync.Mutex
	cached  engineServiceView
	cachAt  time.Time
	cachEr  error
	// capacityProvider is set only for a Managed Instances engine. It is what makes
	// `draining` observable: with desired 0 the service says "stopped" the moment the task
	// goes, while the EC2 instance behind it lives on for several more minutes (measured:
	// 427 and 463 seconds on a GPU box, 93 on a CPU one) and bills the whole time. Empty =
	// Fargate, where there is no such state.
	//
	// 🔴 Under the mutex, and read through provider(): replacing a capacity provider renames
	// it, and the engine table the Control Plane re-reads carries the new name (ADR 0074, the
	// gap #536 measured on the Spot swap). It is a destination string and a match string —
	// it keys nothing this process holds — so it moves live rather than needing a restart.
	capacityProvider string
	// spotProvider is the Spot half of the pair (ADR 0075 decision 3). Empty on every deployment
	// that declares none, and then this engine behaves exactly as it did with one name.
	//
	// 🔴 The two are held TOGETHER, under the same mutex, and every read that asks "is this my
	// box" asks about BOTH. #542 narrowed the copy to one field so that the destination and the
	// match could not disagree; widening it to a pair keeps that property — what must never
	// happen is a box bought on one of them being invisible because the other was the one
	// consulted (measured during the 0074 rename: `box: null` while the panel said the engine was
	// up on an l4).
	spotProvider string
	cachBoxes    []engineBox
	cachBoxAt    time.Time
	now          func() time.Time // test seam
}

// provider is the ON-DEMAND capacity provider name, now. It is also the historical single name:
// a role that declares no Spot provider has this one and nothing else.
func (t *engineECS) provider() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.capacityProvider
}

// providerFor is the capacity provider one offer is bought from (ADR 0075 decision 3). "" means
// this deployment declares none for that purchase option, and an offer nobody can address is an
// offer that cannot be tried.
func (t *engineECS) providerFor(buy string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if buy == engineBuySpot {
		return t.spotProvider
	}
	return t.capacityProvider
}

// providers is the pair, for the callers that have to match a box against either of them.
func (t *engineECS) providers() (onDemand, spot string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.capacityProvider, t.spotProvider
}

// setProviders replaces the pair, reporting whether either name actually changed.
//
// 🔴 It drops the cached box as well. That entry was matched against the OLD names, so
// keeping it would report the previous provider's instance as this engine's for the rest of
// engineBoxTTL — which is the panel telling the operator a box is up on a card it is not on,
// the failure shape ADR 0074 decision 4 is about.
func (t *engineECS) setProviders(onDemand, spot string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.capacityProvider == onDemand && t.spotProvider == spot {
		return false
	}
	t.capacityProvider, t.spotProvider = onDemand, spot
	t.cachBoxes, t.cachBoxAt = nil, time.Time{}
	return true
}

// engineBox is the EC2 instance a Managed Instances engine is running on, as ECS sees it.
//
// It exists because the service's own timestamps answer a different question. `lastStart` is
// when the primary deployment last changed state — a fact about the service — and after a
// task is replaced underneath, or a stack update, it moves without a new box being bought.
// What an operator looking at a $1.26/hour GPU wants is when THE BOX started, and the only
// place that is written down is the container instance's registeredAt.
//
// ⚠️ Do not reach for `ec2 describe-instances` to answer this. A Managed Instances box does
// NOT appear in an unfiltered listing — measured on af-sandbox while the task was RUNNING:
// the listing returned three unrelated instances and not the engine's, while
// `--instance-ids i-08a9…` returned it as a running g6.xlarge (ADR 0071, P1 の実測 2). ECS is
// the source that can be enumerated.
type engineBox struct {
	instanceID string    // i-08a9… — the id `describe-instances --instance-ids` will accept
	arn        string    // the container instance ARN
	status     string    // ACTIVE while it can take tasks, DRAINING once it is going away
	since      time.Time // registeredAt: when the box joined the cluster
	// instanceType is the box's EC2 type, read from the container instance's own
	// `ecs.instance-type` attribute (ADR 0074). It answers a question the capacity provider
	// cannot: the provider says what the NEXT box will be, and after a rung change the two
	// differ for as long as the old box lives. Empty when ECS did not report the attribute,
	// and empty must never be read as "it matches" — see startGate.
	instanceType string
	// provider is which of the role's two capacity providers this box was bought from (ADR 0075
	// decision 3). It is the honest answer to "on-demand or Spot" the CP can give without
	// `ec2:DescribeInstances` — the `InstanceLifecycle` that would PROVE it is reachable by
	// instance id only, and ADR 0045 decision 21 keeps that call out of the CP.
	provider string
}

// engineProviderMatches reports whether a container instance's capacity provider is one of this
// engine's. An empty declared name never matches: a role with no Spot provider must not adopt
// every instance ECS reports without one.
func engineProviderMatches(got, onDemand, spot string) bool {
	if got == "" {
		return false
	}
	return (onDemand != "" && got == onDemand) || (spot != "" && got == spot)
}

// engineBoxTypeAttr is the container-instance attribute holding the EC2 instance type. ECS
// registers it on every instance; it is the only place the CP can read the type of a Managed
// Instances box, which does not appear in an unfiltered `ec2 describe-instances` at all
// (measured, ADR 0071).
const engineBoxTypeAttr = "ecs.instance-type"

// engineBoxTTL is the cache in front of the two container-instance calls. Longer than
// engineViewTTL because it answers a slower question: a box takes minutes to appear and 427-477
// seconds to go away (measured), so nothing here changes inside three seconds, and the admin
// panel polls every five while an engine is moving.
const engineBoxTTL = 20 * time.Second

// engineServiceView is one DescribeServices answer, reduced to what the readiness gate and
// the on-demand controller look at.
type engineServiceView struct {
	state     string    // running | starting | draining | stopped | none
	desired   int32     // the service's desired count
	running   int32     // tasks actually running
	rollout   string    // the primary deployment's rolloutState (COMPLETED / IN_PROGRESS / FAILED)
	lastStart time.Time // when the primary deployment last changed state — see describe()
	// events holds the newest service events, which is the only place ECS writes down why
	// a start failed ("no container instances met the placement constraints", a pull
	// failure). Reading them needs no permission the CP role does not already have.
	events []string
	// provider is the capacity provider the SERVICE's own strategy names right now (ADR 0075
	// decision 11). The panel says what the engine is running on from this and never from what
	// the CP remembers choosing: CloudFormation rewrites the strategy on every release that
	// touches the service, and a remembered choice would go on claiming Spot while the box is
	// on-demand — the shape of the lie the 0074 rename produced (`box: null`, "starting on l4").
	// Empty for a service that declares a launch type instead of a strategy.
	provider string
	// deployment is the PRIMARY deployment's id. A forced deployment replaces it, so a CHANGED
	// id is the evidence that a strategy write has actually landed — which is what the start
	// waits for before it moves the desired count (ADR 0075, live run 3's two boxes).
	deployment string
}

// engineViewTTL is the short cache in front of DescribeServices (ADR 0070 decision 10).
// /api/tts/status called it once per request, which is harmless while only the admin panel
// polls and is not once every client polls to render "starting". Same shape and roughly
// the same length as the readiness cache next to it (vvReadyTTL).
const engineViewTTL = 3 * time.Second

// view is the cached DescribeServices. An error is cached for the same TTL: a service that
// answers with an error answers with it for every caller in that window, and hammering the
// API is how one misconfiguration becomes a throttle.
func (t *engineECS) view(ctx context.Context) (engineServiceView, error) {
	now := t.clock()
	t.mu.Lock()
	if !t.cachAt.IsZero() && now.Sub(t.cachAt) < engineViewTTL {
		v, err := t.cached, t.cachEr
		t.mu.Unlock()
		return v, err
	}
	t.mu.Unlock()

	v, err := t.describe(ctx)
	t.mu.Lock()
	t.cached, t.cachEr, t.cachAt = v, err, now
	t.mu.Unlock()
	return v, err
}

// invalidate drops the cached view, so the answer right after a start or stop reflects the
// desired count that was just written rather than the one from up to a TTL ago.
func (t *engineECS) invalidate() {
	t.mu.Lock()
	t.cachAt = time.Time{}
	t.mu.Unlock()
}

// invalidateBox drops the container-instance cache. Its 20-second TTL answers a question that
// normally moves in minutes; after a start or a stop the box is exactly what has changed, and
// an admin panel reading a stale one reports the previous box's instance type as the current
// one (ADR 0074).
func (t *engineECS) invalidateBox() {
	t.mu.Lock()
	t.cachBoxAt = time.Time{}
	t.mu.Unlock()
}

func (t *engineECS) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// describe is the uncached call.
func (t *engineECS) describe(ctx context.Context) (engineServiceView, error) {
	out, err := t.api.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(t.cluster),
		Services: []string{t.service},
	})
	if err != nil {
		return engineServiceView{}, err
	}
	for _, s := range out.Services {
		v := engineServiceView{desired: s.DesiredCount, running: s.RunningCount}
		// The FIRST entry of the strategy, not a weighted reading of all of them: this CP writes
		// exactly one provider at weight 1 (setStrategy) and 60-engines declares one, so a second
		// entry would be somebody else's edit and reporting the first is then the honest "this is
		// what the service says" rather than a computed guess.
		for _, cp := range s.CapacityProviderStrategy {
			if name := strings.TrimSpace(aws.ToString(cp.CapacityProvider)); name != "" {
				v.provider = name
				break
			}
		}
		if aws.ToString(s.Status) == "INACTIVE" {
			v.state = "none"
			v.desired = 0
			return v, nil
		}
		switch {
		case s.DesiredCount >= 1 && s.RunningCount >= 1:
			v.state = "running"
		case s.DesiredCount >= 1:
			v.state = "starting"
		default:
			v.state = "stopped"
			// `draining` is stopped-but-still-billing. ECS reports the service as stopped the
			// moment the task goes; Managed Instances then keeps the EC2 instance for minutes
			// more. Two things need it: an idle window shortened below the drain buys nothing
			// (the money is already spent), and a start landing on a box that is still there is
			// warm — the image layers and the model file are on its disk, so it skips the S3
			// fetch entirely (ADR 0071 decision 7).
			if t.draining(ctx) {
				v.state = "draining"
			}
		}
		// When the current transition began, read back from ECS rather than remembered: a CP
		// replaced mid-start has to judge the start deadline from the same clock as its
		// predecessor (ADR 0070 decision 6).
		//
		// It is the primary deployment's `updatedAt`, NOT its `createdAt`. Measured on a real
		// service: `createdAt` is when the *deployment* was created — the stack's, hours or
		// days ago — and it does not move when the desired count goes 0 → 1. Judging the
		// start deadline by it means every start on a service older than the deadline is
		// declared failed the instant it begins, and the engine can never come up at all.
		// `updatedAt` moves with the scale-up (and with a task ECS replaces underneath).
		for _, d := range s.Deployments {
			if aws.ToString(d.Status) != "PRIMARY" {
				continue
			}
			// The deployment's id, which is how "ECS has taken the strategy" is told from "the
			// call returned": forcing a deployment REPLACES the PRIMARY one, id and all
			// (measured, ADR 0075 live test 0: `ecs-svc/0995…` → `ecs-svc/5816…`). The start
			// splits on this — see setStrategy.
			v.deployment = aws.ToString(d.Id)
			v.rollout = string(d.RolloutState)
			switch {
			case d.UpdatedAt != nil:
				v.lastStart = *d.UpdatedAt
			case d.CreatedAt != nil:
				v.lastStart = *d.CreatedAt
			}
		}
		for i, e := range s.Events {
			if i >= engineServiceEventsKept {
				break
			}
			if m := strings.TrimSpace(aws.ToString(e.Message)); m != "" {
				v.events = append(v.events, m)
			}
		}
		return v, nil
	}
	return engineServiceView{state: "none"}, fmt.Errorf("ecs service %s not found in cluster %s", t.service, t.cluster)
}

// engineServiceEventsKept bounds how much of the event list is carried around; ECS returns
// the newest first and only the last few say anything about the start that just failed.
const engineServiceEventsKept = 3

// draining reports whether this engine's capacity provider still has a container instance
// registered. Only asked when the service is at desired 0 and only for an engine that
// declares a provider, so a Fargate engine and a running engine both cost nothing here.
//
// A failure answers false. The alternative — treating an unreadable cluster as "draining" —
// would keep an engine in a state the controller reads as "do not start yet", i.e. an
// AccessDenied would silently turn the whole feature off.
func (t *engineECS) draining(ctx context.Context) bool {
	_, ok := t.box(ctx)
	return ok
}

// box is the cached container-instance lookup. Two callers with two reasons: `draining`
// (stopped-but-still-billing, ADR 0071 decision 7) and the admin panel, which wants the
// registeredAt this is the only source of.
//
// Not found and not readable both answer false, and the difference is deliberately dropped:
// the only caller that acts on it is the controller, and it must read an unreadable cluster
// as "not draining" rather than as "do not start yet" — see draining above.
func (t *engineECS) box(ctx context.Context) (engineBox, bool) {
	return engineFirstBox(t.boxes(ctx), "")
}

// boxOn is the same lookup narrowed to ONE of the role's capacity providers. Rule 2 needs it:
// "has this offer produced a box" is a question about the provider the offer buys from, and a
// start that changed provider can have two boxes registered at once — the previous offer's on its
// way out and this one's coming up. Asking `box()` would answer with whichever ECS listed first
// (measured on the deployment: exactly that pair, 17 seconds apart).
func (t *engineECS) boxOn(ctx context.Context, provider string) (engineBox, bool) {
	if strings.TrimSpace(provider) == "" {
		return engineBox{}, false
	}
	return engineFirstBox(t.boxes(ctx), provider)
}

// engineFirstBox picks one box out of the ones registered on this engine's providers: the first
// ACTIVE one, and only then a draining one. Preferring ACTIVE is what keeps the panel — and rule
// 2 — describing the box that is coming up rather than the one going away.
func engineFirstBox(list []engineBox, provider string) (engineBox, bool) {
	var fallback engineBox
	found := false
	for _, b := range list {
		if provider != "" && b.provider != provider {
			continue
		}
		if b.status == "ACTIVE" {
			return b, true
		}
		if !found {
			fallback, found = b, true
		}
	}
	return fallback, found
}

// boxes is the cached container-instance lookup: every instance registered on either of this
// engine's capacity providers.
func (t *engineECS) boxes(ctx context.Context) []engineBox {
	if od, spot := t.providers(); od == "" && spot == "" {
		return nil // Fargate: nothing to look up, and no call to pay for
	}
	now := t.clock()
	t.mu.Lock()
	if !t.cachBoxAt.IsZero() && now.Sub(t.cachBoxAt) < engineBoxTTL {
		b := t.cachBoxes
		t.mu.Unlock()
		return b
	}
	t.mu.Unlock()

	b := t.describeBoxes(ctx)
	t.mu.Lock()
	t.cachBoxes, t.cachBoxAt = b, now
	t.mu.Unlock()
	return b
}

// describeBoxes is the uncached walk of the cluster's container instances.
func (t *engineECS) describeBoxes(ctx context.Context) []engineBox {
	// Read once, up front: the names can be replaced under this walk by the table reloader,
	// and matching half the pages against one name and half against another would answer
	// "no box" on the run that happens to straddle a rename.
	wantOD, wantSpot := t.providers()
	var arns []string
	var next *string
	for {
		out, err := t.api.ListContainerInstances(ctx, &ecs.ListContainerInstancesInput{
			Cluster: aws.String(t.cluster), NextToken: next,
		})
		if err != nil {
			log.Printf("%s: listing container instances failed: %v", t.logKey(), err)
			return nil
		}
		arns = append(arns, out.ContainerInstanceArns...)
		if next = out.NextToken; next == nil {
			break
		}
	}
	var out []engineBox
	for len(arns) > 0 {
		n := min(len(arns), 100)
		page, err := t.api.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
			Cluster: aws.String(t.cluster), ContainerInstances: arns[:n],
		})
		if err != nil {
			log.Printf("%s: describing container instances failed: %v", t.logKey(), err)
			// What was found so far is dropped with the error, exactly as the single-box walk
			// dropped it: a partial answer would read as "no box on the other provider", which is
			// the claim this must never make cheaply.
			return nil
		}
		for _, ci := range page.ContainerInstances {
			// The capacity provider is what tells this engine's box apart from a workspace
			// slot on the same cluster. Matching on anything looser would report the pool's
			// m8g as the GPU that is costing $1.26 an hour.
			//
			// EITHER name counts (ADR 0075 decision 3). The two providers are one role's two
			// wallets, so a box bought on the Spot one is this engine's box in every sense that
			// matters here — it is what `draining` is about to bill for, and it is what the start
			// gate compares an instance type against.
			cp := aws.ToString(ci.CapacityProviderName)
			if !engineProviderMatches(cp, wantOD, wantSpot) {
				continue
			}
			b := engineBox{
				instanceID: aws.ToString(ci.Ec2InstanceId),
				arn:        aws.ToString(ci.ContainerInstanceArn),
				status:     aws.ToString(ci.Status),
				provider:   cp,
			}
			if ci.RegisteredAt != nil {
				b.since = *ci.RegisteredAt
			}
			for _, at := range ci.Attributes {
				if aws.ToString(at.Name) == engineBoxTypeAttr {
					b.instanceType = strings.TrimSpace(aws.ToString(at.Value))
					break
				}
			}
			out = append(out, b)
		}
		arns = arns[n:]
	}
	return out
}

func (t *engineECS) logKey() string {
	if t == nil || t.key == "" {
		return "engine"
	}
	return "engine " + t.key
}

// newTTSEngine is how the TTS routes obtain their engine adapter. It is a variable so a test
// can install one backed by a fake ECS API: everything on-demand hangs off "is there a
// service to start", and that question is unanswerable in a test otherwise. Production never
// assigns it.
var newTTSEngine = newTTSEngineFromEnv

// newTTSEngineFromEnv returns a controller only when AF_TTS_ECS_SERVICE is set. Unset
// means the engine is not managed here (a long-running dev docker, say) and its lifecycle
// belongs to someone else. Cluster and region fall back to the workspace's AF_ECS_* when
// the dedicated AF_TTS_ECS_* are absent.
func newTTSEngineFromEnv() *engineECS {
	service := firstEnv("AF_TTS_ECS_SERVICE")
	if service == "" {
		return nil
	}
	region := firstEnv("AF_TTS_ECS_REGION", "AF_ECS_REGION", "AWS_REGION", "AWS_DEFAULT_REGION")
	ac, err := awscfg.LoadDefaultConfig(context.Background(), awscfg.WithRegion(region))
	if err != nil {
		log.Printf("tts: ecs engine control disabled (aws config: %v)", err)
		return nil
	}
	cluster := firstEnv("AF_TTS_ECS_CLUSTER", "AF_ECS_CLUSTER")
	log.Printf("tts: voicevox engine managed via ecs (cluster=%s service=%s)", cluster, service)
	return &engineECS{api: ecs.NewFromConfig(ac), key: "tts", cluster: cluster, service: service}
}

// setEnabled flips the desired count between 0 and 1. The cold start (70-77 s measured on
// a real deployment, image pull included) is absorbed by the readiness gate
// (voicevoxProvider.Ready, /api/tts/status), and auto routing sends Japanese synthesis to
// Polly JP meanwhile.
func (t *engineECS) setEnabled(ctx context.Context, on bool) error {
	desired := int32(0)
	if on {
		desired = 1
	}
	_, err := t.api.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:      aws.String(t.cluster),
		Service:      aws.String(t.service),
		DesiredCount: aws.Int32(desired),
	})
	t.invalidate()
	// The box too: a stop is the beginning of one going away and a start may buy a different
	// one, so the cached container instance is the reading most likely to be wrong from here.
	t.invalidateBox()
	return err
}

// setStrategy is THE ONLY PLACE the Control Plane writes a service's capacityProviderStrategy,
// and the whole safety of ADR 0075 is the guard three lines into it (decisions 4 and 12).
//
// ADR 0074 refused to let the CP touch a service's strategy at all, because re-applying a choice
// idempotently in front of every start would kill whatever the engine was generating. That
// reason holds for exactly as long as there is a task to kill, so the rule is not "be careful"
// — it is RUNNING == 0, checked here, on a reading taken now.
//
// 🔴 CHANGING A STRATEGY REQUIRES `forceNewDeployment`, EVEN AT DESIRED 0. Measured on a throwaway
// Managed Instances provider (ADR 0075 live test 0, $0): without the flag ECS answers HTTP 400
// `InvalidParameterException` — "…on a service that is already using one, you must force a new
// deployment" — in both directions, and it says nothing about the desired count. With the flag the
// call is accepted, a new PRIMARY deployment appears, and its `updatedAt` always moves. So:
//
//   - the provider is DIFFERENT from what the service names: send the strategy AND
//     `forceNewDeployment: true`, whether this is the 0 → 1 start or a move to the next offer.
//     There is nothing running to kill — that is what the guard above is for, and it is now the
//     only thing standing between this call and a killed generation;
//   - the provider is THE SAME: do not send a strategy at all. Sending one that changes nothing
//     is how a start would pay for a refused call, and it is also the ordinary shape of the first
//     offer after a release, where CloudFormation has already pointed the service at the
//     on-demand provider. A move that stays on the same provider still forces a deployment: the
//     capacity provider's instance requirements changed underneath (two offers can share one
//     provider), and the PENDING task has to be placed again for that to be asked for.
//
// 🔴 A START THAT ALSO CHANGES THE STRATEGY IS TWO CALLS, NOT ONE. Measured on the deployment
// (ADR 0075 live run 3, twice): handing `desiredCount: 1` and a new strategy to ONE UpdateService
// makes ECS place the task against the OLD strategy first, and that provider BUYS A BOX. The
// forced deployment then places the task on the new provider, but the first box stays — 16
// minutes 28 seconds of billing ($0.51), and while it deregisters the ADR 0074 swap wait holds
// the NEXT start for six minutes. So the strategy goes first, on its own, and the desired count
// follows only once ECS has replaced the PRIMARY deployment.
//
// Because every accepted write creates a new PRIMARY deployment, `StartDeadlineSec` and the
// per-offer budget both re-clock themselves off `updatedAt` — the CP needs no timer of its own
// (ADR 0075 open question 1 (c), measured).
//
// 🔴 The reading is UNCACHED on purpose. view()'s three seconds are the right price for a panel
// and the wrong one for the only check standing between this function and a killed generation.
func (t *engineECS) setStrategy(ctx context.Context, provider string, start bool) error {
	if strings.TrimSpace(provider) == "" {
		return fmt.Errorf("%s: no capacity provider to start on", t.logKey())
	}
	v, err := t.describe(ctx)
	if err != nil {
		return fmt.Errorf("%s: reading the service before writing its capacity provider: %w", t.logKey(), err)
	}
	if v.running >= 1 {
		// Not a retry and not a warning: the caller asked for something this ADR forbids, and the
		// answer is the refusal. The offer a running engine is on stays until it stops.
		return fmt.Errorf("%s: refusing to write the capacity provider strategy while %d task(s) are running",
			t.logKey(), v.running)
	}
	// "Is this a change" is answered by the service's own strategy, read back a line ago — never
	// by what this process remembers writing. CloudFormation rewrites it on every release that
	// touches the service (ADR 0075 decision 12).
	if v.provider != provider {
		if err := t.writeStrategyOnly(ctx, provider, v.deployment); err != nil {
			return err
		}
		if !start {
			return nil // the move is done: the forced deployment places the PENDING task again
		}
	} else if !start {
		// Same provider, and this is still a move to another offer: the capacity provider's
		// instance requirements changed underneath (two offers can share one provider), so the
		// PENDING task has to be placed again for the new ones to be asked for.
		_, err := t.api.UpdateService(ctx, &ecs.UpdateServiceInput{
			Cluster: aws.String(t.cluster), Service: aws.String(t.service), ForceNewDeployment: true,
		})
		t.invalidate()
		t.invalidateBox()
		return err
	}
	if v.desired >= 1 {
		// Already asked for. Two start paths can reach here within a second of each other — the
		// admin toggle and the controller's next tick — and a second `desiredCount: 1` is a write
		// that buys nothing and makes the panel show the offer being taken twice.
		return nil
	}
	_, err = t.api.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(t.cluster), Service: aws.String(t.service), DesiredCount: aws.Int32(1),
	})
	t.invalidate()
	t.invalidateBox()
	return err
}

// engineDeploymentSettle bounds the wait for ECS to replace the PRIMARY deployment after a forced
// strategy write. Measured (live test 0): the new deployment exists in the SAME response — its
// `createdAt` equals the update's timestamp — so the first read normally answers, and the wait is
// here only so that a slow answer does not turn into the two-box start above. Variables, so a
// test can take the sleep out.
var (
	engineDeploymentSettleTries = 4
	engineDeploymentSettleWait  = 500 * time.Millisecond
)

// errEngineStrategySettling says the strategy was written and ECS has not shown the new
// deployment yet. NOT a failed start: the strategy is now what this engine wants, so the next
// attempt takes the short path and only moves the desired count. Counting it as a failure would
// double a cooldown over a call that did exactly what it was asked to.
var errEngineStrategySettling = errors.New("the capacity provider strategy was written; waiting for the new deployment")

// writeStrategyOnly is the first half: the strategy, forced, with the desired count untouched.
func (t *engineECS) writeStrategyOnly(ctx context.Context, provider, wasDeployment string) error {
	_, err := t.api.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(t.cluster),
		Service: aws.String(t.service),
		CapacityProviderStrategy: []ecstypes.CapacityProviderStrategyItem{
			{CapacityProvider: aws.String(provider), Weight: 1},
		},
		// Required, not optional: without it ECS answers 400 on a service that already uses a
		// strategy (measured, both directions, at desired 0).
		ForceNewDeployment: true,
	})
	t.invalidate()
	t.invalidateBox()
	if err != nil {
		return err
	}
	for i := 0; i < engineDeploymentSettleTries; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return errEngineStrategySettling
			case <-time.After(engineDeploymentSettleWait):
			}
		}
		v, err := t.describe(ctx)
		if err == nil && v.deployment != "" && v.deployment != wasDeployment {
			return nil
		}
	}
	log.Printf("%s: the strategy was written but ECS still reports the old deployment; leaving the desired count alone",
		t.logKey())
	return errEngineStrategySettling
}
