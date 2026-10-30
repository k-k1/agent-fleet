package sessionx

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// SSM login status (docs/log/p3-ssm-session.md). An ssm session runs
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

// HandleStartSession (POST /sessions/{name}/start) relaunches a stopped session from
// its recorded meta WITHOUT attaching a terminal — used by the SSM login modal so the
// SSO handshake runs before the pane is shown (resume flow). ?force=1 forces re-login
// (logout + login) for ssm sessions. Idempotent: a live session is left as-is.
func HandleStartSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	lease, ok := enterSessionGate(w, r, name)
	if !ok {
		return
	}
	defer lease.Release()
	if _, ok := session.ReadMeta(name); !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	force := r.URL.Query().Get("force") == "1"
	if r.URL.Query().Get("fresh") == "1" {
		if err := startFresh(name); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "fresh_failed", err.Error())
			return
		}
	}
	if err := ensureSessionTmux(lease, name, force); err != nil {
		if writeStartRefusal(w, err) {
			return
		}
		if !writeCodexReleasingErr(w, err) {
			httpx.WriteErr(w, http.StatusInternalServerError, "start_failed", err.Error())
		}
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// startFresh is the user's answer to transcript_lost: a new conversation in this slot, with no
// old context. A fork's source is dropped too, or the "new" conversation would copy the parent
// (or be unstartable once the parent's log is gone as well).
func startFresh(name string) error {
	m, ok := session.ReadMeta(name)
	if !ok || m.Kind != session.KindClaude {
		return nil
	}
	sid := session.UUID(m.Dir, m.Name)
	// Only while the log is still lost: a stale ?fresh=1 (a toast left open, a second tab) must
	// not set aside a conversation that has since become resumable.
	started, err := claude.StartFreshIfLost(sid)
	if err != nil {
		return err
	}
	if !started {
		return nil
	}
	UpdateSessionMeta(name, func(cur *session.Meta) bool {
		if cur.ForkFrom == "" && cur.ForkAt == "" {
			return false
		}
		cur.ForkFrom, cur.ForkAt = "", ""
		return true
	})
	return nil
}

func HandleSSMLoginStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if meta.Kind != session.KindSSM {
		httpx.WriteErr(w, http.StatusBadRequest, "unsupported_kind", "ssm-login is for ssm sessions only")
		return
	}
	alive := tmuxx.HasSession(session.TmuxName(name))
	buf := ""
	if pane := tmuxx.SessionPaneID(session.TmuxName(name)); pane != "" {
		// -S - captures the whole scrollback so the URL (early) and the SessionId line
		// (later) are both visible regardless of pane size. -J joins lines the pane
		// wrapped: a narrow client (a phone, a split pane, or one attaching mid-login and
		// resizing the window) wraps the device URL, and without it the regex took the
		// first row as the whole URL — a broken link in the login modal (#1025).
		if out, err := tmuxx.Cmd("capture-pane", "-p", "-J", "-S", "-", "-t", pane).Output(); err == nil {
			buf = string(out)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, parseSSMLogin(buf, alive))
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
	if url, code := DeviceAuthorization(buf); url != "" {
		return ssmLoginStatus{Phase: "authorize", URL: url, Code: code}
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

// DeviceAuthorization picks the verification URL and the user code out of what
// `aws sso login --use-device-code` printed; url is "" while neither is there yet. The
// patterns accept any https host, so a caller that shows the URL to a person checks the
// host itself.
func DeviceAuthorization(out string) (url, code string) {
	url = ssmURLWithCode.FindString(out)
	if url == "" {
		url = ssmDeviceURL.FindString(out)
	}
	if url == "" {
		return "", ""
	}
	return url, ssmCodeRe.FindString(out)
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
