package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// fakeVoicevox stands up a minimal VOICEVOX engine: /audio_query returns a params
// JSON, /synthesis echoes the received speedScale into fake WAV bytes so the test can
// assert the speed override round-trips, /version returns 200.
func fakeVoicevox(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var gotSpeaker string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /audio_query", func(w http.ResponseWriter, r *http.Request) {
		gotSpeaker = r.URL.Query().Get("speaker")
		if r.URL.Query().Get("text") == "" {
			http.Error(w, "no text", http.StatusBadRequest)
			return
		}
		// accent_phrases carries one pause_mora (the comma equivalent) so that
		// scalePauseMoras' injection is testable, plus a null pause_mora (no break)
		// that has to be ignored.
		writeJSON(w, http.StatusOK, map[string]any{
			"speedScale": 1.0,
			"kana":       "テスト",
			"accent_phrases": []map[string]any{
				{"pause_mora": nil},
				{"pause_mora": map[string]any{"text": "、", "vowel": "pau", "vowel_length": 0.2}},
			},
		})
	})
	mux.HandleFunc("POST /synthesis", func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		ss, _ := m["speedScale"].(float64)
		pre, _ := m["prePhonemeLength"].(float64)
		post, _ := m["postPhonemeLength"].(float64)
		pauseLen := -1.0 // -1 is the test sentinel for "no pause_mora found"
		if phrases, ok := m["accent_phrases"].([]any); ok {
			for _, p := range phrases {
				if phrase, ok := p.(map[string]any); ok {
					if pause, ok := phrase["pause_mora"].(map[string]any); ok {
						pauseLen, _ = pause["vowel_length"].(float64)
					}
				}
			}
		}
		w.Header().Set("Content-Type", "audio/wav")
		// Encode the overridden params back into the "WAV" so the caller can verify injection.
		_, _ = w.Write([]byte("WAVspeed=" + trimFloat(ss) + ",pre=" + trimFloat(pre) + ",post=" + trimFloat(post) + ",pause=" + trimFloat(pauseLen)))
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0.14.0"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &gotSpeaker
}

func trimFloat(f float64) string {
	b, _ := json.Marshal(f)
	return strings.TrimRight(strings.TrimRight(string(b), "0"), ".")
}

// clearTTSEnv detaches the test from the host's AWS/TTS env so provider readiness
// is deterministic (polly not-ready, no ECS engine control).
func clearTTSEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"AF_POLLY_REGION", "AF_ECS_REGION", "AWS_REGION", "AWS_DEFAULT_REGION",
		"AF_TTS_ECS_SERVICE", "AF_TTS_ECS_CLUSTER", "AF_TTS_ECS_REGION",
	} {
		t.Setenv(k, "")
	}
}

func TestVoicevoxSynthesize(t *testing.T) {
	srv, gotSpeaker := fakeVoicevox(t)

	wav, aerr := voicevoxSynthesize(t.Context(), srv.URL, "こんにちは。", "3", 1.25, false)
	if aerr != nil {
		t.Fatalf("synthesize: %+v", aerr)
	}
	if *gotSpeaker != "3" {
		t.Errorf("speaker = %q, want 3", *gotSpeaker)
	}
	// Both the speedScale override and the trimmed leading/trailing silence (which is what
	// keeps the wait between sentences bearable) must reach /synthesis. With
	// particlePause=false, pause_mora.vowel_length stays at the engine's own 0.2.
	if got := string(wav); got != "WAVspeed=1.25,pre=0.02,post=0.05,pause=0.2" {
		t.Errorf("wav = %q, want WAVspeed=1.25,pre=0.02,post=0.05,pause=0.2 (param override not injected?)", got)
	}

	// Empty voice defaults to ずんだもん (speaker 3); speed 0 keeps speedScale=1
	// while the silence trim still applies.
	wav, aerr = voicevoxSynthesize(t.Context(), srv.URL, "テスト。", "", 0, false)
	if aerr != nil {
		t.Fatalf("default-voice synthesize: %+v", aerr)
	}
	if *gotSpeaker != "3" {
		t.Errorf("default speaker = %q, want 3", *gotSpeaker)
	}
	if got := string(wav); got != "WAVspeed=1,pre=0.02,post=0.05,pause=0.2" {
		t.Errorf("wav = %q, want WAVspeed=1,pre=0.02,post=0.05,pause=0.2", got)
	}

	// particlePause=true tightens pause_mora.vowel_length by particlePauseScale (0.6).
	wav, aerr = voicevoxSynthesize(t.Context(), srv.URL, "神は細部に。", "3", 1, true)
	if aerr != nil {
		t.Fatalf("particle-pause synthesize: %+v", aerr)
	}
	if got := string(wav); got != "WAVspeed=1,pre=0.02,post=0.05,pause=0.12" {
		t.Errorf("wav = %q, want WAVspeed=1,pre=0.02,post=0.05,pause=0.12 (pause scale not injected?)", got)
	}
}

func TestCollapseJaSpaces(t *testing.T) {
	cases := []struct{ in, want string }{
		{"submit 時に", "submit時に"},                // latin word -> Japanese
		{"green です。", "greenです。"},                // latin word -> Japanese, sentence end
		{"設定 tts_engine を見る", "設定tts_engineを見る"}, // Japanese -> latin word -> Japanese
		{"tsc / vitest", "tsc / vitest"},         // space between latin words is kept
		{"This is a pen", "This is a pen"},       // an all-latin sentence is untouched
		{"submit   時に", "submit時に"},              // a run of spaces is removed too
		{"a  b", "a b"},                          // a run between latin words collapses to one
		{"それは　いい", "それは　いい"},                     // a full-width space is a deliberate pause; kept
		{"「code です」", "「codeです」"},                // Japanese punctuation counts as adjacency too
		{"67件 まで OK です", "67件までOKです"},            // digits + counter belong to the Japanese side
	}
	for _, c := range cases {
		if got := collapseJaSpaces(c.in); got != c.want {
			t.Errorf("collapseJaSpaces(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestVoicevoxUnreachable(t *testing.T) {
	// A closed port → BadGateway with the engine-unreachable code, not a panic.
	_, aerr := voicevoxSynthesize(t.Context(), "http://127.0.0.1:1", "x。", "3", 1, false)
	if aerr == nil || aerr.status != http.StatusBadGateway {
		t.Fatalf("want 502, got %+v", aerr)
	}
}

func TestTTSRoutes(t *testing.T) {
	clearTTSEnv(t)
	srv, _ := fakeVoicevox(t)
	mux := http.NewServeMux()
	registerTTSRoutes(mux, config{voicevoxURL: srv.URL})

	// status → voicevox ready true (engine /version reachable), polly false (no region).
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tts/status", nil))
	var st struct {
		Providers map[string]struct {
			Ready   bool `json:"ready"`
			Enabled bool `json:"enabled"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("status body: %v", err)
	}
	if !st.Providers["voicevox"].Ready {
		t.Error("voicevox should be ready")
	}
	if !st.Providers["voicevox"].Enabled {
		t.Error("voicevox should be enabled by default")
	}
	if st.Providers["polly"].Ready {
		t.Error("polly should be not-ready without an AWS region")
	}

	// synthesize (auto) → voicevox: audio/wav bytes + X-TTS-Provider header.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/tts/synthesize", strings.NewReader(`{"text":"やあ。","voice":"3","speed":1}`))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("synthesize status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("content-type = %q, want audio/wav", ct)
	}
	if p := rec.Header().Get("X-TTS-Provider"); p != "voicevox" {
		t.Errorf("X-TTS-Provider = %q, want voicevox", p)
	}
	if !strings.HasPrefix(rec.Body.String(), "WAV") {
		t.Errorf("body not WAV: %q", rec.Body.String())
	}

	// empty text → 400.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/tts/synthesize", strings.NewReader(`{"text":"  "}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty text status = %d, want 400", rec.Code)
	}

	// explicit polly without AWS config → 503 (unavailable), not a panic.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/tts/synthesize", strings.NewReader(`{"text":"hi.","provider":"polly"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("polly status = %d, want 503", rec.Code)
	}

	// unknown provider → 501.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/tts/synthesize", strings.NewReader(`{"text":"hi.","provider":"nope"}`)))
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("unknown provider status = %d, want 501", rec.Code)
	}
}

// TestTTSAutoNoProviders — with auto + Japanese and neither the engine nor polly present,
// voicevox's 502 comes back unchanged: the case where nothing is left to catch the request.
func TestTTSAutoNoProviders(t *testing.T) {
	clearTTSEnv(t)
	mux := http.NewServeMux()
	registerTTSRoutes(mux, config{voicevoxURL: "http://127.0.0.1:1"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/tts/synthesize", strings.NewReader(`{"text":"やあ。","lang":"ja"}`)))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if p := rec.Header().Get("X-TTS-Provider"); p != "" {
		t.Errorf("X-TTS-Provider = %q, want empty on error", p)
	}
}

// TestChooseTTSProvider is the pure-function test of the selection table in docs/log/24.
func TestChooseTTSProvider(t *testing.T) {
	cases := []struct {
		name                        string
		pref, lang                  string
		engineOff, vvReady, plReady bool
		want                        string
	}{
		{"明示voicevox", "voicevox", "en", false, false, true, "voicevox"},
		{"明示polly", "polly", "ja", false, true, true, "polly"},
		{"日本語×engine ready→ずんだもん", "auto", "ja", false, true, true, "voicevox"},
		{"auto言語×engine ready→ずんだもん", "", "auto", false, true, true, "voicevox"},
		{"日本語×engine不在→Polly JP", "auto", "ja", false, false, true, "polly"},
		{"日本語×engine無効→Polly JP", "auto", "ja", true, true, true, "polly"},
		{"非日本語→Polly", "auto", "en", false, true, true, "polly"},
		{"非日本語×Polly不在→voicevox受け皿", "auto", "en", false, true, false, "voicevox"},
		{"日本語×両方不在→voicevox(502)", "auto", "ja", false, false, false, "voicevox"},
	}
	for _, c := range cases {
		if got := chooseTTSProvider(c.pref, c.lang, c.engineOff, c.vvReady, c.plReady); got != c.want {
			t.Errorf("%s: chooseTTSProvider(%q,%q,%v,%v,%v) = %q, want %q",
				c.name, c.pref, c.lang, c.engineOff, c.vvReady, c.plReady, got, c.want)
		}
	}
}

// TestTTSAdminToggleSetting checks through the route that with auto + Japanese and the
// engine stopped, the fallback lands on polly when Polly is configured. Polly is never
// actually called: a fake region sends the SDK out to real AWS, so the unit test of
// chooseTTSProvider and the enabled/managed fields of status stand in for it. Here the
// admin toggle goes off and status has to reflect it.
func TestTTSAdminToggleSetting(t *testing.T) {
	clearTTSEnv(t)
	srv, _ := fakeVoicevox(t)
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	mgr := &manager{store: st}
	mux := http.NewServeMux()
	registerTTSRoutes(mux, config{voicevoxURL: srv.URL, mgr: mgr})

	// Flip the setting off directly (PUT needs super_admin auth, so go through the store)
	// and watch status.enabled drop, i.e. the engineOff branch of routing takes effect.
	if err := st.SetSetting(t.Context(), ttsEngineSetting, "off"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tts/status", nil))
	var status struct {
		Providers map[string]struct {
			Ready   bool `json:"ready"`
			Enabled bool `json:"enabled"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("status body: %v", err)
	}
	if status.Providers["voicevox"].Enabled {
		t.Error("voicevox should be disabled after tts_engine=off")
	}
	if !status.Providers["voicevox"].Ready {
		t.Error("readiness probe should still see the engine")
	}

	// Engine disabled and no polly: auto still falls to voicevox and synthesis succeeds.
	// It is the last resort, and a ready engine means sound still comes out.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/tts/synthesize", strings.NewReader(`{"text":"やあ。"}`)))
	if rec.Code != http.StatusOK {
		t.Errorf("synthesize with engine off = %d, want 200 (last-resort voicevox)", rec.Code)
	}
}

// TestTTSDict — the tenant-wide reading dictionary: what is stored in the setting must be
// readable through GET /api/tts/dict, which is open to every user. PUT /api/admin/tts/dict
// needs super_admin auth, so the value is written through the store, as in the admin-toggle
// test. A configuration with no store returns an empty string.
func TestTTSDict(t *testing.T) {
	clearTTSEnv(t)

	// No store (test configuration): an empty dictionary.
	mux := http.NewServeMux()
	registerTTSRoutes(mux, config{voicevoxURL: "http://127.0.0.1:1"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tts/dict", nil))
	var got struct{ Dict string }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("dict body: %v", err)
	}
	if got.Dict != "" {
		t.Errorf("dict without store = %q, want empty", got.Dict)
	}

	// With a store, the setting's contents come back verbatim.
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	const dict = "GPT-4=ジーピーティーフォー\n# コメント\nk-k1=ケーケーワン"
	if err := st.SetSetting(t.Context(), ttsDictSetting, dict); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	mux = http.NewServeMux()
	registerTTSRoutes(mux, config{voicevoxURL: "http://127.0.0.1:1", mgr: &manager{store: st}})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tts/dict", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("dict body: %v", err)
	}
	if got.Dict != dict {
		t.Errorf("dict = %q, want %q", got.Dict, dict)
	}
}

// TestTTSSpeakers — the character-list proxy: the engine's /speakers is converted into
// character names plus their talk styles, with speaker numbers stringified. Singing styles
// (type != "talk") are excluded, and a character left with no talk style is dropped. The
// second call is served from cache without touching the engine; no engine is a 502.
func TestTTSSpeakers(t *testing.T) {
	clearTTSEnv(t)

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/speakers" {
			http.NotFound(w, r)
			return
		}
		hits++
		_, _ = w.Write([]byte(`[
			{"name":"ずんだもん","styles":[{"id":3,"name":"ノーマル"},{"id":1,"name":"あまあま","type":"talk"}]},
			{"name":"波音リツ","styles":[{"id":9,"name":"ノーマル","type":"talk"},{"id":65,"name":"クイーン","type":"talk"},{"id":100,"name":"ハミング","type":"frame_decode"}]},
			{"name":"歌唱専用","styles":[{"id":200,"name":"ソング","type":"sing"}]}
		]`))
	}))
	defer srv.Close()

	mux := http.NewServeMux()
	registerTTSRoutes(mux, config{voicevoxURL: srv.URL})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tts/speakers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("speakers = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got struct{ Speakers []ttsSpeaker }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("speakers body: %v", err)
	}
	want := []ttsSpeaker{
		{Name: "ずんだもん", Styles: []ttsSpeakerStyle{{ID: "3", Name: "ノーマル"}, {ID: "1", Name: "あまあま"}}},
		{Name: "波音リツ", Styles: []ttsSpeakerStyle{{ID: "9", Name: "ノーマル"}, {ID: "65", Name: "クイーン"}}},
	}
	if !reflect.DeepEqual(got.Speakers, want) {
		t.Errorf("speakers = %+v, want %+v", got.Speakers, want)
	}

	// The second call comes from the cache and does not hit the engine.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tts/speakers", nil))
	if rec.Code != http.StatusOK || hits != 1 {
		t.Errorf("cached speakers: code=%d hits=%d, want 200 / 1", rec.Code, hits)
	}

	// No engine: 502.
	mux = http.NewServeMux()
	registerTTSRoutes(mux, config{voicevoxURL: "http://127.0.0.1:1"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tts/speakers", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("speakers without engine = %d, want 502", rec.Code)
	}
}
