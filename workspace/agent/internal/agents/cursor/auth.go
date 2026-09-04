package cursor

// cursor authenticates through a dedicated flow (the docs/log/40 contract, the same shape as
// claude/agy). The credentials live in ~/.config/cursor/auth.json (0600, accessToken /
// refreshToken as plaintext JSON — measured). The interactive login is
// `NO_OPEN_BROWSER=1 cursor-agent login` (the URL goes to stdout), and Track C (the CP route)
// adds the start/complete wiring. What this file provides is Status():
// `cursor-agent status --format json` returns clean structured JSON (measured).

import (
	"context"
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
)

// statusOut is the shape of `cursor-agent status --format json` (measured on v2026.07.20).
type statusOut struct {
	Status          string `json:"status"` // "authenticated" | …
	IsAuthenticated bool   `json:"isAuthenticated"`
	UserInfo        struct {
		Email string `json:"email"`
	} `json:"userInfo"`
}

// Status is the `cursor` field of GET /connections. supported=false hides the kind
// in the Console (the registry's available gate — the same wiring as copilot/agy): no binary
// = an old image. connected is the real login state (status --format json). The probe is
// cached for ~30s so that a connections poll does not spawn a child process every time.
func Status() map[string]any {
	if _, err := exec.LookPath(bin()); err != nil {
		return map[string]any{"connected": false, "supported": false, "reason": "not_installed"}
	}
	m := map[string]any{"supported": true}
	st := probeStatus()
	m["connected"] = st.IsAuthenticated
	if st.UserInfo.Email != "" {
		m["email"] = st.UserInfo.Email
	}
	return m
}

var statusMu sync.Mutex
var statusAt time.Time
var statusVal statusOut

const statusTTL = 30 * time.Second

func probeStatus() statusOut {
	statusMu.Lock()
	defer statusMu.Unlock()
	if !statusAt.IsZero() && time.Since(statusAt) < statusTTL {
		return statusVal
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := probeCmd(ctx, disableAutoUpdateFlag, "status", "--format", "json").Output()
	if err != nil {
		// stale-if-error: a transient failure must not drop the connection state (keep the
		// previous value).
		return statusVal
	}
	var st statusOut
	if json.Unmarshal([]byte(strings.TrimSpace(string(out))), &st) == nil {
		statusVal = st
		statusAt = time.Now()
	}
	return statusVal
}

// LoggedIn reports (cached, ~30s) whether cursor-agent has an authenticated
// session — the headless-chat availability probe (chat_providers.go
// headlessAgentAvailable). Guards on the binary being present so a missing CLI
// (old image) reads as unavailable rather than paying a failed exec each call.
func LoggedIn() bool {
	if _, err := exec.LookPath(bin()); err != nil {
		return false
	}
	return probeStatus().IsAuthenticated
}

// invalidateStatus drops the cached status so the next /connections poll reflects
// a login/logout at once instead of after statusTTL.
func invalidateStatus() {
	statusMu.Lock()
	statusAt = time.Time{}
	statusMu.Unlock()
}

// --- interactive login flow (docs/log/40 Track C) ---------------------------------
//
// `NO_OPEN_BROWSER=1 cursor-agent login` prints an authorize URL to stdout, then
// polls Cursor itself until the user approves in a browser and finally writes
// ~/.config/cursor/auth.json (measured). We hold the process on a PTY (shared
// agents.Flow plumbing) so we can scrape the URL and keep the poller alive; the
// Console shows the URL and polls /connections/cursor/poll until authenticated.
// Unlike claude/agy there is no pasted code — approval happens entirely in the
// browser, so the flow is start→poll (the codex device-auth shape), not start→complete.
// v1 is login-only; a manual CURSOR_API_KEY registration path is deferred to
// Track D (cursor's CLI has no key-persistence command, and injecting the key into
// the tmux TUI pane program would leak it into `ps` — docs/log/40 decision 5 / Track D).

// loginURLRe matches the deep-control authorize URL cursor prints (measured, Track 0):
// https://cursor.com/loginDeepControl?challenge=…&uuid=…&mode=login&redirectTarget=cli
var loginURLRe = regexp.MustCompile(`https://cursor\.com/loginDeepControl\?\S+`)

// The device-approval window is short; don't keep orphan login PTYs around.
const loginFlowTTL = 10 * time.Minute

var loginFlows = agents.NewFlowStore(loginFlowTTL)

// HandleStart launches the interactive login, scrapes the authorize URL, and
// returns it with a flow_id the client polls. The cursor process is kept alive to
// do the Cursor-side polling. POST /connections/cursor/start.
func HandleStart(w http.ResponseWriter, r *http.Request) {
	if _, err := exec.LookPath(bin()); err != nil {
		httpx.WriteErr(w, http.StatusConflict, "cursor_unsupported", "cursor-agent が見つかりません（旧イメージの可能性）")
		return
	}
	if loggedInFresh() {
		// A signed-in cursor prints no login URL — the scrape below would just hang.
		httpx.WriteErr(w, http.StatusConflict, "already_connected", "すでに接続済みです。再認証するには一度切断してください。")
		return
	}
	loginFlows.Reap()
	cmd := exec.Command(bin(), disableAutoUpdateFlag, "login")
	cmd.Env = EnvWithoutCI(append(os.Environ(), "TERM=xterm-256color", "NO_OPEN_BROWSER=1")) // CI is stripped (ci_env.go)
	f, err := agents.StartFlow(cmd)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "pty_failed", err.Error())
		return
	}
	url := sanitizeURL(f.WaitFor(loginURLRe, 20*time.Second))
	if url == "" {
		f.Close()
		httpx.WriteErr(w, http.StatusBadGateway, "no_url", "cursor がログイン URL を表示しませんでした")
		return
	}
	id := loginFlows.Put(f)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"flow_id": id, "url": url})
}

// sanitizeURL defensively drops a trailing OSC-8 hyperlink remnant (the agy a1b91c8 lesson —
// cursor emits no OSC-8 as measured, but versions drift).
func sanitizeURL(u string) string {
	const scheme = "https://"
	if rest := strings.TrimPrefix(u, scheme); rest != u {
		if i := strings.Index(rest, scheme); i >= 0 {
			u = u[:len(scheme)+i]
		}
	}
	return strings.TrimSuffix(u, "]8;;")
}

type pollReq struct {
	FlowID string `json:"flow_id"`
}

// HandlePoll reports whether the browser approval has completed. On success it
// tears down the flow; cursor has written auth.json by then.
// POST /connections/cursor/poll.
func HandlePoll(w http.ResponseWriter, r *http.Request) {
	var req pollReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if loggedInFresh() {
		if f := loginFlows.Take(req.FlowID); f != nil {
			f.Close()
		}
		invalidateStatus()
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": true})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": false})
}

// HandleDisconnect signs cursor out (clears ~/.config/cursor/auth.json) via the
// CLI. DELETE /connections/cursor.
func HandleDisconnect(w http.ResponseWriter, r *http.Request) {
	// logout can hit the network — without a timeout the handler blocks indefinitely.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	_ = probeCmd(ctx, disableAutoUpdateFlag, "logout").Run()
	invalidateStatus()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "cursor"})
}

// loggedInFresh runs an UNCACHED `cursor-agent status --format json` for the login
// poll (probeStatus caches 30s — too stale for a live poll loop).
func loggedInFresh() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := probeCmd(ctx, disableAutoUpdateFlag, "status", "--format", "json").Output()
	if err != nil {
		return false
	}
	var st statusOut
	if json.Unmarshal([]byte(strings.TrimSpace(string(out))), &st) != nil {
		return false
	}
	return st.IsAuthenticated
}
