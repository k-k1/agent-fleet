// tts.go — CP-native な音声読み上げ（TTS）。エージェント回答テキストを VOICEVOX
// エンジン（ずんだもん等）または AWS Polly で合成し音声バイト列を返す。docs/log/24 + ADR0013。
//
// チャット（docs/log/19）が「Agent が headless CLI を実行 → CP がプロキシ」なのとは責務が
// 異なり、TTS は CP が外部サービス（VOICEVOX HTTP / Polly SDK）を直接叩くだけ。CP の
// 外向き通信は egress 制限外（oauth_google.go と同様）なので allowlist 変更は不要。
// バイナリ応答は git_lfs.go の octet-stream と同じ要領（base64 は使わない）。
//
// Phase 2（docs/log/24）: polly プロバイダ（IAM ロール, tts_polly.go）、auto ルーティング
// （下の chooseTTSProvider）、ECS オンデマンド起動（tts_ecs.go + /api/admin/tts）。
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

// ttsHTTP は VOICEVOX への合成呼び出し用。1 文ずつの合成なので短命だが、コールド時の
// エンジンを見越して余裕のあるタイムアウトにする。
var ttsHTTP = &http.Client{Timeout: 30 * time.Second}

type ttsSynthReq struct {
	Text          string  `json:"text"`
	Provider      string  `json:"provider"`      // "" | "auto" | "voicevox" | "polly"
	Voice         string  `json:"voice"`         // voicevox の speaker 番号（例 "3"=ずんだもん）
	PollyVoice    string  `json:"pollyVoice"`    // Polly の VoiceId（例 "Takumi"）。auto で Polly に落ちた時も使う
	Speed         float64 `json:"speed"`         // 0.5〜2.0（voicevox speedScale / Polly prosody rate）。0/未指定=1.0
	Lang          string  `json:"lang"`          // 言語ヒント: "auto" | "ja" | "en"（設定 outputLanguage を再利用）
	EnKana        bool    `json:"enkana"`        // 英単語をカタカナ英語に前処理してから合成（voicevox のみ, enkana.go）
	ParticlePause bool    `json:"particlePause"` // 設定 ttsParticlePause（クライアントが挿入した読点の間を詰める。voicevox のみ）
}

// ttsProvider は合成エンジンのプロバイダ抽象（docs/log/24）。chat_providers.go の
// chatProviders と同型の map dispatch。テキスト前処理（enkana / ユーザー辞書）は
// プロバイダの外（ハンドラ / クライアント）が担い、ここは「テキスト → 音声」だけ。
type ttsProvider interface {
	// Synthesize は text を合成して音声バイト列と MIME タイプを返す。
	Synthesize(ctx context.Context, text string, o voiceOpts) (audio []byte, mime string, aerr *apiError)
	// Ready はエンジン到達性（voicevox の /version 等）。Polly は設定の有無で即答。
	Ready(ctx context.Context) bool
}

type voiceOpts struct {
	voice         string  // プロバイダ固有の話者 ID
	speed         float64 // 0.5〜2.0。0/未指定 = 1.0
	lang          string  // "auto" | "ja" | "en"
	particlePause bool    // 助詞の小休止（voicevox のみ）の間を詰める
}

// chooseTTSProvider は auto（既定）の使い分けを決める純関数（docs/log/24 の表）。
// ずんだもんは日本語専用・要起動、Polly は多言語・常時稼働という非対称を吸収する:
//   - 明示指定（voicevox / polly）はそのまま。
//   - 非日本語（lang=en）→ Polly。Polly 不在なら enkana 併用前提で voicevox が受け皿。
//   - 日本語（ja / auto）→ engine が有効かつ ready なら voicevox、不在（起動中/無効）なら
//     Polly JP に自動フォールバック（次の文の Ready 復帰で voicevox に戻る）。
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
	return "voicevox" // 両方不在: voicevox のエラー（502）をそのまま返す
}

// ttsEngineSetting は管理者トグルの設定キー。"off" のときだけ voicevox への
// ルーティングを止める（"" = 既定で有効。dev の外部管理エンジンは従来どおり動く）。
const ttsEngineSetting = "tts_engine"

// ttsDictSetting はテナント共通の読み仮名辞書（1 行 "表記=読み"、クライアントの
// ユーザー辞書と同じ書式）。管理者が /api/admin/tts/dict で編集し、全ユーザーの
// クライアントが GET /api/tts/dict で取得してユーザー辞書と合成する（同じ表記は
// ユーザー辞書が勝つ＝上書き）。適用はクライアント側（docs/log/24）。
const ttsDictSetting = "tts_dict"

// registerTTSRoutes は CP-native TTS のルートを登録する（buildMux から呼ぶ）。認証は
// 既定の authGate 配下（exempt しない）＝ログイン必須。ワークスペース非依存なので
// withResolved は使わない。cfg.mgr が nil（一部テスト）でも合成ルートは動く。
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
			// 英語をカタカナ英語に前処理（VOICEVOX は英語綴りを読めないため）。Polly は
			// 英語をそのまま読めるので、voicevox に決まったときだけ適用する。docs/log/24。
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
		// 実際に使ったプロバイダ。auto のフォールバック発生を UI が表示できるようにする。
		w.Header().Set("X-TTS-Provider", name)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(audio)
	})

	// エンジン到達性。フロントはトグルの活性/「準備中」表示に使う。managed（ECS 管理下）の
	// ときは ECS サービス状態（running/starting/stopped）も添える（readiness ゲートの可視化）。
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

	// キャラ一覧（VOICEVOX の /speakers のプロキシ）。設定 UI の「キャラクター」選択が
	// 実エンジンのデータで選択肢（キャラ名・スタイル・speaker 番号）を出すために使う —
	// speaker 番号を静的に持つと実エンジンとずれる（docs/log/24）。60s キャッシュで設定画面の
	// 再描画がエンジンを叩き続けないようにする。エンジン停止中は 502（UI は現在の設定を
	// 表示するだけの読み取り専用にフォールバック）。
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

	// テナント共通の読み仮名辞書。全ユーザー（要ログイン）が読める。クライアントは起動時に
	// 取得してユーザー辞書と合成する（適用はクライアント側なので合成ハンドラは触らない）。
	mux.HandleFunc("GET /api/tts/dict", func(w http.ResponseWriter, r *http.Request) {
		dict := ""
		if settings != nil {
			dict, _ = settings.GetSetting(r.Context(), ttsDictSetting)
		}
		writeJSON(w, http.StatusOK, map[string]any{"dict": dict})
	})

	// 管理者トグル（super_admin）: VOICEVOX エンジンの有効/無効。ECS 管理下なら desired
	// count 0↔1 を切り替え（オンデマンド起動・停止中コスト 0）、常に setting へ意図を記録
	// する（egress の SettingsStore と同じ流儀）。docs/log/24 Phase 2。
	adm := ttsAdminAPI{memberAuth{cfg.mgr}, settings, eng, vv, pl}
	mux.HandleFunc("GET /api/admin/tts", adm.withSuperAdmin(adm.get))
	mux.HandleFunc("PUT /api/admin/tts", adm.withSuperAdmin(adm.put))
	mux.HandleFunc("PUT /api/admin/tts/dict", adm.withSuperAdmin(adm.putDict))
}

// --- voicevox プロバイダ -------------------------------------------------------

// voicevoxProvider は cfg.voicevoxURL の VOICEVOX エンジンを指すアダプタ。Ready は
// 短い TTL でキャッシュする（auto ルーティングが文ごとに呼ぶため、毎文 /version を
// 叩かない。エンジンの起動/停止は数秒粒度で追従できれば十分）。
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

// collapseJaSpaces は和文文字に隣接する半角スペースを除去する。「submit 時に」
// 「green です」のような英単語と日本語の間の空白は組版上のもので読みの間ではないが、
// VOICEVOX はスペースをポーズとして合成し読みが途切れる。英単語同士の空白
// （"tsc / vitest" 等）は語の区切りとして残す（連続分は 1 つに正規化）。
// 全角スペースは触らない（意図した「間」の表現として使われる）。
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
	case r >= 0x3040 && r <= 0x30FF: // ひらがな・カタカナ・ー
		return true
	case r >= 0x4E00 && r <= 0x9FFF: // CJK 統合漢字
		return true
	case r >= 0x3001 && r <= 0x303F: // 和文記号（、。「」等。全角スペース U+3000 は除く）
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // 全角英数・全角記号・半角カナ
		return true
	}
	return false
}

// particlePauseScale は「助詞のあとで一呼吸」(ttsParticlePause) ON 時に読点ポーズ
// （pause_mora.vowel_length）へ掛ける係数。挿入頻度が高い（を・は・で・に・と＋漢字の
// 境界ごと）ため、句点相当の間だと文全体がもたつく — 6 割程度に詰めて「軽い息継ぎ」に
// 近づける。地の文にもとから含まれる読点も同じ間になるが、ON 時は全体を詰める方向で
// 一貫させる（クライアントは区別せずテキストへ「、」を挿し込んでいるため）。
const particlePauseScale = 0.6

// scalePauseMoras は audio_query の accent_phrases[].pause_mora.vowel_length を
// scale 倍する（in-place）。pause_mora は読点等の直後にだけ存在する（null のことが多い）。
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

// voicevoxSynthesize は VOICEVOX の 2 段 API（audio_query → synthesis）で WAV を得る。
// speaker=3 がずんだもん・ノーマル。speed は audio_query の speedScale を上書きする。
func voicevoxSynthesize(ctx context.Context, base, text, voice string, speed float64, particlePause bool) ([]byte, *apiError) {
	base = strings.TrimRight(base, "/")
	text = collapseJaSpaces(text)
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

	// 2) 合成パラメータを上書き。前後の無音（既定 0.1s ずつ）は文単位の逐次再生では
	// 文境界ごとに約 0.2s の待機として毎回体感されるため短縮する。文間の「間」は
	// フロントが再生スケジュール（SENTENCE_GAP）で管理する。speedScale は 0/未指定
	// なら audio_query の返した値（1.0）のまま。
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

	// 3) synthesis — パラメータ JSON を渡して WAV を得る。
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

// ttsSpeaker は /api/tts/speakers の 1 キャラ。styles の id は speaker 番号（クライアントが
// 合成リクエストに使う値なので文字列で返す）。
type ttsSpeakerStyle struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type ttsSpeaker struct {
	Name   string            `json:"name"`
	Styles []ttsSpeakerStyle `json:"styles"`
}

// voicevoxSpeakers はエンジンの GET /speakers をキャラ名＋トーク用スタイル一覧に変換する。
// 歌唱系スタイル（type が "talk" 以外。0.14 以降の song/humming 等）は読み上げに使えない
// ので除き、トークスタイルが 1 つも無いキャラは落とす。
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

// --- 管理者トグル（/api/admin/tts） -------------------------------------------

// ttsAdminAPI は VOICEVOX エンジンの管理者トグルのハンドラ組。enabled の真実源は
// ECS 管理下なら desired count（ライブ）、非管理（dev の外部エンジン）なら setting。
type ttsAdminAPI struct {
	memberAuth
	settings store.SettingsStore // nil あり（テスト）
	eng      *ttsEngineECS       // nil = ECS 管理外
	vv       *voicevoxProvider
	pl       *pollyProvider
}

// get (GET /api/admin/tts) — トグル状態＋エンジン到達性/ECS 状態。
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
			enabled = desired >= 1 // ECS 管理下は desired count が真実源
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

// put (PUT /api/admin/tts) — body {enabled:bool}。ECS 管理下なら desired 0↔1、
// あわせて setting に意図を記録（非管理はこれがルーティングの ON/OFF になる）。監査あり。
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

// putDict (PUT /api/admin/tts/dict) — body {dict:string}。テナント共通の読み仮名辞書を
// 丸ごと差し替える（1 行 "表記=読み"、パース規約はクライアントの parseUserDict と同じ）。
// 監査あり（中身は個票で追えるようサイズだけ記録）。
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
