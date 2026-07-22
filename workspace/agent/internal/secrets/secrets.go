// Package secrets は Workspace の暗号化資格情報ストア（docs/23 残① Wave B で
// package main から抽出）。git トークン・Claude OAuth トークン・Bitbucket の
// リフレッシュ資格情報・opencode の API キーを単一ストアに保持する。
// ディスク上のフォーマット（secrets.enc / secrets.json）は抽出前と同一。
//
// The store is the single encrypted home for every provider credential the
// Workspace holds: git tokens, the Claude OAuth token, and Bitbucket's refresh
// creds. It replaces the former plaintext files (~/.git-credentials,
// claude-oauth-token, bitbucket.json). At rest it is AES-256-GCM sealed with a
// per-user subkey the Control Plane injects as AF_SECRET_KEY (derived from the
// deployment master key; CP never stores plaintext). With no key (dev) the same
// store is written as plaintext JSON — one code path, encryption is just the
// seal. git reads it on demand via the `workspace-agent cred` helper, so no
// plaintext credential file is ever written to the bind-mounted disk. (A3)
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

type GitEntry struct {
	User  string `json:"user"`
	Token string `json:"token"`
	Login string `json:"login,omitempty"` // cached real provider account/handle (resolved from the API)
	Email string `json:"email,omitempty"` // cached account email (resolved from the API)
}

// GitIdentity is a provider's explicit commit identity (user.name / user.email),
// decoupled from the credential entry so it works for either connect method (token or
// OAuth). Empty fields fall back to the resolved account (Login/Email).
type GitIdentity struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// BitbucketCreds are the Bitbucket OAuth refresh creds (access tokens expire in
// ~2h; the cred helper refreshes on demand — git_oauth.go in package main).
type BitbucketCreds struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Expiry       int64  `json:"expiry"` // unix seconds
	Key          string `json:"key"`
	Secret       string `json:"secret"`
	Account      string `json:"account,omitempty"` // cached real Bitbucket handle (resolved from the API)
	Email        string `json:"email,omitempty"`   // cached account email (resolved from the API)
}

// PagerDutyCreds is the user's PagerDuty API credential (docs/25 Phase 1). The
// key is injected into `uvx pagerduty-mcp` at spawn time by the `mcp-run`
// wrapper (workspace-agent mcp-run pagerduty) so no plaintext key ever reaches
// the MCP config — only the wrapper reference does. Host overrides the API base
// (EU accounts use https://api.eu.pagerduty.com).
type PagerDutyCreds struct {
	APIKey string `json:"apiKey"`
	Host   string `json:"host,omitempty"`
}

// GrafanaCreds is the user's Grafana connection (docs/25). URL is the instance
// base (self-hosted, Grafana Cloud, or an Amazon Managed Grafana workspace
// endpoint — AMG uses the same service-account token auth, just with a max
// 30-day token life). Token is a service-account token (Viewer recommended),
// injected into mcp-grafana at spawn by `mcp-run grafana`.
type GrafanaCreds struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// CloudWatchConn is the user's CloudWatch connection settings (docs/25). Unlike
// the other ops integrations it holds NO secret: auth is the AWS credential
// chain already in the container (the user's SSO login, same as ssm sessions).
// Profile selects the profile; Region optionally overrides its region.
//
// The SSO fields mirror session.SSMMeta: SSM profiles live in per-session
// isolated config files (~/.aws/af-sessions/*.config), NOT in ~/.aws/config, so
// a bare AWS_PROFILE is invisible to boto3. When StartURL is set, mcp-run
// regenerates a durable ops config (~/.aws/af-ops/cloudwatch.config) from these
// fields at every spawn (idempotent; self-heals after clean-home) and points
// AWS_CONFIG_FILE at it. When StartURL is empty the profile is assumed to exist
// in the member's own ~/.aws (manual setups).
type CloudWatchConn struct {
	Profile   string `json:"profile"`
	Region    string `json:"region,omitempty"`
	StartURL  string `json:"startUrl,omitempty"`  // SSO access-portal start URL
	SSORegion string `json:"ssoRegion,omitempty"` // SSO region
	AccountID string `json:"accountId,omitempty"` // SSO account id
	RoleName  string `json:"roleName,omitempty"`  // SSO permission-set role name
}

// DiscordCreds is the user's Discord chat-bridge connection (docs/37 P1). Token
// is the user's OWN bot token (private guild + bot — no central shared app,
// docs/37 契約3). Exactly one destination: ChannelID posts to a guild channel;
// UserID DMs the bound Discord user (the identity binding of docs/37 契約5 —
// P2's inbound routing will verify against this same ID). DMChannelID caches
// the DM channel resolved from UserID so sends don't re-resolve every time.
// Events selects the pushed notification groups (bridge.EventKeys); empty = all.
//
// Channel-mode extras (docs/37 P1.5): Threads groups notifications into one
// thread per session; MentionUserID is @mentioned in every notification so
// mobile push fires regardless of the user's channel/thread notification
// settings (Discord defaults to "only @mentions"). The wizard auto-fills it
// with the guild's owner_id — no Developer Mode needed.
type DiscordCreds struct {
	Token         string   `json:"token"`
	ChannelID     string   `json:"channelId,omitempty"`
	UserID        string   `json:"userId,omitempty"`
	DMChannelID   string   `json:"dmChannelId,omitempty"` // cache: resolved from UserID
	BotName       string   `json:"botName,omitempty"`     // cache: bot account name for the UI
	Events        []string `json:"events,omitempty"`
	Threads       bool     `json:"threads,omitempty"`       // thread-per-session (channel mode only)
	MentionUserID string   `json:"mentionUserId,omitempty"` // @mentioned in notifications (channel mode)
	Lang          string   `json:"lang,omitempty"`          // notification language: "en" | "" (=ja) — Console locale at connect time
	// Receive opts into the P2a inbound Gateway (docs/37): when on, a long-lived
	// WSS connection routes the bound user's thread replies back into the session.
	// Default off — it needs the MESSAGE_CONTENT privileged intent enabled on the
	// bot (a one-checkbox step in the Developer Portal for bots in <100 guilds),
	// and it bounds the daemon's memory to opted-in users only (docs/37「メモリ」).
	Receive bool `json:"receive,omitempty"`
}

type Data struct {
	Git         map[string]GitEntry    `json:"git"`                   // host -> https cred
	GitIdentity map[string]GitIdentity `json:"gitIdentity,omitempty"` // host -> explicit commit identity
	Claude      string                 `json:"claude"`                // CLAUDE_CODE_OAUTH_TOKEN
	Bitbucket   *BitbucketCreds        `json:"bitbucket"`             // OAuth refresh creds (bitbucket.org)
	Opencode    map[string]string      `json:"opencode"`              // provider env var name -> API key (injected for opencode sessions)
	PagerDuty   *PagerDutyCreds        `json:"pagerduty,omitempty"`   // ops MCP credential (docs/25)
	Grafana     *GrafanaCreds          `json:"grafana,omitempty"`     // ops MCP credential (docs/25)
	CloudWatch  *CloudWatchConn        `json:"cloudwatch,omitempty"`  // ops MCP settings (docs/25; no secret — AWS cred chain)
	Discord     *DiscordCreds          `json:"discord,omitempty"`     // chat-bridge connection (docs/37)
}

// agentSecretKey returns the 32-byte per-user key from AF_SECRET_KEY (hex), or
// nil when unset/invalid (dev: store is plaintext JSON).
func agentSecretKey() []byte {
	h := os.Getenv("AF_SECRET_KEY")
	if h == "" {
		return nil
	}
	b, err := hex.DecodeString(strings.TrimSpace(h))
	if err != nil || len(b) != 32 {
		log.Printf("WARNING: AF_SECRET_KEY is set but not 32-byte hex — storing secrets in PLAINTEXT")
		return nil
	}
	return b
}

// Path is the on-disk location of the store (encrypted or plaintext by key presence).
func Path() string {
	name := "secrets.json"
	if agentSecretKey() != nil {
		name = "secrets.enc"
	}
	return filepath.Join(paths.AgentConfigDir(), name)
}

func Load() (*Data, error) {
	s := &Data{Git: map[string]GitEntry{}}
	b, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if key := agentSecretKey(); key != nil {
		if b, err = aesOpen(key, b); err != nil {
			return s, fmt.Errorf("decrypt secrets: %w", err)
		}
	}
	if err := json.Unmarshal(b, s); err != nil {
		return s, err
	}
	if s.Git == nil {
		s.Git = map[string]GitEntry{}
	}
	return s, nil
}

func (s *Data) Save() error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if key := agentSecretKey(); key != nil {
		if b, err = aesSeal(key, b); err != nil {
			return err
		}
	}
	return os.WriteFile(p, b, 0o600)
}

func aesSeal(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil // nonce prepended
}

func aesOpen(key, ct []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(ct) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	return gcm.Open(nil, ct[:ns], ct[ns:], nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
