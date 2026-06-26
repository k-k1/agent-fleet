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
)

// Claude auth is driven from the WebUI, not the terminal. `claude setup-token`
// is an interactive (Ink/TTY) OAuth flow that emits an authorize URL and waits
// for a pasted code, then prints a long-lived CLAUDE_CODE_OAUTH_TOKEN (1 year,
// subscription, portable). We drive it through a PTY: a very wide PTY keeps Ink
// from wrapping the URL across rows, we strip ANSI to scrape it, the Console
// shows it + collects the code, we submit the code and capture the token, then
// store it for per-session env injection (session.go). See plan / docs/06 §6.8.

var (
	// CSI/escape sequences and lone control chars Ink emits while redrawing.
	ansiRe  = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[()][AB012]|\x1b[<>=]|[\x00-\x08\x0b\x0c\x0e-\x1f]`)
	urlRe = regexp.MustCompile(`https://claude\.com/cai/oauth/authorize\?\S+`)
	// CLAUDE_CODE_OAUTH_TOKEN is an Anthropic secret token; match the sk-ant-
	// family broadly (oat01/oat02/…) so a format tweak doesn't break capture.
	tokenRe = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)
	errRe   = regexp.MustCompile(`OAuth error:[^\n]*`)
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
	cmd := exec.Command("claude", "setup-token")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "pty_failed", err.Error())
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
		writeErr(w, http.StatusBadGateway, "no_url", "setup-token did not emit an authorize URL")
		return
	}
	id := newFlowID()
	flowsMu.Lock()
	flows[id] = f
	flowsMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"flow_id": id, "url": url})
}

type claudeCompleteReq struct {
	FlowID string `json:"flow_id"`
	Code   string `json:"code"`
}

// handleClaudeComplete submits the pasted code, captures the printed token, and
// stores it. The flow's PTY/process is always cleaned up.
func handleClaudeComplete(w http.ResponseWriter, r *http.Request) {
	var req claudeCompleteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		writeErr(w, http.StatusBadRequest, "bad_code", "code is required")
		return
	}
	flowsMu.Lock()
	f := flows[req.FlowID]
	delete(flows, req.FlowID)
	flowsMu.Unlock()
	if f == nil {
		writeErr(w, http.StatusNotFound, "no_flow", "unknown or expired flow_id")
		return
	}
	defer f.close()

	// Submit the code, then send Enter as a SEPARATE keystroke after a short
	// delay. Ink ignores the carriage return if it arrives in the same write as
	// the pasted code, leaving the form unsubmitted (verified via a PTY probe).
	if _, err := f.ptmx.Write([]byte(code)); err != nil {
		writeErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := f.ptmx.Write([]byte("\r")); err != nil {
		writeErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}

	token, oauthErr := awaitToken(f, 30*time.Second)
	if token == "" {
		if oauthErr != "" {
			writeErr(w, http.StatusBadGateway, "oauth_error", oauthErr)
		} else {
			writeErr(w, http.StatusBadGateway, "no_token", "did not capture a token (code wrong or expired?)")
		}
		return
	}
	if err := storeClaudeToken(token); err != nil {
		writeErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true})
}

func handleClaudeDisconnect(w http.ResponseWriter, r *http.Request) {
	s, err := loadSecrets()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.Claude = ""
	if err := s.save(); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"disconnected": "claude"})
}

// awaitToken polls for the printed token, or a surfaced OAuth error, until the
// timeout. setup-token prints "OAuth error: ... Press Enter to retry." on a bad
// or expired code, which we return instead of a generic timeout.
func awaitToken(f *claudeFlow, timeout time.Duration) (token, oauthErr string) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c := f.clean()
		if m := tokenRe.FindString(c); m != "" {
			return m, ""
		}
		if m := errRe.FindString(c); m != "" {
			return "", strings.TrimSpace(m)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return "", ""
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

func storeClaudeToken(token string) error {
	s, err := loadSecrets()
	if err != nil {
		return err
	}
	s.Claude = token
	return s.save()
}

// readClaudeToken returns the stored CLAUDE_CODE_OAUTH_TOKEN, or "" if unset.
func readClaudeToken() string {
	s, err := loadSecrets()
	if err != nil {
		return ""
	}
	return s.Claude
}
