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
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
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
	// capacityProvider is set only for a Managed Instances engine. It is what makes
	// `draining` observable: with desired 0 the service says "stopped" the moment the task
	// goes, while the EC2 instance behind it lives on for several more minutes (measured:
	// 427 and 463 seconds on a GPU box, 93 on a CPU one) and bills the whole time. Empty =
	// Fargate, where there is no such state.
	capacityProvider string

	mu        sync.Mutex
	cached    engineServiceView
	cachAt    time.Time
	cachEr    error
	cachBox   engineBox
	cachBoxOn bool
	cachBoxAt time.Time
	now       func() time.Time // test seam
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
}

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
	if t.capacityProvider == "" {
		return engineBox{}, false // Fargate: nothing to look up, and no call to pay for
	}
	now := t.clock()
	t.mu.Lock()
	if !t.cachBoxAt.IsZero() && now.Sub(t.cachBoxAt) < engineBoxTTL {
		b, ok := t.cachBox, t.cachBoxOn
		t.mu.Unlock()
		return b, ok
	}
	t.mu.Unlock()

	b, ok := t.describeBox(ctx)
	t.mu.Lock()
	t.cachBox, t.cachBoxOn, t.cachBoxAt = b, ok, now
	t.mu.Unlock()
	return b, ok
}

// describeBox is the uncached walk of the cluster's container instances.
func (t *engineECS) describeBox(ctx context.Context) (engineBox, bool) {
	var arns []string
	var next *string
	for {
		out, err := t.api.ListContainerInstances(ctx, &ecs.ListContainerInstancesInput{
			Cluster: aws.String(t.cluster), NextToken: next,
		})
		if err != nil {
			log.Printf("%s: listing container instances failed: %v", t.logKey(), err)
			return engineBox{}, false
		}
		arns = append(arns, out.ContainerInstanceArns...)
		if next = out.NextToken; next == nil {
			break
		}
	}
	for len(arns) > 0 {
		n := min(len(arns), 100)
		out, err := t.api.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
			Cluster: aws.String(t.cluster), ContainerInstances: arns[:n],
		})
		if err != nil {
			log.Printf("%s: describing container instances failed: %v", t.logKey(), err)
			return engineBox{}, false
		}
		for _, ci := range out.ContainerInstances {
			// The capacity provider is what tells this engine's box apart from a workspace
			// slot on the same cluster. Matching on anything looser would report the pool's
			// m8g as the GPU that is costing $1.26 an hour.
			if aws.ToString(ci.CapacityProviderName) != t.capacityProvider {
				continue
			}
			b := engineBox{
				instanceID: aws.ToString(ci.Ec2InstanceId),
				arn:        aws.ToString(ci.ContainerInstanceArn),
				status:     aws.ToString(ci.Status),
			}
			if ci.RegisteredAt != nil {
				b.since = *ci.RegisteredAt
			}
			return b, true
		}
		arns = arns[n:]
	}
	return engineBox{}, false
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
	return err
}
