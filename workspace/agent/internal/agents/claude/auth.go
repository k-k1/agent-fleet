package claude

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Claude auth is driven from the WebUI, not the terminal. We run the real
// subscription login — `claude auth login --claudeai` — an interactive (Ink/TTY)
// OAuth flow that emits an authorize URL and waits for a pasted code, then writes
// claude's own .credentials.json (subscription, WITH a refresh token) under
// CLAUDE_CONFIG_DIR. This is what authenticates the INTERACTIVE TUI (the env-only
// `claude setup-token` does not — it's headless-only, and a synthetic creds file
// without a refresh token is rejected). We drive it through a PTY (the shared
// agents.Flow plumbing): a very wide PTY keeps Ink from wrapping the URL, we strip
// ANSI to scrape it, the Console shows it + collects the code, we submit the code,
// then confirm via `claude auth status`.
// No env injection or token storage is needed — claude owns the credentials file.

var (
	urlRe = regexp.MustCompile(`https://claude\.com/cai/oauth/authorize\?\S+`)
	errRe = regexp.MustCompile(`OAuth error:[^\n]*`)
)

// setup-token's OAuth code is short-lived; don't keep orphan PTYs around.
const flowTTL = 10 * time.Minute

var flows = agents.NewFlowStore(flowTTL)

// HandleStart launches setup-token, waits for the authorize URL, and
// returns it with a flow_id the client uses to submit the code.
// POST /connections/claude/start.
func HandleStart(w http.ResponseWriter, r *http.Request) {
	flows.Reap()
	// Real subscription login (writes .credentials.json with a refresh token).
	// CLAUDE_CONFIG_DIR is inherited from os.Environ() so creds land where sessions
	// read them. Same authorize URL as setup-token (claude.com/cai/oauth/authorize).
	cmd := exec.Command("claude", "auth", "login", "--claudeai")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := agents.StartFlow(cmd)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "pty_failed", err.Error())
		return
	}

	url := f.WaitFor(urlRe, 20*time.Second)
	if url == "" {
		f.Close()
		httpx.WriteErr(w, http.StatusBadGateway, "no_url", "setup-token did not emit an authorize URL")
		return
	}
	id := flows.Put(f)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"flow_id": id, "url": url})
}

type completeReq struct {
	FlowID string `json:"flow_id"`
	Code   string `json:"code"`
}

// HandleComplete submits the pasted code, captures the printed token, and
// stores it. The flow's PTY/process is always cleaned up.
// POST /connections/claude/complete.
func HandleComplete(w http.ResponseWriter, r *http.Request) {
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

	// Submit the code, then send Enter as a SEPARATE keystroke after a short
	// delay. Ink ignores the carriage return if it arrives in the same write as
	// the pasted code, leaving the form unsubmitted (verified via a PTY probe).
	if _, err := f.Ptmx.Write([]byte(code)); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := f.Ptmx.Write([]byte("\r")); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}

	ok, oauthErr := awaitLogin(f, 40*time.Second)
	if !ok {
		if oauthErr != "" {
			httpx.WriteErr(w, http.StatusBadGateway, "oauth_error", oauthErr)
		} else {
			httpx.WriteErr(w, http.StatusBadGateway, "login_failed", "login did not complete (code wrong or expired?)")
		}
		return
	}
	// claude wrote its own .credentials.json; nothing for us to store.
	resetCredCache() // 期限判定を書き戻した資格情報で取り直す（同じ stat のまま中身が変わりうる）
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": true})
}

// HandleDisconnect logs claude out via the CLI. DELETE /connections/claude.
func HandleDisconnect(w http.ResponseWriter, r *http.Request) {
	// claude owns its credentials; log out via the CLI so it clears them properly.
	_ = exec.Command("claude", "auth", "logout").Run()
	resetCredCache()
	// Best-effort: drop any legacy stored token from the encrypted store too.
	if s, err := secrets.Load(); err == nil && s.Claude != "" {
		s.Claude = ""
		_ = s.Save()
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "claude"})
}

// awaitLogin polls `claude auth status` until login succeeds, or surfaces an
// OAuth error from the flow output, until the timeout. `claude auth login` prints
// "OAuth error: …" on a bad/expired code, which we return instead of a generic
// timeout.
func awaitLogin(f *agents.Flow, timeout time.Duration) (ok bool, oauthErr string) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if loggedIn() {
			return true, ""
		}
		if m := errRe.FindString(f.Clean()); m != "" {
			return false, strings.TrimSpace(m)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false, ""
}

// LoggedIn is the exported form of loggedIn for cross-package availability checks
// (the assistant chat / title suggestion pick the first authenticated backend).
func LoggedIn() bool { return loggedIn() }

// loggedIn reports whether claude has valid credentials, via `claude auth
// status` (JSON: {"loggedIn": bool, …}). This reads the same CLAUDE_CONFIG_DIR the
// sessions use, so it reflects the interactive TUI's auth state.
func loggedIn() bool {
	out, err := exec.Command("claude", "auth", "status").Output()
	if err != nil {
		return false // non-zero exit = not logged in
	}
	var st struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if json.Unmarshal(out, &st) != nil {
		return false
	}
	return st.LoggedIn
}

// Status reports connection status plus the authenticated account (email,
// plan) for the Console — `claude auth status` exposes the logged-in identity.
// GET /connections.
//
// 期限（expires_at / days_left / expired）を足しているのは、`claude auth status` が
// **期限を一切返さない**から（--json も --text も loggedIn/email/orgId/subscriptionType
// だけ・実測 2.1.231）。カードはそれだけを見ていたので、切れても「接続済み」のまま
// だった（docs/47 §4-7 で書いた「カードの状態表示を根拠にするな」の続き）。期限は
// authexpiry.go が資格情報から直接読む。
func Status() map[string]any {
	out, err := exec.Command("claude", "auth", "status").Output()
	if err != nil {
		return map[string]any{"connected": false}
	}
	var st struct {
		LoggedIn         bool   `json:"loggedIn"`
		Email            string `json:"email"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if json.Unmarshal(out, &st) != nil || !st.LoggedIn {
		return map[string]any{"connected": false}
	}
	m := map[string]any{"connected": true, "email": st.Email, "plan": st.SubscriptionType}
	if e := CredentialExpiry(); e.Known {
		now := time.Now()
		m["expires_at"] = e.Refresh.UTC().Format(time.RFC3339)
		if e.Dead(now) {
			m["expired"] = true
		} else if e.Soon(now) {
			m["days_left"] = e.DaysLeft(now)
		}
	}
	return m
}

var (
	idMu    sync.Mutex
	idEmail string
	idPlan  string
	idAt    time.Time
)

// identity returns the account email + subscription tier from `claude auth status`,
// cached briefly. Status() execs the CLI, so the usage endpoint (polled) must not shell
// out every time. Both "" when signed out. Shared by Plan() and Account() so one exec
// serves both.
func identity() (email, plan string) {
	idMu.Lock()
	defer idMu.Unlock()
	if !idAt.IsZero() && time.Since(idAt) < 5*time.Minute {
		return idEmail, idPlan
	}
	idEmail, idPlan = "", ""
	if m := Status(); m != nil {
		if p, _ := m["plan"].(string); p != "" {
			idPlan = p
		}
		if e, _ := m["email"].(string); e != "" {
			idEmail = e
		}
	}
	idAt = time.Now()
	return idEmail, idPlan
}

// Plan returns the subscription tier (subscriptionType, e.g. "pro" / "max") for the
// WsBar usage chip. "" when signed out or unknown.
func Plan() string { _, p := identity(); return p }

// Account returns the signed-in account email for the usage chip. "" when signed out.
func Account() string { e, _ := identity(); return e }
