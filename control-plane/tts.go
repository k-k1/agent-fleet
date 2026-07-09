// tts.go — CP-native な音声読み上げ（TTS）。エージェント回答テキストを VOICEVOX
// エンジン（ずんだもん等）で合成し WAV を返す。docs/24 + ADR0013。
//
// チャット（docs/19）が「Agent が headless CLI を実行 → CP がプロキシ」なのとは責務が
// 異なり、TTS は CP が外部 HTTP サービス（VOICEVOX / 将来 Polly）を直接叩くだけ。CP の
// 外向き通信は egress 制限外（oauth_google.go と同様）なので allowlist 変更は不要。
// バイナリ応答は git_lfs.go の octet-stream と同じ要領（base64 は使わない）。
//
// Phase 1 は voicevox プロバイダのみ。Polly（IAM ロール）と AWS の ECS オンデマンド
// 起動は Phase 2（docs/24）。使い分け（auto）のルーティングも Phase 2 で CP に入る。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ttsHTTP は VOICEVOX への合成呼び出し用。1 文ずつの合成なので短命だが、コールド時の
// エンジンを見越して余裕のあるタイムアウトにする。
var ttsHTTP = &http.Client{Timeout: 30 * time.Second}

type ttsSynthReq struct {
	Text     string  `json:"text"`
	Provider string  `json:"provider"` // "" | "auto" | "voicevox" | "polly"
	Voice    string  `json:"voice"`    // provider 固有。voicevox は speaker 番号（例 "3"=ずんだもん）
	Speed    float64 `json:"speed"`    // 0.5〜2.0（voicevox の speedScale）。0/未指定=1.0
}

// registerTTSRoutes は CP-native TTS のルートを登録する（buildMux から呼ぶ）。認証は
// 既定の authGate 配下（exempt しない）＝ログイン必須。ワークスペース非依存なので
// withResolved は使わない。
func registerTTSRoutes(mux *http.ServeMux, cfg config) {
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
		// Phase 1: auto / voicevox → voicevox。polly は Phase 2 まで未実装。
		switch req.Provider {
		case "", "auto", "voicevox":
			// ok
		default:
			writeAPIErr(w, &apiError{http.StatusNotImplemented, "tts_provider_unavailable", "provider not available yet: " + req.Provider})
			return
		}
		wav, aerr := voicevoxSynthesize(r.Context(), cfg.voicevoxURL, text, req.Voice, req.Speed)
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-TTS-Provider", "voicevox")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wav)
	})

	// エンジン到達性。フロントはトグルの活性/「準備中」表示に使う。
	mux.HandleFunc("GET /api/tts/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"providers": map[string]any{
				"voicevox": map[string]any{"ready": voicevoxReady(r.Context(), cfg.voicevoxURL)},
				// Polly は Phase 2。UI 側で未実装として扱えるよう ready=false を返す。
				"polly": map[string]any{"ready": false},
			},
		})
	})
}

// voicevoxSynthesize は VOICEVOX の 2 段 API（audio_query → synthesis）で WAV を得る。
// speaker=3 がずんだもん・ノーマル。speed は audio_query の speedScale を上書きする。
func voicevoxSynthesize(ctx context.Context, base, text, voice string, speed float64) ([]byte, *apiError) {
	base = strings.TrimRight(base, "/")
	speaker := strings.TrimSpace(voice)
	if speaker == "" {
		speaker = "3" // ずんだもん（ノーマル）
	}

	// 1) audio_query — text と speaker から合成パラメータ JSON を得る。
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

	// 2) speedScale を上書き（0/未指定は 1.0 のまま）。
	if speed > 0 {
		var m map[string]any
		if json.Unmarshal(aqBody, &m) == nil {
			m["speedScale"] = clampSpeed(speed)
			if nb, e := json.Marshal(m); e == nil {
				aqBody = nb
			}
		}
	}

	// 3) synthesis — パラメータ JSON を渡して WAV を得る。
	sReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/synthesis?speaker="+url.QueryEscape(speaker), bytes.NewReader(aqBody))
	sReq.Header.Set("Content-Type", "application/json")
	sReq.Header.Set("Accept", "audio/wav")
	sResp, err := ttsHTTP.Do(sReq)
	if err != nil {
		return nil, &apiError{http.StatusBadGateway, "tts_engine_unreachable", "voicevox unreachable: " + err.Error()}
	}
	defer sResp.Body.Close()
	wav, _ := io.ReadAll(sResp.Body)
	if sResp.StatusCode != http.StatusOK {
		return nil, &apiError{http.StatusBadGateway, "tts_engine_error", "voicevox synthesis failed: " + strings.TrimSpace(string(wav))}
	}
	return wav, nil
}

// voicevoxReady は /version が 200 を返すかで到達性を判定する（短いタイムアウト）。
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
