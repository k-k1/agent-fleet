package main

// Slack chat-bridge connection handlers (docs/log/37 Slack follow-up) — the Socket-Mode twin of
// the Discord connection code in connections.go. Same three-point set (agent routes + CP proxy
// allowlist + Console card) and the same secrets-store discipline: two of the user's OWN
// tokens (bot xoxb- + app-level xapp-) are stored encrypted, never held by the CP.

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// slackChannelRe / slackUserRe match Slack ids (channels start C/G, users U/W) so a pasted
// name/handle where an id belongs is caught early.
var (
	slackChannelRe = regexp.MustCompile(`^[CG][A-Z0-9]{6,}$`)
	slackUserRe    = regexp.MustCompile(`^[UW][A-Z0-9]{6,}$`)
)

// slackStatus reports the chat-bridge Slack connection (docs/log/37 Slack follow-up) — never a token.
func slackStatus(s *secrets.Data) map[string]any {
	sl := s.Slack
	if sl == nil || sl.BotToken == "" {
		return map[string]any{"connected": false}
	}
	m := map[string]any{"connected": true}
	m["notify"] = !sl.NotifyOff // master mute state (default on) for the notification toggle
	if sl.BotName != "" {
		m["botName"] = sl.BotName
	}
	if sl.TeamName != "" {
		m["teamName"] = sl.TeamName
	}
	if sl.ChannelID != "" {
		m["mode"] = "channel"
		m["channelId"] = sl.ChannelID
	} else {
		m["mode"] = "dm"
	}
	if sl.UserID != "" {
		m["userId"] = sl.UserID // edit-form prefill (the bound user is also the mention target)
	}
	if sl.Threads {
		m["threads"] = true
		m["mirrorInput"] = !sl.MirrorInputOff
	}
	if sl.Receive {
		m["receive"] = true
	}
	if sl.FullText {
		m["fullText"] = true
	}
	if ref, ok := bridge.SlackOperatorState(); ok && ref.Thread != "" {
		m["operator"] = true
	}
	events := sl.Events
	if len(events) == 0 {
		events = bridge.EventKeys
	}
	m["events"] = events
	return m
}

// handleSlackInspect (POST /connections/slack/inspect {botToken, appToken}) is step 1 of the
// card wizard: validate the pasted tokens and hand back the workspace + bot identity. The app
// token is validated only when supplied (it is needed for Receive, optional otherwise).
// Nothing is stored yet.
func handleSlackInspect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BotToken string `json:"botToken"`
		AppToken string `json:"appToken"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	bot := strings.TrimSpace(req.BotToken)
	if bot == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnSlackTokenRequired, "enter a bot token")
		return
	}
	auth, err := bridge.SlackAuthTest(bot)
	if errors.Is(err, bridge.ErrSlackUnauthorized) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnSlackTokenInvalid, "Slack rejected the bot token")
		return
	}
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "slack_unreachable", err.Error())
		return
	}
	if app := strings.TrimSpace(req.AppToken); app != "" {
		if err := bridge.SlackAppTest(app); errors.Is(err, bridge.ErrSlackUnauthorized) {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeConnSlackTokenInvalid, "Slack rejected the app-level token")
			return
		} else if err != nil {
			httpx.WriteErr(w, http.StatusBadGateway, "slack_unreachable", err.Error())
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"botName": auth.BotName, "teamName": auth.Team, "botUserId": auth.BotUserID,
	})
}

// handleSlackChannels (POST /connections/slack/channels {botToken, email}) is step 2: list the
// channels the bot is a member of (the invite target) and, when the caller passes the member's
// email, auto-resolve their Slack user id (the mention + identity binding) so no Copy-Member-ID
// is needed. Both are best-effort; the card falls back to manual id entry.
func handleSlackChannels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BotToken string `json:"botToken"`
		Email    string `json:"email"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	bot := strings.TrimSpace(req.BotToken)
	if bot == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnSlackTokenRequired, "enter a bot token")
		return
	}
	chs, err := bridge.SlackChannels(bot)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "slack_unreachable", err.Error())
		return
	}
	out := []map[string]string{}
	for _, c := range chs {
		out = append(out, map[string]string{"id": c.ID, "name": c.Name})
	}
	res := map[string]any{"channels": out}
	if email := strings.TrimSpace(req.Email); email != "" {
		if uid, err := bridge.SlackUserByEmail(bot, email); err == nil && uid != "" {
			res["resolvedUserId"] = uid
		}
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

type slackConnReq struct {
	BotToken    string   `json:"botToken"`
	AppToken    string   `json:"appToken"`
	ChannelID   string   `json:"channelId"`
	UserID      string   `json:"userId"`
	Events      []string `json:"events"`
	Threads     bool     `json:"threads"`
	Receive     bool     `json:"receive"`
	FullText    bool     `json:"fullText"`
	MirrorInput *bool    `json:"mirrorInput"`
	NotifyOff   *bool    `json:"notifyOff"` // master mute (notifications on/off); pointer = preserve on edit
	Lang        string   `json:"lang"`
}

// handlePutSlackConn stores the user's Slack tokens + destination in the encrypted store
// (docs/log/37 Slack follow-up), mirroring handlePutDiscordConn. Exactly one destination: a channel
// id or the bound user id (DM); the bound user id is required in channel mode too (it is the
// mention + identity binding). Edit-after-connect: an omitted bot token patches the existing
// connection (tokens + destination reused).
func handlePutSlackConn(w http.ResponseWriter, r *http.Request) {
	var req slackConnReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	bot := strings.TrimSpace(req.BotToken)
	app := strings.TrimSpace(req.AppToken)
	channelID := strings.TrimSpace(req.ChannelID)
	userID := strings.TrimSpace(req.UserID)
	editing := bot == "" && s.Slack != nil && s.Slack.BotToken != ""
	if bot == "" {
		if !editing {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeConnSlackTokenRequired, "enter a bot token")
			return
		}
		bot = s.Slack.BotToken
		if app == "" {
			app = s.Slack.AppToken
		}
		if channelID == "" && userID == "" {
			channelID, userID = s.Slack.ChannelID, s.Slack.UserID
		}
	}
	if (channelID == "") == (userID == "") {
		// Channel mode also needs the bound user id (mention + identity), so a channel
		// destination with no user is only valid when receive is off AND mention isn't needed;
		// to keep the model simple we require the user id whenever a channel is set for receive.
		if channelID == "" && userID == "" {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeConnSlackDestRequired, "set a channel id or a user id")
			return
		}
	}
	if channelID != "" && !slackChannelRe.MatchString(channelID) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnSlackDestInvalid, "channel must be a Slack channel id (C…)")
		return
	}
	if userID != "" && !slackUserRe.MatchString(userID) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeConnSlackDestInvalid, "user must be a Slack user id (U…)")
		return
	}
	// Receive (Socket Mode) needs the app token and a bound user to verify against (contract 5).
	if req.Receive && channelID != "" {
		if app == "" {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeConnSlackAppTokenRequired, "receive needs an app-level token (xapp-)")
			return
		}
		if userID == "" {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeConnSlackDestRequired, "receive needs the bound user id")
			return
		}
	}
	var events []string
	for _, ev := range req.Events {
		if bridge.EventEnabled(bridge.EventKeys, ev) {
			events = append(events, ev)
		}
	}
	if len(events) == len(bridge.EventKeys) {
		events = nil
	}
	lang := ""
	if req.Lang == "en" {
		lang = "en"
	}
	// Validate the bot token + capture identity on a fresh connect; reuse the cache on an edit.
	botName, teamName, botUserID := "", "", ""
	if editing {
		botName, teamName, botUserID = s.Slack.BotName, s.Slack.TeamName, s.Slack.BotUserID
	} else {
		auth, err := bridge.SlackAuthTest(bot)
		if errors.Is(err, bridge.ErrSlackUnauthorized) {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeConnSlackTokenInvalid, "Slack rejected the bot token")
			return
		} else if err != nil {
			httpx.WriteErr(w, http.StatusBadGateway, "slack_unreachable", err.Error())
			return
		}
		botName, teamName, botUserID = auth.BotName, auth.Team, auth.BotUserID
	}
	mirrorOff := editing && s.Slack != nil && s.Slack.MirrorInputOff
	if req.MirrorInput != nil {
		mirrorOff = !*req.MirrorInput
	}
	notifyOff := editing && s.Slack != nil && s.Slack.NotifyOff
	if req.NotifyOff != nil {
		notifyOff = *req.NotifyOff
	}
	creds := &secrets.SlackCreds{
		BotToken: bot, AppToken: app, ChannelID: channelID, UserID: userID,
		BotName: botName, TeamName: teamName, BotUserID: botUserID,
		Events: events, Lang: lang,
		Threads:        channelID != "" && req.Threads,
		Receive:        channelID != "" && req.Receive,
		FullText:       req.FullText,
		NotifyOff:      notifyOff,
		MirrorInputOff: channelID != "" && req.Threads && mirrorOff,
	}
	// Resolve the DM channel eagerly for DM mode; reuse the cache on an edit, else tolerate.
	if channelID == "" && userID != "" {
		if editing && s.Slack.DMChannelID != "" && s.Slack.UserID == userID {
			creds.DMChannelID = s.Slack.DMChannelID
		} else if ch, err := bridge.SlackResolveDM(bot, userID); err == nil {
			creds.DMChannelID = ch
		}
	}
	s.Slack = creds
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	res := slackStatus(s)
	// Fire one synchronous test notification on a FRESH connect only — not on a settings
	// edit (auto-save per toggle would otherwise spam the channel with tests).
	if !editing {
		// Pick the Slack provider by name — positional ps[len-1] would hit Discord when
		// Slack is excluded (e.g. NotifyOff) and falsely report the test as sent.
		for _, p := range bridge.Providers(s, nil, nil) {
			if p.Name() != "slack" {
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
	if creds.Receive && creds.ChannelID != "" {
		go provisionSlackOperator(creds.BotToken, creds.ChannelID, creds.Lang)
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func handleDeleteSlackConn(w http.ResponseWriter, r *http.Request) {
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.Slack = nil
	if err := s.Save(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	bridge.ResetSlackThreads()
	bridge.ResetSlackOperatorThread()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": "slack"})
}
