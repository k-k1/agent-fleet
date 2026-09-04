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
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
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
		"cursor":    cursor.Status(),
		"kiro":      kiro.Status(),
		"agy":       agy.Status(),
		// copilot rides on the GitHub connection (docs/log/36 contract): no flow of its own.
		"copilot":    copilot.Status(ghConnected),
		"jira":       jiraStatus(s), // where work items are fetched from (docs/log/80 P1)
		"pagerduty":  pagerdutyStatus(s),
		"grafana":    grafanaStatus(s),
		"cloudwatch": cloudwatchStatus(s),
		"aws":        awsMCPStatus(s),
		"discord":    discordStatus(s),
		"slack":      slackStatus(s),
		"svn":        svnConnStatus(s), // saved SVN servers (urlPrefix + username; docs/log/41)
	})
}

// discordStatus reports the chat-bridge Discord connection (docs/log/37 P1) — never
// the token. Echoes the destination mode (channel/dm), the cached bot name, and
// the enabled event groups so the card can render the current selection.
func discordStatus(s *secrets.Data) map[string]any {
	d := s.Discord
	if d == nil || d.Token == "" {
		return map[string]any{"connected": false}
	}
	m := map[string]any{"connected": true}
	m["notify"] = !d.NotifyOff // master mute state (default on) for the notification toggle
	if d.BotName != "" {
		m["botName"] = d.BotName
	}
	if d.ChannelID != "" {
		m["mode"] = "channel"
		m["channelId"] = d.ChannelID // for the card's edit form to prefill (not a secret to its owner)
	} else {
		m["mode"] = "dm"
	}
	if d.Threads {
		m["threads"] = true
		// Console-input mirror (docs/log/37 Fix ②): default-on, so echo the resolved state
		// (not just when off) for the card's edit-form prefill.
		m["mirrorInput"] = !d.MirrorInputOff
	}
	if d.MentionUserID != "" {
		m["mention"] = true
		m["mentionUserId"] = d.MentionUserID // edit-form prefill
	}
	if d.Receive {
		m["receive"] = true
	}
	if d.FullText {
		m["fullText"] = true
	}
	// docs/log/37 P3, brought forward: signal that the standing fleet-operator thread is provisioned so
	// the card can show an "operator" pill (present only once the thread was actually opened).
	if ref, ok := bridge.OperatorState(); ok && ref.Thread != "" {
		m["operator"] = true
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
		entry := map[string]any{"id": g.ID, "name": g.Name, "channels": cl}
		// Owner = the user's own id in the recommended "your private server"
		// setup (docs/log/37 P1.5) — the card auto-fills the mention target from it,
		// so no Developer Mode / Copy-ID is ever needed. Best-effort.
		if owner, err := bridge.DiscordGuildOwner(token, g.ID); err == nil && owner != "" {
			entry["ownerId"] = owner
			if name, err := bridge.DiscordUserName(token, owner); err == nil {
				entry["ownerName"] = name
			}
		}
		out = append(out, entry)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"guilds": out})
}

type discordConnReq struct {
	Token         string   `json:"token"`
	ChannelID     string   `json:"channelId"`
	UserID        string   `json:"userId"`
	Events        []string `json:"events"`
	Threads       bool     `json:"threads"`       // thread-per-session (channel mode)
	MentionUserID string   `json:"mentionUserId"` // @mentioned per notification (channel mode)
	Lang          string   `json:"lang"`          // Console locale at connect time ("ja"/"en")
	Receive       bool     `json:"receive"`       // inbound: route thread replies back (docs/log/37 P2a)
	FullText      bool     `json:"fullText"`      // post the answer-ready turn body (docs/log/37 full-text bridge)
	// MirrorInput echoes Console-typed prompts into the session thread (docs/log/37 Fix ②).
	// A pointer so an omitted field means "default" (on) rather than false — the card
	// always sends it, but an older/edit request that leaves it out keeps the default.
	MirrorInput *bool `json:"mirrorInput"`
	// NotifyOff is the master mute (personal settings > notifications, and the card). A pointer so
	// an omitted field preserves the stored value on an edit; the toggle sends it explicitly.
	NotifyOff *bool `json:"notifyOff"`
}

// discordSnowflakeRe matches a Discord snowflake id — catches names/mentions
// pasted where an id belongs (both destinations are numeric ids).
var discordSnowflakeRe = regexp.MustCompile(`^[0-9]{5,25}$`)

// handlePutDiscordConn stores the user's Discord bot token + destination in the
// encrypted store (docs/log/37 P1; the same three pieces as the PagerDuty card). Exactly one
// destination: a guild channel id, or the user's own Discord user id for DMs
// (the identity binding of docs/log/37 contract 5). The token is validated against the
// Discord API when reachable — a network failure saves anyway (outbound may be
// restricted; sends will surface in the daemon log), but a 401/403 rejects.
//
// Edit-after-connect: when the token is OMITTED and a connection already exists,
// this patches the existing one — the stored token (and destination, if the
// request also omits it) is reused, so the card can change notification settings /
// toggles without re-pasting the token or re-picking the channel.
func handlePutDiscordConn(w http.ResponseWriter, r *http.Request) {
	var req discordConnReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	token := strings.TrimSpace(req.Token)
	channelID := strings.TrimSpace(req.ChannelID)
	userID := strings.TrimSpace(req.UserID)
	// Patch mode: no token in the request → reuse the stored connection's token, and
	// reuse its destination too when the request left that blank (an events/toggles edit).
	editing := token == "" && s.Discord != nil && s.Discord.Token != ""
	if token == "" {
		if !editing {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeConnDiscordTokenRequired, "enter a bot token")
			return
		}
		token = s.Discord.Token
		if channelID == "" && userID == "" {
			channelID, userID = s.Discord.ChannelID, s.Discord.UserID
		}
	}
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
	mention := strings.TrimSpace(req.MentionUserID)
	if mention != "" && !discordSnowflakeRe.MatchString(mention) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnDiscordDestInvalid, "mention target must be a numeric Discord id")
		return
	}
	// Reuse the cached bot name on an edit (token unchanged); only a fresh token needs
	// the /users/@me round-trip to validate + name it.
	botName := ""
	if editing {
		botName = s.Discord.BotName
	} else {
		n, err := bridge.DiscordBotName(token)
		if errors.Is(err, bridge.ErrUnauthorized) {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeConnDiscordTokenInvalid, "Discord rejected the bot token")
			return
		}
		botName = n
	}
	// Notification language rides the Console's active locale; anything but "en"
	// renders Japanese (pre-lang connections included — this deployment's default).
	lang := ""
	if req.Lang == "en" {
		lang = "en"
	}
	// Console-input mirror (docs/log/37 Fix ②): ON by default in channel+thread mode.
	// Preserve the stored value on an edit that omits the field; an explicit value wins.
	mirrorOff := editing && s.Discord != nil && s.Discord.MirrorInputOff
	if req.MirrorInput != nil {
		mirrorOff = !*req.MirrorInput
	}
	// Master mute (notifications on/off). Preserve the stored value on an edit that omits it.
	notifyOff := editing && s.Discord != nil && s.Discord.NotifyOff
	if req.NotifyOff != nil {
		notifyOff = *req.NotifyOff
	}
	creds := &secrets.DiscordCreds{Token: token, ChannelID: channelID, UserID: userID,
		BotName: botName, Events: events, Lang: lang,
		// Channel-mode extras (docs/log/37 P1.5 / P2a); meaningless for DM, so not stored there.
		// Receive (P2a inbound) rides thread mode — replies arrive in session threads.
		Threads: channelID != "" && req.Threads, MentionUserID: mention,
		Receive: channelID != "" && req.Receive,
		// Full-text mode (docs/log/37 full-text bridge) works in either destination mode —
		// the body posts to the channel/thread or the DM alike.
		FullText:  req.FullText,
		NotifyOff: notifyOff,
		// Mirror opt-out is meaningful only alongside thread mode.
		MirrorInputOff: channelID != "" && req.Threads && mirrorOff}
	if channelID == "" {
		creds.MentionUserID = ""
	}
	// Resolve the DM channel eagerly so the first notification doesn't pay the
	// round-trip; reuse the cached one on an edit, else a failure is tolerated.
	if userID != "" {
		if editing && s.Discord.DMChannelID != "" && s.Discord.UserID == userID {
			creds.DMChannelID = s.Discord.DMChannelID
		} else if ch, err := bridge.DiscordResolveDM(token, userID); err == nil {
			creds.DMChannelID = ch
		}
	}
	s.Discord = creds
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	res := discordStatus(s)
	// Fire one synchronous test notification on a FRESH connect so "did it arrive?" is
	// answered on the spot — but NOT on a settings edit (the card auto-saves each toggle,
	// so a test per change would spam the channel). Saved either way; a failed test (e.g.
	// missing channel permission) is surfaced to the card, not treated as a bad config.
	if !editing {
		// Pick the Discord provider by name — positional ps[0] would hit Slack when
		// Discord is excluded (e.g. NotifyOff) and falsely report the test as sent.
		for _, p := range bridge.Providers(s, nil, nil) {
			if p.Name() != "discord" {
				continue
			}
			if err := p.Send(bridge.Message{Kind: "bridge-test"}); err != nil {
				res["testError"] = err.Error()
			} else {
				res["test"] = "sent"
			}
			break
		}
	}
	// docs/log/37 P3, brought forward: with receive + channel mode, stand up (or reuse) the dedicated
	// fleet-operator thread + conversation so @mentions route to the operator assistant.
	// Async + best-effort — it does its own Discord round-trips, so it must not slow the
	// PUT or fail the connect (reuse across reconnects avoids duplicate threads).
	if creds.Receive && creds.ChannelID != "" {
		go provisionDiscordOperator(creds.Token, creds.ChannelID, creds.Lang)
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
	bridge.ResetThreads()        // stale session↔thread mappings die with the connection
	bridge.ResetOperatorThread() // drop the operator thread coordinates (the conv is kept)
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
// store (docs/log/25 Phase 1). The key is consumed only by `mcp-run pagerduty`,
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
// the encrypted store (docs/log/25). The token is consumed only by `mcp-run grafana`,
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

// cloudwatchStatus reports the stored CloudWatch settings (all non-secret — see
// awsProfileStatus).
func cloudwatchStatus(s *secrets.Data) map[string]any {
	if s.CloudWatch == nil || s.CloudWatch.Profile == "" {
		return map[string]any{"connected": false}
	}
	return awsProfileStatus(s.CloudWatch.AWSProfileRef)
}

// awsProfileConnReq is the wire shape shared by the AWS-profile-backed ops
// integrations (CloudWatch / AWS MCP): the Console's SSM profile picker sends the
// picked profile's non-secret SSO meta, or just a profile name for a manual entry.
type awsProfileConnReq struct {
	Profile   string `json:"profile"`
	Region    string `json:"region"`
	StartURL  string `json:"startUrl"`
	SSORegion string `json:"ssoRegion"`
	AccountID string `json:"accountId"`
	RoleName  string `json:"roleName"`
}

// awsProfileRef validates and normalizes the request into the stored form. It
// writes the error response itself and reports ok=false, so a handler is just
// "decode → this → store". Two rejections, both of which would otherwise fail much
// later and much less legibly: an empty profile, and SSO meta without its region
// (the generated aws config would be unusable).
func (req awsProfileConnReq) awsProfileRef(w http.ResponseWriter) (secrets.AWSProfileRef, bool) {
	profile := awsProfileRe.ReplaceAllString(strings.TrimSpace(req.Profile), "-")
	if profile == "" || profile == "-" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnAWSProfileRequired, "specify an AWS profile")
		return secrets.AWSProfileRef{}, false
	}
	startURL := strings.TrimSpace(req.StartURL)
	if startURL != "" && strings.TrimSpace(req.SSORegion) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnSSORegionMissing, "no SSO region (check the SSM profile configuration)")
		return secrets.AWSProfileRef{}, false
	}
	return secrets.AWSProfileRef{
		Profile:   profile,
		Region:    strings.TrimSpace(req.Region),
		StartURL:  startURL,
		SSORegion: strings.TrimSpace(req.SSORegion),
		AccountID: strings.TrimSpace(req.AccountID),
		RoleName:  strings.TrimSpace(req.RoleName),
	}, true
}

// awsProfileStatus is the non-secret echo shared by those cards: profile / region
// (they select the credential chain, they don't hold it) and sso=true for an
// SSM-linked profile (the ops aws config is generated from the stored SSO meta).
func awsProfileStatus(p secrets.AWSProfileRef) map[string]any {
	m := map[string]any{"connected": true, "profile": p.Profile}
	if p.Region != "" {
		m["region"] = p.Region
	}
	if p.StartURL != "" {
		m["sso"] = true
	}
	return m
}

// awsProfileRe strips characters unsafe for an ~/.aws/config profile
// header — the same sanitization as the CP's ssmProfileName (control-plane/
// ssm.go), so an SSM profile label yields the same profile name here as in an
// SSM session's config.
var awsProfileRe = regexp.MustCompile(`[^A-Za-z0-9._@-]+`)

// handlePutCloudWatchConn stores the AWS profile the CloudWatch MCP should use
// (docs/log/25). No secret is stored: auth is the AWS credential chain (the user's
// `aws sso login`, same as ssm sessions). Two shapes:
//   - SSO meta present (startUrl and friends, via the Console's SSM profile picker):
//     a durable ops aws config is generated from the meta (SSM profiles live in
//     per-session isolated configs, invisible to a bare AWS_PROFILE), here at
//     connect time and again at every mcp-run spawn.
//   - Profile name only: assumed to exist in the member's own ~/.aws.
func handlePutCloudWatchConn(w http.ResponseWriter, r *http.Request) {
	var req awsProfileConnReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	ref, ok := req.awsProfileRef(w)
	if !ok {
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.CloudWatch = &secrets.CloudWatchConn{AWSProfileRef: ref}
	// Materialize the ops config now (mcp-run also regenerates it per spawn) so
	// the user can `aws sso login` against it before the first chat turn.
	if ref.StartURL != "" {
		if err := mcpx.WriteOpsAWSConfig("cloudwatch", ref); err != nil {
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

// awsMCPStatus reports the stored Agent Toolkit for AWS settings. Same non-secret
// echo as CloudWatch plus the two knobs the card renders: the MCP service endpoint
// region and whether the mutating tools were opted into.
func awsMCPStatus(s *secrets.Data) map[string]any {
	if s.AWS == nil || s.AWS.Profile == "" {
		return map[string]any{"connected": false}
	}
	m := awsProfileStatus(s.AWS.AWSProfileRef)
	m["endpoint"] = awsMCPEndpoint(s.AWS.Endpoint)
	m["write"] = s.AWS.Write
	return m
}

type awsMCPConnReq struct {
	awsProfileConnReq
	Endpoint string `json:"endpoint"`
	Write    bool   `json:"write"`
}

// awsMCPEndpoints are the regions AWS publishes the MCP Server in. Constrained to a
// list rather than free text: the value goes into a hostname AND into the SigV4
// signing region, so a typo would fail as a connection error with no hint of why.
var awsMCPEndpoints = []string{"us-east-1", "eu-central-1"}

// awsMCPEndpoint normalizes a requested endpoint region, falling back to the default
// for empty or unknown input.
func awsMCPEndpoint(region string) string {
	for _, r := range awsMCPEndpoints {
		if strings.TrimSpace(region) == r {
			return r
		}
	}
	return mcpx.AWSMCPDefaultEndpoint
}

// handlePutAWSMCPConn stores the AWS profile + endpoint the AWS MCP proxy should use
// (docs/log/25 §AWS MCP). Profile handling is identical to CloudWatch — no secret, SSO
// meta materialized into a durable ops config. `write` is the one addition: it opts
// into the mutating tools (call_aws / run_script), so it is stored explicitly and
// echoed back for the card to show.
func handlePutAWSMCPConn(w http.ResponseWriter, r *http.Request) {
	var req awsMCPConnReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	ref, ok := req.awsProfileRef(w)
	if !ok {
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.AWS = &secrets.AWSConn{
		AWSProfileRef: ref,
		Endpoint:      awsMCPEndpoint(req.Endpoint),
		Write:         req.Write,
	}
	if ref.StartURL != "" {
		if err := mcpx.WriteOpsAWSConfig("aws", ref); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "config_failed", err.Error())
			return
		}
	}
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, awsMCPStatus(s))
}

func handleDeleteAWSMCPConn(w http.ResponseWriter, r *http.Request) {
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.AWS = nil
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "aws"})
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
			if auth, err := gitx.BitbucketAuthHeader(s); err == nil {
				if h, email, err := gitx.BitbucketAccount(auth); err == nil && h != "" {
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
			if auth, err := gitx.BitbucketAuthHeader(s); err == nil {
				if h, email, err := gitx.BitbucketAccount(auth); err == nil && h != "" {
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
		if login, email, err := gitx.GithubAccount(e.Token); err == nil && login != "" {
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

	// Verify a pasted Bitbucket credential at connect time so a scopeless token / wrong
	// email / missing scope is reported on the spot, not as a later opaque list/clone
	// failure. Hard failures block the save; a missing push scope connects with a warning.
	var warn string
	if host == "bitbucket.org" {
		w2, verr := gitx.BitbucketConnectCheck(user, token)
		if verr != nil {
			code := "bb_verify_failed"
			switch {
			case errors.Is(verr, gitx.ErrBBScopeless):
				code = "bb_scopeless"
			case errors.Is(verr, gitx.ErrBBNoRepoRead):
				code = "bb_no_repo_read"
			}
			httpx.WriteErr(w, http.StatusBadRequest, code, verr.Error())
			return
		}
		warn = w2
	}

	if err := upsertGitCredential(host, user, token); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	// Commit identity is set separately (per provider) via /identity — not clobbered
	// into the global config at connect time.
	resp := map[string]any{"connected": true, "host": host, "username": user}
	if warn != "" {
		resp["warn"] = warn
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
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
		gitx.RemoveBitbucketOAuth() // also clear OAuth tokens + the refresh helper
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": host})
}

// upsertGitCredential stores an HTTPS credential for host in the encrypted store
// and ensures the cred helper is the active git credential source.
func upsertGitCredential(host, user, token string) error {
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Git[host] = secrets.GitEntry{User: user, Token: token}
		return nil
	}); err != nil {
		return err
	}
	return ensureCredHelper()
}

func removeGitCredential(host string) error {
	return secrets.Update(func(s *secrets.Data) error {
		delete(s.Git, host)
		return nil
	})
}

func gitConfigGlobal(key, val string) error {
	if out, err := gitx.Combined("", "config", "--global", key, val); err != nil {
		return fmt.Errorf("git config %s: %v: %s", key, err, out)
	}
	return nil
}
