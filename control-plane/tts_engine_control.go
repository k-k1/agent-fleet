package main

// tts_engine_control.go — the VOICEVOX (TTS) specific wiring of the generic engine controller
// and ECS adapter. Kept apart from engine_control.go / engine_ecs.go so the engine files do
// not reach into the TTS ones.

import (
	"context"
	"log"
	"time"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ttsEngineSettings is the VOICEVOX engine's row names. tts.go writes two of them directly
// (the admin toggle), so they are named in one place rather than spelled out twice.
func ttsEngineSettings() engineSettings {
	return engineSettings{mode: ttsEngineSetting, modeAt: "tts_engine_at", demandAt: "tts_demand_at"}
}

// ttsDemandIntent reports whether one synthesis request wanted the engine, judged from the
// member's configured preference rather than from what answered it (decision 3). An
// explicit "voicevox" counts whatever the language, because that request does reach the
// engine; "polly" never counts; everything else counts unless the text is English, which
// is what routing would have sent to Polly anyway.
func ttsDemandIntent(pref, lang, mode string) bool {
	if mode == engineModeOff {
		return false // routing is off: nothing would have reached the engine
	}
	switch pref {
	case "polly":
		return false
	case "voicevox":
		return true
	}
	return lang != "en"
}

// newTTSController is newEngineController for the VOICEVOX engine, plus the one wiring that
// must not be left to a caller: an engine this controller starts and stops is exactly the
// engine whose readiness has to wait for a warm-up, and a gate somebody forgot to attach
// fails silently — it just reads ready too early, once, per start.
//
// speakers may be nil. When it is not, the transition to warm is also where the character
// catalogue is captured (ADR 0070 decision 12): doing it only from the /speakers handler
// would mean a deployment where nobody opens the settings screen during the engine's half
// hour of life never stores one at all, and then the picker is empty for the rest of that
// deployment's life. The closure below runs the capture exactly where the ttsController's own
// maintainWarm used to — after a successful warm-up synthesis, i.e. once per start.
func newTTSController(eng *engineECS, vv *voicevoxProvider, demand *engineDemand, settings store.SettingsStore, audit engineAuditor, speakers *ttsSpeakerCache, cfg engineControlCfg) *engineController {
	c := newEngineController(eng, ttsEngineSettings(), func(ctx context.Context, alreadyWarm bool) bool {
		if !voicevoxReady(ctx, vv.base) {
			return false
		}
		if alreadyWarm {
			return true
		}
		if _, aerr := voicevoxSynthesize(ctx, vv.base, ttsWarmupText, "", 0, false); aerr != nil {
			log.Printf("tts: warm-up synthesis failed: %s", aerr.message)
			return false
		}
		if speakers != nil {
			if _, aerr := speakers.refresh(ctx, vv.base); aerr != nil {
				log.Printf("tts: refreshing the character catalogue failed: %s", aerr.message)
			}
		}
		return true
	}, demand, settings, audit, cfg)
	vv.warmGate = c.warmed
	return c
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

// ttsWarmupText is the warm-up utterance (decision 16). /version answers 200 before any
// voice model is loaded — measured: the engine boots in under a second without
// --load_all_models and then pays 650 ms on the first audio_query — so the engine is not
// called ready until one synthesis has actually come back. Short, and never heard by
// anyone: the audio is discarded.
const ttsWarmupText = "こんにちは。"

// ttsControlCfgFromEnv reads the tuning. The defaults are ADR 0070's: a 5-minute window,
// 2,000 characters (one answer read aloud, about three times the measured break-even
// against Polly neural), a 30-minute idle window, and a start deadline set from the
// measured cold start of 70-77 seconds with room for a slow pull.
func ttsControlCfgFromEnv() engineControlCfg {
	return engineControlCfg{
		interval:   time.Duration(runtime.EnvInt("AF_TTS_ECS_CONTROL_INTERVAL_SEC", 30)) * time.Second,
		window:     time.Duration(runtime.EnvInt("AF_TTS_ECS_WINDOW_SEC", 300)) * time.Second,
		startUnits: runtime.EnvInt("AF_TTS_ECS_START_CHARS", 2000),
		idle:       time.Duration(runtime.EnvInt("AF_TTS_ECS_IDLE_SEC", 1800)) * time.Second,
		deadline:   time.Duration(runtime.EnvInt("AF_TTS_ECS_START_DEADLINE_SEC", 300)) * time.Second,
		cooldown:   time.Duration(runtime.EnvInt("AF_TTS_ECS_FAIL_COOLDOWN_SEC", 900)) * time.Second,
		offGrace:   time.Duration(runtime.EnvInt("AF_TTS_ECS_OFF_GRACE_SEC", 60)) * time.Second,
	}
}
