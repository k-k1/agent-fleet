package awsx

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// The Agent's half of ADR 0102: it lists the pending requests (joined with Settings, so
// nothing a caller wrote decides the account shown), runs `aws sso login` only when the
// member presses "Log in", and shows the code of that attempt to the tab that asked.
// Every Agent route is callable with AGENT_TOKEN, which agents hold; what keeps a code
// started by anyone else off the member's screen is the attempt id, returned only to
// the caller that started it.

// LoginAWSBin finds (or installs) the aws CLI for a login attempt. main sets it.
var LoginAWSBin = func() (string, error) { return exec.LookPath("aws") }

// loginAttemptTimeout outlives the device code (about ten minutes), so the CLI decides
// when the code is dead and says so.
const loginAttemptTimeout = 15 * time.Minute

const (
	attemptStarting  = "starting"
	attemptAuthorize = "authorize"
	attemptDone      = "done"
	attemptFailed    = "failed"
	attemptReplaced  = "replaced"
	attemptCancelled = "cancelled"
	attemptGone      = "gone"
)

type loginAttempt struct {
	id, requestID, ssoSession, profile string
	allowed                            []string

	mu      sync.Mutex
	phase   string
	url     string
	code    string
	message string
	ended   time.Time
	stop    context.CancelFunc
}

func (a *loginAttempt) live() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.phase == attemptStarting || a.phase == attemptAuthorize
}

// end moves a live attempt to phase and stops its process; a finished one keeps its phase.
func (a *loginAttempt) end(phase, message string) {
	a.mu.Lock()
	if a.phase == attemptStarting || a.phase == attemptAuthorize {
		a.phase, a.message, a.ended = phase, message, time.Now()
		a.url, a.code = "", ""
	}
	stop := a.stop
	a.mu.Unlock()
	if stop != nil {
		stop()
	}
}

var loginAttempts = struct {
	sync.Mutex
	byID    map[string]*loginAttempt
	current map[string]*loginAttempt // by sso-session
}{byID: map[string]*loginAttempt{}, current: map[string]*loginAttempt{}}

// liveAttemptFor reports whether an attempt for ssoSession is still running: its request
// must not expire under it.
func liveAttemptFor(ssoSession string) bool {
	loginAttempts.Lock()
	a := loginAttempts.current[ssoSession]
	loginAttempts.Unlock()
	return a != nil && a.live()
}

// pruneAttemptsLocked forgets attempts that ended long ago.
func pruneAttemptsLocked(now time.Time) {
	for id, a := range loginAttempts.byID {
		a.mu.Lock()
		old := !a.ended.IsZero() && now.Sub(a.ended) > 30*time.Minute
		a.mu.Unlock()
		if old {
			delete(loginAttempts.byID, id)
			if loginAttempts.current[a.ssoSession] == a {
				delete(loginAttempts.current, a.ssoSession)
			}
		}
	}
}

// settingsFor is the Settings profile a request names, if Settings still defines it the
// way the request was filed for.
func settingsFor(r LoginRequest, settings map[string]Profile) (Profile, bool) {
	sp, ok := settings[r.Profile]
	return sp, ok && r.SSOSession == "af-"+sp.Name && IncompleteReason(sp) == ""
}

// sweepLoginRequests drops what is settled and returns what is still pending. A request is
// resolved once the token cache changed from the state it recorded and holds an unexpired
// token, however the login happened; it expires loginRequestTTL after the last run asked,
// never while an attempt for it runs. Only the Agent removes request files.
func sweepLoginRequests(now time.Time) []LoginRequest {
	unlock, err := lockLogin()
	if err != nil {
		return nil
	}
	defer unlock()
	entries, _ := os.ReadDir(loginDir())
	var out []LoginRequest
	for _, e := range entries {
		path := filepath.Join(loginDir(), e.Name())
		switch {
		case strings.HasSuffix(e.Name(), ".cancel.json"):
			var m cancelMarker
			at, perr := time.Time{}, error(nil)
			if readJSON(path, &m) {
				at, perr = time.Parse(time.RFC3339Nano, m.At)
			}
			if perr != nil || at.IsZero() || now.Sub(at) >= loginCancelHold {
				_ = os.Remove(path)
			}
		case strings.HasSuffix(e.Name(), ".request.json"):
			var r LoginRequest
			if !readJSON(path, &r) || r.ID == "" {
				_ = os.Remove(path)
				continue
			}
			cur := ReadCacheState(r.SSOSession)
			if cur != r.Cache && cur.Unexpired(now) {
				_ = os.Remove(path)
				continue
			}
			last, perr := time.Parse(time.RFC3339Nano, r.LastAt)
			if (perr != nil || now.Sub(last) >= loginRequestTTL) && !liveAttemptFor(r.SSOSession) {
				_ = os.Remove(path)
				continue
			}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstAt < out[j].FirstAt })
	return out
}

// loginRequestWire is one pending request as the Console shows it. The account, role and
// label come from Settings; waiters are the cut-down text of decision 1. No attempt id,
// URL or code is ever here.
type loginRequestWire struct {
	ID        string       `json:"id"`
	Profile   string       `json:"profile"`
	Label     string       `json:"label"`
	AccountID string       `json:"accountId"`
	RoleName  string       `json:"roleName"`
	Waiters   []waiterWire `json:"waiters"`
	FirstAt   string       `json:"firstAt"`
	LastAt    string       `json:"lastAt"`
}

type loginListWire struct {
	Requests []loginRequestWire `json:"requests"`
}

type loginStartWire struct {
	Attempt string `json:"attempt"`
}

// loginAttemptWire carries a URL and a code only while the attempt waits for the member.
type loginAttemptWire struct {
	Phase   string `json:"phase"`
	URL     string `json:"url,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type loginCancelWire struct {
	OK bool `json:"ok"`
}

type waiterWire struct {
	Session string `json:"session,omitempty"`
	Command string `json:"command,omitempty"`
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
	for _, req := range sweepLoginRequests(time.Now()) {
		sp, ok := settingsFor(req, settings)
		if !ok {
			continue
		}
		seen := map[waiterWire]bool{}
		waiters := []waiterWire{}
		for i := len(req.Waiters) - 1; i >= 0 && len(waiters) < 5; i-- {
			ww := waiterWire{Session: req.Waiters[i].Session, Command: req.Waiters[i].Command}
			if !seen[ww] {
				seen[ww] = true
				waiters = append(waiters, ww)
			}
		}
		out = append(out, loginRequestWire{ID: req.ID, Profile: sp.Name, Label: sp.Label, AccountID: sp.AccountID,
			RoleName: sp.RoleName, Waiters: waiters, FirstAt: req.FirstAt, LastAt: req.LastAt})
	}
	httpx.WriteJSON(w, http.StatusOK, loginListWire{Requests: out})
}

var loginIDRe = regexp.MustCompile(`^[0-9a-f]{24}$`)

// pendingRequest finds the pending request with id.
func pendingRequest(id string) (LoginRequest, bool) {
	if !loginIDRe.MatchString(id) {
		return LoginRequest{}, false
	}
	for _, r := range sweepLoginRequests(time.Now()) {
		if r.ID == id {
			return r, true
		}
	}
	return LoginRequest{}, false
}

// relayedByCP is a hint for the log only: the Control Plane marks what it relays, and an
// agent calling the Agent directly could set the same header.
func relayedByCP(r *http.Request) bool { return r.Header.Get("X-AF-Relay") == "cp" }

// HandleLoginStart is POST /aws-login/{id}/start: a new attempt, which ends the one
// running before it for the same sso-session.
func HandleLoginStart(w http.ResponseWriter, r *http.Request) {
	req, ok := pendingRequest(r.PathValue("id"))
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
	a, err := startLoginAttempt(bin, req, sp)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "start_failed", err.Error())
		return
	}
	log.Printf("aws-login: start profile=%s relayed=%t", sp.Name, relayedByCP(r))
	httpx.WriteJSON(w, http.StatusOK, loginStartWire{Attempt: a.id})
}

// HandleLoginAttempt is GET /aws-login/{id}/attempts/{attempt}: the phase of one attempt,
// and its URL and code only while it is waiting for the member.
func HandleLoginAttempt(w http.ResponseWriter, r *http.Request) {
	loginAttempts.Lock()
	a := loginAttempts.byID[r.PathValue("attempt")]
	loginAttempts.Unlock()
	if a == nil || a.requestID != r.PathValue("id") {
		// Unknown, or lost with an Agent restart: the modal offers to start again.
		httpx.WriteJSON(w, http.StatusOK, loginAttemptWire{Phase: attemptGone})
		return
	}
	a.mu.Lock()
	out := loginAttemptWire{Phase: a.phase, Message: a.message}
	if a.phase == attemptAuthorize {
		out.URL, out.Code = a.url, a.code
	}
	a.mu.Unlock()
	httpx.WriteJSON(w, http.StatusOK, out)
}

// HandleLoginCancel is POST /aws-login/{id}/cancel: end any attempt, write the cancel
// marker, then drop the request.
func HandleLoginCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	req, ok := pendingRequest(id)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no pending login request with that id")
		return
	}
	loginAttempts.Lock()
	cur := loginAttempts.current[req.SSOSession]
	loginAttempts.Unlock()
	if cur != nil {
		cur.end(attemptCancelled, "")
	}
	unlock, err := lockLogin()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "cancel_failed", err.Error())
		return
	}
	defer unlock()
	m := cancelMarker{RequestID: req.ID, Profile: req.Profile, At: time.Now().UTC().Format(time.RFC3339Nano), Cache: req.Cache}
	if err := writeJSONFile(markerPath(req.SSOSession), m); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "cancel_failed", err.Error())
		return
	}
	if cur, ok := readRequest(req.SSOSession); ok && cur.ID == req.ID {
		_ = os.Remove(requestPath(req.SSOSession))
	}
	log.Printf("aws-login: cancel profile=%s relayed=%t", req.Profile, relayedByCP(r))
	httpx.WriteJSON(w, http.StatusOK, loginCancelWire{OK: true})
}

// allowedDeviceHosts lists the hosts a verification URL may have for sp: the device
// authorization host of its SSO region, in that region's partition, and its start URL's.
func allowedDeviceHosts(sp Profile) []string {
	suffix := "amazonaws.com"
	if strings.HasPrefix(sp.SSORegion, "cn-") {
		suffix = "amazonaws.com.cn"
	}
	hosts := []string{"device.sso." + sp.SSORegion + "." + suffix}
	if u, err := url.Parse(sp.StartURL); err == nil && u.Hostname() != "" {
		hosts = append(hosts, strings.ToLower(u.Hostname()))
	}
	return hosts
}

// deviceURLAllowed compares the whole host name, never a part of it: the pattern that
// finds the URL also matches inside device.sso.evil.example.
func deviceURLAllowed(raw string, allowed []string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range allowed {
		if host == h {
			return true
		}
	}
	return false
}

func newAttemptID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// startLoginAttempt runs `aws sso login` for req's sso-session with a config holding
// only what Settings says, detached from every pane.
func startLoginAttempt(bin string, req LoginRequest, sp Profile) (*loginAttempt, error) {
	ini, err := ssoOnlyConfig(ssoInfo{Session: req.SSOSession, Scopes: "sso:account:access", StartURL: sp.StartURL,
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
	ctx, stop := context.WithTimeout(context.Background(), loginAttemptTimeout)
	cmd := exec.CommandContext(ctx, bin, "sso", "login", "--profile", ssoOnlyProfile, "--use-device-code", "--no-browser")
	cmd.Env = verifierEnv(baseEnv(os.Environ()), cfg)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	pr, pw, err := os.Pipe()
	if err != nil {
		stop()
		os.RemoveAll(dir)
		return nil, err
	}
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = pw, pw

	a := &loginAttempt{id: newAttemptID(), requestID: req.ID, ssoSession: req.SSOSession, profile: sp.Name,
		allowed: allowedDeviceHosts(sp), phase: attemptStarting, stop: stop}

	loginAttempts.Lock()
	prev := loginAttempts.current[req.SSOSession]
	pruneAttemptsLocked(time.Now())
	loginAttempts.byID[a.id] = a
	loginAttempts.current[req.SSOSession] = a
	loginAttempts.Unlock()
	if prev != nil {
		prev.end(attemptReplaced, "")
	}

	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		os.RemoveAll(dir)
		a.end(attemptFailed, "could not start aws sso login: "+err.Error())
		return a, nil
	}
	pw.Close()
	go readLoginOutput(a, pr)
	go func() {
		err := cmd.Wait()
		pr.Close()
		os.RemoveAll(dir)
		if err == nil && ReadCacheState(req.SSOSession).Unexpired(time.Now()) {
			a.end(attemptDone, "")
			return
		}
		msg := "aws sso login did not complete"
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			msg = fmt.Sprintf("aws sso login exited with status %d (the code may have expired or been denied)", ee.ExitCode())
		}
		a.end(attemptFailed, msg)
	}()
	return a, nil
}

// readLoginOutput watches the CLI's output for the verification URL and code. The
// output is kept only in memory and never logged: it holds the code.
func readLoginOutput(a *loginAttempt, r io.Reader) {
	var buf bytes.Buffer
	chunk := make([]byte, 4096)
	for {
		n, err := r.Read(chunk)
		if n > 0 && buf.Len() < 64<<10 {
			buf.Write(chunk[:n])
			url, code := sessionx.DeviceAuthorization(buf.String())
			if url != "" {
				if !deviceURLAllowed(url, a.allowed) {
					a.end(attemptFailed, "unexpected sign-in URL")
					return
				}
				a.mu.Lock()
				if a.phase == attemptStarting || a.phase == attemptAuthorize {
					a.phase, a.url, a.code = attemptAuthorize, url, code
				}
				a.mu.Unlock()
			}
		}
		if err != nil {
			return
		}
	}
}
