package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		writeJSON(w, http.StatusOK, map[string]any{"speedScale": 1.0, "kana": "テスト"})
	})
	mux.HandleFunc("POST /synthesis", func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		ss, _ := m["speedScale"].(float64)
		w.Header().Set("Content-Type", "audio/wav")
		// Encode the speedScale back into the "WAV" so the caller can verify injection.
		_, _ = w.Write([]byte("WAVspeed=" + strings.TrimRight(strings.TrimRight(
			formatFloat(ss), "0"), ".")))
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0.14.0"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &gotSpeaker
}

func formatFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func TestVoicevoxSynthesize(t *testing.T) {
	srv, gotSpeaker := fakeVoicevox(t)

	wav, aerr := voicevoxSynthesize(t.Context(), srv.URL, "こんにちは。", "3", 1.25)
	if aerr != nil {
		t.Fatalf("synthesize: %+v", aerr)
	}
	if *gotSpeaker != "3" {
		t.Errorf("speaker = %q, want 3", *gotSpeaker)
	}
	if got := string(wav); got != "WAVspeed=1.25" {
		t.Errorf("wav = %q, want WAVspeed=1.25 (speedScale not injected?)", got)
	}

	// Empty voice defaults to ずんだもん (speaker 3).
	if _, aerr := voicevoxSynthesize(t.Context(), srv.URL, "テスト。", "", 0); aerr != nil {
		t.Fatalf("default-voice synthesize: %+v", aerr)
	}
	if *gotSpeaker != "3" {
		t.Errorf("default speaker = %q, want 3", *gotSpeaker)
	}
}

func TestVoicevoxUnreachable(t *testing.T) {
	// A closed port → BadGateway with the engine-unreachable code, not a panic.
	_, aerr := voicevoxSynthesize(t.Context(), "http://127.0.0.1:1", "x。", "3", 1)
	if aerr == nil || aerr.status != http.StatusBadGateway {
		t.Fatalf("want 502, got %+v", aerr)
	}
}

func TestTTSRoutes(t *testing.T) {
	srv, _ := fakeVoicevox(t)
	mux := http.NewServeMux()
	registerTTSRoutes(mux, config{voicevoxURL: srv.URL})

	// status → voicevox ready true (engine /version reachable), polly false.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tts/status", nil))
	var st struct {
		Providers map[string]struct {
			Ready bool `json:"ready"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("status body: %v", err)
	}
	if !st.Providers["voicevox"].Ready {
		t.Error("voicevox should be ready")
	}
	if st.Providers["polly"].Ready {
		t.Error("polly should be not-ready in Phase 1")
	}

	// synthesize → audio/wav bytes + X-TTS-Provider header.
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

	// polly provider → 501 (not implemented in Phase 1).
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/tts/synthesize", strings.NewReader(`{"text":"hi.","provider":"polly"}`)))
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("polly status = %d, want 501", rec.Code)
	}
}
