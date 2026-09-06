// tts_ecs.go — on-demand ECS control of the VOICEVOX engine (docs/log/24).
//
// On AWS the engine is an ECS service whose desired count the admin toggle flips between
// 0 and 1, so a stopped engine costs nothing. Addressing is a fixed Cloud Map DNS name
// (e.g. voicevox.af.local) put into AF_VOICEVOX_URL, which leaves CP's synthesis handler
// untouched. The service, task definition and Cloud Map entry are owned by IaC
// (deploy/aws); CP only calls DescribeServices and UpdateService, the two permissions the
// CP role needs. A small controller independent of the workspace ECS adapter
// (runtime_ecs.go).
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

// ttsEngineAPI is the narrow ECS port (two methods), so tests can pass a fake. The real
// *ecs.Client satisfies it.
type ttsEngineAPI interface {
	DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	UpdateService(context.Context, *ecs.UpdateServiceInput, ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
}

type ttsEngineECS struct {
	api     ttsEngineAPI
	cluster string
	service string

	mu     sync.Mutex
	cached ttsServiceView
	cachAt time.Time
	cachEr error
	now    func() time.Time // test seam
}

// ttsServiceView is one DescribeServices answer, reduced to what the readiness gate and
// the on-demand controller look at.
type ttsServiceView struct {
	state     string    // running | starting | stopped | none
	desired   int32     // the service's desired count
	running   int32     // tasks actually running
	lastStart time.Time // when the primary deployment was created
	// events holds the newest service events, which is the only place ECS writes down why
	// a start failed ("no container instances met the placement constraints", a pull
	// failure). Reading them needs no permission the CP role does not already have.
	events []string
}

// ttsViewTTL is the short cache in front of DescribeServices (ADR 0070 decision 10).
// /api/tts/status called it once per request, which is harmless while only the admin panel
// polls and is not once every client polls to render "starting". Same shape and roughly
// the same length as the readiness cache next to it (vvReadyTTL).
const ttsViewTTL = 3 * time.Second

// view is the cached DescribeServices. An error is cached for the same TTL: a service that
// answers with an error answers with it for every caller in that window, and hammering the
// API is how one misconfiguration becomes a throttle.
func (t *ttsEngineECS) view(ctx context.Context) (ttsServiceView, error) {
	now := t.clock()
	t.mu.Lock()
	if !t.cachAt.IsZero() && now.Sub(t.cachAt) < ttsViewTTL {
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
func (t *ttsEngineECS) invalidate() {
	t.mu.Lock()
	t.cachAt = time.Time{}
	t.mu.Unlock()
}

func (t *ttsEngineECS) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// describe is the uncached call.
func (t *ttsEngineECS) describe(ctx context.Context) (ttsServiceView, error) {
	out, err := t.api.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(t.cluster),
		Services: []string{t.service},
	})
	if err != nil {
		return ttsServiceView{}, err
	}
	for _, s := range out.Services {
		v := ttsServiceView{desired: s.DesiredCount, running: s.RunningCount}
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
		}
		// The primary deployment's creation time is when this start began, read back from
		// ECS rather than remembered: a CP replaced mid-start has to judge the start
		// deadline from the same clock as its predecessor (ADR 0070 decision 6).
		for _, d := range s.Deployments {
			if aws.ToString(d.Status) == "PRIMARY" && d.CreatedAt != nil {
				v.lastStart = *d.CreatedAt
			}
		}
		for i, e := range s.Events {
			if i >= ttsServiceEventsKept {
				break
			}
			if m := strings.TrimSpace(aws.ToString(e.Message)); m != "" {
				v.events = append(v.events, m)
			}
		}
		return v, nil
	}
	return ttsServiceView{state: "none"}, fmt.Errorf("ecs service %s not found in cluster %s", t.service, t.cluster)
}

// ttsServiceEventsKept bounds how much of the event list is carried around; ECS returns
// the newest first and only the last few say anything about the start that just failed.
const ttsServiceEventsKept = 3

// newTTSEngine is how the routes obtain the engine adapter. It is a variable so a test can
// install one backed by a fake ECS API: everything on-demand hangs off "is there a service
// to start", and that question is unanswerable in a test otherwise. Production never
// assigns it.
var newTTSEngine = newTTSEngineFromEnv

// newTTSEngineFromEnv returns a controller only when AF_TTS_ECS_SERVICE is set. Unset
// means the engine is not managed here (a long-running dev docker, say) and its lifecycle
// belongs to someone else. Cluster and region fall back to the workspace's AF_ECS_* when
// the dedicated AF_TTS_ECS_* are absent.
func newTTSEngineFromEnv() *ttsEngineECS {
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
	return &ttsEngineECS{api: ecs.NewFromConfig(ac), cluster: cluster, service: service}
}

// setEnabled flips the desired count between 0 and 1. The cold start (70-77 s measured on
// a real deployment, image pull included) is absorbed by the readiness gate
// (voicevoxProvider.Ready, /api/tts/status), and auto routing sends Japanese synthesis to
// Polly JP meanwhile.
func (t *ttsEngineECS) setEnabled(ctx context.Context, on bool) error {
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
