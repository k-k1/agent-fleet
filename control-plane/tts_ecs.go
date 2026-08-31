// tts_ecs.go — VOICEVOX エンジンの ECS オンデマンド制御（docs/log/24 Phase 2）。
//
// AWS 本番ではエンジンを ECS Service として置き、管理者トグルで desired count を
// 0↔1 する（停止中コスト 0）。アドレッシングは Cloud Map の固定 DNS（例
// voicevox.af.local）を AF_VOICEVOX_URL に差すだけで、CP の合成ハンドラは不変。
// Service / タスク定義 / Cloud Map は IaC（deploy/aws）が所有し、CP は
// DescribeServices / UpdateService しか呼ばない（CP ロールに要この 2 権限）。
// ワークスペースの ECS アダプタ（runtime_ecs.go）とは独立の小さなコントローラ。
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

// ttsEngineAPI は ECS 呼び出しの narrow port（ecsAPI の 2 メソッド版）。実 *ecs.Client
// が満たし、テストは偽物を差す。
type ttsEngineAPI interface {
	DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	UpdateService(context.Context, *ecs.UpdateServiceInput, ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
}

type ttsEngineECS struct {
	api     ttsEngineAPI
	cluster string
	service string
}

// newTTSEngineFromEnv は AF_TTS_ECS_SERVICE が設定されたときだけコントローラを返す
// （未設定 = 管理外: dev の常駐 docker 等、ライフサイクルは外部管理）。cluster / region は
// 専用の AF_TTS_ECS_* が無ければワークスペースの AF_ECS_* に相乗りする。
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

// state はエンジン Service の状態を running | starting | stopped | none に写す
// （runtime_ecs.go の State と同じ規約）。desired も返し、管理下では「トグルの現在値」
// として使う（setting より ECS 側が真実源）。
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

// setEnabled は desired count を 0↔1 する。コールドスタート（image pull 込みで 1〜2 分）
// は readiness ゲート（voicevoxProvider.Ready / /api/tts/status）が吸収し、その間の
// 日本語合成は auto ルーティングが Polly JP に逃がす。
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
