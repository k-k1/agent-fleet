package claude

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
	resetCredCache() // re-read the freshly written credentials: the contents can change under an unchanged stat
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
// The expiry fields (expires_at / days_left / expired) are added here because `claude auth
// status` returns no expiry at all: both --json and --text carry only loggedIn/email/orgId/
// subscriptionType (measured on 2.1.231). The card looked at nothing else, so an expired
// login still read as "connected" (docs/log/47 §4-7: never take a card's status display as
// evidence). The expiry itself is read straight from the credentials by authexpiry.go.
// Bounding this probe is not an optimisation: launching the CLI is not an operation this
// process controls the cost of. Measured on a busy production workspace, `claude auth status`
// took 21-28s on EVERY run with 0.24s of CPU — it sits in futex, waiting on locks over the
// CLAUDE_CONFIG_DIR that every other Claude session in the container shares, so the cost
// grows with how many sessions the person is running. GET /connections is the only caller,
// the ALB in front of the Control Plane cuts a client at 60s, and the request carried this
// probe inline — which is how Settings > Git hosting came to sit on "loading" forever
// instead of rendering.
//
// So the CLI now runs off the request path. A probe that does not answer in time yields the
// PREVIOUS answer; a late one stores itself and serves the next poll. Serving a stale card
// beats serving none, and both beat `{"connected": false}` — that is not "unknown", it is a
// claim that the person is signed out, and the launch pickers hide the agent when they read it.
// Budgets are vars so the tests can shrink them: the behaviour worth pinning is "what is
// served when the probe overruns", and waiting out the real budget to see it would make the
// suite slower than the bug.
var (
	statusBudget = 3 * time.Second
	// The first answer has nothing to fall back to, so it waits longer — but still bounded,
	// because dying at the ALB renders as a card that never loads rather than a slow one.
	statusColdBudget = 25 * time.Second
)

// A safety net on the abandoned probe itself, so a CLI that never returns cannot pile up
// one stranded process per poll for the life of the agent.
const statusExecCeiling = 60 * time.Second

var (
	stMu      sync.Mutex
	stLast    map[string]any // last completed answer; nil until the first one lands
	stRunning bool           // a probe is already out — do not launch a second
)

func Status() map[string]any {
	stMu.Lock()
	last, running := stLast, stRunning
	if !running {
		stRunning = true
	}
	stMu.Unlock()

	// Starting a second CLI while one is still out would feed the contention that makes it
	// slow: at the Console's 4s poll rate, a 20s probe would leave five of them fighting
	// each other over the same config directory.
	if running {
		return last
	}

	done := make(chan map[string]any, 1)
	go func() {
		m := probeStatus()
		stMu.Lock()
		if m != nil {
			stLast = m // a real "signed out" answer must replace a stale "connected" one
		}
		stRunning = false
		stMu.Unlock()
		done <- m
	}()

	budget := statusBudget
	if last == nil {
		budget = statusColdBudget
	}
	select {
	case m := <-done:
		return m
	case <-time.After(budget):
		return last // nil on a cold miss: unknown, which the card reads as not-connected
	}
}

// probeStatus is Status's actual work: the CLI call and the shape the card reads. A var so a
// test can stand in for the CLI — the bounding above is the behaviour under test, and it can
// only be exercised by a probe whose duration the test controls.
var probeStatus = func() map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), statusExecCeiling)
	defer cancel()
	out, err := exec.CommandContext(ctx, "claude", "auth", "status").Output()
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
