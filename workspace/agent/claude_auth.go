package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// Claude auth is driven from the WebUI, not the terminal. We run the real
// subscription login — `claude auth login --claudeai` — an interactive (Ink/TTY)
// OAuth flow that emits an authorize URL and waits for a pasted code, then writes
// claude's own .credentials.json (subscription, WITH a refresh token) under
// CLAUDE_CONFIG_DIR. This is what authenticates the INTERACTIVE TUI (the env-only
// `claude setup-token` does not — it's headless-only, and a synthetic creds file
// without a refresh token is rejected). We drive it through a PTY: a very wide PTY
// keeps Ink from wrapping the URL, we strip ANSI to scrape it, the Console shows it
// + collects the code, we submit the code, then confirm via `claude auth status`.
// No env injection or token storage is needed — claude owns the credentials file.

var (
	// CSI/escape sequences and lone control chars Ink emits while redrawing.
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[()][AB012]|\x1b[<>=]|[\x00-\x08\x0b\x0c\x0e-\x1f]`)
	urlRe  = regexp.MustCompile(`https://claude\.com/cai/oauth/authorize\?\S+`)
	errRe  = regexp.MustCompile(`OAuth error:[^\n]*`)
)

type claudeFlow struct {
	ptmx    *os.File
	cmd     *exec.Cmd
	mu      sync.Mutex
	out     strings.Builder
	created time.Time
}

// clean returns the accumulated PTY output with ANSI/control noise removed.
func (f *claudeFlow) clean() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := ansiRe.ReplaceAllString(f.out.String(), "")
	return strings.ReplaceAll(s, "\r", "\n")
}

func (f *claudeFlow) close() {
	_ = f.cmd.Process.Kill()
	_ = f.ptmx.Close()
}

var (
	flowsMu sync.Mutex
	flows   = map[string]*claudeFlow{}
)

// setup-token's OAuth code is short-lived; don't keep orphan PTYs around.
const claudeFlowTTL = 10 * time.Minute

func newFlowID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func reapFlows() {
	flowsMu.Lock()
	defer flowsMu.Unlock()
	for id, f := range flows {
		if time.Since(f.created) > claudeFlowTTL {
			f.close()
			delete(flows, id)
		}
	}
}

// handleClaudeStart launches setup-token, waits for the authorize URL, and
// returns it with a flow_id the client uses to submit the code.
func handleClaudeStart(w http.ResponseWriter, r *http.Request) {
	reapFlows()
	// Real subscription login (writes .credentials.json with a refresh token).
	// CLAUDE_CONFIG_DIR is inherited from os.Environ() so creds land where sessions
	// read them. Same authorize URL as setup-token (claude.com/cai/oauth/authorize).
	cmd := exec.Command("claude", "auth", "login", "--claudeai")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "pty_failed", err.Error())
		return
	}
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 50, Cols: 4000}) // wide => URL on one line

	f := &claudeFlow{ptmx: ptmx, cmd: cmd, created: time.Now()}
	go func() {
		buf := make([]byte, 8192)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				f.mu.Lock()
				f.out.Write(buf[:n])
				f.mu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}()

	url := waitFor(f, urlRe, 20*time.Second)
	if url == "" {
		f.close()
		httpx.WriteErr(w, http.StatusBadGateway, "no_url", "setup-token did not emit an authorize URL")
		return
	}
	id := newFlowID()
	flowsMu.Lock()
	flows[id] = f
	flowsMu.Unlock()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"flow_id": id, "url": url})
}

type claudeCompleteReq struct {
	FlowID string `json:"flow_id"`
	Code   string `json:"code"`
}

// handleClaudeComplete submits the pasted code, captures the printed token, and
// stores it. The flow's PTY/process is always cleaned up.
func handleClaudeComplete(w http.ResponseWriter, r *http.Request) {
	var req claudeCompleteReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_code", "code is required")
		return
	}
	flowsMu.Lock()
	f := flows[req.FlowID]
	delete(flows, req.FlowID)
	flowsMu.Unlock()
	if f == nil {
		httpx.WriteErr(w, http.StatusNotFound, "no_flow", "unknown or expired flow_id")
		return
	}
	defer f.close()

	// Submit the code, then send Enter as a SEPARATE keystroke after a short
	// delay. Ink ignores the carriage return if it arrives in the same write as
	// the pasted code, leaving the form unsubmitted (verified via a PTY probe).
	if _, err := f.ptmx.Write([]byte(code)); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := f.ptmx.Write([]byte("\r")); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}

	ok, oauthErr := awaitClaudeLogin(f, 40*time.Second)
	if !ok {
		if oauthErr != "" {
			httpx.WriteErr(w, http.StatusBadGateway, "oauth_error", oauthErr)
		} else {
			httpx.WriteErr(w, http.StatusBadGateway, "login_failed", "login did not complete (code wrong or expired?)")
		}
		return
	}
	// claude wrote its own .credentials.json; nothing for us to store.
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": true})
}

func handleClaudeDisconnect(w http.ResponseWriter, r *http.Request) {
	// claude owns its credentials; log out via the CLI so it clears them properly.
	_ = exec.Command("claude", "auth", "logout").Run()
	// Best-effort: drop any legacy stored token from the encrypted store too.
	if s, err := loadSecrets(); err == nil && s.Claude != "" {
		s.Claude = ""
		_ = s.save()
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "claude"})
}

// awaitClaudeLogin polls `claude auth status` until login succeeds, or surfaces an
// OAuth error from the flow output, until the timeout. `claude auth login` prints
// "OAuth error: …" on a bad/expired code, which we return instead of a generic
// timeout.
func awaitClaudeLogin(f *claudeFlow, timeout time.Duration) (ok bool, oauthErr string) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if claudeLoggedIn() {
			return true, ""
		}
		if m := errRe.FindString(f.clean()); m != "" {
			return false, strings.TrimSpace(m)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false, ""
}

// claudeLoggedIn reports whether claude has valid credentials, via `claude auth
// status` (JSON: {"loggedIn": bool, …}). This reads the same CLAUDE_CONFIG_DIR the
// sessions use, so it reflects the interactive TUI's auth state.
func claudeLoggedIn() bool {
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

// claudeStatus reports connection status plus the authenticated account (email,
// plan) for the Console — `claude auth status` exposes the logged-in identity.
func claudeStatus() map[string]any {
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
	return map[string]any{"connected": true, "email": st.Email, "plan": st.SubscriptionType}
}

// waitFor polls the flow's cleaned output until re matches or the timeout hits.
func waitFor(f *claudeFlow, re *regexp.Regexp, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m := re.FindString(f.clean()); m != "" {
			return m
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}
