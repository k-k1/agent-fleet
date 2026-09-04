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
}

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

// state maps the engine service onto running | starting | stopped | none, the same
// vocabulary as runtime_ecs.go's State. The desired count comes back too and is the
// toggle's current value while the engine is managed here: ECS is the source of truth,
// not the stored setting.
func (t *ttsEngineECS) state(ctx context.Context) (string, int32, error) {
	out, err := t.api.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(t.cluster),
		Services: []string{t.service},
	})
	if err != nil {
		return "", 0, err
	}
	for _, s := range out.Services {
		if aws.ToString(s.Status) == "INACTIVE" {
			return "none", 0, nil
		}
		switch {
		case s.DesiredCount >= 1 && s.RunningCount >= 1:
			return "running", s.DesiredCount, nil
		case s.DesiredCount >= 1:
			return "starting", s.DesiredCount, nil
		default:
			return "stopped", s.DesiredCount, nil
		}
	}
	return "none", 0, fmt.Errorf("ecs service %s not found in cluster %s", t.service, t.cluster)
}

// setEnabled flips the desired count between 0 and 1. The cold start (1-2 minutes,
// image pull included) is absorbed by the readiness gate (voicevoxProvider.Ready,
// /api/tts/status), and auto routing sends Japanese synthesis to Polly JP meanwhile.
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
	return err
}
