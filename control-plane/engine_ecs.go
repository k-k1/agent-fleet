// engine_ecs.go — the ECS side of an on-demand engine (ADR 0070 for VOICEVOX, ADR 0071 for
// the inference engines, which is what made this general).
//
// An engine is an ECS service whose desired count moves between 0 and 1, so a stopped
// engine costs nothing. Addressing is a fixed Cloud Map DNS name, which leaves every caller
// (the synthesis handler, the /engine/* gateway) pointing at one URL. The service, task
// definition and Cloud Map entry are owned by IaC (deploy/aws); CP only calls
// DescribeServices and UpdateService, plus — for an engine that runs on a box of its own
// (ADR 0077) — the container-instance calls. A small adapter independent of the workspace ECS
// adapter (runtime_ecs.go).
package main

import (
	"context"
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
// satisfies it. The three container-instance calls are only ever made for an engine that runs on
// a box of its own, i.e. one whose table row declares a launch template; a Fargate engine leaves
// them unused.
type engineECSAPI interface {
	DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	UpdateService(context.Context, *ecs.UpdateServiceInput, ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
	ListContainerInstances(context.Context, *ecs.ListContainerInstancesInput, ...func(*ecs.Options)) (*ecs.ListContainerInstancesOutput, error)
	DescribeContainerInstances(context.Context, *ecs.DescribeContainerInstancesInput, ...func(*ecs.Options)) (*ecs.DescribeContainerInstancesOutput, error)
	// DeregisterContainerInstance is the first half of a departure (ADR 0077 decision 5). The
	// CP is the one that terminates the box now, so it is also the one that has to take it out
	// of the cluster: a terminated instance stays registered as ACTIVE with agentConnected
	// false, and a ghost that looks ACTIVE still satisfies placement constraints (ADR 0045
	// decision 3-2, measured again on this deployment).
	DeregisterContainerInstance(context.Context, *ecs.DeregisterContainerInstanceInput, ...func(*ecs.Options)) (*ecs.DeregisterContainerInstanceOutput, error)
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
	// roleAttr is the `af-role` value this engine's boxes carry — "engine-image" — and it is
	// what tells one of them from a workspace slot, from the other role's GPU and from a
	// Managed Instances box left over from ADR 0071. Empty = this engine runs on Fargate (or on
	// a table row written before ADR 0077), and then no container instance is ever this
	// engine's and no call is made to find out.
	//
	// 🔴 Under the mutex, and read through role(): a launch template can be replaced by a
	// CloudFormation update and the table the CP re-reads carries the new one, but the ROLE
	// never changes under a running process — it is derived from the row's key. It is here
	// rather than computed at each call site so that "whose box is this" has exactly one
	// answer.
	roleAttr string
	// boxLive answers `draining` — "is there still an instance behind this stopped service".
	// It is a function rather than a field because the fact is EC2's now (ADR 0077 decision 5:
	// draining runs from the CP's terminate until EC2 reports `terminated`), and this type has
	// no EC2 client. nil = nothing to ask, which reads as "not draining" — the safe direction,
	// since the other one would leave the controller refusing to start for ever.
	boxLive   func(context.Context) bool
	cachBoxes []engineBox
	cachBoxAt time.Time
	now       func() time.Time // test seam
}

// role is the `af-role` attribute value this engine's boxes carry, "" for Fargate.
func (t *engineECS) role() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.roleAttr
}

// engineBox is the EC2 instance this engine is running on, as ECS sees it.
//
// It exists because the service's own timestamps answer a different question. `lastStart` is
// when the primary deployment last changed state — a fact about the service — and after a
// task is replaced underneath, or a stack update, it moves without a new box being bought.
// What an operator looking at a $1.26/hour GPU wants is when THE BOX started, and the only
// place that is written down is the container instance's registeredAt.
//
// The EC2 side of the same box (which offer bought it, whether it was Spot) is `engineFleetBox`:
// a box the CP bought with EC2 Fleet IS enumerable by `describe-instances`, which a Managed
// Instances box was not (ADR 0071 P1 measurement 2, the limit ADR 0077 removes).
type engineBox struct {
	instanceID string    // i-08a9… — the id `describe-instances --instance-ids` will accept
	arn        string    // the container instance ARN
	status     string    // ACTIVE while it can take tasks, DRAINING once it is going away
	since      time.Time // registeredAt: when the box joined the cluster
	// connected is the agent's own connection. A box is not "registered" until ECS will place
	// on it, and that is ACTIVE **and** agentConnected — the slot pool's waitSlotRegistered
	// waits for exactly this pair, and the desired count must not go to 1 a moment early.
	connected bool
	// tasks is running + pending tasks on the box. Departure (decision 5) waits for it to
	// reach 0 before the box is taken out of the cluster.
	tasks int
	// instanceType is the box's EC2 type, read from the container instance's own
	// `ecs.instance-type` attribute (ADR 0074). It is what a start's swap wait compares against
	// the offer it is about to buy. Empty when ECS did not report the attribute, and empty must
	// never be read as "it matches" — see startGate.
	instanceType string
}

// registered reports whether ECS will place this engine's task on the box.
func (b engineBox) registered() bool { return b.status == "ACTIVE" && b.connected }

// engineBoxTypeAttr is the container-instance attribute holding the EC2 instance type. ECS
// registers it on every instance.
const engineBoxTypeAttr = "ecs.instance-type"

// engineBoxRoleAttr is the container-instance attribute that says which role's box this is
// (ADR 0077 decision 3). The launch template's user data writes it into `/etc/ecs/ecs.config`
// as `ECS_INSTANCE_ATTRIBUTES={"af-role":"engine-<role>"}`, where a slot's user data writes
// `ECS_CLUSTER`.
//
// 🔴 The same word as the EC2 tag (`runtime.EC2TagRole`), on purpose. The service's placement
// constraint names this attribute, the slot pool's `isPoolContainerInstance` reads it to know
// the box is not a slot, and the CP's own sweep reads the tag — one vocabulary, three readers.
const engineBoxRoleAttr = "af-role"

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
	//
	// ⚠️ They are EVIDENCE FOR A PERSON now, not an input to a decision. ADR 0075 had to read
	// the capacity verdict out of them because ECS was the buyer; under ADR 0077 the verdict is
	// `CreateFleet`'s own response, and one misread event can no longer throw away an offer list
	// that had a working row left in it (ADR 0075 re-run 4).
	events []engineServiceEvent
}

// engineServiceEvent is one line ECS wrote about this service, with the moment it wrote it.
type engineServiceEvent struct {
	at      time.Time
	message string
}

// eventMessages is the events as text, for the places that only display or log them.
func (v engineServiceView) eventMessages() []string {
	out := make([]string, 0, len(v.events))
	for _, e := range v.events {
		out = append(out, e.message)
	}
	return out
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
			// moment the task goes, while the instance behind it lives on — under ADR 0071 that
			// was Managed Instances draining it out of our hands, and under ADR 0077 decision 5
			// it is the window between the CP issuing the terminate and EC2 reporting
			// `terminated`. Two things need it: an idle window shortened below the drain buys
			// nothing (the money is already spent), and a start landing on a box that is still
			// there is warm — the image layers and the model file are on its disk (ADR 0071
			// decision 7).
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
				ev := engineServiceEvent{message: m}
				if e.CreatedAt != nil {
					ev.at = *e.CreatedAt
				}
				v.events = append(v.events, ev)
			}
		}
		return v, nil
	}
	return engineServiceView{state: "none"}, fmt.Errorf("ecs service %s not found in cluster %s", t.service, t.cluster)
}

// engineServiceEventsKept bounds how much of the event list is carried around; ECS returns
// the newest first and only the last few say anything about the start that just failed.
const engineServiceEventsKept = 3

// draining reports whether a box of this engine's is still in existence behind a stopped
// service. Only asked when the service is at desired 0 and only for an engine that has boxes at
// all, so a Fargate engine and a running engine both cost nothing here.
//
// 🔴 It asks EC2, not ECS (ADR 0077 decision 5). Departure now DEREGISTERS the container instance
// before terminating the instance, so between those two steps ECS knows nothing about a box that
// is still billing — and `draining` is precisely that window.
//
// A failure answers false. The alternative — treating an unreadable answer as "draining" — would
// keep an engine in a state the controller reads as "do not start yet", i.e. one AccessDenied
// would silently turn the whole feature off.
func (t *engineECS) draining(ctx context.Context) bool {
	t.mu.Lock()
	live := t.boxLive
	t.mu.Unlock()
	return live != nil && live(ctx)
}

// box is the cached container-instance lookup: what the CLUSTER knows about this engine's box,
// which is the registeredAt, the status and the instance type. The admin panel and the start
// gate are its callers.
func (t *engineECS) box(ctx context.Context) (engineBox, bool) {
	return engineFirstBox(t.boxes(ctx))
}

// boxFor is the same lookup narrowed to one EC2 instance id: "has the box we just bought joined
// the cluster yet" (ADR 0077 decision 2). It is the one question the desired count waits on.
func (t *engineECS) boxFor(ctx context.Context, instanceID string) (engineBox, bool) {
	if strings.TrimSpace(instanceID) == "" {
		return engineBox{}, false
	}
	for _, b := range t.boxes(ctx) {
		if b.instanceID == instanceID {
			return b, true
		}
	}
	return engineBox{}, false
}

// engineFirstBox picks one box out of this engine's: the first ACTIVE one, and only then a
// draining one. Preferring ACTIVE is what keeps the panel describing the box that is coming up
// rather than the one going away.
func engineFirstBox(list []engineBox) (engineBox, bool) {
	var fallback engineBox
	found := false
	for _, b := range list {
		if b.status == "ACTIVE" {
			return b, true
		}
		if !found {
			fallback, found = b, true
		}
	}
	return fallback, found
}

// deregisterBox takes one box out of the cluster, by ARN, forced (ADR 0077 decision 5).
//
// 🔴 It deliberately does NOT go through the slot pool's deregisterSlot: that applies the pool
// test, which an engine box fails by construction (decision 3), so it would refuse exactly the
// box this is for. Force, because the point of the call is that nothing may be placed here again
// — by the time it is made the box is about to stop existing.
func (t *engineECS) deregisterBox(ctx context.Context, arn string) error {
	if strings.TrimSpace(arn) == "" {
		return nil
	}
	_, err := t.api.DeregisterContainerInstance(ctx, &ecs.DeregisterContainerInstanceInput{
		Cluster: aws.String(t.cluster), ContainerInstance: aws.String(arn), Force: aws.Bool(true),
	})
	t.invalidateBox()
	return err
}

// boxes is the cached container-instance lookup: every instance in the cluster carrying this
// role's `af-role` attribute.
func (t *engineECS) boxes(ctx context.Context) []engineBox {
	if t.role() == "" {
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
	want := t.role()
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
			// 🔴 The `af-role` ATTRIBUTE is what tells this engine's box apart from a workspace
			// slot on the same cluster, from the other role's GPU, and from a Managed Instances
			// box left over from ADR 0071 (which carries a capacity provider name and no
			// attribute at all). Matching on anything looser would report the pool's m8g as the
			// card that is costing $1.26 an hour — the shape of the lie ADR 0074 decision 4 is
			// about.
			if engineInstanceAttr(ci, engineBoxRoleAttr) != want {
				continue
			}
			b := engineBox{
				instanceID: aws.ToString(ci.Ec2InstanceId),
				arn:        aws.ToString(ci.ContainerInstanceArn),
				status:     aws.ToString(ci.Status),
				connected:  ci.AgentConnected,
				tasks:      int(ci.RunningTasksCount + ci.PendingTasksCount),
			}
			if ci.RegisteredAt != nil {
				b.since = *ci.RegisteredAt
			}
			b.instanceType = engineInstanceAttr(ci, engineBoxTypeAttr)
			out = append(out, b)
		}
		arns = arns[n:]
	}
	return out
}

// engineInstanceAttr reads one container-instance attribute, "" when it is not there. An absent
// attribute is a real answer — a slot has no `af-role` — and it must never be read as a match.
func engineInstanceAttr(ci ecstypes.ContainerInstance, name string) string {
	for _, at := range ci.Attributes {
		if aws.ToString(at.Name) == name {
			return strings.TrimSpace(aws.ToString(at.Value))
		}
	}
	return ""
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

// 🔴 There is NO strategy write here any more, and that is ADR 0077 decision 2. The service
// declares `LaunchType: EC2` and nothing else, the CP buys the box itself (engine_fleet.go), and
// the desired count above is the whole of what it says to ECS. Everything ADR 0075 had to build
// around `UpdateService` carrying a `capacityProviderStrategy` — the forced deployment, the
// running==0 guard on it, the wait for the deployment it replaced, the two-call split — went
// with it. What protected a generation in flight is now structural: there is nothing to write
// that could replace a running task.
