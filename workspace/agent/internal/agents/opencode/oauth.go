package opencode

// opencode Console アカウント（opencode.ai の org＝workspace）の OAuth device flow。
// APIキー方式（auth.go の env 注入）と併存する第2の認証経路で、どちらか一方だけでも
// 両方でも成立する（実測: 有料モデルのゲートは
// `OPENCODE_API_KEY || Console接続あり || 明示apiKey` の OR）。
//
// 経路は CLI の PTY スクレイプではなく `opencode serve` の HTTP API（実測 1.18.13、
// OpenAPI /doc が正）。cursor/codex のフロー型（start→poll）と同じ形が最初から
// 構造化された JSON で取れる:
//
//	POST /api/integration/opencode/connect/oauth {methodID:"device",inputs:{}}
//	  → {data:{attemptID,url,instructions,mode:"auto"|"code",time:{created,expires}}}
//	GET  /api/integration/attempt/{attemptID}
//	  → {data:{status:"pending"|"complete"|"failed"|"expired", message?}}
//	DELETE /api/integration/attempt/{attemptID}      … 中断
//	GET  /api/integration/opencode
//	  → {data:{connections:[{type:"credential",id,label} | {type:"env",name}]}}
//	DELETE /auth/opencode                            … 切断（資格情報の削除）
//
// device の mode は "auto"（opencode 側が自前でトークンをポーリングする）なので、
// Console からコードを貼らせる必要はない — cursor と同じ「URL を出して poll」型。
//
// daemon を経由する利点は反映タイミング（docs/54）: ログイン成立時に daemon 内で
// integration.connection.updated が publish され、opencode プラグインがそれを購読して
// Console org の /api/config を取り直し catalog.reload() する。つまり共有 daemon 上の
// managed セッションは**再起動なしで**新しいモデル集合を見る。こちら側で古くなるのは
// models.go の 60 秒キャッシュだけなので、成立時に明示的に落とす。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// integrationID/methodID は opencode 側の固定 ID（実測）。opencode Console アカウントは
// integration "opencode" の oauth メソッド "device" として公開されている。
const (
	oauthIntegrationID = "opencode"
	oauthMethodID      = "device"
)

// oauthDaemon は認証 API を叩く serve daemon を（必要なら起動して）返す。
// テストが差し替える継ぎ目。
var oauthDaemon = func() (string, error) {
	addr, _, err := Serve().Ensure()
	return addr, err
}

// oauthProbe は「今すでに動いている」daemon の URL を返す（起動はしない）。
// /connections のポーリングから呼ばれるので、状態表示のために daemon を
// 起こすことはしない。テストが差し替える継ぎ目。
var oauthProbe = func() (string, bool) {
	addr := serveAddr()
	if !healthy(addr) {
		return "", false
	}
	return addr, true
}

// oauthClient は制御系の短タイムアウト HTTP（serveClient と同じ性格）。
var oauthClient = &http.Client{Timeout: 15 * time.Second}

// --- daemon 呼び出し ---------------------------------------------------------

// attemptInfo is the `data` of a connect/oauth response（実測 OpenAPI）。
type attemptInfo struct {
	AttemptID    string `json:"attemptID"`
	URL          string `json:"url"`
	Instructions string `json:"instructions"`
	Mode         string `json:"mode"` // "auto" | "code"
	Time         struct {
		Created float64 `json:"created"`
		Expires float64 `json:"expires"`
	} `json:"time"`
}

// attemptStatus is the `data` of an attempt status response（実測 OpenAPI）。
type attemptStatus struct {
	Status  string `json:"status"` // pending | complete | failed | expired
	Message string `json:"message"`
}

// integrationInfo is the `data` of GET /api/integration/{id}（実測 OpenAPI）。
type integrationInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Connections []struct {
		Type  string `json:"type"` // "credential"（key/oauth 由来）| "env"
		ID    string `json:"id"`
		Label string `json:"label"` // device 接続では Console org 名（実測の label 解決）
		Name  string `json:"name"`  // env 接続の変数名
	} `json:"connections"`
}

// envelope はこの API 群共通の {location, data} 包み。
type envelope[T any] struct {
	Data T `json:"data"`
}

func daemonJSON[T any](method, addr, path string, body any, out *envelope[T]) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, addr+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := oauthClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2<<10))
		return fmt.Errorf("opencode serve %s %s: %s: %s", method, path, res.Status, bytes.TrimSpace(msg))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// --- 状態 -------------------------------------------------------------------

// oauthState is the cached view of the Console connection for /connections.
type oauthState struct {
	connected bool
	label     string
	known     bool // 一度でも daemon から読めたか（stale-if-error の判定に使う）
}

var (
	oauthMu    sync.Mutex
	oauthCache oauthState
	oauthAt    time.Time
)

// oauthStatusTTL: /connections は数秒おきに来るので、daemon への往復は間引く。
const oauthStatusTTL = 15 * time.Second

// invalidateOAuthStatus drops the cached connection view so the next /connections
// poll reflects a login/logout at once instead of after the TTL.
func invalidateOAuthStatus() {
	oauthMu.Lock()
	oauthAt = time.Time{}
	oauthMu.Unlock()
}

// oauthStatus reports the Console-account connection for Status(). daemon が居ない
// ときは「起こしてまで確かめない」— 最後に読めた値を stale として返し、一度も読めて
// いなければ unknown（connected=false, known=false）を返す。
func oauthStatus() oauthState {
	oauthMu.Lock()
	defer oauthMu.Unlock()
	if !oauthAt.IsZero() && time.Since(oauthAt) < oauthStatusTTL {
		return oauthCache
	}
	addr, up := oauthProbe()
	if !up {
		return oauthCache // stale-if-error: daemon 停止で接続表示を落とさない
	}
	var env envelope[integrationInfo]
	if err := daemonJSON("GET", addr, "/api/integration/"+oauthIntegrationID, nil, &env); err != nil {
		return oauthCache
	}
	st := oauthState{known: true}
	for _, c := range env.Data.Connections {
		// env 由来（OPENCODE_API_KEY）は APIキー方式の表示であって Console 接続ではない。
		if c.Type == "credential" {
			st.connected = true
			st.label = c.Label
			break
		}
	}
	oauthCache = st
	oauthAt = time.Now()
	return st
}

// --- ハンドラ ---------------------------------------------------------------

// HandleOAuthStart begins the Console device flow and returns the verification URL
// with a flow_id the Console polls. POST /connections/opencode/oauth/start.
func HandleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if !Available() {
		httpx.WriteErr(w, http.StatusConflict, "opencode_unsupported", "opencode が見つかりません（旧イメージの可能性）")
		return
	}
	addr, err := oauthDaemon()
	if err != nil {
		// managed opencode を切っている場合ここに来る。CLI 対話ログイン
		// （`opencode auth login`）は端末セッションからなら従来どおり可能。
		httpx.WriteErr(w, http.StatusServiceUnavailable, "serve_unavailable", "opencode serve を起動できませんでした: "+err.Error())
		return
	}
	var env envelope[attemptInfo]
	body := map[string]any{"methodID": oauthMethodID, "inputs": map[string]string{}}
	if err := daemonJSON("POST", addr, "/api/integration/"+oauthIntegrationID+"/connect/oauth", body, &env); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "oauth_start_failed", err.Error())
		return
	}
	at := env.Data
	if at.AttemptID == "" || at.URL == "" {
		httpx.WriteErr(w, http.StatusBadGateway, "no_url", "opencode が認証 URL を返しませんでした")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"flow_id":      at.AttemptID,
		"url":          at.URL,
		"user_code":    userCode(at.URL, at.Instructions),
		"instructions": at.Instructions,
		"mode":         at.Mode,
		"expires":      at.Time.Expires,
	})
}

// userCodeRe is the fallback source: opencode phrases the instruction as
// "Enter code: ABCD-EFGH"（実測）。文言が変わっても URL 側は OAuth device flow の
// 標準パラメータなので、そちらを先に見る。
var userCodeRe = regexp.MustCompile(`\b([A-Z0-9]{4,8}-[A-Z0-9]{4,8})\b`)

// userCode extracts the device code to show as its own step. 取れなければ空 —
// Console は「リンクを開く」だけの表示に落ちる（URL 自体がコードを含むので成立する）。
func userCode(rawURL, instructions string) string {
	if u, err := url.Parse(rawURL); err == nil {
		for _, k := range []string{"user_code", "userCode", "code"} {
			if v := u.Query().Get(k); v != "" {
				return v
			}
		}
	}
	return userCodeRe.FindString(instructions)
}

type oauthPollReq struct {
	FlowID string `json:"flow_id"`
}

// HandleOAuthPoll reports whether the browser approval has completed. device flow は
// mode="auto"（opencode 側がトークンを自前でポーリングする）なので、こちらは
// attempt の状態を見るだけでよい。POST /connections/opencode/oauth/poll.
func HandleOAuthPoll(w http.ResponseWriter, r *http.Request) {
	var req oauthPollReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.FlowID == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "flow_id が必要です")
		return
	}
	addr, up := oauthProbe()
	if !up {
		httpx.WriteErr(w, http.StatusServiceUnavailable, "serve_unavailable", "opencode serve が停止しました")
		return
	}
	var env envelope[attemptStatus]
	if err := daemonJSON("GET", addr, "/api/integration/attempt/"+url.PathEscape(req.FlowID), nil, &env); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "oauth_poll_failed", err.Error())
		return
	}
	st := env.Data
	out := map[string]any{"status": st.Status, "connected": st.Status == "complete"}
	if st.Message != "" {
		out["message"] = st.Message
	}
	if st.Status == "complete" {
		// daemon 側のカタログはイベント購読で既に更新済み（docs/54）。こちらの
		// 60 秒キャッシュだけが古いので落とす — 起動モーダルと MCP list_models が
		// すぐ新しいモデル集合を見るように。
		InvalidateModels()
		invalidateOAuthStatus()
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// HandleOAuthCancel aborts an in-flight attempt so a half-finished flow doesn't sit
// in the daemon until it expires. POST /connections/opencode/oauth/cancel.
func HandleOAuthCancel(w http.ResponseWriter, r *http.Request) {
	var req oauthPollReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if addr, up := oauthProbe(); up && req.FlowID != "" {
		_ = daemonJSON[struct{}]("DELETE", addr, "/api/integration/attempt/"+url.PathEscape(req.FlowID), nil, nil)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"cancelled": true})
}

// HandleOAuthDisconnect removes the stored Console credential. APIキー（env 注入）は
// 別経路なのでこの操作では消えない。DELETE /connections/opencode/oauth.
func HandleOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	addr, up := oauthProbe()
	if !up {
		// daemon が居ないなら CLI で消す（同じ auth.json を見る）。
		if err := oauthLogoutCLI(); err != nil {
			httpx.WriteErr(w, http.StatusServiceUnavailable, "serve_unavailable", "opencode serve が停止しており切断できませんでした")
			return
		}
		invalidateOAuthStatus()
		InvalidateModels()
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "opencode"})
		return
	}
	if err := daemonJSON[struct{}]("DELETE", addr, "/auth/"+oauthIntegrationID, nil, nil); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "oauth_disconnect_failed", err.Error())
		return
	}
	invalidateOAuthStatus()
	InvalidateModels()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "opencode"})
}

// oauthLogoutCLI is the daemon-less fallback for disconnect.
var oauthLogoutCLI = func() error {
	if !Available() {
		return errors.New("opencode not installed")
	}
	return exec.Command("opencode", "auth", "logout", oauthIntegrationID).Run()
}
