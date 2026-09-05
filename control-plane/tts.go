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
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ttsHTTP is the client for synthesis calls to VOICEVOX. Calls are short (one sentence at
// a time), but the timeout is generous to survive a cold engine.
var ttsHTTP = &http.Client{Timeout: 30 * time.Second}

type ttsSynthReq struct {
	Text          string  `json:"text"`
	Provider      string  `json:"provider"`      // "" | "auto" | "voicevox" | "polly"
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
//   - an explicit choice (voicevox / polly) is honoured as-is;
//   - non-Japanese (lang=en) goes to Polly, falling back to voicevox — which then needs
//     enkana — when Polly is absent;
//   - Japanese (ja / auto) goes to voicevox when the engine is enabled and ready, otherwise
//     to Polly JP (the next sentence returns to voicevox once Ready recovers).
func chooseTTSProvider(pref, lang string, engineOff, vvReady, plReady bool) string {
	switch pref {
	case "voicevox", "polly":
		return pref
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

// ttsEngineSetting is the setting key behind the admin toggle. Only "off" stops routing to
// voicevox ("" means enabled, so an externally managed dev engine keeps working).
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
	eng := newTTSEngineFromEnv()
	var settings store.SettingsStore
	if cfg.mgr != nil && cfg.mgr.store != nil {
		settings = cfg.mgr.store
	}
	engineOff := func(ctx context.Context) bool {
		if settings == nil {
			return false
		}
		v, _ := settings.GetSetting(ctx, ttsEngineSetting)
		return v == "off"
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
		name := req.Provider
		if name == "" || name == "auto" {
			name = chooseTTSProvider(req.Provider, req.Lang, engineOff(r.Context()), vv.Ready(r.Context()), pl.Ready(r.Context()))
		}
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
		vvSt := map[string]any{
			"ready":   vv.Ready(r.Context()),
			"enabled": !engineOff(r.Context()),
		}
		if eng != nil {
			vvSt["managed"] = true
			if st, _, err := eng.state(r.Context()); err == nil {
				vvSt["state"] = st
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"providers": map[string]any{
				"voicevox": vvSt,
				"polly":    map[string]any{"ready": pl.Ready(r.Context())},
			},
		})
	})

	// Character list, proxied from VOICEVOX /speakers, so the character picker in settings
	// offers names, styles and speaker numbers from the live engine — a static table of
	// speaker numbers drifts away from it (docs/log/24). The 60s cache keeps settings
	// re-renders off the engine. A stopped engine gives 502, and the UI falls back to
	// read-only display of the current setting.
	var spMu sync.Mutex
	var spCache []ttsSpeaker
	var spAt time.Time
	mux.HandleFunc("GET /api/tts/speakers", func(w http.ResponseWriter, r *http.Request) {
		spMu.Lock()
		cached, fresh := spCache, !spAt.IsZero() && time.Since(spAt) < 60*time.Second
		spMu.Unlock()
		if !fresh {
			list, aerr := voicevoxSpeakers(r.Context(), cfg.voicevoxURL)
			if aerr != nil {
				writeAPIErr(w, aerr)
				return
			}
			spMu.Lock()
			spCache, spAt = list, time.Now()
			spMu.Unlock()
			cached = list
		}
		writeJSON(w, http.StatusOK, map[string]any{"speakers": cached})
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
	adm := ttsAdminAPI{memberAuth{cfg.mgr}, settings, eng, vv, pl}
	mux.HandleFunc("GET /api/admin/tts", adm.withSuperAdmin(adm.get))
	mux.HandleFunc("PUT /api/admin/tts", adm.withSuperAdmin(adm.put))
	mux.HandleFunc("PUT /api/admin/tts/dict", adm.withSuperAdmin(adm.putDict))
}

// --- voicevox provider ---------------------------------------------------------

// voicevoxProvider adapts the VOICEVOX engine at cfg.voicevoxURL. Ready is cached for a
// short TTL because auto routing asks once per sentence and must not hit /version that
// often; tracking the engine coming up or going down to a few seconds is accurate enough.
type voicevoxProvider struct {
	base      string
	mu        sync.Mutex
	ready     bool
	checkedAt time.Time
}

const vvReadyTTL = 4 * time.Second

func (v *voicevoxProvider) Ready(ctx context.Context) bool {
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

// ttsAdminAPI is the handler set behind the admin toggle for the VOICEVOX engine. The source
// of truth for enabled is the live desired count under ECS, and the setting otherwise (an
// externally run dev engine).
type ttsAdminAPI struct {
	memberAuth
	settings store.SettingsStore // may be nil (tests)
	eng      *ttsEngineECS       // nil = not ECS-managed
	vv       *voicevoxProvider
	pl       *pollyProvider
}

// get (GET /api/admin/tts) reports the toggle state plus engine reachability and ECS state.
func (a ttsAdminAPI) get(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	writeJSON(w, http.StatusOK, a.status(r.Context()))
}

func (a ttsAdminAPI) status(ctx context.Context) map[string]any {
	enabled := true
	if a.settings != nil {
		v, _ := a.settings.GetSetting(ctx, ttsEngineSetting)
		enabled = v != "off"
	}
	engine := map[string]any{"ready": a.vv.Ready(ctx)}
	managed := a.eng != nil
	if managed {
		if st, desired, err := a.eng.state(ctx); err == nil {
			engine["state"] = st
			enabled = desired >= 1 // under ECS the desired count is the source of truth
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
		"enabled": enabled,
		"engine":  engine,
		"polly":   map[string]any{"ready": a.pl.Ready(ctx)},
		"dict":    dict,
	}
}

// put (PUT /api/admin/tts) takes body {enabled:bool}: under ECS it moves desired between 0
// and 1, and it always records the intent in the setting, which is what switches routing on
// and off when the engine is not ECS-managed. Audited.
func (a ttsAdminAPI) put(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	var b struct{ Enabled bool }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "invalid JSON"})
		return
	}
	val := "on"
	if !b.Enabled {
		val = "off"
	}
	if a.settings != nil {
		if err := a.settings.SetSetting(r.Context(), ttsEngineSetting, val); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
	}
	if a.eng != nil {
		if err := a.eng.setEnabled(r.Context(), b.Enabled); err != nil {
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
