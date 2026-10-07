// Package secrets is the Workspace's encrypted credential store (secrets.enc /
// secrets.json on disk).
//
// The store is the single encrypted home for every provider credential the
// Workspace holds: git tokens, the Claude OAuth token, Bitbucket's refresh
// creds and the opencode API key. It replaces the former plaintext files (~/.git-credentials,
// claude-oauth-token, bitbucket.json). At rest it is AES-256-GCM sealed with a
// per-user subkey the Control Plane injects as AF_SECRET_KEY (derived from the
// deployment master key; CP never stores plaintext). With no key (dev) the same
// store is written as plaintext JSON — one code path, encryption is just the
// seal. git reads it on demand via the `workspace-agent cred` helper, so no
// plaintext credential file is ever written to the bind-mounted disk. (A3)
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

type GitEntry struct {
	User  string `json:"user"`
	Token string `json:"token"`
	Login string `json:"login,omitempty"` // cached real provider account/handle (resolved from the API)
	Email string `json:"email,omitempty"` // cached account email (resolved from the API)

	// Renewal state of an expiring GitHub App user token from the device flow. All empty
	// for a pasted PAT and for a non-expiring app token, which is how those keep their
	// old behaviour: no RefreshToken, nothing to renew. No client secret is stored — the
	// device flow's refresh grant takes only the ClientID.
	RefreshToken  string `json:"refresh_token,omitempty"`
	Expiry        int64  `json:"expiry,omitempty"`         // access token expiry, unix seconds; 0 = unknown
	RefreshExpiry int64  `json:"refresh_expiry,omitempty"` // refresh token expiry, unix seconds; 0 = unknown
	ClientID      string `json:"client_id,omitempty"`      // the OAuth app that minted the pair
	// ReconnectNeeded is set once GitHub has refused the refresh token for good (expired,
	// revoked, app uninstalled). Only a new connect clears it.
	ReconnectNeeded bool `json:"reconnect_needed,omitempty"`
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
//
// ★ Key/Secret are the TENANT's OAuth app and are no longer written here (docs/log/71
// §71.8): the refresh grant runs in the CP, which holds the secret, so a member's
// container never sees another tenant-wide credential. They remain in the struct only
// to read — and then clear — what stores written before that change still hold; see
// refreshBitbucket, which drops them the first time the CP bridge answers.
type BitbucketCreds struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Expiry       int64  `json:"expiry"`            // unix seconds
	Key          string `json:"key,omitempty"`     // legacy: pre-docs/log/71 stores only
	Secret       string `json:"secret,omitempty"`  // legacy: pre-docs/log/71 stores only
	Account      string `json:"account,omitempty"` // cached real Bitbucket handle (resolved from the API)
	Email        string `json:"email,omitempty"`   // cached account email (resolved from the API)
}

// CPBridge is how a SEPARATE process reaches the Control Plane's /internal face: the
// public base plus this membership's bridge token, both injected as container env at
// start and copied here at agent startup.
//
// ★ It is in the store rather than read from env at the point of use because the git
// credential helper is its own binary, spawned by git — which may be running under a
// tmux server that was started before (or without) those variables. seedInternalGit
// takes the same precaution for the same reason: runCredHelper reads the store and
// nothing else.
type CPBridge struct {
	BaseURL string `json:"baseUrl"`
	Token   string `json:"token"`
}

// PagerDutyCreds is the user's PagerDuty API credential (docs/log/25 Phase 1). The
// key is injected into `uvx pagerduty-mcp` at spawn time by the `mcp-run`
// wrapper (workspace-agent mcp-run pagerduty) so no plaintext key ever reaches
// the MCP config — only the wrapper reference does. Host overrides the API base
// (EU accounts use https://api.eu.pagerduty.com).
type PagerDutyCreds struct {
	APIKey string `json:"apiKey"`
	Host   string `json:"host,omitempty"`
}

// GrafanaCreds is the user's Grafana connection (docs/log/25). URL is the instance
// base (self-hosted, Grafana Cloud, or an Amazon Managed Grafana workspace
// endpoint — AMG uses the same service-account token auth, just with a max
// 30-day token life). Token is a service-account token (Viewer recommended),
// injected into mcp-grafana at spawn by `mcp-run grafana`.
// JiraCreds is the user's Jira (Atlassian Cloud) connection for the work item inbox
// (docs/log/80 P1). Site is the instance base URL (https://<tenant>.atlassian.net), Email
// the Atlassian account address and Token an API token — Jira's REST v3 authenticates
// with HTTP Basic over those two, so BOTH are credentials and neither leaves the
// container. A read-only account is recommended (see guide): af only ever reads.
type JiraCreds struct {
	// AuthKind is "oauth" (Atlassian 3LO, docs/log/80 §80.17) or "token" (email + API
	// token). "" reads as "token": stores written before OAuth existed have no field
	// and must keep working untouched.
	AuthKind string `json:"authKind,omitempty"`
	// Site is the base URL of the site in use — typed by the member on the token path,
	// picked from Sites on the OAuth path. Both paths need it for /browse/<KEY> links,
	// which are NOT api.atlassian.com URLs even when the API calls are.
	Site  string `json:"site"`
	Email string `json:"email"` // token auth only (and a credential — half of the Basic pair)
	Token string `json:"token"` // token auth only
	// Account is the resolved display name, cached at connect time so the card can
	// name the account instead of echoing the email back at the user. Not a secret.
	Account string `json:"account,omitempty"`

	// --- OAuth (3LO) only ---
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	// Expiry is the unix second the access token dies. ~1h out, which is why the CP
	// refresh bridge is load-bearing here rather than an optimisation.
	Expiry int64 `json:"expiry,omitempty"`
	// CloudID selects which site the API calls address
	// (api.atlassian.com/ex/jira/<CloudID>). One authorization can cover several.
	CloudID string     `json:"cloudId,omitempty"`
	Sites   []JiraSite `json:"sites,omitempty"`
}

// JiraSite is one Jira site the authorization covers (accessible-resources). No secret.
type JiraSite struct {
	CloudID string `json:"cloudId"`
	URL     string `json:"url"`
	Name    string `json:"name"`
}

type GrafanaCreds struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// LcppConn is the member's own llama.cpp connection (docs/log/107): a LAN llama-server (or
// router) URL and an optional API key. When set, it OVERRIDES the deployment's own "llm"
// engine role for this member's lcpp sessions — engines.go's harnessEngineToken/Window/
// Available read it before falling back to the Control Plane's catalogue — which is why it
// lives in the encrypted store next to every other provider credential rather than in
// ui-prefs.json (plaintext, and the wrong trust boundary for a bearer token). APIKey may be
// empty: an unauthenticated llama-server on a LAN is a real, common setup.
//
// URL is stored normalized WITHOUT a trailing "/v1" (normalizeLcppURL in connections.go) so
// every reader can append the mount it needs (.../v1 for chat, the bare base for /props).
type LcppConn struct {
	URL    string `json:"url"`
	APIKey string `json:"apiKey,omitempty"`
}

// AWSProfileRef points an ops integration at an AWS profile. It holds NO secret:
// auth is the AWS credential chain already in the container (the user's SSO login,
// same as ssm sessions). Profile selects the profile; Region optionally overrides
// its region.
//
// The SSO fields mirror session.SSMMeta: SSM profiles live in per-session isolated
// config files (~/.aws/af-sessions/*.config), NOT in ~/.aws/config, so a bare
// AWS_PROFILE is invisible to boto3/botocore. When StartURL is set, mcp-run
// regenerates a durable ops config (~/.aws/af-ops/<id>.config) from these fields at
// every spawn (idempotent; self-heals after clean-home) and points AWS_CONFIG_FILE
// at it. When StartURL is empty the profile is assumed to exist in the member's own
// ~/.aws (manual setups).
//
// Embedded (not copied) into the connections that need it so both speak the same
// wire shape — the JSON stays flat, so stores written before the split load as-is.
type AWSProfileRef struct {
	Profile   string `json:"profile"`
	Region    string `json:"region,omitempty"`
	StartURL  string `json:"startUrl,omitempty"`  // SSO access-portal start URL
	SSORegion string `json:"ssoRegion,omitempty"` // SSO region
	AccountID string `json:"accountId,omitempty"` // SSO account id
	RoleName  string `json:"roleName,omitempty"`  // SSO permission-set role name
}

// CloudWatchConn is the user's CloudWatch connection settings (docs/log/25).
type CloudWatchConn struct {
	AWSProfileRef
}

// AWSConn is the user's Agent Toolkit for AWS connection (docs/log/25 §AWS MCP): the
// AWS-operated MCP Server reached through the `mcp-proxy-for-aws` stdio proxy,
// which SigV4-signs every call with the profile below. Like CloudWatch it stores no
// secret.
//
// Endpoint is the region the MCP *service* runs in (us-east-1 / eu-central-1) and is
// what SigV4 signs against; AWSProfileRef.Region is where the member's own resources
// live and rides along as request metadata. They are frequently different, which is
// why both exist.
//
// Write opts INTO the mutating tools. Default off = the proxy runs with --read-only,
// which drops call_aws / run_script / get_presigned_url and leaves the documentation
// and inventory tools. The container is untrusted by design (reference/security.md
// §4.1-4.3), so "an agent can call 15,000+ AWS API actions" is a deliberate,
// per-member opt-in rather than a side effect of connecting.
type AWSConn struct {
	AWSProfileRef
	Endpoint string `json:"endpoint,omitempty"` // MCP service region; "" = awsMCPDefaultEndpoint
	Write    bool   `json:"write,omitempty"`    // opt in to the mutating tools
}

// DiscordCreds is the user's Discord chat-bridge connection (docs/log/37 P1). Token
// is the user's OWN bot token (private guild + bot — no central shared app,
// docs/log/37 contract 3). Exactly one destination: ChannelID posts to a guild channel;
// UserID DMs the bound Discord user (the identity binding of docs/log/37 contract 5 —
// P2's inbound routing will verify against this same ID). DMChannelID caches
// the DM channel resolved from UserID so sends don't re-resolve every time.
// Events selects the pushed notification groups (bridge.EventKeys); empty = all.
//
// Channel-mode extras (docs/log/37 P1.5): Threads groups notifications into one
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
	// Receive opts into the P2a inbound Gateway (docs/log/37): when on, a long-lived
	// WSS connection routes the bound user's thread replies back into the session.
	// Default off — it needs the MESSAGE_CONTENT privileged intent enabled on the
	// bot (a one-checkbox step in the Developer Portal for bots in <100 guilds),
	// and it bounds the daemon's memory to opted-in users only (docs/log/37 §memory).
	Receive bool `json:"receive,omitempty"`
	// FullText opts into the P2 full-text bridge (docs/log/37 §future direction): when on, the
	// final assistant turn body rides along the answer-ready push so the chat is a
	// self-sufficient remote UI (the deep link is useless on a local-only,
	// externally-unreachable deployment). Default off — the chat side is a mirror
	// by default; only the owner of both ends opts into posting their own output.
	// The body is secret-scrubbed and chunked to Discord's 2000-char limit.
	FullText bool `json:"fullText,omitempty"`
	// MirrorInputOff opts OUT of echoing Console-typed prompts into the session's
	// thread (docs/log/37 Fix ②). The mirror is ON by default in channel+thread mode so
	// the thread reflects BOTH directions; stored inverted so pre-existing connections
	// (absent field = false = on) keep mirroring without a re-save.
	MirrorInputOff bool `json:"mirrorInputOff,omitempty"`
	// NotifyOff mutes ALL outbound notifications to this service WITHOUT disconnecting —
	// a master switch toggled from Personal settings › Notifications (and the chat-integration
	// card). Stored
	// inverted so a pre-existing connection (absent = false) keeps notifying with no re-save.
	NotifyOff bool `json:"notifyOff,omitempty"`
}

// SlackCreds is the user's Slack chat-bridge connection (docs/log/37 Slack follow-up), the
// Socket-Mode twin of DiscordCreds. It needs TWO of the user's OWN tokens (no central
// shared app, docs/log/37 contract 3): BotToken (xoxb-) drives the Web API (post/react/update),
// AppToken (xapp-, connections:write) opens the Socket-Mode WSS for receiving. Exactly one
// destination: ChannelID posts to a channel; UserID DMs the bound Slack user. UserID is
// also the identity binding of docs/log/37 contract 5 (the receive path verifies replies/clicks
// against it) AND the @mention target in channel mode — Slack has no guild-owner concept,
// so one field serves both, unlike Discord's separate MentionUserID.
//
// Cached, non-secret fields (BotUserID/BotName/TeamName/DMChannelID) are resolved once at
// connect time so the send path and the self-echo filter don't re-fetch. Threads groups
// notifications into one thread per session (keyed by the root message ts — Slack threads
// share the parent channel and have no separate id, so the store's Thread holds the ts).
type SlackCreds struct {
	BotToken    string   `json:"botToken"`
	AppToken    string   `json:"appToken,omitempty"`    // xapp- (Socket Mode receive); only needed for Receive
	ChannelID   string   `json:"channelId,omitempty"`   // channel destination (C…)
	UserID      string   `json:"userId,omitempty"`      // bound user (U…): DM target + mention + identity binding
	DMChannelID string   `json:"dmChannelId,omitempty"` // cache: IM channel resolved from UserID
	BotUserID   string   `json:"botUserId,omitempty"`   // cache: to ignore the bot's own message echoes
	BotName     string   `json:"botName,omitempty"`     // cache: bot account name for the UI
	TeamName    string   `json:"teamName,omitempty"`    // cache: workspace name for the UI
	Events      []string `json:"events,omitempty"`
	Threads     bool     `json:"threads,omitempty"` // thread-per-session (channel mode only)
	Lang        string   `json:"lang,omitempty"`    // notification language: "en" | "" (=ja)
	// Receive opts into the Socket-Mode inbound WSS (docs/log/37 P2a): the bound user's thread
	// replies route back into the session and button clicks are honored. Default off; needs
	// the AppToken and bounds the daemon's memory to opted-in users only (docs/log/37 §memory).
	Receive bool `json:"receive,omitempty"`
	// FullText opts into the full-text bridge (docs/log/37): the final assistant turn body rides the
	// answer-ready push. Default off; secret-scrubbed and chunked to Slack's limit.
	FullText bool `json:"fullText,omitempty"`
	// MirrorInputOff opts OUT of echoing Console-typed prompts into the session thread
	// (docs/log/37 Fix ②); stored inverted so the default is on (mirror both directions).
	MirrorInputOff bool `json:"mirrorInputOff,omitempty"`
	// NotifyOff mutes ALL outbound notifications without disconnecting (see
	// DiscordCreds.NotifyOff). Stored inverted so a pre-existing connection keeps notifying.
	NotifyOff bool `json:"notifyOff,omitempty"`
}

// SVNCred is a stored basic-auth credential for a Subversion server (docs/log/41).
// SVN has no credential-helper analog to git's `workspace-agent cred`, so the
// REST checkout/update paths look these up by longest-matching URLPrefix and pass
// them to `svn` as --username / --password-from-stdin. URLPrefix is the repository
// root URL (or any prefix): the longest match wins, so a per-repo entry overrides a
// broader per-server one.
type SVNCred struct {
	URLPrefix string `json:"urlPrefix"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	// TrustCert accepts an otherwise-rejected server certificate (self-signed /
	// unknown CA / hostname mismatch) for this server, so checkout/update work
	// against a dev SVN server without a trusted cert. Not a secret — persisted
	// independently of the password Save opt-in (docs/log/41).
	TrustCert bool `json:"trustCert,omitempty"`
}

// MCPServer is one registered MCP server definition (docs/log/48 + ADR0031). It is the
// single shape for every origin: a user-registered server (stored here, in this
// encrypted store), a tenant-distributed one (cached from the CP), and the builtin
// ops integrations normalized into the same type. Name is what the target CLI sees
// as the server key, so it is restricted to the narrowest character set among the
// CLIs (codex writes it as a TOML bare key).
//
// Env / Headers VALUES are secret: they are masked on the wire (see mcpreg.Masked)
// and only ever written out at materialize time, into 0600 files under home.
type MCPServer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Label     string `json:"label,omitempty"`
	Origin    string `json:"origin"`    // "user" | "tenant" | "builtin"
	Transport string `json:"transport"` // "stdio" | "http"

	// stdio
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	// http
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	Enabled   bool       `json:"enabled"`
	Targets   MCPTargets `json:"targets"`
	Kinds     []string   `json:"kinds,omitempty"` // empty = every agent kind
	TimeoutMS int        `json:"timeoutMs,omitempty"`
	CreatedAt int64      `json:"createdAt,omitempty"`
	UpdatedAt int64      `json:"updatedAt,omitempty"`

	// UserSecret marks a TENANT-distributed definition that arrives with header NAMES
	// but no values: the tenant describes the endpoint, each member supplies their own
	// credential into this store (docs/log/48 §5.2 / P4). It exists because a token in a
	// distributed header is readable in plaintext by every member of the tenant, which
	// per-user container isolation cannot prevent. Never set on a user-scope row —
	// there is nobody else to supply the value.
	UserSecret bool `json:"userSecret,omitempty"`
}

// MCPTargets selects where a server is handed to. Both false means the definition is
// stored but attached nowhere — legal (a staging state), just inert.
type MCPTargets struct {
	Assistant bool `json:"assistant"` // selectable as an assistant integration
	Session   bool `json:"session"`   // materialized into the agent CLIs' native config
}

// ghSnap is the GitHub entry as a snapshot saw it: whether there was one, and what it held.
type ghSnap struct {
	Exists bool
	E      GitEntry
}

// sameConn reports whether two snapshots describe the same connection: the same entry
// (existence, user, tokens, expiries, client id, flag). The cached Login/Email are the
// account lookup's to refine and do not make a different connection.
func (a ghSnap) sameConn(b ghSnap) bool {
	a.E.Login, a.E.Email, b.E.Login, b.E.Email = "", "", "", ""
	return a == b
}

type Data struct {
	// ghBase is the GitHub entry as this snapshot was loaded (or last saved); ghBaseSet is
	// false for a Data that did not come from the store. Save uses it to tell "the caller
	// left the GitHub connection alone" from "the caller changed it".
	ghBase      ghSnap
	ghBaseSet   bool
	Git         map[string]GitEntry    `json:"git"`                   // host -> https cred
	GitIdentity map[string]GitIdentity `json:"gitIdentity,omitempty"` // host -> explicit commit identity
	Claude      string                 `json:"claude"`                // CLAUDE_CODE_OAUTH_TOKEN
	Bitbucket   *BitbucketCreds        `json:"bitbucket"`             // OAuth refresh creds (bitbucket.org)
	// GitOAuthBridge points at the CP endpoint that runs the git OAuth refresh grant on
	// this workspace's behalf (docs/log/71 §71.8). Not a provider credential — it is how the
	// cred helper reaches the CP without holding the tenant's client secret.
	GitOAuthBridge *CPBridge         `json:"gitOAuthBridge,omitempty"`
	Opencode       map[string]string `json:"opencode"`             // provider env var name -> API key (injected for opencode sessions)
	PagerDuty      *PagerDutyCreds   `json:"pagerduty,omitempty"`  // ops MCP credential (docs/log/25)
	Grafana        *GrafanaCreds     `json:"grafana,omitempty"`    // ops MCP credential (docs/log/25)
	Jira           *JiraCreds        `json:"jira,omitempty"`       // work item inbox source (docs/log/80 P1)
	Lcpp           *LcppConn         `json:"lcpp,omitempty"`       // member's own llama.cpp connection (docs/log/107)
	CloudWatch     *CloudWatchConn   `json:"cloudwatch,omitempty"` // ops MCP settings (docs/log/25; no secret — AWS cred chain)
	AWS            *AWSConn          `json:"aws,omitempty"`        // Agent Toolkit for AWS MCP settings (docs/log/25; no secret — AWS cred chain)
	Discord        *DiscordCreds     `json:"discord,omitempty"`    // chat-bridge connection (docs/log/37)
	Slack          *SlackCreds       `json:"slack,omitempty"`      // chat-bridge connection (docs/log/37 Slack follow-up)
	SVN            []SVNCred         `json:"svn,omitempty"`        // SVN basic-auth creds by URL prefix (docs/log/41)
	MCP            []MCPServer       `json:"mcp,omitempty"`        // user-registered MCP servers (docs/log/48)
	// MCPSecrets holds the member's OWN header values for tenant-distributed servers
	// marked user_secret (docs/log/48 §5.2): server id -> header name -> value. Keyed by the
	// tenant definition's id, so it survives the tenant editing the label/URL and is
	// dropped naturally when the definition stops being distributed.
	MCPSecrets map[string]map[string]string `json:"mcpSecrets,omitempty"`
	// InternalGitStaleEnv is the sha256 (hex) of the AF_INTERNAL_GIT_TOKEN this
	// container was started with, once the CP pushed a rotated token over it (issue
	// #1199). An Agent restart in the same container skips seeding that env token, which
	// would otherwise put the dead credential back.
	InternalGitStaleEnv string `json:"internalGitStaleEnv,omitempty"`
	// InternalGitEpoch is the epoch of the internal git token the store holds: the one
	// AF_INTERNAL_GIT_EPOCH named at start, or the last CP push. A push for an older
	// epoch, delivered late by another CP replica, is not written over it.
	InternalGitEpoch int64 `json:"internalGitEpoch,omitempty"`
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

// storeMu serializes this process's access to the store file; the flock below
// extends that across processes (the git cred helper is a separate binary).
var storeMu = make(ctxMutex, 1)

// ctxMutex is a mutex whose wait can end with a context, so the bounded entry points
// (LoadContext, UpdateContext) honour their deadline here as well as at the file lock.
type ctxMutex chan struct{}

func (m ctxMutex) Lock()   { m <- struct{}{} }
func (m ctxMutex) Unlock() { <-m }
func (m ctxMutex) LockContext(ctx context.Context) error {
	select {
	case m <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ErrLockTimeout
	}
}

// withFileLock runs fn while holding an exclusive flock on <store>.lock. The
// lockfile is separate from the store itself so the atomic rename in Save never
// replaces the locked inode.
func withFileLock(fn func() error) error {
	return withFileLockContext(context.Background(), fn)
}

func withFileLockContext(ctx context.Context, fn func() error) error {
	return flockedContext(ctx, Path()+".lock", fn)
}

// flockedContext runs fn under an exclusive flock on lockPath, giving up with
// ErrLockTimeout when ctx ends first.
func flockedContext(ctx context.Context, lockPath string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EINTR {
			return err
		}
		select {
		case <-ctx.Done():
			return ErrLockTimeout
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}

// WithLock runs fn while holding an exclusive flock on <store>.<name>.lock, across
// processes (the agent and the git credential helper are separate binaries). It is a
// different lock from the store's own, so fn may call Load / Update — but must never be
// called from inside an Update, or two such callers can deadlock in opposite orders.
func WithLock(name string, fn func() error) error {
	return WithLockContext(context.Background(), name, fn)
}

// ErrLockTimeout is WithLockContext giving up its wait.
var ErrLockTimeout = errors.New("timed out waiting for the credential store lock")

// WithLockContext is WithLock that stops waiting for the lock when ctx ends. A holder that
// is stopped or stuck must not hold every other process (the git credential helper runs
// under git's own patience) behind it.
func WithLockContext(ctx context.Context, name string, fn func() error) error {
	return flockedContext(ctx, Path()+"."+name+".lock", fn)
}

// Update atomically applies fn to the store under the process mutex + file lock:
// load → fn → save as ONE critical section, so concurrent writers (HTTP handlers,
// the cred helper process) cannot lose each other's changes. Prefer this over a
// bare Load→modify→Save for any read-modify-write.
func Update(fn func(*Data) error) error {
	return UpdateContext(context.Background(), fn)
}

// UpdateContext is Update whose wait for the store (this process's mutex and the file
// lock) ends with ctx. Once the lock is held the update itself runs to the end.
func UpdateContext(ctx context.Context, fn func(*Data) error) error {
	if err := storeMu.LockContext(ctx); err != nil {
		return err
	}
	defer storeMu.Unlock()
	return withFileLockContext(ctx, func() error {
		s, err := load()
		if err != nil {
			return err
		}
		if err := fn(s); err != nil {
			return err
		}
		return s.save()
	})
}

func Load() (*Data, error) { return LoadContext(context.Background()) }

// LoadContext is Load whose wait for the store ends with ctx (ErrLockTimeout).
func LoadContext(ctx context.Context) (*Data, error) {
	var s *Data
	if err := storeMu.LockContext(ctx); err != nil {
		return &Data{Git: map[string]GitEntry{}}, err
	}
	defer storeMu.Unlock()
	err := withFileLockContext(ctx, func() (lerr error) {
		s, lerr = load()
		return lerr
	})
	if s == nil {
		s = &Data{Git: map[string]GitEntry{}}
	}
	return s, err
}

func load() (*Data, error) {
	s := &Data{Git: map[string]GitEntry{}}
	b, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			// A store not yet created is a successful, empty snapshot: Save must still tell
			// a GitHub connection added after it from one the caller made.
			s.ghBase, s.ghBaseSet = ghSnap{}, true
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
	e, ok := s.Git["github.com"]
	s.ghBase, s.ghBaseSet = ghSnap{ok, e}, true
	return s, nil
}

// AdoptGitHub replaces the snapshot's GitHub entry with e, which was read from the store
// (a renewal's result), and moves the baseline along with it. Assigning s.Git directly
// would leave the baseline behind, and a later Save would take the newer entry for a
// change the caller meant and write the old pair over it.
func (s *Data) AdoptGitHub(e GitEntry) {
	s.Git["github.com"] = e
	s.ghBase, s.ghBaseSet = ghSnap{true, e}, true
}

// Save writes the whole snapshot, so a snapshot loaded before the GitHub connection
// changed would write the old one back: a spent token pair after a renewal (the next
// renewal would meet bad_refresh_token), a connection that was replaced (its client id
// with the new tokens), one that was removed, or one added meanwhile. Inside the lock Save
// compares the stored GitHub entry with the one this snapshot was loaded with. When the
// store has moved on and the caller left the connection alone, the stored state wins as a
// whole — present, absent or replaced — and the caller's cached Login/Email ride along only
// while the stored entry still carries the account they were read for. A caller that did
// change the connection (a reconnect, a disconnect) wins. Code that rewrites other fields
// should still prefer Update.
func (s *Data) Save() error {
	storeMu.Lock()
	defer storeMu.Unlock()
	return withFileLock(func() error {
		if cur, err := load(); err == nil && s.ghBaseSet {
			ep, ok := s.Git["github.com"]
			ce, cok := cur.Git["github.com"]
			if (ghSnap{ok, ep}).sameConn(s.ghBase) && !(ghSnap{cok, ce}).sameConn(s.ghBase) {
				if !cok {
					delete(s.Git, "github.com")
				} else {
					if ok && s.ghBase.Exists && s.ghBase.E.Login != "" &&
						ce.Login == s.ghBase.E.Login && ce.Email == s.ghBase.E.Email {
						ce.Login, ce.Email = ep.Login, ep.Email
					}
					s.Git["github.com"] = ce
				}
			}
		}
		if err := s.save(); err != nil {
			return err
		}
		e, ok := s.Git["github.com"]
		s.ghBase, s.ghBaseSet = ghSnap{ok, e}, true
		return nil
	})
}

// save writes the store atomically (tmp + rename): every git token, OAuth,
// Slack/Discord and MCP secret lives in this ONE file, so a crash mid-write must
// never leave a torn store behind.
func (s *Data) save() error {
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
	tmp, err := os.CreateTemp(filepath.Dir(p), ".secrets-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(b)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, p); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
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
