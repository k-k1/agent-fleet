package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/polly"
)

// fakePolly records the last SynthesizeSpeech input and returns canned MP3 bytes.
type fakePolly struct {
	in  *polly.SynthesizeSpeechInput
	err error
}

func (f *fakePolly) SynthesizeSpeech(_ context.Context, in *polly.SynthesizeSpeechInput, _ ...func(*polly.Options)) (*polly.SynthesizeSpeechOutput, error) {
	f.in = in
	if f.err != nil {
		return nil, f.err
	}
	return &polly.SynthesizeSpeechOutput{
		AudioStream: io.NopCloser(strings.NewReader("MP3DATA")),
	}, nil
}

func TestPollySynthesize(t *testing.T) {
	f := &fakePolly{}
	p := &pollyProvider{region: "ap-northeast-1", engine: "neural", client: f}

	audio, mime, aerr := p.Synthesize(t.Context(), "こんにちは & <みんな>。", voiceOpts{speed: 1.25, lang: "ja"})
	if aerr != nil {
		t.Fatalf("synthesize: %+v", aerr)
	}
	if string(audio) != "MP3DATA" || mime != "audio/mpeg" {
		t.Errorf("audio=%q mime=%q, want MP3DATA / audio/mpeg", audio, mime)
	}
	if got := string(f.in.VoiceId); got != "Takumi" {
		t.Errorf("voice = %q, want Takumi (ja default)", got)
	}
	ssml := aws.ToString(f.in.Text)
	if !strings.Contains(ssml, `rate="125%"`) {
		t.Errorf("ssml missing rate 125%%: %s", ssml)
	}
	if !strings.Contains(ssml, "&amp;") || !strings.Contains(ssml, "&lt;みんな&gt;") {
		t.Errorf("ssml not escaped: %s", ssml)
	}
	if !strings.HasPrefix(ssml, "<speak>") || !strings.HasSuffix(ssml, "</speak>") {
		t.Errorf("ssml not wrapped in <speak>: %s", ssml)
	}
}

func TestPollyVoiceDefaults(t *testing.T) {
	if v := pollyVoiceFor(voiceOpts{lang: "en"}); v != "Joanna" {
		t.Errorf("en default = %q, want Joanna", v)
	}
	if v := pollyVoiceFor(voiceOpts{lang: "ja"}); v != "Takumi" {
		t.Errorf("ja default = %q, want Takumi", v)
	}
	if v := pollyVoiceFor(voiceOpts{lang: "auto"}); v != "Takumi" {
		t.Errorf("auto default = %q, want Takumi", v)
	}
	if v := pollyVoiceFor(voiceOpts{voice: "Kazuha", lang: "en"}); v != "Kazuha" {
		t.Errorf("explicit voice = %q, want Kazuha", v)
	}
}

func TestPollySSMLSpeedDefault(t *testing.T) {
	// 0 (unset) must mean 100%: routing it through clampSpeed would turn 0 into 0.5.
	if s := pollySSML("x", 0); !strings.Contains(s, `rate="100%"`) {
		t.Errorf("speed 0 → %s, want rate=100%%", s)
	}
	if s := pollySSML("x", 9); !strings.Contains(s, `rate="200%"`) {
		t.Errorf("speed 9 → %s, want clamped rate=200%%", s)
	}
}

func TestPollyNotConfigured(t *testing.T) {
	p := &pollyProvider{} // no region
	if p.Ready(t.Context()) {
		t.Error("Ready should be false without a region")
	}
	_, _, aerr := p.Synthesize(t.Context(), "hi", voiceOpts{})
	if aerr == nil || aerr.status != 503 {
		t.Fatalf("want 503, got %+v", aerr)
	}
}
