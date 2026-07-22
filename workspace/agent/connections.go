package main

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Connections hold the per-user provider credentials the Workspace consumes:
// git provider tokens (HTTPS) and the Claude OAuth token. They live in the
// user's home — the container's isolation boundary — inside the encrypted store
// (internal/secrets). The Control Plane delegates here and never holds secrets itself.
// See plan / docs/06 §6.7-6.8, docs/07 §7.3.

// gitHosts maps a supported provider host to its default git username. GitHub
// accepts any non-empty username with a token, so we use the conventional
// "x-access-token"; Bitbucket requires the user's Atlassian email, so there is
// no default and the caller must supply one.
var gitHosts = map[string]string{
	"github.com":    "x-access-token",
	"bitbucket.org": "",
}

// handleConnectionsGet reports connection status per provider, never secrets.
func handleConnectionsGet(w http.ResponseWriter, r *http.Request) {
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	github := gitConnStatus(s, "github.com")
	ghConnected, _ := github["connected"].(bool)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"claude":    claude.Status(),
		"github":    github,
		"bitbucket": bitbucketStatus(s),
		"internal":  internalGitStatus(s),
		"opencode":  opencode.Status(s),
		"codex":     codex.Status(),
		"agy":       agy.Status(),
		// copilot は GitHub 連携相乗り（docs/36 契約）: 専用フロー無し。
		"copilot":    copilot.Status(ghConnected),
		"pagerduty":  pagerdutyStatus(s),
		"grafana":    grafanaStatus(s),
		"cloudwatch": cloudwatchStatus(s),
		"discord":    discordStatus(s),
	})
}

// discordStatus reports the chat-bridge Discord connection (docs/37 P1) — never
// the token. Echoes the destination mode (channel/dm), the cached bot name, and
// the enabled event groups so the card can render the current selection.
func discordStatus(s *secrets.Data) map[string]any {
	d := s.Discord
	if d == nil || d.Token == "" {
		return map[string]any{"connected": false}
	}
	m := map[string]any{"connected": true}
	if d.BotName != "" {
		m["botName"] = d.BotName
	}
	if d.ChannelID != "" {
		m["mode"] = "channel"
	} else {
		m["mode"] = "dm"
	}
	events := d.Events
	if len(events) == 0 {
		events = bridge.EventKeys
	}
	m["events"] = events
	return m
}

// handleDiscordInspect (POST /connections/discord/inspect {token}) is step 1 of
// the card's setup wizard: validate the pasted bot token and hand back the bot
// name plus a ready-made invite URL (application id resolved from the token),
// so the user never touches the OAuth2 URL Generator. Nothing is stored yet.
func handleDiscordInspect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnDiscordTokenRequired, "enter a bot token")
		return
	}
	botName, err := bridge.DiscordBotName(token)
	if errors.Is(err, bridge.ErrUnauthorized) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnDiscordTokenInvalid, "Discord rejected the bot token")
		return
	}
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "discord_unreachable", err.Error())
		return
	}
	app, err := bridge.DiscordAppInfo(token)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "discord_unreachable", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"botName": botName, "applicationId": app.ID, "inviteUrl": bridge.DiscordInviteURL(app.ID),
	})
}

// handleDiscordGuilds (POST /connections/discord/guilds {token}) is step 2: the
// card polls it after showing the invite link and renders a channel picker the
// moment the bot lands in a guild. Text channels only; no privileged intents.
func handleDiscordGuilds(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnDiscordTokenRequired, "enter a bot token")
		return
	}
	gs, err := bridge.DiscordGuilds(token)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "discord_unreachable", err.Error())
		return
	}
	out := []map[string]any{}
	for _, g := range gs {
		chs, err := bridge.DiscordGuildChannels(token, g.ID)
		if err != nil {
			continue // e.g. missing view permission in this guild — skip, keep the rest
		}
		var cl []map[string]string
		for _, c := range chs {
			cl = append(cl, map[string]string{"id": c.ID, "name": c.Name})
		}
		out = append(out, map[string]any{"id": g.ID, "name": g.Name, "channels": cl})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"guilds": out})
}

type discordConnReq struct {
	Token     string   `json:"token"`
	ChannelID string   `json:"channelId"`
	UserID    string   `json:"userId"`
	Events    []string `json:"events"`
}

// discordSnowflakeRe matches a Discord snowflake id — catches names/mentions
// pasted where an id belongs (both destinations are numeric ids).
var discordSnowflakeRe = regexp.MustCompile(`^[0-9]{5,25}$`)

// handlePutDiscordConn stores the user's Discord bot token + destination in the
// encrypted store (docs/37 P1; PagerDuty カードと同じ三点セット). Exactly one
// destination: a guild channel id, or the user's own Discord user id for DMs
// (the identity binding of docs/37 契約5). The token is validated against the
// Discord API when reachable — a network failure saves anyway (outbound may be
// restricted; sends will surface in the daemon log), but a 401/403 rejects.
func handlePutDiscordConn(w http.ResponseWriter, r *http.Request) {
	var req discordConnReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnDiscordTokenRequired, "enter a bot token")
		return
	}
	channelID := strings.TrimSpace(req.ChannelID)
	userID := strings.TrimSpace(req.UserID)
	if (channelID == "") == (userID == "") {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnDiscordDestRequired, "set exactly one of channel id / user id")
		return
	}
	if id := firstNonEmpty(channelID, userID); !discordSnowflakeRe.MatchString(id) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnDiscordDestInvalid, "destination must be a numeric Discord id")
		return
	}
	var events []string
	for _, ev := range req.Events {
		if bridge.EventEnabled(bridge.EventKeys, ev) { // known key?
			events = append(events, ev)
		}
	}
	if len(events) == len(bridge.EventKeys) {
		events = nil // all on — store the compact "everything" default
	}
	botName, err := bridge.DiscordBotName(token)
	if errors.Is(err, bridge.ErrUnauthorized) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnDiscordTokenInvalid, "Discord rejected the bot token")
		return
	}
	creds := &secrets.DiscordCreds{Token: token, ChannelID: channelID, UserID: userID,
		BotName: botName, Events: events}
	// Resolve the DM channel eagerly so the first notification doesn't pay the
	// round-trip; a failure here is tolerated (the sender resolves lazily).
	if userID != "" {
		if ch, err := bridge.DiscordResolveDM(token, userID); err == nil {
			creds.DMChannelID = ch
		}
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.Discord = creds
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	res := discordStatus(s)
	// Fire one synchronous test notification so "did it arrive?" is answered on
	// the spot. The connection is saved either way — a failed test (e.g. missing
	// channel permission) is surfaced to the card, not treated as a bad config.
	if ps := bridge.Providers(s, nil); len(ps) > 0 {
		if err := ps[0].Send(bridge.Message{Kind: "bridge-test"}); err != nil {
			res["testError"] = err.Error()
		} else {
			res["test"] = "sent"
		}
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func handleDeleteDiscordConn(w http.ResponseWriter, r *http.Request) {
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.Discord = nil
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "discord"})
}

// pagerdutyStatus reports whether a PagerDuty API key is stored (never the key
// itself), plus the non-secret host override so the UI can show EU vs global.
func pagerdutyStatus(s *secrets.Data) map[string]any {
	if s.PagerDuty == nil || s.PagerDuty.APIKey == "" {
		return map[string]any{"connected": false}
	}
	m := map[string]any{"connected": true}
	if s.PagerDuty.Host != "" {
		m["host"] = s.PagerDuty.Host
	}
	return m
}

type pagerdutyConnReq struct {
	APIKey string `json:"apiKey"`
	Host   string `json:"host"`
}

// handlePutPagerDutyConn stores the user's PagerDuty API key in the encrypted
// store (docs/25 Phase 1). The key is consumed only by `mcp-run pagerduty`,
// which injects it as env into `uvx pagerduty-mcp` at spawn — it never lands in
// any MCP config file. A read-only PagerDuty key is recommended (see guide).
func handlePutPagerDutyConn(w http.ResponseWriter, r *http.Request) {
	var req pagerdutyConnReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	key := strings.TrimSpace(req.APIKey)
	if key == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnAPIKeyRequired, "enter an API key")
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.PagerDuty = &secrets.PagerDutyCreds{APIKey: key, Host: strings.TrimSpace(req.Host)}
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pagerdutyStatus(s))
}

func handleDeletePagerDutyConn(w http.ResponseWriter, r *http.Request) {
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.PagerDuty = nil
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "pagerduty"})
}

// grafanaStatus reports whether a Grafana connection is stored, plus the
// non-secret instance URL so the UI can show which Grafana it points at
// (self-hosted / Cloud / AMG workspace endpoint). Never the token.
func grafanaStatus(s *secrets.Data) map[string]any {
	if s.Grafana == nil || s.Grafana.URL == "" || s.Grafana.Token == "" {
		return map[string]any{"connected": false}
	}
	return map[string]any{"connected": true, "url": s.Grafana.URL}
}

type grafanaConnReq struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// handlePutGrafanaConn stores the user's Grafana URL + service-account token in
// the encrypted store (docs/25). The token is consumed only by `mcp-run grafana`,
// which injects it as env into mcp-grafana at spawn (read-only flags enforced
// there). A Viewer-permission service account is recommended; for Amazon Managed
// Grafana the token expires after at most 30 days and must be re-pasted.
func handlePutGrafanaConn(w http.ResponseWriter, r *http.Request) {
	var req grafanaConnReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	url := strings.TrimRight(strings.TrimSpace(req.URL), "/")
	token := strings.TrimSpace(req.Token)
	if url == "" || token == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnGrafanaFields, "enter the Grafana URL and service account token")
		return
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnURLScheme, "URL must start with http(s)://")
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.Grafana = &secrets.GrafanaCreds{URL: url, Token: token}
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, grafanaStatus(s))
}

func handleDeleteGrafanaConn(w http.ResponseWriter, r *http.Request) {
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.Grafana = nil
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "grafana"})
}

// cloudwatchStatus reports the stored CloudWatch settings. Nothing here is
// secret (profile/region select the AWS credential chain, they don't hold it),
// so the status can echo both for the UI. sso=true marks an SSM-linked profile
// (the ops aws config is generated from the stored SSO meta).
func cloudwatchStatus(s *secrets.Data) map[string]any {
	if s.CloudWatch == nil || s.CloudWatch.Profile == "" {
		return map[string]any{"connected": false}
	}
	m := map[string]any{"connected": true, "profile": s.CloudWatch.Profile}
	if s.CloudWatch.Region != "" {
		m["region"] = s.CloudWatch.Region
	}
	if s.CloudWatch.StartURL != "" {
		m["sso"] = true
	}
	return m
}

type cloudwatchConnReq struct {
	Profile   string `json:"profile"`
	Region    string `json:"region"`
	StartURL  string `json:"startUrl"`
	SSORegion string `json:"ssoRegion"`
	AccountID string `json:"accountId"`
	RoleName  string `json:"roleName"`
}

// cloudwatchProfileRe strips characters unsafe for an ~/.aws/config profile
// header — the same sanitization as the CP's ssmProfileName (control-plane/
// ssm.go), so an SSM profile label yields the same profile name here as in an
// SSM session's config.
var cloudwatchProfileRe = regexp.MustCompile(`[^A-Za-z0-9._@-]+`)

// handlePutCloudWatchConn stores the AWS profile the CloudWatch MCP should use
// (docs/25). No secret is stored: auth is the AWS credential chain (the user's
// `aws sso login`, same as ssm sessions). Two shapes:
//   - SSO meta present (startUrl 等 — Console の SSM プロファイルピッカー経由):
//     a durable ops aws config is generated from the meta (SSM profiles live in
//     per-session isolated configs, invisible to a bare AWS_PROFILE), here at
//     connect time and again at every mcp-run spawn.
//   - Profile name only: assumed to exist in the member's own ~/.aws.
func handlePutCloudWatchConn(w http.ResponseWriter, r *http.Request) {
	var req cloudwatchConnReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	profile := cloudwatchProfileRe.ReplaceAllString(strings.TrimSpace(req.Profile), "-")
	if profile == "" || profile == "-" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnAWSProfileRequired, "specify an AWS profile")
		return
	}
	startURL := strings.TrimSpace(req.StartURL)
	if startURL != "" && strings.TrimSpace(req.SSORegion) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnSSORegionMissing, "no SSO region (check the SSM profile configuration)")
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.CloudWatch = &secrets.CloudWatchConn{
		Profile:   profile,
		Region:    strings.TrimSpace(req.Region),
		StartURL:  startURL,
		SSORegion: strings.TrimSpace(req.SSORegion),
		AccountID: strings.TrimSpace(req.AccountID),
		RoleName:  strings.TrimSpace(req.RoleName),
	}
	// Materialize the ops config now (mcp-run also regenerates it per spawn) so
	// the user can `aws sso login` against it before the first chat turn.
	if startURL != "" {
		if err := writeCloudWatchOpsConfig(s.CloudWatch); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "config_failed", err.Error())
			return
		}
	}
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, cloudwatchStatus(s))
}

func handleDeleteCloudWatchConn(w http.ResponseWriter, r *http.Request) {
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.CloudWatch = nil
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "cloudwatch"})
}

// internalGitStatus reports the tenant's self-hosted git provider (docs/reference/
// internal-git-provider). It is CP-managed (the token is injected, not user-set),
// so it reports connected whenever the CP seeded a credential for the internal
// host. Absent AF_INTERNAL_GIT_HOST (internal git disabled) it is not connected.
func internalGitStatus(s *secrets.Data) map[string]any {
	host := internalGitHost()
	if host == "" {
		return map[string]any{"connected": false}
	}
	e, ok := s.Git[host]
	if !ok {
		return map[string]any{"connected": false, "host": host}
	}
	return map[string]any{"connected": true, "host": host, "username": firstNonEmpty(e.User, "x-access-token")}
}

// bitbucketStatus reports connected for either path: a pasted token (Git entry)
// or stored OAuth refresh creds (used via the cred helper). It surfaces the real
// Bitbucket account, resolved once from the API and cached in the store (so the
// polled endpoint doesn't re-fetch); on resolve failure it falls back to the
// stored email (token paste) or a placeholder (OAuth).
func bitbucketStatus(s *secrets.Data) map[string]any {
	if e, ok := s.Git["bitbucket.org"]; ok {
		m := map[string]any{"connected": true}
		if (e.Login == "" || e.Email == "") && e.Token != "" {
			if auth, err := bitbucketAuthHeader(s); err == nil {
				if h, email, err := bitbucketAccount(auth); err == nil && h != "" {
					e.Login, e.Email = h, email
					s.Git["bitbucket.org"] = e
					_ = s.Save()
				}
			}
		}
		if name := firstNonEmpty(e.Login, e.User); name != "" {
			m["username"] = name
		}
		if e.Email != "" {
			m["email"] = e.Email
		}
		id := s.GitIdentity["bitbucket.org"]
		m["commitName"] = firstNonEmpty(id.Name, e.Login)
		m["commitEmail"] = firstNonEmpty(id.Email, e.Email)
		return m
	}
	if s.Bitbucket != nil {
		m := map[string]any{"connected": true}
		if s.Bitbucket.Account == "" || s.Bitbucket.Email == "" {
			if auth, err := bitbucketAuthHeader(s); err == nil {
				if h, email, err := bitbucketAccount(auth); err == nil && h != "" {
					s.Bitbucket.Account, s.Bitbucket.Email = h, email
					_ = s.Save()
				}
			}
		}
		m["username"] = firstNonEmpty(s.Bitbucket.Account, "x-token-auth (oauth)")
		if s.Bitbucket.Email != "" {
			m["email"] = s.Bitbucket.Email
		}
		id := s.GitIdentity["bitbucket.org"]
		m["commitName"] = firstNonEmpty(id.Name, s.Bitbucket.Account)
		m["commitEmail"] = firstNonEmpty(id.Email, s.Bitbucket.Email)
		return m
	}
	return map[string]any{"connected": false}
}

// gitConnStatus reports a git provider's connection + the real account (handle +
// email), resolved once from the provider API and cached in the store
// (write-through), so the polled endpoint doesn't re-fetch. Falls back to the
// git-username placeholder; email is omitted when unavailable.
func gitConnStatus(s *secrets.Data, host string) map[string]any {
	e, ok := s.Git[host]
	m := map[string]any{"connected": ok}
	if !ok {
		return m
	}
	if host == "github.com" && (e.Login == "" || e.Email == "") && e.Token != "" {
		if login, email, err := githubAccount(e.Token); err == nil && login != "" {
			e.Login, e.Email = login, email
			s.Git[host] = e
			_ = s.Save()
		}
	}
	if name := firstNonEmpty(e.Login, e.User); name != "" {
		m["username"] = name
	}
	if e.Email != "" {
		m["email"] = e.Email
	}
	id := s.GitIdentity[host]
	m["commitName"] = firstNonEmpty(id.Name, e.Login)
	m["commitEmail"] = firstNonEmpty(id.Email, e.Email)
	return m
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

type gitConnReq struct {
	Username string `json:"username"`
	Token    string `json:"token"`
	Name     string `json:"name"`  // optional git author name (for commits)
	Email    string `json:"email"` // optional git author email
}

// handlePutGitConn stores an HTTPS credential for a provider so git's `store`
// helper authenticates clone/fetch/push transparently. The token→git binding
// mirrors CodeLeaf (GitHub user "x-access-token"; Bitbucket user = email).
func handlePutGitConn(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	defUser, ok := gitHosts[host]
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_host", "unsupported host: "+host)
		return
	}
	var req gitConnReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_token", "token is required")
		return
	}
	user := strings.TrimSpace(req.Username)
	if user == "" {
		user = defUser
	}
	if user == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_username", "username (Atlassian email) is required for "+host)
		return
	}

	if err := upsertGitCredential(host, user, token); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	// Commit identity is set separately (per provider) via /identity — not clobbered
	// into the global config at connect time.
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": true, "host": host, "username": user})
}

func handleDeleteGitConn(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if _, ok := gitHosts[host]; !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_host", "unsupported host: "+host)
		return
	}
	if err := removeGitCredential(host); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	if host == "bitbucket.org" {
		removeBitbucketOAuth() // also clear OAuth tokens + the refresh helper
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": host})
}

// upsertGitCredential stores an HTTPS credential for host in the encrypted store
// and ensures the cred helper is the active git credential source.
func upsertGitCredential(host, user, token string) error {
	s, err := secrets.Load()
	if err != nil {
		return err
	}
	s.Git[host] = secrets.GitEntry{User: user, Token: token}
	if err := s.Save(); err != nil {
		return err
	}
	return ensureCredHelper()
}

func removeGitCredential(host string) error {
	s, err := secrets.Load()
	if err != nil {
		return err
	}
	delete(s.Git, host)
	return s.Save()
}

func gitConfigGlobal(key, val string) error {
	if out, err := gitx.Combined("", "config", "--global", key, val); err != nil {
		return fmt.Errorf("git config %s: %v: %s", key, err, out)
	}
	return nil
}
