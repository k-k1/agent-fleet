package awsx

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudlogin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Console login requests (ADR 0102). af-aws-exec files one when an SSO login is needed
// and nobody is at a terminal; the Console shows a toast, and the device code starts
// only when the member presses "Log in" there (login_agent.go). The request and attempt
// lifecycle is cloudlogin's; this file says what a login is for an sso-session.

// NoticeKindAWSLogin is the notification kind that carries a request id to the Console.
const NoticeKindAWSLogin = "aws-login-required"

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

// LoginWaiter is one run waiting on a request.
type LoginWaiter = cloudlogin.Waiter

// LoginRequest is one pending request, one file per sso-session.
type LoginRequest = cloudlogin.Request[CacheState]

// cacheBackend tells cloudlogin what a login is on AWS: a request is resolved once the
// token cache changed from the state it recorded and holds an unexpired token, however
// the login happened.
type cacheBackend struct{}

func (cacheBackend) State(ssoSession string) CacheState { return ReadCacheState(ssoSession) }

func (cacheBackend) Landed(cur, recorded CacheState, now time.Time) bool {
	return cur != recorded && cur.Unexpired(now)
}

// logins holds the Console login requests and attempts of the AWS profiles, keyed by
// sso-session.
var logins = &cloudlogin.Store[CacheState]{Dir: "aws-login", NoticeKind: NoticeKindAWSLogin, NoticeKey: "aws-login",
	LogPrefix: "aws-login", Backend: cacheBackend{}}

func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(b, v) == nil
}
