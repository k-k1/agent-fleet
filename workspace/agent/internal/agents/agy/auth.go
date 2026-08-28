package agy

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/hostcaps"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// agy auth is WebUI-driven like claude's: the interactive TUI login (Google
// OAuth, PKCE, redirect_uri=antigravity.google/oauth-callback) prints an
// authorize URL and waits for a pasted code — there is no headless login
// subcommand, so we drive the real TUI through the shared agents.Flow PTY,
// scrape the URL, submit the code, then walk the first-run onboarding screens.
// The measured screen sequence (docs/32 Track A 実測, v1.1.4):
//
//	ログイン方式セレクタ（1=Google OAuth 既定 / 2=GCP project）
//	→ 認可 URL ＋コード貼付欄
//	→ カラースキーム（既定 "terminal"）
//	→ ToS ＋ Interactions データ収集トグル（既定オン → 必ずオフに倒す）
//	→ workspace trust（事前 trust 済みならスキップ）
//	→ メイン画面（"? for shortcuts" フッタ）
//
// 再ログイン（オンボーディング済み）ではセレクタ→URL→コードの後、画面列は出ず
// メイン画面に直行する — driveOnboarding は出た画面にだけ応答する。
// 資格は agy 自身が ~/.gemini/antigravity-cli/antigravity-oauth-token（平文）に
// 書く＝暗号ストア外・denylist 対象（fs.go）。

// trustRe（workspace trust 画面）と readyRe（メイン画面フッタ）、planRe（アカウント
// 行 email+plan）は usage.go（Track C）と共有。
var (
	selectorRe = regexp.MustCompile(`Select login method:`)
	urlRe      = regexp.MustCompile(`https://accounts\.google\.com/o/oauth2/auth\?\S+`)
	colorRe    = regexp.MustCompile(`Choose your color scheme:`)
	tosRe      = regexp.MustCompile(`Terms of Service & Data Use`)
)

// Key sequences for the TUI (bubbletea-style): plain ANSI arrows + CR.
const (
	keyEnter = "\r"
	keyDown  = "\x1b[B"
	keyRight = "\x1b[C"
)

// The pasted OAuth code is short-lived; don't keep orphan PTYs around.
const flowTTL = 10 * time.Minute

var flows = agents.NewFlowStore(flowTTL)

// hostcapsOK rejects an auth request on hosts where agy can't run (binary
// absent / no RDRAND) — the same gate as the Console's kind selector and
// BuildLaunch (docs/32 Track B 契約).
func hostcapsOK(w http.ResponseWriter) bool {
	supported, reason := hostcaps.AgyStatus()
	if !supported {
		httpx.WriteErr(w, http.StatusConflict, "agy_unsupported", reason)
	}
	return supported
}

type startReq struct {
	// Method selects the login route: "oauth" (default) or "gcp-project"
	// (M2 — the field is part of the API contract now, the route comes later).
	Method    string `json:"method"`
	ProjectID string `json:"project_id"`
}

// HandleStart launches the agy TUI, picks Google OAuth on the method selector,
// waits for the authorize URL, and returns it with a flow_id the client uses to
// submit the code. POST /connections/agy/start.
func HandleStart(w http.ResponseWriter, r *http.Request) {
	if !hostcapsOK(w) {
		return
	}
	var req startReq
	// The body is optional ({} / absent ⇒ oauth); tolerate an empty body rather
	// than requiring the Console to always send one.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	switch req.Method {
	case "", "oauth":
	case "gcp-project":
		httpx.WriteErr(w, http.StatusNotImplemented, "method_unsupported", "gcp-project 経路は M2 で実装予定です（docs/32）")
		return
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_method", "method は oauth か gcp-project を指定してください")
		return
	}
	if SignedIn() {
		// A signed-in agy shows no login selector — the flow below would just hang.
		httpx.WriteErr(w, http.StatusConflict, "already_connected", "すでに接続済みです。再認証するには一度切断してください。")
		return
	}
	flows.Reap()

	// Launch in a dedicated empty dir, pre-trusted so the onboarding's final
	// workspace-trust prompt targets a harmless path (never the whole home) and
	// is skipped outright.
	loginDir := filepath.Join(stateDir(), "login-flow")
	_ = os.MkdirAll(loginDir, 0o700)
	EnsureWorkspaceTrusted(loginDir)

	cmd := exec.Command("agy")
	cmd.Dir = loginDir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := agents.StartFlow(cmd)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "pty_failed", err.Error())
		return
	}

	// "Select login method:" — option 1 (Google OAuth) is pre-selected; Enter picks it.
	if f.WaitFor(selectorRe, 20*time.Second) == "" {
		f.Close()
		httpx.WriteErr(w, http.StatusBadGateway, "no_selector", "agy が ログイン方式セレクタを表示しませんでした")
		return
	}
	if _, err := f.Ptmx.Write([]byte(keyEnter)); err != nil {
		f.Close()
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	url := sanitizeAuthURL(f.WaitFor(urlRe, 20*time.Second))
	if url == "" {
		f.Close()
		httpx.WriteErr(w, http.StatusBadGateway, "no_url", "agy が認可 URL を表示しませんでした")
		return
	}
	id := flows.Put(f)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"flow_id": id, "url": url})
}

// sanitizeAuthURL undoes the TUI's OSC-8 hyperlink rendering of the authorize
// URL: the escape stream carries the URI twice (once as the link target, once
// as the visible text) with no separator, so after ANSI stripping the buffer
// reads "<url><url>]8;;" and urlRe's \S+ swallows it all — a mangled state
// param the OAuth server rejects (統合E2E実測). Keep the first copy and drop
// any trailing OSC remnant.
func sanitizeAuthURL(u string) string {
	const scheme = "https://"
	if rest := strings.TrimPrefix(u, scheme); rest != u {
		if i := strings.Index(rest, scheme); i >= 0 {
			u = u[:len(scheme)+i]
		}
	}
	return strings.TrimSuffix(u, "]8;;")
}

type completeReq struct {
	FlowID string `json:"flow_id"`
	Code   string `json:"code"`
}

// HandleComplete submits the pasted authorization code and walks the remaining
// onboarding screens to the main screen (data-collection toggle forced OFF).
// The flow's PTY/process is always cleaned up. POST /connections/agy/complete.
func HandleComplete(w http.ResponseWriter, r *http.Request) {
	if !hostcapsOK(w) {
		return
	}
	var req completeReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_code", "code is required")
		return
	}
	f := flows.Take(req.FlowID)
	if f == nil {
		httpx.WriteErr(w, http.StatusNotFound, "no_flow", "unknown or expired flow_id")
		return
	}
	defer f.Close()

	// Submit the code, then Enter as a separate keystroke after a short delay
	// (claude と同じ轍: 同一 write に載せた CR は入力欄に食われ得る).
	if _, err := f.Ptmx.Write([]byte(code)); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := f.Ptmx.Write([]byte(keyEnter)); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}

	if err := driveOnboarding(f, 60*time.Second); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "login_failed", "ログインが完了しませんでした（"+err.Error()+"）")
		return
	}
	// The main-screen header shows "email (plan)" — capture it for Status()'s
	// email/plan fields (AgyCard). Best-effort: the plan suffix can render a few
	// seconds late, and Status degrades fine without it.
	captureAccount(f, 8*time.Second)
	// Belt and braces on top of the ToS-screen toggle: pin the Interactions
	// data-collection opt-out in settings.json (agy is closed by then).
	enforceTelemetryOff()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": true})
}

// driveOnboarding answers the post-code onboarding screens as they appear, each
// exactly once, until the main screen is reached with the token on disk. A
// re-login (already onboarded) shows none of them. Returns nil on success;
// a timeout is the usual failure (wrong or expired code — agy prints no
// scrapeable error line, it just stays put).
func driveOnboarding(f *agents.Flow, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	done := map[string]bool{}
	// step answers screen `name` (matched by re on the accumulated output) once,
	// sending keys with small gaps so the TUI processes them as separate events.
	step := func(out, name string, re *regexp.Regexp, keys ...string) {
		if done[name] || !re.MatchString(out) {
			return
		}
		done[name] = true
		for _, k := range keys {
			_, _ = f.Ptmx.Write([]byte(k))
			time.Sleep(250 * time.Millisecond)
		}
	}
	for time.Now().Before(deadline) {
		out := f.Clean()
		if readyRe.MatchString(out) && SignedIn() {
			return nil
		}
		// カラースキーム: 既定 "terminal" を Enter で確定。
		step(out, "color", colorRe, keyEnter)
		// ToS: フォーカスはトグル上 → Enter でオフ（[x]→[ ]）、↓ で [Previous]、
		// → で [Done]、Enter で確定（実測列）。
		step(out, "tos", tosRe, keyEnter, keyDown, keyRight, keyEnter)
		// workspace trust: login-flow dir は事前 trust 済みで通常出ないが、出ても答える。
		// **Enter は盲打ちしない** — 既定の選択肢は上流の都合で入れ替わり、その日から
		// 「承認」が「終了」になる（claude 2.1.248 で実際に起きた）。必ず「Yes, I trust」
		// の行に乗ってから押す（trustprompt.go）。
		if !done["trust"] && trustRe.MatchString(out) {
			done["trust"] = true
			if err := answerTrustPrompt(f); err != nil {
				return err
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return errString("コードの誤り/期限切れ、またはオンボーディング画面の変化")
}

// HandleDisconnect logs agy out. v1.1.4 has no logout subcommand (TUI /logout
// only), and in a keyring-less container /logout amounts to deleting the token
// file — so that's what we do. DELETE /connections/agy.
func HandleDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := os.Remove(tokenPath()); err != nil && !os.IsNotExist(err) {
		httpx.WriteErr(w, http.StatusInternalServerError, "logout_failed", err.Error())
		return
	}
	removeAccount()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "agy"})
}

// Status reports the agy connection state for GET /connections, plus the host
// capability gate the Console uses to hide the kind (supported/reason — docs/32
// Track B 契約). Login state is the token file's existence (`agy models` is the
// authoritative probe but hits the network — not for this polled path). method
// is the token's auth_method ("consumer" = Starter OAuth; GCP 経路は M2);
// email/plan come from the auth-time main-screen capture (AgyCard 表示用、
// best-effort — 欠けても Console は劣化表示で受ける).
func Status() map[string]any {
	m := map[string]any{"connected": false}
	supported, reason := hostcaps.AgyStatus()
	m["supported"] = supported
	if !supported {
		m["reason"] = reason
	}
	b, err := os.ReadFile(tokenPath())
	if err != nil {
		return m
	}
	m["connected"] = true
	var tok struct {
		AuthMethod string `json:"auth_method"`
	}
	if json.Unmarshal(b, &tok) == nil && tok.AuthMethod != "" {
		m["method"] = tok.AuthMethod
	}
	if email, plan := loadAccount(); email != "" {
		m["email"] = email
		if plan != "" {
			m["plan"] = plan
		}
	}
	return m
}
