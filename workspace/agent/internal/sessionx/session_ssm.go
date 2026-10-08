package sessionx

import (
	"errors"
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
	if _, ok := session.ReadMeta(name); !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	force := r.URL.Query().Get("force") == "1"
	if err := ensureSessionTmux(name, force); err != nil {
		if !writeCodexReleasingErr(w, err) {
			httpx.WriteErr(w, http.StatusInternalServerError, "start_failed", err.Error())
		}
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
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

// SsmConfigPath is the per-session ~/.aws config file for an SSM session. It is
// per-session (not shared) so concurrent SSM sessions to different accounts don't
// clobber each other's AWS_CONFIG_FILE; the SSO token cache stays in the default
// ~/.aws/sso/cache so one `aws sso login` is reused across sessions of the same
// portal. The ".aws" tree is denylisted from the file browser (fs.go).
func SsmConfigPath(name string) string {
	return filepath.Join(homeDir(), ".aws", "af-sessions", name+".config")
}

// SSM/SSO meta allowlists. These values are written into an INI aws config
// (WriteSSMConfig) and the Profile also names the per-session config FILE: a
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

	// Role chaining. The same shapes as the Control Plane's validateProfile: a value that
	// passes holds no newline and so cannot add a key of its own to the config.
	ssmRoleARNRe     = regexp.MustCompile(`^arn:aws[a-z-]*:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]{1,512}$`)
	ssmExternalIDRe  = regexp.MustCompile(`^[A-Za-z0-9+=,.@:/_-]{2,}$`)
	ssmSessionNameRe = regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]{2,64}$`)
)

// ValidateAssumeRole checks the role-chaining fields of s (RoleARN, SourceProfile,
// ExternalID, RoleSessionName, DurationSeconds) plus the profile name and region. The source
// must be a different profile name: botocore treats a profile naming itself as source as
// "use my own static keys", which is not what a chained Settings profile means.
func ValidateAssumeRole(s session.SSMMeta) error {
	if !ssmProfileRe.MatchString(s.Profile) {
		return errors.New("ssm meta: invalid profile")
	}
	if !ssmProfileRe.MatchString(s.SourceProfile) || s.SourceProfile == s.Profile {
		return errors.New("ssm meta: invalid source profile")
	}
	if !ssmRoleARNRe.MatchString(s.RoleARN) || strings.HasSuffix(s.RoleARN, "/") || strings.Contains(s.RoleARN, "//") {
		return errors.New("ssm meta: invalid role arn")
	}
	if s.ExternalID != "" && !(len(s.ExternalID) <= 1224 && ssmExternalIDRe.MatchString(s.ExternalID)) {
		return errors.New("ssm meta: invalid external id")
	}
	if s.RoleSessionName != "" && !ssmSessionNameRe.MatchString(s.RoleSessionName) {
		return errors.New("ssm meta: invalid role session name")
	}
	if s.DurationSeconds != 0 && (s.DurationSeconds < 900 || s.DurationSeconds > 43200) {
		return errors.New("ssm meta: invalid duration")
	}
	if s.Region != "" && !ssmRegionRe.MatchString(s.Region) {
		return errors.New("ssm meta: invalid region")
	}
	return nil
}

// RenderAssumeRoleProfile returns the [profile ...] section of s that assumes s.RoleARN from
// the profile s.SourceProfile, validated. Nothing in it is a secret: the credentials of the
// chain come from the source's Identity Center login at run time.
func RenderAssumeRoleProfile(s session.SSMMeta) (string, error) {
	if err := ValidateAssumeRole(s); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[profile %s]\n", s.Profile)
	fmt.Fprintf(&b, "role_arn = %s\n", s.RoleARN)
	fmt.Fprintf(&b, "source_profile = %s\n", s.SourceProfile)
	if s.ExternalID != "" {
		fmt.Fprintf(&b, "external_id = %s\n", s.ExternalID)
	}
	if s.RoleSessionName != "" {
		fmt.Fprintf(&b, "role_session_name = %s\n", s.RoleSessionName)
	}
	if s.DurationSeconds != 0 {
		fmt.Fprintf(&b, "duration_seconds = %d\n", s.DurationSeconds)
	}
	if s.Region != "" {
		fmt.Fprintf(&b, "region = %s\n", s.Region)
	}
	return b.String(), nil
}

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

// WriteSSMConfig writes an isolated aws config (sso-session + profile) from the
// non-secret SSM meta. Idempotent — rewritten on every (re)launch. Contains no
// secrets (only the SSO start URL / account / role).
func WriteSSMConfig(path string, s session.SSMMeta) error {
	ini, err := RenderSSMConfig(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(ini), 0o600)
}

// RenderSSMConfig returns the sso-session + profile sections for s, validated. The
// sso-session is named "af-<profile>" wherever it is written, so every file that
// describes the same profile shares one cached SSO login.
func RenderSSMConfig(s session.SSMMeta) (string, error) {
	if s.RoleARN != "" {
		return renderChained(s)
	}
	if s.SourceProfile != "" || s.ExternalID != "" || s.RoleSessionName != "" || s.DurationSeconds != 0 {
		return "", errors.New("ssm meta: assume-role fields without a role arn")
	}
	if err := validateSSMMeta(s); err != nil {
		return "", err
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
	return b.String(), nil
}

// renderChained is RenderSSMConfig for a profile that assumes a role: the source sso profile
// (named s.SourceProfile, with the sso-session "af-<source>" that the exported ~/.aws/config
// uses too, so one cached login serves both) followed by the assume-role profile. The SSO
// fields of s describe the source.
func renderChained(s session.SSMMeta) (string, error) {
	if err := ValidateAssumeRole(s); err != nil {
		return "", err
	}
	src := session.SSMMeta{Profile: s.SourceProfile, StartURL: s.StartURL, SSORegion: s.SSORegion,
		AccountID: s.AccountID, RoleName: s.RoleName}
	srcINI, err := RenderSSMConfig(src)
	if err != nil {
		return "", err
	}
	chain := s
	if chain.Region == "" {
		chain.Region = s.SSORegion
	}
	chainINI, err := RenderAssumeRoleProfile(chain)
	if err != nil {
		return "", err
	}
	return srcINI + "\n" + chainINI, nil
}

// ssmForgetLogin drops the cached login of the profile the pane is about to use, so the
// `aws sso login` after it has to run the device-code flow again. `aws sso logout` cannot do
// this: it revokes and deletes every SSO token in ~/.aws/sso/cache whatever --profile says,
// which would sign the member out of every other profile too.
//
// The token's key is the SHA-1 of the profile's sso_session (or of sso_start_url for a legacy
// profile). The role credentials' key is the SHA-1 of botocore's sorted, compact JSON of
// account, role and that session name or start URL; account and role may be set on the
// sso-session section only, hence the --sso-session fallback. Other profiles' role
// credentials must stay: a legacy profile whose token has expired still works from them
// until they expire. Only when a value holds a character the JSON would escape (non-ASCII,
// quotes, spaces...) and the shell cannot reproduce the key are every SSO role credential
// dropped instead, so the old identity is never left behind. Keys measured with aws-cli
// 2.36.46.
const ssmForgetLogin = `afs=$(aws configure get sso_session 2>/dev/null) || afs=; ` +
	`if [ -n "$afs" ]; then afk=$afs; afj="\"sessionName\":\"$afs\""; ` +
	`else afk=$(aws configure get sso_start_url 2>/dev/null) || afk=; afj="\"startUrl\":\"$afk\""; fi; ` +
	`afa=$(aws configure get sso_account_id 2>/dev/null) || { [ -n "$afs" ] && afa=$(aws configure get sso_account_id --sso-session "$afs" 2>/dev/null); } || afa=; ` +
	`afr=$(aws configure get sso_role_name 2>/dev/null) || { [ -n "$afs" ] && afr=$(aws configure get sso_role_name --sso-session "$afs" 2>/dev/null); } || afr=; ` +
	`if [ -n "$afk" ]; then ` +
	`rm -f "$HOME/.aws/sso/cache/$(printf '%s' "$afk" | sha1sum | cut -d' ' -f1).json"; ` +
	`case "$afk$afa$afr" in ` +
	`*[!A-Za-z0-9._:/@+=,~%?#-]*) for f in "$HOME"/.aws/cli/cache/*.json; do grep -qs '"ProviderType": *"sso"' "$f" && rm -f "$f" || :; done ;; ` +
	`*) [ -z "$afa" ] || [ -z "$afr" ] || rm -f "$HOME/.aws/cli/cache/$(printf '{"accountId":"%s","roleName":"%s",%s}' "$afa" "$afr" "$afj" | sha1sum | cut -d' ' -f1).json" ;; ` +
	`esac; fi; `

// buildSSMProgram assembles the pane command for an SSM session: refresh SSO creds
// only when the cached token is missing/expired (surfacing the login URL in the
// terminal), then exec start-session. When StartURL is set an isolated aws config is
// generated; otherwise the profile is assumed to exist in the member's own ~/.aws.
func buildSSMProgram(name string, s session.SSMMeta, force bool) (string, error) {
	var b strings.Builder
	// Lean rootfs ships without the AWS CLI / Session Manager plugin; install the
	// pinned versions into the home on first use, with the progress streaming
	// into this very pane (docs/log/35 §35.7.2-6). No-op when both are present.
	b.WriteString("{ command -v aws && command -v session-manager-plugin; } >/dev/null 2>&1 || " +
		"workspace-agent install-awscli || { echo '[Agent Fleet] AWS CLI の導入に失敗しました（ネットワークを確認して再試行してください）'; exit 1; }; ")
	if s.StartURL != "" && s.Profile != "" {
		cfg := SsmConfigPath(name)
		if err := WriteSSMConfig(cfg, s); err != nil {
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
	// A role-chaining profile has no login of its own: the login (and the cache the forced
	// re-login drops) is the source profile's, so those steps run under it and the session
	// goes back to the chained profile for the check and the start-session.
	loginAs, back := "", ""
	if s.RoleARN != "" && s.SourceProfile != "" {
		loginAs = fmt.Sprintf("export AWS_PROFILE=%s; ", session.ShellQuote(s.SourceProfile))
		back = fmt.Sprintf("export AWS_PROFILE=%s; ", session.ShellQuote(s.Profile))
	}
	if force {
		b.WriteString("echo '[Agent Fleet] 再ログインします（自分で開始したこのログインのみ承認してください）'; " +
			loginAs + ssmForgetLogin + "aws sso login --use-device-code --no-browser; " + back)
	} else {
		b.WriteString("aws sts get-caller-identity >/dev/null 2>&1 || { " +
			"echo '[Agent Fleet] 自分で開始したこのログインのみ承認してください（身に覚えのないコード/URL は入力しない）'; " +
			loginAs + "aws sso login --use-device-code --no-browser; " + back + "}; ")
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
