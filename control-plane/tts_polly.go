// tts_polly.go — the AWS Polly TTS provider (docs/log/24 Phase 2).
//
// Authentication uses the SDK's default chain (the ECS/EC2 IAM role); no key is stored
// (ADR0013). Output is MP3, which the front end's AudioContext.decodeAudioData decodes
// directly. Speed is expressed as SSML <prosody rate>, since Polly has no equivalent of
// voicevox's speedScale. A deployment with no resolvable region (a dev host, say) counts
// as not-ready, and auto routing falls back to voicevox.
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
	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
)

// pollyAPI is the narrow port for Polly calls, in the same style as ecsAPI in
// runtime_ecs.go: the real *polly.Client satisfies it and tests substitute a fake.
type pollyAPI interface {
	SynthesizeSpeech(context.Context, *polly.SynthesizeSpeechInput, ...func(*polly.Options)) (*polly.SynthesizeSpeechOutput, error)
}

type pollyProvider struct {
	region string // "" means unset, i.e. not-ready
	engine string // neural | standard (AF_POLLY_ENGINE)
	mu     sync.Mutex
	client pollyAPI // created lazily, on the first Synthesize
}

// newPollyProvider reads the Polly configuration from the environment. Without a dedicated
// AF_POLLY_REGION the region rides on the ECS adapter's / SDK's region, which is normally
// the same because Polly runs under the CP's own role.
func newPollyProvider() *pollyProvider {
	return &pollyProvider{
		region: firstEnv("AF_POLLY_REGION", "AF_ECS_REGION", "AWS_REGION", "AWS_DEFAULT_REGION"),
		engine: envx.Or("AF_POLLY_ENGINE", "neural"),
	}
}

// Ready answers from configuration alone, with no network I/O. Credential and similar
// failures surface as a 502 at Synthesize time.
func (p *pollyProvider) Ready(context.Context) bool { return p.region != "" }

func (p *pollyProvider) api(ctx context.Context) (pollyAPI, error) {
	p.mu.Lock()
	if p.client != nil {
		p.mu.Unlock()
		return p.client, nil
	}
	p.mu.Unlock()
	// Load outside the lock and on the caller's ctx, so it stays cancellable and does not
	// block other Synthesize calls. Concurrent initialisation keeps whichever client won.
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

// pollyVoiceFor picks the VoiceId: an explicit choice wins, otherwise the per-language
// default (Takumi for Japanese, Joanna for English). Japanese is the overall default
// because the main path here is auto routing falling back for Japanese.
func pollyVoiceFor(o voiceOpts) string {
	if v := strings.TrimSpace(o.voice); v != "" {
		return v
	}
	if o.lang == "en" {
		return "Joanna"
	}
	return "Takumi"
}

// pollySSML builds SSML with the speed mapped to <prosody rate="N%">; 0 or unset is 100%.
// The text is XML-escaped so & < > and friends cannot break the SSML.
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

// firstEnv returns the first environment variable that has a value; whitespace-only counts
// as unset.
func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
