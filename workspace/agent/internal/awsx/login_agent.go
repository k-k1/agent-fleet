package awsx

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudlogin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// The Agent's half of ADR 0102: it lists the pending requests (joined with Settings, so
// nothing a caller wrote decides the account shown), runs `aws sso login` only when the
// member presses "Log in", and shows the code of that attempt to the tab that asked
// (cloudlogin keeps the attempt and its id).

// LoginAWSBin finds (or installs) the aws CLI for a login attempt. main sets it.
var LoginAWSBin = func() (string, error) { return exec.LookPath("aws") }

// loginAttemptTimeout outlives the device code (about ten minutes), so the CLI decides
// when the code is dead and says so.
const loginAttemptTimeout = 15 * time.Minute

// settingsFor is the Settings profile a request names, if Settings still defines it the
// way the request was filed for.
func settingsFor(r LoginRequest, settings map[string]Profile) (Profile, bool) {
	sp, ok := settings[r.Profile]
	return sp, ok && r.Key == "af-"+sp.Name && IncompleteReason(sp) == ""
}

// loginRequestWire is one pending request as the Console shows it. The account, role and
// label come from Settings; waiters are the cut-down text of decision 1. No attempt id,
// URL or code is ever here.
type loginRequestWire struct {
	ID        string                  `json:"id"`
	Profile   string                  `json:"profile"`
	Label     string                  `json:"label"`
	AccountID string                  `json:"accountId"`
	RoleName  string                  `json:"roleName"`
	Waiters   []cloudlogin.WaiterWire `json:"waiters"`
	FirstAt   string                  `json:"firstAt"`
	LastAt    string                  `json:"lastAt"`
}

type loginListWire struct {
	Requests []loginRequestWire `json:"requests"`
}

func loginSettings() map[string]Profile {
	m, _, ok := CachedSettings()
	if !ok {
		return nil
	}
	return m
}

// HandleLoginList is GET /aws-login: the pending requests the Console may show.
func HandleLoginList(w http.ResponseWriter, r *http.Request) {
	settings := loginSettings()
	out := []loginRequestWire{}
	for _, req := range logins.Sweep(time.Now()) {
		sp, ok := settingsFor(req, settings)
		if !ok {
			continue
		}
		out = append(out, loginRequestWire{ID: req.ID, Profile: sp.Name, Label: sp.Label, AccountID: sp.AccountID,
			RoleName: sp.RoleName, Waiters: cloudlogin.RecentWaiters(req.Waiters, 5), FirstAt: req.FirstAt, LastAt: req.LastAt})
	}
	httpx.WriteJSON(w, http.StatusOK, loginListWire{Requests: out})
}

// HandleLoginStart is POST /aws-login/{id}/start: a new attempt, which ends the one
// running before it for the same sso-session.
func HandleLoginStart(w http.ResponseWriter, r *http.Request) {
	req, ok := logins.Pending(r.PathValue("id"))
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no pending login request with that id")
		return
	}
	sp, ok := settingsFor(req, loginSettings())
	if !ok {
		httpx.WriteErr(w, http.StatusConflict, "not_a_settings_profile", "the request's profile is no longer a Settings profile")
		return
	}
	bin, err := LoginAWSBin()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "no_aws_cli", err.Error())
		return
	}
	a, err := startLoginAttempt(bin, req.Key, req.ID, sp)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "start_failed", err.Error())
		return
	}
	log.Printf("aws-login: start profile=%s relayed=%t", sp.Name, cloudlogin.RelayedByCP(r))
	httpx.WriteJSON(w, http.StatusOK, cloudlogin.StartWire{Attempt: a.ID})
}

// syncForLogin pulls Settings from the CP and rewrites the managed block, as the poll
// does. A row the member just added or edited can be up to PollInterval newer than the
// last poll, and the press must log in to what the row shows.
var syncForLogin = Sync

// HandleProfileLoginStart is POST /aws-login/profiles/{name}/start: the Settings > AWS profiles/SSM
// row's "Log in" (#1028). It needs no pending request, so no cancel hold can block it;
// it shares the sso-session's one attempt slot with the request route, so the two
// replace each other the same way two presses on one request do.
func HandleProfileLoginStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// Only a fresh list that was also written counts: the cache the offline path re-applies
	// can predate the row the member pressed, and a failed write leaves names in Exported
	// that are not in ~/.aws/config.
	res, err := syncForLogin()
	if err != nil || !res.Fetched {
		msg := "the workspace could not read Settings just now"
		if err != nil {
			msg += ": " + err.Error()
		}
		httpx.WriteErr(w, http.StatusServiceUnavailable, "settings_unavailable", msg)
		return
	}
	sp, ok := res.Settings[name]
	switch {
	case !ok:
		httpx.WriteErr(w, http.StatusNotFound, "not_a_settings_profile", "no Settings profile with that name reached this workspace")
		return
	case res.Incomplete[name] != "":
		httpx.WriteErr(w, http.StatusConflict, "incomplete_profile", res.Incomplete[name])
		return
	case !slices.Contains(res.Exported, name):
		// Shadowed by the member's own ~/.aws, held back by [DEFAULT], or refused by the
		// INI allowlist: the same checks af-aws-exec passes before it files a request, so
		// the login never writes a token under an af-<name> key a section of theirs uses.
		httpx.WriteErr(w, http.StatusConflict, "not_exported", "this profile is not in ~/.aws/config; `af-aws-exec --list` says why")
		return
	}
	bin, err := LoginAWSBin()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "no_aws_cli", err.Error())
		return
	}
	a, err := startLoginAttempt(bin, "af-"+sp.Name, "", sp)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "start_failed", err.Error())
		return
	}
	log.Printf("aws-login: start profile=%s from=settings relayed=%t", sp.Name, cloudlogin.RelayedByCP(r))
	httpx.WriteJSON(w, http.StatusOK, cloudlogin.StartWire{Attempt: a.ID})
}

// Login states of a Settings profile's token cache, for the row's badge. The badge shows no
// "expires in": the cache holds only the access token's expiry (about an hour), which the
// CLI renews with the refresh token until the portal session ends, and that end is written
// nowhere the Agent can read. expiresAt is set only for a login that cannot renew
// (readSSOExpiry), for the expiry warning.
const (
	loginStateSignedIn = "signed_in" // an access token that has not expired
	loginStateRenew    = "renew"     // expired, but a refresh token may renew it on next use
	loginStateNone     = "none"      // no cache, or nothing that can be renewed
)

// profileLoginStateWire carries Settings' account, role and label so the Console can open
// the login modal from an expiry warning without a second list. No token is ever here.
type profileLoginStateWire struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Label     string `json:"label,omitempty"`
	AccountID string `json:"accountId,omitempty"`
	RoleName  string `json:"roleName,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	Expiring  bool   `json:"expiring,omitempty"`
}

type profileLoginStatesWire struct {
	Profiles []profileLoginStateWire `json:"profiles"`
}

// profileLoginState reads the token cache of ssoSession. Neither token leaves this function.
func profileLoginState(ssoSession string, now time.Time) string {
	if ReadCacheState(ssoSession).Unexpired(now) {
		return loginStateSignedIn
	}
	var doc struct {
		RefreshToken string `json:"refreshToken"`
	}
	if readJSON(ssoCachePath(ssoSession), &doc) && doc.RefreshToken != "" {
		return loginStateRenew
	}
	return loginStateNone
}

// HandleProfileLoginStates is GET /aws-login/profiles: the login state of every Settings
// profile the Agent knows, from the last poll's list (a read must not pull from the CP).
func HandleProfileLoginStates(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	out := []profileLoginStateWire{}
	exported := ExportedIn(ConfigPath())
	for name, sp := range loginSettings() {
		p := profileLoginStateWire{Name: name, State: profileLoginState("af-"+name, now),
			Label: sp.Label, AccountID: sp.AccountID, RoleName: sp.RoleName}
		if end, ok := readSSOExpiry("af-"+name, now); ok {
			p.ExpiresAt = end.Format(time.RFC3339)
			// Only a profile in the managed block: the row's login refuses any other.
			p.Expiring = expiringAt(end, now) && slices.Contains(exported, name)
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	httpx.WriteJSON(w, http.StatusOK, profileLoginStatesWire{Profiles: out})
}

// HandleLoginAttempt is GET /aws-login/{id}/attempts/{attempt}: the phase of one attempt,
// and its URL and code only while it is waiting for the member.
func HandleLoginAttempt(w http.ResponseWriter, r *http.Request) {
	logins.WriteAttempt(w, r.PathValue("attempt"), func(a *cloudlogin.Attempt) bool { return a.RequestID == r.PathValue("id") })
}

// HandleProfileLoginAttempt is GET /aws-login/profiles/{name}/attempts/{attempt}. It
// answers only for attempts a row started, so neither route reads the other's code.
func HandleProfileLoginAttempt(w http.ResponseWriter, r *http.Request) {
	logins.WriteAttempt(w, r.PathValue("attempt"), func(a *cloudlogin.Attempt) bool {
		return a.RequestID == "" && a.Profile == r.PathValue("name")
	})
}

// HandleLoginCancel is POST /aws-login/{id}/cancel: end any attempt, write the cancel
// marker, then drop the request.
func HandleLoginCancel(w http.ResponseWriter, r *http.Request) { logins.HandleCancel(w, r) }

// allowedDeviceHosts lists the hosts a verification URL may have for sp: the device
// authorization host of its SSO region, in that region's partition, and its start URL's.
//
// An issuer-form start URL (https://identitycenter.amazonaws.com/ssoins-<id>) gets its
// instance's access portal back instead (measured: d-<id>.awsapps.com/start/#/device for
// both start URL forms of one instance). For that form only, the instance's documented
// portal endpoints are added. The classic d-<id> or alias label of an awsapps.com portal
// cannot be derived from ssoins-<id>, so the entry ".awsapps.com" admits exactly one label
// in front of it; the alternative IPv4 and dual-stack portals are derivable and compared
// whole. China has no per-instance awsapps.cn host, only shared start hosts (AWS China's
// IAM Identity Center allow lists).
func allowedDeviceHosts(sp Profile) []string {
	region := sp.SSORegion
	china := strings.HasPrefix(region, "cn-")
	suffix := "amazonaws.com"
	if china {
		suffix = "amazonaws.com.cn"
	}
	hosts := []string{"device.sso." + region + "." + suffix}
	u, err := url.Parse(sp.StartURL)
	if err != nil || u.Hostname() == "" {
		return hosts
	}
	host := strings.ToLower(u.Hostname())
	hosts = append(hosts, host)
	if host != "identitycenter."+suffix {
		return hosts
	}
	id := strings.ToLower(strings.Trim(u.Path, "/"))
	if !issuerIDRe.MatchString(id) {
		id = ""
	}
	if china {
		hosts = append(hosts, "start.home.awsapps.cn", "start."+region+".home.awsapps.cn")
		if id != "" {
			hosts = append(hosts, id+"."+region+".portal.amazonaws.com.cn",
				id+".portal."+region+".app.amazonwebservices.com.cn")
		}
		return hosts
	}
	hosts = append(hosts, ".awsapps.com")
	if id != "" {
		hosts = append(hosts, id+"."+region+".portal.amazonaws.com", id+".portal."+region+".app.aws")
	}
	return hosts
}

// issuerIDRe is the path of an issuer-form start URL; it must be one DNS label so it
// cannot smuggle a dot into the host built from it.
var issuerIDRe = regexp.MustCompile(`^ssoins-[0-9a-z]+$`)

// deviceURLAllowed compares the whole host name, never a part of it: the pattern that
// finds the URL also matches inside device.sso.evil.example. An entry starting with "."
// admits exactly one label in front of it, so evil-awsapps.com and a.b.awsapps.com stay
// out.
func deviceURLAllowed(raw string, allowed []string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range allowed {
		if strings.HasPrefix(h, ".") {
			label, ok := strings.CutSuffix(host, h)
			if ok && label != "" && !strings.Contains(label, ".") {
				return true
			}
			continue
		}
		if host == h {
			return true
		}
	}
	return false
}

// startLoginAttempt runs `aws sso login` for ssoSession with a config holding only what
// Settings says, detached from every pane. requestID is "" for a login started from
// Settings.
func startLoginAttempt(bin, ssoSession, requestID string, sp Profile) (*cloudlogin.Attempt, error) {
	ini, err := ssoOnlyConfig(ssoInfo{Session: ssoSession, Scopes: "sso:account:access", StartURL: sp.StartURL,
		Region: sp.SSORegion, Account: sp.AccountID, Role: sp.RoleName})
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "af-aws-login-")
	if err != nil {
		return nil, err
	}
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte(ini), 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	allowed := allowedDeviceHosts(sp)
	return logins.Start(ssoSession, requestID, sp.Name, cloudlogin.Process{
		Name:    "aws sso login",
		Path:    bin,
		Args:    []string{"sso", "login", "--profile", ssoOnlyProfile, "--use-device-code", "--no-browser"},
		Env:     verifierEnv(baseEnv(os.Environ()), cfg),
		Timeout: loginAttemptTimeout,
		Parse: func(out string) (string, string, error) {
			url, code := sessionx.DeviceAuthorization(out)
			if url != "" && !deviceURLAllowed(url, allowed) {
				return "", "", errors.New("unexpected sign-in URL")
			}
			return url, code, nil
		},
		Exited: func(err error) (bool, string) {
			if err == nil && ReadCacheState(ssoSession).Unexpired(time.Now()) {
				return true, ""
			}
			msg := "aws sso login did not complete"
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				msg = fmt.Sprintf("aws sso login exited with status %d (the code may have expired or been denied)", ee.ExitCode())
			}
			return false, msg
		},
		Cleanup: func() { os.RemoveAll(dir) },
	})
}
