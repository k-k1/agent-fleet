package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Codex auth is driven from the WebUI, like Claude: codex owns its credential file
// (~/.codex/auth.json) and we just run `codex login` for the user, then report
// status via `codex login status`. Two paths, both ending in a written auth.json:
//   - API key:      `codex login --with-api-key` reads the key from stdin.
//   - Subscription: `codex login --device-auth` prints a verification URL + a
//     one-time code and polls OpenAI until the user approves in the browser.
// Unlike opencode, an env-injected OPENAI_API_KEY is NOT honored by codex (its
// `login status` stays "Not logged in"), so we must write auth.json via the CLI.
// The file is codex-owned (out of our encrypted store, like claude's creds) and
// denylisted from the file browser.

// codexLoggedIn reports whether codex has stored credentials, via `codex login
// status` (prints "Not logged in" when logged out; anything else => connected).
func codexLoggedIn() bool {
	out, err := exec.Command("codex", "login", "status").CombinedOutput()
	if err != nil {
		return false
	}
	return !strings.Contains(strings.ToLower(string(out)), "not logged in")
}

// codexStatus reports connection status plus the authenticated account for the
// Console. `codex login status` only prints the method ("Logged in using ChatGPT"),
// so we read ~/.codex/auth.json for the auth_mode and, for a ChatGPT login, the
// account email + plan from the id_token's claims (codex stores them there). Only
// non-secret claims are surfaced; tokens themselves are never returned.
func codexStatus() map[string]any {
	if !codexLoggedIn() {
		return map[string]any{"connected": false}
	}
	info := map[string]any{"connected": true}
	b, err := os.ReadFile(filepath.Join(homeDir(), ".codex", "auth.json"))
	if err != nil {
		return info
	}
	var a struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			IDToken string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(b, &a) != nil {
		return info
	}
	if a.AuthMode != "" {
		info["method"] = a.AuthMode // "chatgpt" | "apikey"
	}
	if a.AuthMode == "chatgpt" {
		if email, plan := codexIDTokenInfo(a.Tokens.IDToken); email != "" {
			info["email"] = email
			if plan != "" {
				info["plan"] = plan
			}
		}
	}
	return info
}

// codexIDTokenInfo decodes the (unverified) JWT payload of a ChatGPT id_token and
// returns the account email + plan claim. We only read identity claims for display;
// no signature check is needed since we're reading codex's own stored token.
func codexIDTokenInfo(idToken string) (email, plan string) {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return "", ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims struct {
		Email string `json:"email"`
		Auth  struct {
			Plan string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return "", ""
	}
	return claims.Email, claims.Auth.Plan
}

type codexKeyReq struct {
	Key string `json:"key"` // the OpenAI/Codex API key
}

// handleCodexApiKey logs codex in with an API key by piping it to
// `codex login --with-api-key` (codex writes its own auth.json).
func handleCodexApiKey(w http.ResponseWriter, r *http.Request) {
	var req codexKeyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		writeErr(w, http.StatusBadRequest, "bad_key", "key is required")
		return
	}
	cmd := exec.Command("codex", "login", "--with-api-key")
	cmd.Stdin = strings.NewReader(key)
	out, err := cmd.CombinedOutput()
	if err != nil || !codexLoggedIn() {
		writeErr(w, http.StatusBadGateway, "login_failed", "codex login failed: "+strings.TrimSpace(string(out)))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true})
}

// --- device-auth (subscription) flow ---

var (
	// The verification URL and one-time code codex prints under `--device-auth`.
	codexURLRe = regexp.MustCompile(`https://auth\.openai\.com/\S*`)
	// The one-time code is XXXX-XXXXX (4 then 5 in observed samples); allow 4-6 on
	// each side so a length tweak doesn't silently drop it.
	codexCodeRe = regexp.MustCompile(`\b[A-Z0-9]{4,6}-[A-Z0-9]{4,6}\b`)
)

// codex device-auth keeps the codex process running (it polls OpenAI itself) until
// the user approves; we reuse the claudeFlow PTY plumbing to hold it and scrape
// the URL + code. The device code expires in ~15 min.
const codexFlowTTL = 16 * time.Minute

var (
	codexFlowsMu sync.Mutex
	codexFlows   = map[string]*claudeFlow{}
)

func reapCodexFlows() {
	codexFlowsMu.Lock()
	defer codexFlowsMu.Unlock()
	for id, f := range codexFlows {
		if time.Since(f.created) > codexFlowTTL {
			f.close()
			delete(codexFlows, id)
		}
	}
}

// handleCodexDeviceStart launches `codex login --device-auth`, scrapes the
// verification URL + one-time code, and returns them with a flow_id the client
// polls. The codex process is kept alive to do the OpenAI-side polling.
func handleCodexDeviceStart(w http.ResponseWriter, r *http.Request) {
	reapCodexFlows()
	cmd := exec.Command("codex", "login", "--device-auth")
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

	url := waitFor(f, codexURLRe, 20*time.Second)
	if url == "" {
		f.close()
		writeErr(w, http.StatusBadGateway, "no_url", "codex did not emit a device-auth URL (device code login may be disabled for this account)")
		return
	}
	// The one-time code prints on the line AFTER the URL, so wait for it separately
	// rather than reading the buffer the instant the URL matched (it isn't there yet).
	code := waitFor(f, codexCodeRe, 8*time.Second)
	id := newFlowID()
	codexFlowsMu.Lock()
	codexFlows[id] = f
	codexFlowsMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"flow_id": id, "url": url, "user_code": code})
}

type codexPollReq struct {
	FlowID string `json:"flow_id"`
}

// handleCodexDevicePoll reports whether the device-auth login has completed. On
// success it tears down the flow; codex has written auth.json by then.
func handleCodexDevicePoll(w http.ResponseWriter, r *http.Request) {
	var req codexPollReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if codexLoggedIn() {
		codexFlowsMu.Lock()
		if f := codexFlows[req.FlowID]; f != nil {
			f.close()
			delete(codexFlows, req.FlowID)
		}
		codexFlowsMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"connected": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": false})
}

// handleCodexDisconnect logs codex out (clears auth.json) via the CLI.
func handleCodexDisconnect(w http.ResponseWriter, r *http.Request) {
	_ = exec.Command("codex", "logout").Run()
	writeJSON(w, http.StatusOK, map[string]any{"disconnected": "codex"})
}

// ensureCodexFolderTrusted pre-accepts codex's per-directory trust gate ("Do you trust
// the contents of this directory?") for dir, the way claude's ensureFolderTrusted does
// for its own dialog. codex records trust in ~/.codex/config.toml as a
// [projects."<dir>"] section with trust_level = "trusted" (what it writes when the user
// clicks Yes); a freshly cloned repo has no such entry and stalls at the prompt on
// launch. The bypass flags on the launch command cover approvals/sandbox/hook-trust,
// NOT this project trust, so we seed the entry here. Best-effort and idempotent: appends
// the section only when absent, leaving any existing config untouched.
func ensureCodexFolderTrusted(dir string) {
	if dir == "" {
		return
	}
	p := filepath.Join(homeDir(), ".codex", "config.toml")
	b, _ := os.ReadFile(p)
	// tomlString quotes+escapes the path as a TOML key, matching codex's own format.
	header := "[projects." + tomlString(dir) + "]"
	if strings.Contains(string(b), header) {
		return // already recorded — don't duplicate the section
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	var sb strings.Builder
	sb.Write(b)
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("\n" + header + "\ntrust_level = \"trusted\"\n")
	tmp := p + ".af-tmp"
	if os.WriteFile(tmp, []byte(sb.String()), 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}
