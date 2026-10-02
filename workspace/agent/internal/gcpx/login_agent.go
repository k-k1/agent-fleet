package gcpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudlogin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// The Agent's half of ADR 0107 decision 3: it lists the pending requests (joined with
// Settings, so nothing a caller wrote decides what is shown), runs `gcloud auth login
// --no-launch-browser` only when the member presses "Log in", shows the checked sign-in URL
// only to the tab that pressed, and writes the verification code that tab posts to that
// gcloud's stdin once.
//
// The gcloud root's lock: the terminal login holds it for its whole run, because gcloud
// writes core/account into the configuration and nothing may read or rewrite it meanwhile.
// A Console login waits on a person for minutes, and holding the lock that long would stall
// every af-gcloud-exec run of every profile. gcloud touches the configuration only before it
// prints the URL (when a stored credential lets it finish at once) and after it has the
// code, so the attempt holds the lock exactly then: from the start until the URL is out,
// and from the code's submit until the process has exited.

// LoginGCloudBin finds (or installs) gcloud for a login attempt. main sets it.
var LoginGCloudBin = func() (string, error) { return exec.LookPath("gcloud") }

// loginAttemptTimeout outlives the sign-in a person does in the browser.
const loginAttemptTimeout = 15 * time.Minute

// urlWait bounds how long an attempt may run without printing its sign-in URL: it holds the
// root's lock meanwhile, which every af-gcloud-exec run waits on. gcloud prints the URL, or
// finishes with a stored credential, within seconds. A var for tests.
var urlWait = time.Minute

// msgNoURL ends an attempt that printed no sign-in URL in time.
const msgNoURL = "gcloud printed no sign-in URL"

// maxCodeBody bounds the code route's body. A verification code is under 100 characters.
const maxCodeBody = 4 << 10

// codeRe is what a verification code may look like. One line, nothing a prompt after the
// code could take as an answer: the code is the only line gcloud's stdin ever receives.
var codeRe = regexp.MustCompile(`^[A-Za-z0-9/._~+=-]{1,512}$`)

// The sign-in URL gcloud's --no-launch-browser login must print (decision 3 step 2).
const (
	signInHost     = "accounts.google.com"
	signInRedirect = "https://sdk.cloud.google.com/authcode.html"
)

// Messages an attempt ends with. Fixed text: the Console maps them, and none may quote
// gcloud's output, which holds the URL.
const (
	msgUnexpectedURL = "unexpected sign-in URL"
	msgPrompt        = "gcloud asked a question the Console login does not answer (on a Compute Engine VM: whether to use a personal account); log in from a terminal"
	msgProfileMoved  = "the profile changed in Settings during the login; start it again"
)

// urlRe finds a URL gcloud printed; it is taken only once whitespace follows it, so a URL
// still arriving in pieces is never checked half-read.
var urlRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s]+\s`)

// promptRe is a yes/no question gcloud asks on stdin. Before the URL nothing may answer
// it: the only line the Console ever writes is the code.
var promptRe = regexp.MustCompile(`\((?:Y/n|y/N)\)`)

// signInURL parses gcloud's output so far: the sign-in URL once it is there and passes the
// check, "" while it is not there yet, or an error that ends the attempt.
func signInURL(out string) (string, error) {
	m := urlRe.FindStringIndex(out)
	if loc := promptRe.FindStringIndex(out); loc != nil && (m == nil || loc[0] < m[0]) {
		return "", errors.New(msgPrompt)
	}
	if m == nil {
		return "", nil
	}
	raw := strings.TrimSpace(out[m[0]:m[1]])
	if !signInURLAllowed(raw) {
		return "", errors.New(msgUnexpectedURL)
	}
	return raw, nil
}

// signInURLAllowed is decision 3 step 2: https, the host exactly accounts.google.com, no
// userinfo, port or fragment, and exactly one redirect_uri, exactly gcloud's auth-code page.
// The URL carries PKCE, so a code from it is redeemable only by the gcloud that printed it.
func signInURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Host != signInHost ||
		u.Fragment != "" || strings.Contains(raw, "#") {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	r := q["redirect_uri"]
	return len(r) == 1 && r[0] == signInRedirect
}

// rootHold is the gcloud root's lock as one attempt holds it. take and release may race
// with the process's exit: a hold closed by the exit releases what is handed to it after.
type rootHold struct {
	mu     sync.Mutex
	unlock func()
	closed bool
}

func (h *rootHold) take(unlock func()) {
	h.mu.Lock()
	if h.closed || h.unlock != nil {
		h.mu.Unlock()
		unlock()
		return
	}
	h.unlock = unlock
	h.mu.Unlock()
}

func (h *rootHold) held() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.unlock != nil
}

func (h *rootHold) release() {
	h.mu.Lock()
	u := h.unlock
	h.unlock = nil
	h.mu.Unlock()
	if u != nil {
		u()
	}
}

// close releases the lock for good: the process is gone.
func (h *rootHold) close() {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	h.release()
}

// consoleAttempt is what the code route needs of an attempt beyond cloudlogin's: the
// profile version it started for and its hold on the root.
type consoleAttempt struct {
	p    Profile
	hold *rootHold
	// gone is set (under attemptsMu) once the process is gone, so an attempt whose process
	// ended before Start returned is never registered.
	gone bool
	// exchanged is set once gcloud took a code: only such a login is a new sign-in.
	exchanged atomic.Bool
}

var (
	attemptsMu sync.Mutex
	attempts   = map[string]*consoleAttempt{}
)

// startLoginAttempt runs gcloud's login for p in the clean environment against the Agent's
// root, as a new attempt (cloudlogin.Start: own process group, one attempt per profile, the
// id only for the caller). requestID is "" for a login started from Settings. force passes
// --force, so a stored credential is not reused.
func startLoginAttempt(bin, requestID string, p Profile, force bool) (*cloudlogin.Attempt, error) {
	root, unlock, err := lockRootWithin(rootBusyWait)
	if err != nil {
		return nil, err
	}
	// The version of p this press read from Settings must be the one the root holds, as for
	// the terminal login: gcloud writes its account into that configuration.
	if _, err := syncedAs(root, p); err != nil {
		unlock()
		return nil, err
	}
	hold := &rootHold{unlock: unlock}
	ca := &consoleAttempt{p: p, hold: hold}
	var id string
	var noURL *time.Timer
	// urlOnce lets go of the start's lock the first time the URL is seen, and only then:
	// Parse runs again on every later output (the whole output so far, URL included), and
	// by then the lock it would release is the one the code's submit took for the exchange.
	var urlOnce sync.Once
	// The last output, kept only in memory to word a failure; never logged.
	var outMu sync.Mutex
	var lastOut string
	a, err := logins.Start(ConfigName(p.Name), requestID, p.Name, cloudlogin.Process{
		Name:    "gcloud auth login",
		Path:    bin,
		Args:    LoginArgs(p, force),
		Env:     AgentEnv(os.Environ(), root),
		Timeout: loginAttemptTimeout,
		Stdin:   true,
		Parse: func(out string) (string, string, error) {
			outMu.Lock()
			lastOut = out
			outMu.Unlock()
			u, err := signInURL(out)
			if u != "" {
				// gcloud now waits for the code and writes nothing until it has one.
				urlOnce.Do(hold.release)
			}
			return u, "", err
		},
		Exited: func(err error) (bool, string) {
			outMu.Lock()
			out := lastOut
			outMu.Unlock()
			return finishLogin(bin, p, hold, err, out, ca.exchanged.Load())
		},
		Cleanup: func() {
			hold.close()
			attemptsMu.Lock()
			if noURL != nil {
				noURL.Stop()
			}
			ca.gone = true
			delete(attempts, id)
			attemptsMu.Unlock()
		},
	})
	if err != nil {
		hold.close()
		return nil, err
	}
	attemptsMu.Lock()
	id = a.ID
	if !ca.gone {
		attempts[id] = ca
		noURL = time.AfterFunc(urlWait, func() {
			if a.View().Phase == cloudlogin.PhaseStarting {
				a.End(cloudlogin.PhaseFailed, msgNoURL)
			}
		})
	}
	attemptsMu.Unlock()
	return a, nil
}

// finishLogin decides how a login process ended, under the root's lock (taken back if the
// attempt let go of it): done only when gcloud exited cleanly, the configuration now
// selects an account with a user credential (the profile's own when it names one), and a
// token can be minted from it in the clean environment — decision 3 step 5's "print-access-
// token succeeds", checked once here rather than on every sweep. A done login is recorded;
// that mark is what settles a request (loginBackend.Landed) — but only for a login that
// exchanged a code (exchanged). One that ended at once on a stored credential is done for
// the member, and records nothing: it is the same credential, possibly the very one Google
// rejected (gcloud reuses a cached access token without asking Google), and that must not
// settle a request filed for the rejection (step 5).
func finishLogin(bin string, p Profile, hold *rootHold, err error, out string, exchanged bool) (bool, string) {
	if err != nil {
		msg := "gcloud auth login did not complete"
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			msg = fmt.Sprintf("gcloud auth login exited with status %d", ee.ExitCode())
		}
		if detail := loginFailure(out); detail != "" {
			msg += ": " + detail
		}
		return false, msg
	}
	if !hold.held() {
		_, unlock, lerr := lockRootWithin(rootBusyWait)
		if lerr != nil {
			return false, "the login finished but could not be checked: " + lerr.Error()
		}
		hold.take(unlock)
	}
	root := ConfigRoot()
	account := readProperty(configPath(root, p.Name), "core", "account")
	switch {
	case account == "" || !emailRe.MatchString(account):
		return false, "the login finished but selected no account"
	case p.Account != "" && account != p.Account:
		return false, "the login finished for another account than the profile's"
	}
	if kind, err := credentialType(root, account); err != nil || kind != "authorized_user" {
		return false, "the login finished but left no user credential"
	}
	// The user's own credential, without the profile's impersonation: whether the login
	// works is the question here; a permission the account lacks on the service account is
	// the waiting run's to report, with its reason.
	user := p
	user.ImpersonateServiceAccount = ""
	if _, err := mint(bin, AgentEnv(os.Environ(), root), user); err != nil {
		if errors.Is(err, ErrLoginRequired) {
			return false, "the login finished but Google refused the credential: " + anyURL.ReplaceAllString(err.Error(), "<url>")
		}
		return false, "the login finished but no token could be minted with it: " + anyURL.ReplaceAllString(err.Error(), "<url>")
	}
	if !exchanged {
		return true, ""
	}
	if err := recordLogin(root, account); err != nil {
		return false, "the login finished but could not be recorded: " + err.Error()
	}
	return true, ""
}

// anyURL matches a URL anywhere in a message, so a failure never quotes the sign-in URL.
var anyURL = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://\S*`)

// loginFailure is gcloud's ERROR line of a failed login, bounded, without URLs or anything
// token-shaped (gcloudError).
func loginFailure(out string) string {
	if !strings.Contains(out, "ERROR:") {
		return ""
	}
	return anyURL.ReplaceAllString(gcloudError(out), "<url>")
}

// syncForLogin pulls Settings and rewrites the configurations before a press starts a login
// (decision 3 step 1), waiting a bounded time for the root's lock. A var for tests.
var syncForLogin = func() (SyncResult, error) {
	return syncLocking(func() (string, func(), error) { return lockRootWithin(rootBusyWait) })
}

// startFor is both start routes after their own lookups: re-sync, refuse a profile that is
// not exported, then start. recorded is the state the request was filed against (nil from
// Settings): a request whose profile changed since is not started.
func startFor(w http.ResponseWriter, r *http.Request, name, requestID string, recorded *LoginState, force bool) {
	res, err := syncForLogin()
	if err != nil || !res.Fetched {
		msg := "the workspace could not read Settings just now"
		if err != nil {
			msg += ": " + err.Error()
		}
		httpx.WriteErr(w, http.StatusServiceUnavailable, "settings_unavailable", msg)
		return
	}
	for _, c := range res.Conflicts {
		if c.Name == name {
			httpx.WriteErr(w, http.StatusConflict, "not_exported",
				"Settings labels "+strings.Join(c.Labels, " / ")+" all map to this name; rename all but one")
			return
		}
	}
	if why, ok := res.Invalid[name]; ok {
		httpx.WriteErr(w, http.StatusConflict, "not_exported", why)
		return
	}
	p, ok := res.Exported[name]
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_a_settings_profile", "no Settings profile with that name reached this workspace")
		return
	}
	if recorded != nil && readLoginState(name).Profile != recorded.Profile {
		httpx.WriteErr(w, http.StatusConflict, "profile_changed", "the profile changed in Settings since the login was requested")
		return
	}
	bin, err := LoginGCloudBin()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "no_gcloud", err.Error())
		return
	}
	a, err := startLoginAttempt(bin, requestID, p, force)
	switch {
	case errors.Is(err, errRootBusy):
		httpx.WriteErr(w, http.StatusConflict, "busy", err.Error())
		return
	case errors.Is(err, ErrSettingsChanged):
		httpx.WriteErr(w, http.StatusConflict, "profile_changed", err.Error())
		return
	case err != nil:
		httpx.WriteErr(w, http.StatusInternalServerError, "start_failed", err.Error())
		return
	}
	from := "request"
	if requestID == "" {
		from = "settings"
	}
	log.Printf("gcp-login: start profile=%s attempt=%s from=%s force=%t relayed=%t", p.Name, AttemptRef(a.ID), from, force,
		cloudlogin.RelayedByCP(r))
	httpx.WriteJSON(w, http.StatusOK, cloudlogin.StartWire{Attempt: a.ID})
}

func loginSettings() map[string]Profile {
	m, _, ok := CachedSettings()
	if !ok {
		return nil
	}
	return m
}

// settingsFor is the Settings profile a request names, if Settings still defines it.
func settingsFor(r cloudlogin.Request[LoginState], settings map[string]Profile) (Profile, bool) {
	sp, ok := settings[r.Profile]
	return sp, ok && r.Key == ConfigName(sp.Name) && InvalidReason(sp) == ""
}

// loginRequestWire is one pending request as the Console shows it. Project and account come
// from Settings (the account the login will be held to, or "" when the login picks it); no
// attempt id or URL is ever here.
type loginRequestWire struct {
	ID      string                  `json:"id"`
	Profile string                  `json:"profile"`
	Label   string                  `json:"label"`
	Project string                  `json:"project"`
	Account string                  `json:"account,omitempty"`
	Relogin bool                    `json:"relogin,omitempty"`
	Waiters []cloudlogin.WaiterWire `json:"waiters"`
	FirstAt string                  `json:"firstAt"`
	LastAt  string                  `json:"lastAt"`
}

type loginListWire struct {
	Requests []loginRequestWire `json:"requests"`
}

// HandleLoginList is GET /gcp-login: the pending requests the Console may show.
func HandleLoginList(w http.ResponseWriter, r *http.Request) {
	settings := loginSettings()
	out := []loginRequestWire{}
	for _, req := range logins.Sweep(time.Now()) {
		sp, ok := settingsFor(req, settings)
		if !ok {
			continue
		}
		out = append(out, loginRequestWire{ID: req.ID, Profile: sp.Name, Label: sp.Label, Project: sp.Project,
			Account: sp.Account, Relogin: req.Snapshot.rejected(), Waiters: cloudlogin.RecentWaiters(req.Waiters, 5),
			FirstAt: req.FirstAt, LastAt: req.LastAt})
	}
	httpx.WriteJSON(w, http.StatusOK, loginListWire{Requests: out})
}

// HandleLoginStart is POST /gcp-login/{id}/start: a new attempt for the request's profile,
// which ends the one running before it. A request filed because Google rejected the stored
// credential starts with --force (decision 3 step 2).
func HandleLoginStart(w http.ResponseWriter, r *http.Request) {
	req, ok := logins.Pending(r.PathValue("id"))
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no pending login request with that id")
		return
	}
	if _, ok := settingsFor(req, loginSettings()); !ok {
		httpx.WriteErr(w, http.StatusConflict, "not_a_settings_profile", "the request's profile is no longer a Settings profile")
		return
	}
	snap := req.Snapshot
	startFor(w, r, req.Profile, req.ID, &snap, snap.rejected())
}

// HandleLoginCancel is POST /gcp-login/{id}/cancel: end any attempt the request started,
// write the cancel marker, then drop the request.
func HandleLoginCancel(w http.ResponseWriter, r *http.Request) { logins.HandleCancel(w, r) }

// HandleProfileLoginStart is POST /gcp-login/profiles/{name}/start: the Settings row's "Log
// in", or with ?force=1 its "Log in again" for a member who knows their login was revoked
// (decision 3 step 5). It shares the profile's one attempt slot with the request route.
func HandleProfileLoginStart(w http.ResponseWriter, r *http.Request) {
	startFor(w, r, r.PathValue("name"), "", nil, r.URL.Query().Get("force") == "1")
}

// HandleProfileLoginAttempt is GET /gcp-login/profiles/{name}/attempts/{attempt}: the
// phase of one attempt of that profile, and its URL only while it waits for the code.
func HandleProfileLoginAttempt(w http.ResponseWriter, r *http.Request) {
	logins.WriteAttempt(w, r.PathValue("attempt"), func(a *cloudlogin.Attempt) bool { return a.Profile == r.PathValue("name") })
}

type codeWire struct {
	Code string `json:"code"`
}

// HandleProfileLoginCode is POST /gcp-login/profiles/{name}/attempts/{attempt}/code: the
// verification code the member pasted, written to that attempt's gcloud once (decision 3
// step 4). It is refused for an attempt of another profile, an unknown one, and one that is
// not waiting for a code (cancelled, replaced, ended, or already given one). The code is
// never logged, kept or echoed; the log names the profile and the attempt's reference.
func HandleProfileLoginCode(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("name"), r.PathValue("attempt")
	var body codeWire
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCodeBody)).Decode(&body); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "the body must be {\"code\": \"…\"} and at most 4 KiB")
		return
	}
	code := strings.TrimSpace(body.Code)
	if !codeRe.MatchString(code) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_code", "that does not look like a verification code")
		return
	}
	a := logins.Attempt(id)
	attemptsMu.Lock()
	ca := attempts[id]
	attemptsMu.Unlock()
	if a == nil || ca == nil || a.Profile != name {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no login attempt of this profile with that id")
		return
	}
	// Checked again under the attempt's lock by Submit; this only keeps a code for an
	// attempt that is not waiting from queueing behind the root's lock.
	if a.View().Phase != cloudlogin.PhaseAuthorize {
		httpx.WriteErr(w, http.StatusConflict, "not_awaiting_code", cloudlogin.ErrNotAwaitingCode.Error())
		return
	}
	root, unlock, err := lockRootWithin(rootBusyWait)
	if err != nil {
		httpx.WriteErr(w, http.StatusConflict, "busy", err.Error())
		return
	}
	// The configuration gcloud is about to write its account into must still be the version
	// the press started for; a sync may have applied another while the member signed in.
	if _, err := syncedAs(root, ca.p); err != nil {
		unlock()
		if a.Live() {
			a.End(cloudlogin.PhaseFailed, msgProfileMoved)
		}
		httpx.WriteErr(w, http.StatusConflict, "profile_changed", msgProfileMoved)
		return
	}
	if err := a.Submit(code); err != nil {
		unlock()
		if errors.Is(err, cloudlogin.ErrNotAwaitingCode) {
			httpx.WriteErr(w, http.StatusConflict, "not_awaiting_code", err.Error())
			return
		}
		// The write failed: the process is gone or going; its exit ends the attempt.
		httpx.WriteErr(w, http.StatusConflict, "not_awaiting_code", "the login process did not take the code")
		return
	}
	// From here gcloud redeems the code and writes the configuration: the attempt keeps the
	// lock until its process has exited.
	ca.exchanged.Store(true)
	ca.hold.take(unlock)
	log.Printf("gcp-login: code profile=%s attempt=%s relayed=%t", name, AttemptRef(id), cloudlogin.RelayedByCP(r))
	httpx.WriteJSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{true})
}

// Login states of a Settings profile, for the row's button. Nothing here can tell a login
// Google has revoked from a good one until something uses it (decision 3 step 5).
const (
	loginStateSignedIn = "signed_in" // an account with a user credential is selected
	loginStateNone     = "none"
)

type profileLoginStateWire struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Label   string `json:"label,omitempty"`
	Project string `json:"project,omitempty"`
	Account string `json:"account,omitempty"`
}

type profileLoginStatesWire struct {
	Profiles []profileLoginStateWire `json:"profiles"`
}

// HandleProfileLoginStates is GET /gcp-login/profiles: each Settings profile's login state,
// from the last poll's list (a read must not pull from the CP).
func HandleProfileLoginStates(w http.ResponseWriter, r *http.Request) {
	out := []profileLoginStateWire{}
	for name, sp := range loginSettings() {
		st := readLoginState(name)
		p := profileLoginStateWire{Name: name, State: loginStateNone, Label: sp.Label, Project: sp.Project, Account: st.Account}
		if st.Account != "" && st.Credential {
			p.State = loginStateSignedIn
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	httpx.WriteJSON(w, http.StatusOK, profileLoginStatesWire{Profiles: out})
}
