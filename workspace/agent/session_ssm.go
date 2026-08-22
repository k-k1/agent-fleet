package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
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
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	if _, ok := session.ReadMeta(name); !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	force := r.URL.Query().Get("force") == "1"
	if err := ensureSessionTmux(name, force); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "start_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func handleSSMLoginStatus(w http.ResponseWriter, r *http.Request) {
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
		// (later) are both visible regardless of pane size.
		if out, err := tmuxx.Cmd("capture-pane", "-p", "-S", "-", "-t", pane).Output(); err == nil {
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

// ssmConfigPath is the per-session ~/.aws config file for an SSM session. It is
// per-session (not shared) so concurrent SSM sessions to different accounts don't
// clobber each other's AWS_CONFIG_FILE; the SSO token cache stays in the default
// ~/.aws/sso/cache so one `aws sso login` is reused across sessions of the same
// portal. The ".aws" tree is denylisted from the file browser (fs.go).
func ssmConfigPath(name string) string {
	return filepath.Join(homeDir(), ".aws", "af-sessions", name+".config")
}

// SSM/SSO meta allowlists. These values are written into an INI aws config
// (writeSSMConfig) and the Profile also names the per-session config FILE: a
// newline in any value would let a crafted profile append arbitrary keys —
// `credential_process = <command>` then RUNS on the next aws invocation — and a
// "../" Profile would escape the af-sessions dir. Validated at the single write
// choke point so every caller (session launch, instance discovery, CloudWatch
// ops) is covered.
var (
	ssmProfileRe = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`) // no slash → filename-safe under its prefixes
	ssmRegionRe  = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	ssmAccountRe = regexp.MustCompile(`^[0-9]{1,20}$`)
	ssmRoleRe    = regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]{1,64}$`)
	ssmURLRe     = regexp.MustCompile(`^https://[!-~]+$`) // printable ASCII, no spaces/newlines
)

// validateSSMMeta rejects meta whose values could not have come from the Console
// forms (INI/path injection defense — the Agent must not trust its callers).
func validateSSMMeta(s session.SSMMeta) error {
	check := func(field, v string, re *regexp.Regexp, required bool) error {
		if v == "" {
			if required {
				return fmt.Errorf("ssm meta: %s is required", field)
			}
			return nil
		}
		if !re.MatchString(v) {
			return fmt.Errorf("ssm meta: invalid %s", field)
		}
		return nil
	}
	for _, e := range []error{
		check("profile", s.Profile, ssmProfileRe, true),
		check("sso start url", s.StartURL, ssmURLRe, true),
		check("sso region", s.SSORegion, ssmRegionRe, false),
		check("region", s.Region, ssmRegionRe, false),
		check("account id", s.AccountID, ssmAccountRe, false),
		check("role name", s.RoleName, ssmRoleRe, false),
	} {
		if e != nil {
			return e
		}
	}
	return nil
}

// writeSSMConfig writes an isolated aws config (sso-session + profile) from the
// non-secret SSM meta. Idempotent — rewritten on every (re)launch. Contains no
// secrets (only the SSO start URL / account / role).
func writeSSMConfig(path string, s session.SSMMeta) error {
	if err := validateSSMMeta(s); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	region := s.Region
	if region == "" {
		region = s.SSORegion
	}
	ssoName := "af-" + s.Profile
	var b strings.Builder
	fmt.Fprintf(&b, "[sso-session %s]\n", ssoName)
	fmt.Fprintf(&b, "sso_start_url = %s\n", s.StartURL)
	fmt.Fprintf(&b, "sso_region = %s\n", s.SSORegion)
	b.WriteString("sso_registration_scopes = sso:account:access\n\n")
	fmt.Fprintf(&b, "[profile %s]\n", s.Profile)
	fmt.Fprintf(&b, "sso_session = %s\n", ssoName)
	if s.AccountID != "" {
		fmt.Fprintf(&b, "sso_account_id = %s\n", s.AccountID)
	}
	if s.RoleName != "" {
		fmt.Fprintf(&b, "sso_role_name = %s\n", s.RoleName)
	}
	if region != "" {
		fmt.Fprintf(&b, "region = %s\n", region)
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// buildSSMProgram assembles the pane command for an SSM session: refresh SSO creds
// only when the cached token is missing/expired (surfacing the login URL in the
// terminal), then exec start-session. When StartURL is set an isolated aws config is
// generated; otherwise the profile is assumed to exist in the member's own ~/.aws.
func buildSSMProgram(name string, s session.SSMMeta, force bool) (string, error) {
	var b strings.Builder
	// Lean rootfs ships without the AWS CLI / Session Manager plugin; install the
	// pinned versions into the home on first use, with the progress streaming
	// into this very pane (docs/35 §35.7.2-6). No-op when both are present.
	b.WriteString("{ command -v aws && command -v session-manager-plugin; } >/dev/null 2>&1 || " +
		"workspace-agent install-awscli || { echo '[Agent Fleet] AWS CLI の導入に失敗しました（ネットワークを確認して再試行してください）'; exit 1; }; ")
	if s.StartURL != "" && s.Profile != "" {
		cfg := ssmConfigPath(name)
		if err := writeSSMConfig(cfg, s); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "export AWS_CONFIG_FILE=%s; ", session.ShellQuote(cfg))
	}
	if s.Profile != "" {
		fmt.Fprintf(&b, "export AWS_PROFILE=%s; ", session.ShellQuote(s.Profile))
	}
	// aws sso login refreshes only when the cached token is missing/expired.
	// --use-device-code forces the device-authorization grant (user_code + verify URL,
	// polled) instead of the default authorization-code+PKCE flow, which spins up a
	// local 127.0.0.1 listener and redirects the browser there — unreachable when the
	// browser is on the user's machine and the CLI runs in this remote container.
	// --no-browser prints the URL instead of trying to open a (nonexistent) browser.
	// Phishing guard: the device-code grant is only safe when the user approves a code
	// they themselves initiated. Warn right before the URL/code appears. force drops the
	// cached-token short-circuit (logout+login) so the user can re-authenticate on demand.
	if force {
		b.WriteString("echo '[Agent Fleet] 再ログインします（自分で開始したこのログインのみ承認してください）'; " +
			"aws sso logout >/dev/null 2>&1; aws sso login --use-device-code --no-browser; ")
	} else {
		b.WriteString("aws sts get-caller-identity >/dev/null 2>&1 || { " +
			"echo '[Agent Fleet] 自分で開始したこのログインのみ承認してください（身に覚えのないコード/URL は入力しない）'; " +
			"aws sso login --use-device-code --no-browser; }; ")
	}
	b.WriteString("exec aws ssm start-session")
	fmt.Fprintf(&b, " --target %s", session.ShellQuote(s.Target))
	if s.Document != "" {
		fmt.Fprintf(&b, " --document-name %s", session.ShellQuote(s.Document))
	}
	if s.Region != "" {
		fmt.Fprintf(&b, " --region %s", session.ShellQuote(s.Region))
	}
	return b.String(), nil
}
