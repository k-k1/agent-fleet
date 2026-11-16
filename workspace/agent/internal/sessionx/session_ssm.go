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
	// In order of preference: the autofill URL that carries the user_code (one-click), any
	// device.sso authorization URL, then a signin.aws URL (see ssmSigninURL).
	ssmURLWithCode = regexp.MustCompile(`https://[^\s"']*user_code=[^\s"']+`)
	ssmDeviceURL   = regexp.MustCompile(`https://[^\s"']*device\.sso\.[^\s"']*`)
	// The sign-in service host newer CLIs print (observed in aws-cli 2.36: the portal URL
	// and the autofill URL both live on <region>.signin.aws, and neither pattern above
	// matches it). The host is anchored: a "/" cannot appear before signin.aws, so the
	// string inside another host's path does not match. What follows the match is checked
	// by completeURL, because the regexp cannot say "and then a delimiter".
	ssmSigninURL = regexp.MustCompile(`https://[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.signin\.aws(?:[/?#][^\s"']*)?`)
	ssmCodeRe    = regexp.MustCompile(`\b[A-Z0-9]{4}-[A-Z0-9]{4}\b`)
)

type ssmLoginStatus struct {
	Phase   string `json:"phase"` // pending | authorize | ready | error
	URL     string `json:"url,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// afterStartGate is a test seam: it runs once HandleStartSession holds both gates, before it
// reads the meta. The lifecycle gate has read the meta already, so only a hook can put a cut file
// between that read and this handler's own.
var afterStartGate func()

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
	// Lock order everywhere: the lifecycle gate, then the retire gate (a Managed input holds a
	// lifecycle lease when it reaches resumeManaged). The retire gate is held from the check of
	// the retired mark to the end of the launch and the clearing of the mark, so a retire cannot
	// slip between them (retireGates).
	lease, ok := enterSessionGate(w, r, name)
	if !ok {
		return
	}
	defer lease.Release()
	g := retireGate(name)
	g.RLock()
	defer g.RUnlock()
	if afterStartGate != nil {
		afterStartGate()
	}
	auto := startIsAutomatic(r)
	// One read decides both questions. An automatic start never goes ahead on a meta it could not
	// read: the retired mark may be on it.
	cur, st := readMetaDecide(name)
	switch {
	case st == session.MetaUnreadable && auto:
		httpx.WriteErr(w, http.StatusConflict, "session_retired", errSessionRetired.Error())
		return
	case st == session.MetaUnreadable:
		httpx.WriteErr(w, http.StatusServiceUnavailable, "meta_unreadable",
			"the session's meta could not be read, so the start was not made; try again")
		return
	case st == session.MetaAbsent:
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	case auto && isRetired(cur):
		httpx.WriteErr(w, http.StatusConflict, "session_retired", errSessionRetired.Error())
		return
	}
	// The retire gate's read side is already held, so the act is recorded without taking it again.
	// Before the launch, and refused when it cannot be written: a person's start that goes
	// unrecorded is one a retire decided on an older list would stop.
	if !auto {
		if err := recordPersonAct(name); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "act_not_recorded",
				"the start was not made, because it could not be recorded as a person's: "+err.Error())
			return
		}
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
		if writeLaunchRefusal(w, err) {
			return
		}
		if !writeCodexReleasingErr(w, err) {
			httpx.WriteErr(w, http.StatusInternalServerError, "start_failed", err.Error())
		}
		return
	}
	// A deliberate start makes a retired session an ordinary one again (ADR 0137 §6). An
	// automatic one was refused above, so only a deliberate start reaches here with the mark.
	if !auto {
		if err := clearRetired(name); err != nil {
			// Started, but still marked: the mark keeps the Agent's own starts away, and the
			// person can press start again.
			httpx.WriteErr(w, http.StatusInternalServerError, "retire_clear_failed",
				"the session started, but its retired mark could not be cleared: "+err.Error())
			return
		}
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
	// capture-pane prints each row with a newline, but a pane that ends without one must not
	// leave a finished URL undetectable: this snapshot never grows a "next chunk". The
	// price is that a URL the CLI is still writing at the very end of the pane can show cut
	// short for one poll; the CLI prints a URL as one line, so that window is a single write.
	if url, code := DeviceAuthorization(buf + "\n"); url != "" {
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
//
// out is taken to be a prefix of a stream: a URL that ends exactly at the end of out is held
// back until a delimiter follows it. A caller passing a finished snapshot (a tmux pane)
// must terminate it with a newline, or a URL ending it is never reported.
func DeviceAuthorization(out string) (url, code string) {
	url = completeURL(ssmURLWithCode, out, false)
	if url == "" {
		url = completeURL(ssmDeviceURL, out, false)
	}
	if url == "" {
		url = completeURL(ssmSigninURL, out, true)
	}
	if url == "" {
		return "", ""
	}
	return url, ssmCodeRe.FindString(out)
}

// completeURL is the first (or, with last, the last) match of re in out that is followed by
// a delimiter (whitespace or a quote). A match that ends exactly at the end of out is
// refused: out is a prefix of a stream, the URL may still be arriving, and a cut-short URL
// shown to the member is wrong in a way the next chunk only corrects later. The same check
// also stops "x.signin.awsevil.example" from yielding the "x.signin.aws" prefix of another
// host. The CLI prints the plain signin URL first and the code-autofill one last, hence
// last for that pattern.
//
// A caller whose out is a finished snapshot rather than a stream (the tmux pane in
// parseSSMLogin) must terminate it with a newline first: a snapshot is re-read unchanged,
// so a URL that ends it would otherwise never be detected.
func completeURL(re *regexp.Regexp, out string, last bool) string {
	url := ""
	for _, m := range re.FindAllStringIndex(out, -1) {
		if m[1] < len(out) && strings.ContainsRune(" \t\r\n\"'", rune(out[m[1]])) {
			url = out[m[0]:m[1]]
			if !last {
				break
			}
		}
	}
	return url
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
