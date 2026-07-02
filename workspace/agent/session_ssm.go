package main

import (
	"net/http"
	"os/exec"
	"regexp"
	"strings"
)

// SSM login status (docs/history/p3-ssm-session.md). An ssm session runs
// `aws sso login` (device-code) then `aws ssm start-session` in its tmux pane. The
// Console drives the login from a modal WITHOUT attaching the terminal yet: it polls
// this endpoint, which reads the pane and reports whether the device-authorization URL
// is up (show it), the session is established ("Starting session with SessionId:" —
// attach now), or it failed. When the cached SSO token is still valid the URL phase is
// skipped and it goes straight to ready.

var (
	// Prefer the autofill URL that carries the user_code (one-click); fall back to any
	// device.sso authorization URL.
	ssmURLWithCode = regexp.MustCompile(`https://[^\s"']*user_code=[^\s"']+`)
	ssmDeviceURL   = regexp.MustCompile(`https://[^\s"']*device\.sso\.[^\s"']*`)
	ssmCodeRe      = regexp.MustCompile(`\b[A-Z0-9]{4}-[A-Z0-9]{4}\b`)
)

type ssmLoginStatus struct {
	Phase   string `json:"phase"` // pending | authorize | ready | error
	URL     string `json:"url,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// handleStartSession (POST /sessions/{name}/start) relaunches a stopped session from
// its recorded meta WITHOUT attaching a terminal — used by the SSM login modal so the
// SSO handshake runs before the pane is shown (resume flow). ?force=1 forces re-login
// (logout + login) for ssm sessions. Idempotent: a live session is left as-is.
func handleStartSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	if _, ok := readSessionMeta(name); !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	force := r.URL.Query().Get("force") == "1"
	if !ensureSessionTmux(name, force) {
		writeErr(w, http.StatusInternalServerError, "start_failed", "could not start session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func handleSSMLoginStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	meta, ok := readSessionMeta(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if meta.Kind != kindSSM {
		writeErr(w, http.StatusBadRequest, "unsupported_kind", "ssm-login is for ssm sessions only")
		return
	}
	alive := tmuxHasSession(tmuxName(name))
	buf := ""
	if pane := sessionPaneID(tmuxName(name)); pane != "" {
		// -S - captures the whole scrollback so the URL (early) and the SessionId line
		// (later) are both visible regardless of pane size.
		if out, err := exec.Command("tmux", "capture-pane", "-p", "-S", "-", "-t", pane).Output(); err == nil {
			buf = string(out)
		}
	}
	writeJSON(w, http.StatusOK, parseSSMLogin(buf, alive))
}

// parseSSMLogin derives the login phase from the pane buffer. Order matters: an
// established session buffer still contains the earlier URL, so check ready first.
//
// Fragile by nature: it matches the aws CLI's on-screen wording ("Starting session
// with SessionId:") and device-authorization URL shapes via regex. An aws-cli version
// that rewords the banner or changes the SSO URL host would silently regress this
// to "pending"/"error"; behavior is pinned by the current CLI output, not a contract.
func parseSSMLogin(buf string, alive bool) ssmLoginStatus {
	if strings.Contains(buf, "Starting session with SessionId:") {
		return ssmLoginStatus{Phase: "ready"}
	}
	url := ssmURLWithCode.FindString(buf)
	if url == "" {
		url = ssmDeviceURL.FindString(buf)
	}
	if url != "" {
		return ssmLoginStatus{Phase: "authorize", URL: url, Code: ssmCodeRe.FindString(buf)}
	}
	if !alive {
		// The pane's program (exec aws ssm start-session) exited before establishing —
		// a login failure/timeout or a start-session error. Surface the tail if any.
		msg := lastNonEmptyLines(buf, 6)
		if msg == "" {
			msg = "セッションを開始できませんでした（認証失敗またはタイムアウトの可能性）"
		}
		return ssmLoginStatus{Phase: "error", Message: msg}
	}
	return ssmLoginStatus{Phase: "pending"}
}

// lastNonEmptyLines returns up to n trailing non-blank lines of s, joined by newlines.
func lastNonEmptyLines(s string, n int) string {
	var keep []string
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0 && len(keep) < n; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			keep = append([]string{t}, keep...)
		}
	}
	return strings.Join(keep, "\n")
}
