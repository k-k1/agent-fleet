package kiro

// kiro authenticates through a Builder ID / device flow (docs/log/43 §2.2). The
// credentials live in `~/.local/share/kiro-cli/data.sqlite3` (mode 600, auth_kv table).
// What this file exposes is Status(): `kiro-cli whoami -f json` returns clean structured
// JSON (`{accountType,email,region,startUrl}`) and exits 1 when not authenticated. The
// interactive login (device flow start→poll, scraping stdout) and the disconnect route are
// added in Track C, the CP route.

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

// whoamiOut is the shape of `kiro-cli whoami -f json` (measured on 2.14.1).
type whoamiOut struct {
	AccountType string `json:"accountType"` // "BuilderId" | …
	Email       string `json:"email"`
}

// statusResult caches the last successful probe (email + logged-in).
type statusResult struct {
	loggedIn bool
	email    string
}

// Status is the `kiro` field of GET /connections. supported=false hides the kind in
// the Console (the registry's available gate, wired the same way as cursor/copilot): no
// binary means not installed, i.e. before on-demand installation. connected is the real
// login state (whoami exit 0). The probe is cached for ~30s so a connections poll does not
// spawn a child process every time.
func Status() map[string]any {
	if _, err := exec.LookPath(bin()); err != nil {
		return map[string]any{"connected": false, "supported": false, "reason": "not_installed"}
	}
	m := map[string]any{"supported": true}
	st := probeStatus()
	m["connected"] = st.loggedIn
	if st.email != "" {
		m["email"] = st.email
	}
	return m
}

var statusMu sync.Mutex
var statusAt time.Time
var statusVal statusResult

const statusTTL = 30 * time.Second

func probeStatus() statusResult {
	statusMu.Lock()
	defer statusMu.Unlock()
	if !statusAt.IsZero() && time.Since(statusAt) < statusTTL {
		return statusVal
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin(), "whoami", "-f", "json").Output()
	if err != nil {
		// Not logged in (exit 1) or a transient error. Distinguish: an empty prior value
		// means we've never seen a login, so report logged-out; otherwise keep the last
		// good value (stale-if-error) rather than flapping the connection chip.
		if statusAt.IsZero() {
			statusVal = statusResult{}
			statusAt = time.Now()
		}
		return statusVal
	}
	var wo whoamiOut
	if json.Unmarshal([]byte(strings.TrimSpace(string(out))), &wo) == nil {
		statusVal = statusResult{loggedIn: true, email: wo.Email}
		statusAt = time.Now()
	}
	return statusVal
}

// LoggedIn reports (cached, ~30s) whether kiro-cli has an authenticated session.
// Guards on the binary being present so a missing CLI reads as unavailable rather
// than paying a failed exec each call.
func LoggedIn() bool {
	if _, err := exec.LookPath(bin()); err != nil {
		return false
	}
	return probeStatus().loggedIn
}

// invalidateStatus drops the cached status so the next /connections poll reflects
// a login/logout at once instead of after statusTTL.
func invalidateStatus() {
	statusMu.Lock()
	statusAt = time.Time{}
	statusMu.Unlock()
}

// --- interactive login flow (docs/log/43 Track C) ---------------------------------
//
// `kiro-cli login --license free --use-device-flow` prints a verification URL
// (with the user_code pre-embedded) and a short code, then self-polls the AWS SSO
// token endpoint until the user approves in a browser and finally writes the
// credentials to ~/.local/share/kiro-cli/data.sqlite3 (measured, docs/log/43 §2.2). We hold
// the process on a PTY (shared agents.Flow plumbing) so we can scrape the URL/code
// and keep the poller alive; the Console shows them and polls
// /connections/kiro/poll until `whoami` reports authenticated. Like codex/cursor
// this is start→poll (no pasted code — the code is only shown for the user to
// confirm it matches in the browser). v1 is login-only (Builder ID / free); the
// API-key path (KIRO_API_KEY, Pro and above) is deferred to Track D — injecting the key
// into the TUI pane program would leak it into `ps` (docs/log/43 §2.2, decision 4).

// loginURLRe matches the device verification URL kiro prints (measured, §2.2):
// https://view.awsapps.com/start/#/device?user_code=… — and the alternate
// device.sso.<region>.amazonaws.com/… host; both carry "device".
var loginURLRe = regexp.MustCompile(`https://\S*device\S*`)

// loginCodeRe matches the short confirmation code ("XXXX-XXXX") kiro prints so the
// Console can show it beside the URL, the device flow's code-confirmation UX.
var loginCodeRe = regexp.MustCompile(`\b[A-Z0-9]{4}-[A-Z0-9]{4}\b`)

// The device-approval window is short; don't keep orphan login PTYs around.
const loginFlowTTL = 15 * time.Minute

var loginFlows = agents.NewFlowStore(loginFlowTTL)

// HandleStart launches the interactive device-flow login, scrapes the URL + code,
// and returns them with a flow_id the client polls. The kiro process is kept alive
// to do the AWS-side polling. POST /connections/kiro/start.
func HandleStart(w http.ResponseWriter, r *http.Request) {
	if _, err := exec.LookPath(bin()); err != nil {
		httpx.WriteErr(w, http.StatusConflict, "kiro_unsupported", "kiro-cli が見つかりません（未導入の可能性）")
		return
	}
	if loggedInFresh() {
		// A signed-in kiro prints no login URL — the scrape below would just hang.
		httpx.WriteErr(w, http.StatusConflict, "already_connected", "すでに接続済みです。再認証するには一度切断してください。")
		return
	}
	loginFlows.Reap()
	cmd := exec.Command(bin(), "login", "--license", "free", "--use-device-flow")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := agents.StartFlow(cmd)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "pty_failed", err.Error())
		return
	}
	url := loginURLRe.FindString(f.WaitFor(loginURLRe, 20*time.Second))
	if url == "" {
		f.Close()
		httpx.WriteErr(w, http.StatusBadGateway, "no_url", "kiro がログイン URL を表示しませんでした")
		return
	}
	code := loginCodeRe.FindString(f.Clean()) // best-effort; the URL already embeds user_code
	id := loginFlows.Put(f)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"flow_id": id, "url": url, "user_code": code})
}

type pollReq struct {
	FlowID string `json:"flow_id"`
}

// HandlePoll reports whether the browser approval has completed. On success it
// tears down the flow; kiro has written its credentials by then.
// POST /connections/kiro/poll.
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

// HandleDisconnect signs kiro out (clears ~/.local/share/kiro-cli auth) via the
// CLI. DELETE /connections/kiro.
func HandleDisconnect(w http.ResponseWriter, r *http.Request) {
	// logout can reach the network, so without a timeout this handler blocks indefinitely.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, bin(), "logout").Run()
	invalidateStatus()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "kiro"})
}

// loggedInFresh runs an UNCACHED `kiro-cli whoami -f json` for the login poll
// (probeStatus caches 30s — too stale for a live poll loop).
func loggedInFresh() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin(), "whoami", "-f", "json").Output()
	if err != nil {
		return false
	}
	var wo whoamiOut
	if json.Unmarshal([]byte(strings.TrimSpace(string(out))), &wo) != nil {
		return false
	}
	return wo.Email != "" || wo.AccountType != ""
}
