// tts_polly.go — AWS Polly の TTS プロバイダ（docs/log/24 Phase 2）。
//
// 認証は SDK の既定チェーン（ECS/EC2 の IAM ロール）で、鍵は保存しない（ADR0013）。
// 出力は MP3（フロントの AudioContext.decodeAudioData がそのまま復号できる）。速度は
// SSML の <prosody rate> で表現する（Polly に voicevox の speedScale 相当は無いため）。
// region が決まらないデプロイ（dev の自ホスト等）では not-ready 扱いになり、auto
// ルーティングは voicevox 側に倒れる。
package main

import (
	"context"
	"encoding/xml"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/polly"
	pollytypes "github.com/aws/aws-sdk-go-v2/service/polly/types"
)

// pollyAPI は Polly 呼び出しの narrow port（runtime_ecs.go の ecsAPI と同じ流儀）。
// 実 *polly.Client が満たし、テストは偽物を差す。
type pollyAPI interface {
	SynthesizeSpeech(context.Context, *polly.SynthesizeSpeechInput, ...func(*polly.Options)) (*polly.SynthesizeSpeechOutput, error)
}

type pollyProvider struct {
	region string // "" = 未設定（not-ready）
	engine string // neural | standard（AF_POLLY_ENGINE）
	mu     sync.Mutex
	client pollyAPI // 遅延生成（初回 Synthesize 時）
}

// newPollyProvider は env から Polly の設定を読む。region は専用の AF_POLLY_REGION が
// 無ければ ECS アダプタ/SDK の region 指定に相乗りする（CP ロールと同居のため通常同一）。
func newPollyProvider() *pollyProvider {
	return &pollyProvider{
		region: firstEnv("AF_POLLY_REGION", "AF_ECS_REGION", "AWS_REGION", "AWS_DEFAULT_REGION"),
		engine: envOr("AF_POLLY_ENGINE", "neural"),
	}
}

// Ready は設定の有無で即答する（ネットワーク I/O なし）。認証エラー等は Synthesize 時に
// 502 として表面化する。
func (p *pollyProvider) Ready(context.Context) bool { return p.region != "" }

func (p *pollyProvider) api(ctx context.Context) (pollyAPI, error) {
	p.mu.Lock()
	if p.client != nil {
		p.mu.Unlock()
		return p.client, nil
	}
	p.mu.Unlock()
	// ロック外・呼び出し元 ctx でロード（キャンセル可能、他の Synthesize をブロック
	// しない）。同時初期化は先着の client を尊重する。
	ac, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(p.region))
	if err != nil {
		return nil, err
	}
	c := polly.NewFromConfig(ac)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == nil {
		p.client = c
	}
	return p.client, nil
}

func (p *pollyProvider) Synthesize(ctx context.Context, text string, o voiceOpts) ([]byte, string, *apiError) {
	if p.region == "" {
		return nil, "", &apiError{http.StatusServiceUnavailable, "tts_provider_unavailable", "polly not configured (set AF_POLLY_REGION)"}
	}
	api, err := p.api(ctx)
	if err != nil {
		return nil, "", &apiError{http.StatusBadGateway, "tts_engine_error", "polly config: " + err.Error()}
	}
	out, err := api.SynthesizeSpeech(ctx, &polly.SynthesizeSpeechInput{
		Engine:       pollytypes.Engine(p.engine),
		OutputFormat: pollytypes.OutputFormatMp3,
		Text:         aws.String(pollySSML(text, o.speed)),
		TextType:     pollytypes.TextTypeSsml,
		VoiceId:      pollytypes.VoiceId(pollyVoiceFor(o)),
	})
	if err != nil {
		return nil, "", &apiError{http.StatusBadGateway, "tts_engine_error", "polly synthesize failed: " + err.Error()}
	}
	defer out.AudioStream.Close()
	audio, err := io.ReadAll(io.LimitReader(out.AudioStream, 16<<20))
	if err != nil {
		return nil, "", &apiError{http.StatusBadGateway, "tts_engine_error", "polly stream: " + err.Error()}
	}
	return audio, "audio/mpeg", nil
}

// pollyVoiceFor は VoiceId を決める: 明示指定 > 言語別の既定（日本語 Takumi / 英語 Joanna）。
// auto ルーティングで日本語がフォールバックしてくるのが主経路なので、既定は日本語。
func pollyVoiceFor(o voiceOpts) string {
	if v := strings.TrimSpace(o.voice); v != "" {
		return v
	}
	if o.lang == "en" {
		return "Joanna"
	}
	return "Takumi"
}

// pollySSML は速度を <prosody rate="N%"> に写した SSML を組む。0/未指定は 100%。
// テキストは XML エスケープ（& < > 等が SSML を壊さないように）。
func pollySSML(text string, speed float64) string {
	rate := 100
	if speed > 0 {
		rate = int(math.Round(clampSpeed(speed) * 100))
	}
	var b strings.Builder
	b.WriteString(`<speak><prosody rate="`)
	b.WriteString(strconv.Itoa(rate))
	b.WriteString(`%">`)
	_ = xml.EscapeText(&b, []byte(text))
	b.WriteString(`</prosody></speak>`)
	return b.String()
}

// firstEnv は最初に値のある環境変数を返す（空白のみは未設定扱い）。
func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
