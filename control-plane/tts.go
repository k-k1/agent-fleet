// CP-native text-to-speech: agent answer text is synthesized by a VOICEVOX engine or by
// AWS Polly and returned as audio bytes (docs/log/24 + ADR0013).
//
// Unlike chat (docs/log/19, where the Agent runs a headless CLI and CP only proxies), CP
// calls the external services (VOICEVOX HTTP / Polly SDK) itself. CP's outbound traffic is
// outside the egress restriction (as in oauth_google.go), so no allowlist change is needed.
// The response is raw octet-stream like git_lfs.go — never base64.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ttsHTTP is the client for synthesis calls to VOICEVOX. Calls are short (one sentence at
// a time), but the timeout is generous to survive a cold engine.
var ttsHTTP = &http.Client{Timeout: 30 * time.Second}

type ttsSynthReq struct {
	Text     string `json:"text"`
	Provider string `json:"provider"` // "" | "auto" | "voicevox" | "polly"
	// Pin is the provider that answered the first sentence of the utterance being read,
	// sent back on every following sentence so one answer is read in one voice (ADR 0070
	// decision 13). It is a routing override and nothing else.
	//
	// ⚠️ It is a field of its own on purpose, and a client must never express a pin by
	// sending provider:"polly" instead. Demand is counted from the CONFIGURED provider
	// (decision 3): a pinned remainder that stopped counting as intent would let the idle
	// window run out while somebody is still listening to it.
	Pin           string  `json:"pin"`           // "" | "voicevox" | "polly"
	Voice         string  `json:"voice"`         // voicevox speaker number (e.g. "3")
	PollyVoice    string  `json:"pollyVoice"`    // Polly VoiceId (e.g. "Takumi"); also used when auto falls back to Polly
	Speed         float64 `json:"speed"`         // 0.5-2.0 (voicevox speedScale / Polly prosody rate); 0 or unset = 1.0
	Lang          string  `json:"lang"`          // language hint "auto" | "ja" | "en" (reuses the outputLanguage setting)
	EnKana        bool    `json:"enkana"`        // pre-transliterate English words to katakana (voicevox only, enkana.go)
	ParticlePause bool    `json:"particlePause"` // ttsParticlePause setting: shorten the pauses the client inserted (voicevox only)
}

// ttsProvider abstracts a synthesis engine (docs/log/24) — the same map dispatch as
// chatProviders in chat_providers.go. Text pre-processing (enkana, user dictionary) belongs
// outside the provider (handler / client); a provider only turns text into audio.
type ttsProvider interface {
	// Synthesize turns text into audio bytes plus their MIME type.
	Synthesize(ctx context.Context, text string, o voiceOpts) (audio []byte, mime string, aerr *apiError)
	// Ready reports engine reachability (voicevox /version); Polly answers from config alone.
	Ready(ctx context.Context) bool
}

type voiceOpts struct {
	voice         string  // provider-specific speaker ID
	speed         float64 // 0.5-2.0; 0 or unset = 1.0
	lang          string  // "auto" | "ja" | "en"
	particlePause bool    // shorten the after-particle pauses (voicevox only)
}

// chooseTTSProvider decides what auto (the default) routes to (the table in docs/log/24).
// It absorbs the asymmetry between VOICEVOX (Japanese only, must be started) and Polly
// (multilingual, always up):
//   - an explicit polly is honoured as-is;
//   - an explicit voicevox is honoured while the engine can answer, and read by Polly
//     while it cannot (ADR 0070 decision 13);
//   - non-Japanese (lang=en) goes to Polly, falling back to voicevox — which then needs
//     enkana — when Polly is absent;
//   - Japanese (ja / auto) goes to voicevox when the engine is enabled and ready, otherwise
//     to Polly JP (the next sentence returns to voicevox once Ready recovers).
func chooseTTSProvider(pref, lang string, engineOff, vvReady, plReady bool) string {
	switch pref {
	case "polly":
		return "polly"
	case "voicevox":
		// Not unconditional, and that is decision 13. Under on-demand a stopped engine is
		// the NORMAL state, so honouring this as written would leave the one member who
		// asked for Zundamon by name as the only member who hears nothing: the request
		// 502s and synthToBuffer turns a 502 into a silently skipped sentence. The same
		// goes for the engine being switched off — routing is off, and this request is
		// routing too. Polly reads instead, and X-TTS-Provider says who did.
		if vvReady && !engineOff {
			return "voicevox"
		}
		if plReady {
			return "polly"
		}
		return "voicevox" // nothing else can answer: surface voicevox's own 502
	}
	if lang == "en" {
		if plReady {
			return "polly"
		}
		return "voicevox"
	}
	if !engineOff && vvReady {
		return "voicevox"
	}
	if plReady {
		return "polly"
	}
	return "voicevox" // neither is available: surface voicevox's own 502
}

// ttsEngineSetting is the setting key behind the admin toggle, and the only record of what
// the administrator wants: "off" | "on" | "ondemand" (ADR 0070 decision 7), read through
// ttsEngineMode, which is also where "" is resolved. Under ECS the desired count moves by
// itself, so it is not the intent and must never be reported as one.
const ttsEngineSetting = "tts_engine"

// ttsDictSetting holds the tenant-wide reading dictionary (one "spelling=reading" per line,
// the same format as the client's user dictionary). An admin edits it through
// /api/admin/tts/dict; every client fetches it with GET /api/tts/dict and merges it with the
// user dictionary, where an entry for the same spelling wins. Applied client-side.
const ttsDictSetting = "tts_dict"

// registerTTSRoutes registers the CP-native TTS routes (called from buildMux). They sit
// under the default authGate — never exempt, so login is required — and are not
// workspace-scoped, hence no withResolved. Synthesis still works when cfg.mgr is nil (tests).
func registerTTSRoutes(mux *http.ServeMux, cfg config) {
	vv := &voicevoxProvider{base: cfg.voicevoxURL}
	pl := newPollyProvider()
	providers := map[string]ttsProvider{"voicevox": vv, "polly": pl}
	eng := newTTSEngine()
	var settings store.SettingsStore
	if cfg.mgr != nil && cfg.mgr.store != nil {
		settings = cfg.mgr.store
	}
	status := ttsStatusView{vv: vv, pl: pl, eng: eng, settings: settings}
	// The character catalogue outlives the engine (ADR 0070 decision 12): under on-demand
	// the engine is stopped most of the time, and a picker that can only be filled by a
	// running engine is a picker nobody can use.
	speakers := &ttsSpeakerCache{settings: settings}

	// The on-demand controller and the demand counter exist only where there is a service
	// to start: elsewhere the engine's lifecycle is somebody else's (a standing dev
	// docker), and counting demand for it would be bookkeeping nobody reads. This is also
	// what keeps every test that registers these routes free of a background goroutine.
	var ctrl *ttsController
	var demand *ttsDemand
	if eng != nil {
		ccfg := ttsControlCfgFromEnv()
		demand = newTTSDemand(settings, ccfg.window)
		var auditor ttsAuditor
		if cfg.mgr != nil && cfg.mgr.store != nil {
			auditor = cfg.mgr.store
		}
		// Constructing it also puts the warm-up gate in front of vv.Ready: /version answers
		// 200 before a voice model is loaded (ADR 0070 decision 16).
		ctrl = newTTSController(eng, vv, demand, settings, auditor, speakers, ccfg)
		if ccfg.interval > 0 {
			log.Printf("tts: on-demand controller every %s (start %d chars / %s, idle %s, off grace %s)",
				ccfg.interval, ccfg.startChars, ccfg.window, ccfg.idle, ccfg.offGrace)
			go ctrl.run(context.Background())
		}
	}

	mux.HandleFunc("POST /api/tts/synthesize", func(w http.ResponseWriter, r *http.Request) {
		var req ttsSynthReq
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid body"})
			return
		}
		text := strings.TrimSpace(req.Text)
		if text == "" {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "empty text"})
			return
		}
		switch req.Provider {
		case "", "auto", "voicevox", "polly":
			// ok
		default:
			writeAPIErr(w, &apiError{http.StatusNotImplemented, "tts_provider_unavailable", "unknown provider: " + req.Provider})
			return
		}
		switch req.Pin {
		case "", "auto", "voicevox", "polly":
			// ok
		default:
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "unknown pin: " + req.Pin})
			return
		}
		// One request, one sentence: the client cuts at sentence ends and splits anything
		// longer than 60 characters before it gets here. The cap is for everything that is
		// not that client — measured, a single request of about 2,000 characters OOM-kills
		// a 4 GiB engine while its health check stays HEALTHY throughout, and one over
		// roughly 300 characters outlives ttsHTTP's own 30 s timeout anyway (ADR 0070
		// decision 17).
		if max := ttsMaxSynthChars(); max > 0 && len([]rune(text)) > max {
			writeAPIErr(w, &apiError{http.StatusRequestEntityTooLarge, "tts_text_too_long",
				fmt.Sprintf("one synthesis request is limited to %d characters; split the text into sentences", max)})
			return
		}
		mode := status.mode(r.Context())
		// Demand is intent, not outcome (ADR 0070 decision 3): count what this request
		// wanted before anything is routed, because while the engine starts every request
		// is served by Polly and an outcome counter would read zero for exactly the two
		// minutes that matter.
		//
		// ⚠️ It is req.Provider — the member's configured preference — and never the pin
		// below. A pinned remainder of an answer that stopped counting would starve the
		// automatic trigger and let the idle window close on somebody who is listening.
		if ttsDemandIntent(req.Provider, req.Lang, mode) {
			demand.record(r.Context(), len([]rune(text)))
		}
		pref := req.Provider
		if req.Pin == "voicevox" || req.Pin == "polly" {
			pref = req.Pin
		}
		name := chooseTTSProvider(pref, req.Lang, mode == ttsModeOff, vv.Ready(r.Context()), pl.Ready(r.Context()))
		o := voiceOpts{voice: req.Voice, speed: req.Speed, lang: req.Lang, particlePause: req.ParticlePause}
		if name == "voicevox" {
			// VOICEVOX cannot read English spelling, so transliterate it first. Polly
			// reads English as-is, hence only on the voicevox branch (docs/log/24).
			if req.EnKana {
				text = englishToKana(text)
			}
		} else {
			o.voice = req.PollyVoice
		}
		audio, mime, aerr := providers[name].Synthesize(r.Context(), text, o)
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		w.Header().Set("Content-Type", mime)
		// The provider actually used, so the UI can show that auto fell back.
		w.Header().Set("X-TTS-Provider", name)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(audio)
	})

	// Engine reachability; the front end drives the toggle's enabled/"starting" state from
	// it. When the engine is ECS-managed the service state (running/starting/stopped) is
	// added so the readiness gate is visible.
	mux.HandleFunc("GET /api/tts/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, status.body(r.Context()))
	})

	// "Call Zundamon" — the explicit start trigger of ADR 0070 decision 4, for any logged
	// in member. Somebody who wants the voice for their own notifications must not have to
	// earn it by volume.
	wake := &ttsWakeAPI{memberAuth: memberAuth{cfg.mgr}, status: status, eng: eng, ctrl: ctrl, demand: demand}
	mux.HandleFunc("POST /api/tts/wake", wake.withIdentity(wake.post))

	// Character list, from VOICEVOX /speakers, so the character picker in settings offers
	// names, styles and speaker numbers from the real engine — a static table of speaker
	// numbers drifts away from it (docs/log/24). Under on-demand the engine is stopped
	// most of the time, so the catalogue is also kept durably and answered from there
	// while it is down (decision 12); "live" says which of the two this is.
	mux.HandleFunc("GET /api/tts/speakers", func(w http.ResponseWriter, r *http.Request) {
		list, live, aerr := speakers.get(r.Context(), cfg.voicevoxURL, vv.reachable(r.Context()))
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"speakers": list, "live": live})
	})

	// The tenant-wide reading dictionary, readable by every logged-in user. The client
	// fetches it at startup and merges it with the user dictionary; because it is applied
	// client-side, the synthesis handler never looks at it.
	mux.HandleFunc("GET /api/tts/dict", func(w http.ResponseWriter, r *http.Request) {
		dict := ""
		if settings != nil {
			dict, _ = settings.GetSetting(r.Context(), ttsDictSetting)
		}
		writeJSON(w, http.StatusOK, map[string]any{"dict": dict})
	})

	// super_admin toggle for the VOICEVOX engine. Under ECS it flips the desired count
	// between 0 and 1 (on-demand start, zero cost while stopped); the intent is always
	// recorded in the setting as well, the same way egress does with SettingsStore.
	adm := ttsAdminAPI{memberAuth{cfg.mgr}, settings, eng, ctrl, vv, pl}
	mux.HandleFunc("GET /api/admin/tts", adm.withSuperAdmin(adm.get))
	mux.HandleFunc("PUT /api/admin/tts", adm.withSuperAdmin(adm.put))
	mux.HandleFunc("PUT /api/admin/tts/dict", adm.withSuperAdmin(adm.putDict))
}

// ttsMaxSynthChars is the ceiling on ONE synthesis request, in characters
// (AF_TTS_MAX_CHARS, 0 = no cap). It is not a quota — it is the guard rail P0 measured
// the need for: about 2,000 characters in a single call OOM-kills a 4 GiB engine, the
// container health check on /version stays HEALTHY right up to the exit, and ECS then
// spends about 2.5 minutes replacing the task while Polly reads. The default sits just
// above ttsHTTP's own limit (a 268-character sentence takes 25.2 s against a 30 s
// timeout), so nothing that would have succeeded is refused, and far below what the
// browser client can produce at all — it cuts at sentence ends and splits anything over
// 60 characters before sending (ADR 0070 decision 17).
func ttsMaxSynthChars() int { return runtime.EnvInt("AF_TTS_MAX_CHARS", 300) }

// --- status ---------------------------------------------------------------------

// ttsStatusView builds what a member is told about the engines. GET /api/tts/status
// answers with it and POST /api/tts/wake echoes it back, so pressing the button shows
// the new state without a second round trip.
//
// Mode (the stored intent) and state (what the deployment is doing about it) are
// separate fields on purpose — ADR 0070 decision 7, and the same defect class as a
// screen that reports a setting as though it were reality.
type ttsStatusView struct {
	vv       *voicevoxProvider
	pl       *pollyProvider
	eng      *ttsEngineECS       // nil = not ECS-managed
	settings store.SettingsStore // may be nil (tests)
}

func (s ttsStatusView) mode(ctx context.Context) string {
	v := ""
	if s.settings != nil {
		v, _ = s.settings.GetSetting(ctx, ttsEngineSetting)
	}
	return ttsEngineMode(v, s.eng != nil)
}

func (s ttsStatusView) body(ctx context.Context) map[string]any {
	mode := s.mode(ctx)
	vvSt := map[string]any{
		"ready":   s.vv.Ready(ctx),
		"enabled": mode != ttsModeOff,
		"mode":    mode,
	}
	if s.eng != nil {
		vvSt["managed"] = true
		if v, err := s.eng.view(ctx); err == nil {
			vvSt["state"] = ttsDisplayState(v.state, mode)
		}
	}
	return map[string]any{
		"providers": map[string]any{
			"voicevox": vvSt,
			"polly":    map[string]any{"ready": s.pl.Ready(ctx)},
		},
	}
}

// --- voicevox provider ---------------------------------------------------------

// voicevoxProvider adapts the VOICEVOX engine at cfg.voicevoxURL. Reachability is cached
// for a short TTL because auto routing asks once per sentence and must not hit /version
// that often; tracking the engine coming up or going down to a few seconds is accurate
// enough.
type voicevoxProvider struct {
	base      string
	mu        sync.Mutex
	ready     bool
	checkedAt time.Time
	// warmGate reports whether the engine has been warmed up since it came up. nil means
	// nothing warms this engine (no on-demand controller), and then /version alone is
	// readiness, exactly as it was before on-demand existed.
	warmGate func() bool
}

const vvReadyTTL = 4 * time.Second

// Ready is reachable AND warmed. The second half is ADR 0070 decision 16: /version answers
// 200 before a voice model is loaded, so a RUNNING engine that has not yet synthesized
// anything is not something to route a listener to — Polly reads for the extra second.
func (v *voicevoxProvider) Ready(ctx context.Context) bool {
	if !v.reachable(ctx) {
		return false
	}
	return v.warmGate == nil || v.warmGate()
}

func (v *voicevoxProvider) reachable(ctx context.Context) bool {
	v.mu.Lock()
	if !v.checkedAt.IsZero() && time.Since(v.checkedAt) < vvReadyTTL {
		ok := v.ready
		v.mu.Unlock()
		return ok
	}
	v.mu.Unlock()
	ok := voicevoxReady(ctx, v.base)
	v.mu.Lock()
	v.ready, v.checkedAt = ok, time.Now()
	v.mu.Unlock()
	return ok
}

func (v *voicevoxProvider) Synthesize(ctx context.Context, text string, o voiceOpts) ([]byte, string, *apiError) {
	wav, aerr := voicevoxSynthesize(ctx, v.base, text, o.voice, o.speed, o.particlePause)
	if aerr != nil {
		return nil, "", aerr
	}
	return wav, "audio/wav", nil
}

// collapseJaSpaces drops ASCII spaces adjacent to Japanese characters. A space between an
// English word and Japanese text is typographic, not a pause, but VOICEVOX synthesizes every
// space as one and the reading breaks up. Spaces between English words ("tsc / vitest") are
// kept as word separators, with runs normalized to one. Ideographic spaces are left alone —
// they are used deliberately to mean a pause.
func collapseJaSpaces(s string) string {
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(runes); i++ {
		if runes[i] != ' ' {
			b.WriteRune(runes[i])
			continue
		}
		j := i
		for j < len(runes) && runes[j] == ' ' {
			j++
		}
		prevJa := i > 0 && isJaRune(runes[i-1])
		nextJa := j < len(runes) && isJaRune(runes[j])
		if !prevJa && !nextJa {
			b.WriteRune(' ')
		}
		i = j - 1
	}
	return b.String()
}

func isJaRune(r rune) bool {
	switch {
	case r >= 0x3040 && r <= 0x30FF: // hiragana, katakana, prolonged sound mark
		return true
	case r >= 0x4E00 && r <= 0x9FFF: // CJK unified ideographs
		return true
	case r >= 0x3001 && r <= 0x303F: // CJK punctuation (the ideographic space U+3000 is excluded)
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // fullwidth forms and halfwidth katakana
		return true
	}
	return false
}

// particlePauseScale scales the comma pause (pause_mora.vowel_length) while the "breath
// after a particle" option (ttsParticlePause) is on. The client inserts one at every
// particle-to-kanji boundary, so at full comma length the whole sentence drags; six tenths
// turns it into a light breath. Commas already present in the text get the same treatment,
// which is deliberate — the client inserts its own indistinguishably, so with the option on
// everything shortens together.
const particlePauseScale = 0.6

// scalePauseMoras multiplies audio_query's accent_phrases[].pause_mora.vowel_length by scale
// in place. pause_mora exists only right after a comma or similar, and is usually null.
func scalePauseMoras(m map[string]any, scale float64) {
	phrases, ok := m["accent_phrases"].([]any)
	if !ok {
		return
	}
	for _, p := range phrases {
		phrase, ok := p.(map[string]any)
		if !ok {
			continue
		}
		pause, ok := phrase["pause_mora"].(map[string]any)
		if !ok {
			continue
		}
		if vl, ok := pause["vowel_length"].(float64); ok {
			pause["vowel_length"] = vl * scale
		}
	}
}

// voicevoxSynthesize gets a WAV through VOICEVOX's two-step API (audio_query, then
// synthesis). speed overrides the speedScale audio_query returned.
func voicevoxSynthesize(ctx context.Context, base, text, voice string, speed float64, particlePause bool) ([]byte, *apiError) {
	base = strings.TrimRight(base, "/")
	text = collapseJaSpaces(text)
	speaker := strings.TrimSpace(voice)
	if speaker == "" {
		speaker = "3" // Zundamon, normal style
	}

	// 1) audio_query — synthesis parameters as JSON, from text and speaker.
	q := url.Values{}
	q.Set("speaker", speaker)
	q.Set("text", text)
	aqReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/audio_query?"+q.Encode(), nil)
	aqResp, err := ttsHTTP.Do(aqReq)
	if err != nil {
		return nil, &apiError{http.StatusBadGateway, "tts_engine_unreachable", "voicevox unreachable: " + err.Error()}
	}
	defer aqResp.Body.Close()
	aqBody, _ := io.ReadAll(io.LimitReader(aqResp.Body, 1<<20))
	if aqResp.StatusCode != http.StatusOK {
		return nil, &apiError{http.StatusBadGateway, "tts_engine_error", "voicevox audio_query failed: " + strings.TrimSpace(string(aqBody))}
	}

	// 2) Override the synthesis parameters. The leading and trailing silence (0.1s each by
	// default) is felt as roughly 0.2s of waiting at every sentence boundary when sentences
	// are played back one by one, so shorten it; the gap between sentences is the front
	// end's job (SENTENCE_GAP in its playback schedule). speedScale keeps whatever
	// audio_query returned when speed is 0 or unset.
	var m map[string]any
	if json.Unmarshal(aqBody, &m) == nil {
		m["prePhonemeLength"] = 0.02
		m["postPhonemeLength"] = 0.05
		if speed > 0 {
			m["speedScale"] = clampSpeed(speed)
		}
		if particlePause {
			scalePauseMoras(m, particlePauseScale)
		}
		if nb, e := json.Marshal(m); e == nil {
			aqBody = nb
		}
	}

	// 3) synthesis — hand the parameter JSON back and get the WAV.
	sReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/synthesis?speaker="+url.QueryEscape(speaker), bytes.NewReader(aqBody))
	sReq.Header.Set("Content-Type", "application/json")
	sReq.Header.Set("Accept", "audio/wav")
	sResp, err := ttsHTTP.Do(sReq)
	if err != nil {
		return nil, &apiError{http.StatusBadGateway, "tts_engine_unreachable", "voicevox unreachable: " + err.Error()}
	}
	defer sResp.Body.Close()
	// Bounded read (a wedged engine must not balloon CP memory), and a read error
	// must not return a truncated WAV as a 200.
	wav, rerr := io.ReadAll(io.LimitReader(sResp.Body, 64<<20))
	if sResp.StatusCode != http.StatusOK {
		return nil, &apiError{http.StatusBadGateway, "tts_engine_error", "voicevox synthesis failed: " + strings.TrimSpace(string(wav))}
	}
	if rerr != nil {
		return nil, &apiError{http.StatusBadGateway, "tts_engine_error", "voicevox synthesis read: " + rerr.Error()}
	}
	return wav, nil
}

// ttsSpeakerStyle and ttsSpeaker make up one character of /api/tts/speakers. A style's id is
// the speaker number, returned as a string because that is the form the client puts into a
// synthesis request.
type ttsSpeakerStyle struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type ttsSpeaker struct {
	Name   string            `json:"name"`
	Styles []ttsSpeakerStyle `json:"styles"`
}

// voicevoxSpeakers turns the engine's GET /speakers into character names plus their talk
// styles. Singing styles (any type other than "talk", e.g. song/humming) cannot read text
// aloud, so they are dropped along with any character left without a talk style.
func voicevoxSpeakers(ctx context.Context, base string) ([]ttsSpeaker, *apiError) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/speakers", nil)
	resp, err := ttsHTTP.Do(req)
	if err != nil {
		return nil, &apiError{http.StatusBadGateway, "tts_engine_unreachable", "voicevox unreachable: " + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &apiError{http.StatusBadGateway, "tts_engine_error", "voicevox speakers failed: " + strings.TrimSpace(string(body))}
	}
	var raw []struct {
		Name   string `json:"name"`
		Styles []struct {
			ID   json.Number `json:"id"`
			Name string      `json:"name"`
			Type string      `json:"type"`
		} `json:"styles"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, &apiError{http.StatusBadGateway, "tts_engine_error", "voicevox speakers: invalid JSON"}
	}
	out := make([]ttsSpeaker, 0, len(raw))
	for _, sp := range raw {
		s := ttsSpeaker{Name: sp.Name}
		for _, st := range sp.Styles {
			if st.Type != "" && st.Type != "talk" {
				continue
			}
			s.Styles = append(s.Styles, ttsSpeakerStyle{ID: st.ID.String(), Name: st.Name})
		}
		if len(s.Styles) > 0 {
			out = append(out, s)
		}
	}
	return out, nil
}

// voicevoxReady judges reachability by whether /version answers 200, on a short timeout.
func voicevoxReady(ctx context.Context, base string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/version", nil)
	resp, err := ttsHTTP.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	return resp.StatusCode == http.StatusOK
}

func clampSpeed(s float64) float64 {
	if s < 0.5 {
		return 0.5
	}
	if s > 2.0 {
		return 2.0
	}
	return s
}

// --- admin toggle (/api/admin/tts) ---------------------------------------------

// ttsAdminAPI is the handler set behind the admin toggle for the VOICEVOX engine. The
// stored setting is the only source of truth for the mode; the desired count is what the
// deployment is doing about it, and the two are reported as separate things.
type ttsAdminAPI struct {
	memberAuth
	settings store.SettingsStore // may be nil (tests)
	eng      *ttsEngineECS       // nil = not ECS-managed
	ctrl     *ttsController      // nil = nothing runs the engine on its own
	vv       *voicevoxProvider
	pl       *pollyProvider
}

// ttsDisplayState is what a person is told the engine is doing. It differs from the ECS
// service state in one place, and that place is the point: right after OFF is pressed the
// desired count has deliberately not moved yet (the undo window of ADR 0070 decision 5),
// and a panel that answered "running" there would be reporting the opposite of the
// administrator's intent — the same class of defect as reporting a setting as reality.
func ttsDisplayState(raw, mode string) string {
	if mode == ttsModeOff && (raw == "running" || raw == "starting") {
		return "stopping"
	}
	return raw
}

// get (GET /api/admin/tts) reports the toggle state plus engine reachability and ECS state.
func (a ttsAdminAPI) get(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	writeJSON(w, http.StatusOK, a.status(r.Context()))
}

func (a ttsAdminAPI) status(ctx context.Context) map[string]any {
	managed := a.eng != nil
	stored := ""
	if a.settings != nil {
		stored, _ = a.settings.GetSetting(ctx, ttsEngineSetting)
	}
	mode := ttsEngineMode(stored, managed)
	engine := map[string]any{"ready": a.vv.Ready(ctx)}
	if managed {
		if v, err := a.eng.view(ctx); err == nil {
			engine["state"] = ttsDisplayState(v.state, mode)
			engine["desired"] = v.desired
		} else {
			engine["error"] = err.Error()
		}
	}
	dict := ""
	if a.settings != nil {
		dict, _ = a.settings.GetSetting(ctx, ttsDictSetting)
	}
	return map[string]any{
		"managed": managed,
		"mode":    mode,
		// enabled is kept for clients written against the two-valued toggle, and it is the
		// intent — never the desired count. Deriving it from ECS is what made the toggle
		// appear to move on its own the moment the engine started stopping itself.
		"enabled": mode != ttsModeOff,
		"engine":  engine,
		"polly":   map[string]any{"ready": a.pl.Ready(ctx)},
		"dict":    dict,
	}
}

// put (PUT /api/admin/tts) takes body {mode:"off"|"on"|"ondemand"} — or {enabled:bool}
// from a client written before the mode existed — and records it in the setting, which is
// the intent. What happens to the desired count is not symmetric:
//
//   - "on" starts the engine now: somebody pressed a button and is waiting.
//   - "ondemand" touches nothing; the controller decides from here on.
//   - "off" does NOT stop it here. The stop is debounced by the controller's undo window
//     (ADR 0070 decision 5), because this panel makes it easy to press OFF and then ON
//     again, and each of those bought a 2 GB pull and 70-80 seconds. Routing stops at
//     once regardless — the setting is what chooseTTSProvider reads — so the grace costs
//     no listener anything. Without a controller there is nobody to do it later, so the
//     stop happens here.
//
// Audited.
func (a ttsAdminAPI) put(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	var b struct {
		Mode    string `json:"mode"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "invalid JSON"})
		return
	}
	val := b.Mode
	switch val {
	case ttsModeOff, ttsModeOn, ttsModeOnDemand:
	case "":
		if b.Enabled == nil {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "mode is required"})
			return
		}
		val = ttsModeOn
		if !*b.Enabled {
			val = ttsModeOff
		}
	default:
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "unknown mode: " + val})
		return
	}
	if a.settings != nil {
		if err := a.settings.SetSetting(r.Context(), ttsEngineSetting, val); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		// When the mode changed is not decoration: the undo window is measured from it,
		// and it has to survive the CP restart that would otherwise cancel the window.
		if err := a.settings.SetSetting(r.Context(), ttsModeAtSetting, strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
			log.Printf("tts: recording the mode change time failed: %v", err)
		}
	}
	a.ctrl.noteAdminAction() // a cooldown must never refuse the person who pressed the button
	if a.eng != nil && (val == ttsModeOn || (val == ttsModeOff && a.ctrl == nil)) {
		if err := a.eng.setEnabled(r.Context(), val == ttsModeOn); err != nil {
			writeAPIErr(w, &apiError{http.StatusBadGateway, "tts_engine_error", "ecs update failed: " + err.Error()})
			return
		}
	}
	if a.mgr != nil && a.mgr.store != nil {
		_ = a.mgr.store.InsertAudit(r.Context(), store.AuditLog{
			ID: store.NewID(), TenantID: "", ActorKind: "admin", ActorID: ident.ID,
			Action: "tts.engine", Target: val, At: store.NowTS(),
		})
	}
	writeJSON(w, http.StatusOK, a.status(r.Context()))
}

// putDict (PUT /api/admin/tts/dict) takes body {dict:string} and replaces the whole
// tenant-wide reading dictionary (one "spelling=reading" per line, parsed by the same rules
// as the client's parseUserDict). Audited with the size only, not the content.
func (a ttsAdminAPI) putDict(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	var b struct{ Dict string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "invalid JSON (dict は 256KB まで)"})
		return
	}
	if a.settings == nil {
		writeAPIErr(w, &apiError{http.StatusServiceUnavailable, "store_unavailable", "settings store unavailable"})
		return
	}
	if err := a.settings.SetSetting(r.Context(), ttsDictSetting, b.Dict); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if a.mgr != nil && a.mgr.store != nil {
		_ = a.mgr.store.InsertAudit(r.Context(), store.AuditLog{
			ID: store.NewID(), TenantID: "", ActorKind: "admin", ActorID: ident.ID,
			Action: "tts.dict", Target: fmt.Sprintf("%d bytes", len(b.Dict)), At: store.NowTS(),
		})
	}
	writeJSON(w, http.StatusOK, a.status(r.Context()))
}
