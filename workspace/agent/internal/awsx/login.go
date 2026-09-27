package awsx

import (
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Console login requests (ADR 0102). af-aws-exec files one when an SSO login is needed
// and nobody is at a terminal; the Console shows a toast, and the device code starts
// only when the member presses "Log in" there (login_agent.go). The files are written
// by the CLI and the Agent alike, both running as the member, under one lock.

const (
	// loginRequestTTL is how long a request stays after the last run asked for it.
	loginRequestTTL = 15 * time.Minute
	// loginCancelHold is how long a cancel keeps new runs from filing again: without it
	// the next run would put the toast straight back.
	loginCancelHold = 10 * time.Minute
	// NoticeKindAWSLogin is the notification kind that carries a request id to the Console.
	NoticeKindAWSLogin = "aws-login-required"
)

// CacheState is what the SSO token cache of one sso-session held at one moment. Two
// states are compared, never interpreted: a cache that holds an unexpired token AWS
// rejects (a revoked session) must not look like a fresh login (ADR 0102 decision 1).
type CacheState struct {
	Present   bool   `json:"present"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	TokenHash string `json:"tokenHash,omitempty"`
}

// Unexpired reports whether the state holds a token whose expiry is still ahead.
func (c CacheState) Unexpired(now time.Time) bool {
	if !c.Present || c.TokenHash == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, c.ExpiresAt)
	if err != nil {
		// botocore writes "2026-09-27T01:02:03Z"; anything else is not a token it wrote.
		return false
	}
	return t.After(now)
}

// ssoCachePath is where botocore keeps the token of an sso-session: the SHA-1 of the
// session name, under ~/.aws/sso/cache.
func ssoCachePath(ssoSession string) string {
	sum := sha1.Sum([]byte(ssoSession))
	return filepath.Join(paths.HomeDir(), ".aws", "sso", "cache", hex.EncodeToString(sum[:])+".json")
}

// ReadCacheState reads the token cache of ssoSession. It never returns the token itself.
func ReadCacheState(ssoSession string) CacheState {
	b, err := os.ReadFile(ssoCachePath(ssoSession))
	if err != nil {
		return CacheState{}
	}
	var doc struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   string `json:"expiresAt"`
	}
	if json.Unmarshal(b, &doc) != nil {
		// Present but unreadable still differs from absent, so a rewrite shows as a change.
		sum := sha256.Sum256(b)
		return CacheState{Present: true, TokenHash: "raw:" + hex.EncodeToString(sum[:8])}
	}
	st := CacheState{Present: true, ExpiresAt: doc.ExpiresAt}
	if doc.AccessToken != "" {
		sum := sha256.Sum256([]byte(doc.AccessToken))
		st.TokenHash = hex.EncodeToString(sum[:8])
	}
	return st
}

// LoginWaiter is one run waiting on a request. Both fields are text an agent wrote, so
// they are cut down before they are stored (see cleanWaiterText).
type LoginWaiter struct {
	Session string `json:"session,omitempty"`
	Command string `json:"command,omitempty"`
	At      string `json:"at"`
}

// LoginRequest is one pending request, one file per sso-session.
type LoginRequest struct {
	ID         string        `json:"id"`
	Profile    string        `json:"profile"`
	SSOSession string        `json:"ssoSession"`
	FirstAt    string        `json:"firstAt"`
	LastAt     string        `json:"lastAt"`
	Cache      CacheState    `json:"cache"`
	Waiters    []LoginWaiter `json:"waiters,omitempty"`
}

// cancelMarker is written by the Agent when a request is cancelled, before the request
// file goes. Waiters react only to the marker naming their own request.
type cancelMarker struct {
	RequestID string     `json:"requestId"`
	Profile   string     `json:"profile"`
	At        string     `json:"at"`
	Cache     CacheState `json:"cache"`
}

func loginDir() string { return filepath.Join(paths.AgentStateDir(), "aws-login") }

// loginFileKey turns an sso-session name into a file name that cannot escape the
// directory, whatever the name holds.
func loginFileKey(ssoSession string) string {
	sum := sha256.Sum256([]byte(ssoSession))
	return hex.EncodeToString(sum[:12])
}

func requestPath(ssoSession string) string {
	return filepath.Join(loginDir(), loginFileKey(ssoSession)+".request.json")
}

func markerPath(ssoSession string) string {
	return filepath.Join(loginDir(), loginFileKey(ssoSession)+".cancel.json")
}

// lockLogin takes the one lock every reader-writer of the directory shares.
func lockLogin() (func(), error) {
	if err := os.MkdirAll(loginDir(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(loginDir(), ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(b, v) == nil
}

func writeJSONFile(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readRequest(ssoSession string) (LoginRequest, bool) {
	var r LoginRequest
	ok := readJSON(requestPath(ssoSession), &r) && r.ID != "" && r.SSOSession == ssoSession
	return r, ok
}

// liveMarker returns the cancel marker of ssoSession while it still holds new runs back:
// younger than loginCancelHold, and not made moot by a cache that changed since.
func liveMarker(ssoSession string, now time.Time) (cancelMarker, bool) {
	var m cancelMarker
	if !readJSON(markerPath(ssoSession), &m) {
		return m, false
	}
	at, err := time.Parse(time.RFC3339Nano, m.At)
	if err != nil || now.Sub(at) >= loginCancelHold || ReadCacheState(ssoSession) != m.Cache {
		return m, false
	}
	return m, true
}

func newRequestID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var waiterTextRe = regexp.MustCompile(`[^A-Za-z0-9._@:/+-]`)

// cleanWaiterText keeps what a waiter field may show: a short run of plain characters.
// The Console prints it next to Settings' account and role, so it must not be able to
// look like either.
func cleanWaiterText(s string) string {
	s = waiterTextRe.ReplaceAllString(strings.TrimSpace(s), "")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// ErrLoginHeld means a cancel in the Console still holds new requests back.
var ErrLoginHeld = errors.New("the login request was cancelled in the Console")

// FileLoginRequest files (or joins) the request of ssoSession for profile, recording
// snap: the cache state this run saw before its failing check. A new request also
// writes the notification that carries its id to the Console.
func FileLoginRequest(profile, ssoSession string, snap CacheState, w LoginWaiter) (LoginRequest, bool, error) {
	unlock, err := lockLogin()
	if err != nil {
		return LoginRequest{}, false, err
	}
	defer unlock()
	now := time.Now().UTC()
	if _, held := liveMarker(ssoSession, now); held {
		return LoginRequest{}, false, ErrLoginHeld
	}
	w.Session, w.Command = cleanWaiterText(w.Session), cleanWaiterText(w.Command)
	w.At = now.Format(time.RFC3339Nano)
	r, exists := readRequest(ssoSession)
	created := !exists
	if created {
		r = LoginRequest{ID: newRequestID(), Profile: profile, SSOSession: ssoSession, FirstAt: w.At}
	}
	r.LastAt = w.At
	// Joining: the record must describe a cache this run found unusable.
	r.Cache = snap
	r.Waiters = append(r.Waiters, w)
	if len(r.Waiters) > 20 {
		r.Waiters = r.Waiters[len(r.Waiters)-20:]
	}
	if err := writeJSONFile(requestPath(ssoSession), r); err != nil {
		return LoginRequest{}, false, err
	}
	if created {
		ev := notice.New(NoticeKindAWSLogin, "", "", "")
		ev.TargetType = "workspace"
		if session.ValidName(w.Session) {
			if m, ok := session.ReadMeta(w.Session); ok {
				ev.TargetType, ev.SessionName, ev.SessionKind = "session", m.Name, m.Kind
			}
		}
		// The Console reads nothing but this id from the payload: the outbox is writable
		// by every agent, so anything else here would be text it could forge.
		ev.Payload["requestId"] = r.ID
		_ = notice.PutOnce("aws-login:"+r.ID, ev)
	}
	return r, created, nil
}

// loginRequestState is what a waiter reads every poll.
type loginRequestState int

const (
	requestPending loginRequestState = iota
	requestCancelled
	requestGone
)

// requestStateFor reports what became of the request id of ssoSession.
func requestStateFor(ssoSession, id string) loginRequestState {
	var m cancelMarker
	if readJSON(markerPath(ssoSession), &m) && m.RequestID == id {
		return requestCancelled
	}
	if r, ok := readRequest(ssoSession); ok && r.ID == id {
		return requestPending
	}
	return requestGone
}
