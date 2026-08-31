package bridge

// Slack chat-bridge provider (docs/log/37 Slack 追随): the Socket-Mode twin of the Discord
// provider, on the SAME bridge.Provider abstraction. This file is the SEND half — the Slack
// Web API plumbing (chat.postMessage / chat.update / reactions.add / the setup lookups) and
// the resumable send path. The receive half (Socket Mode WSS) lives in slack_socket.go; the
// Block Kit button rendering in slack_interact.go.
//
// Slack differs from Discord in ways that SIMPLIFY the port: a "thread" is just messages
// sharing a thread_ts (the root message's ts) in the SAME channel — no separate thread
// object, no archive/unarchive, so the thread store keys on the root ts and self-heal is a
// single recreate. The Web API always returns HTTP 200 with an {ok,error} envelope, so
// success is the `ok` flag, not the status code (429 rate-limits are the exception).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// slackAPIBase is a var so contract tests point it at a local httptest server; the live
// test (AF_SLACK_LIVE=1) keeps the real endpoint.
var slackAPIBase = "https://slack.com/api"

// slackContentLimit is a conservative cap below Slack's real 40000-char message limit —
// well under it for readability (long answers still chunk into digestible posts), and the
// user asked to size Slack chunking to ~4000 (docs/log/37 Slack 追随).
const slackContentLimit = 3900

// ErrSlackUnauthorized marks a token Slack rejected (invalid_auth / not_authed /
// account_inactive) — the connections handler turns it into a 400 for the card, while a
// network error is tolerated (offline-friendly).
var ErrSlackUnauthorized = errors.New("slack: unauthorized")

// slackAPIError carries Slack's error slug so the thread logic can discriminate a deleted
// root (recreate) from a transient failure (retry as-is).
type slackAPIError struct{ Slug string }

func (e *slackAPIError) Error() string { return "slack: " + e.Slug }

func isSlackUnknownThread(err error) bool {
	var se *slackAPIError
	return errors.As(err, &se) && (se.Slug == "thread_not_found" || se.Slug == "message_not_found")
}

// slackDo is the one Web API call shape: JSON in, `Authorization: Bearer <token>`, decode
// the {ok,error,…} envelope. A bounded inline retry respects Slack's 429 Retry-After (the
// primary duplicate-storm guard, same rationale as discordDo). out is unmarshalled from the
// same body on success.
func slackDo(method, token string, body any, out any) error {
	var raw []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		raw = b
	}
	for attempt := 0; ; attempt++ {
		var rdr io.Reader
		if raw != nil {
			rdr = bytes.NewReader(raw)
		}
		req, err := http.NewRequest("POST", slackAPIBase+"/"+method, rdr)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests && attempt < slackRateRetries {
			wait := slackRetryAfter(resp)
			log.Printf("bridge: slack 429 on %s — retrying in %s", method, wait)
			time.Sleep(wait)
			continue
		}
		var env struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(b, &env); err != nil {
			return fmt.Errorf("slack: decode %s: %w", method, err)
		}
		if !env.OK {
			switch env.Error {
			case "invalid_auth", "not_authed", "account_inactive", "token_revoked":
				return fmt.Errorf("%w (%s)", ErrSlackUnauthorized, env.Error)
			}
			return &slackAPIError{Slug: env.Error}
		}
		if out != nil {
			if err := json.Unmarshal(b, out); err != nil {
				return fmt.Errorf("slack: decode %s: %w", method, err)
			}
		}
		return nil
	}
}

var (
	slackRateRetries = 3
	slackRetryCap    = 5 * time.Second
)

func slackRetryAfter(resp *http.Response) time.Duration {
	if h := resp.Header.Get("Retry-After"); h != "" {
		if secs, err := strconv.ParseFloat(h, 64); err == nil && secs > 0 {
			d := time.Duration(secs * float64(time.Second))
			if d > slackRetryCap {
				return slackRetryCap
			}
			if d < 100*time.Millisecond {
				return 100 * time.Millisecond
			}
			return d
		}
	}
	return time.Second
}

// --- setup / lookup helpers (feed the connections card wizard) --------------------------

// SlackAuth is the identity behind a bot token (auth.test): the workspace and the bot's own
// user id (to filter its message echoes on receive).
type SlackAuth struct {
	Team      string `json:"team"`
	TeamID    string `json:"team_id"`
	BotName   string `json:"user"`
	BotUserID string `json:"user_id"`
	URL       string `json:"url"`
}

// SlackAuthTest validates a bot token and returns the workspace + bot identity.
func SlackAuthTest(botToken string) (SlackAuth, error) {
	var a SlackAuth
	err := slackDo("auth.test", botToken, map[string]any{}, &a)
	return a, err
}

// SlackAppTest validates an app-level token (xapp-) by opening a Socket-Mode connection
// ticket; the returned WSS url is discarded here (the receiver opens its own).
func SlackAppTest(appToken string) error {
	var res struct {
		URL string `json:"url"`
	}
	return slackDo("apps.connections.open", appToken, map[string]any{}, &res)
}

// slackOpenSocket opens a Socket-Mode WSS url (used by the receiver each connect).
func slackOpenSocket(appToken string) (string, error) {
	var res struct {
		URL string `json:"url"`
	}
	if err := slackDo("apps.connections.open", appToken, map[string]any{}, &res); err != nil {
		return "", err
	}
	if res.URL == "" {
		return "", fmt.Errorf("slack: apps.connections.open returned no url")
	}
	return res.URL, nil
}

type SlackChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SlackChannels lists the channels the bot is a MEMBER of (users.conversations) — exactly
// the set it can post to and receive from, so the card offers them as a picker. The bot
// must be /invite-d to a channel first; unlike Discord there is no guild-wide listing.
func SlackChannels(botToken string) ([]SlackChannel, error) {
	var res struct {
		Channels []SlackChannel `json:"channels"`
	}
	err := slackDo("users.conversations", botToken,
		map[string]any{"types": "public_channel,private_channel", "exclude_archived": true, "limit": 200}, &res)
	return res.Channels, err
}

// SlackUserByEmail resolves the bound user's Slack id from their email (users.lookupByEmail)
// — the zero-friction identity binding (docs/log/37 契約5): AF already knows the member's email,
// so no Copy-Member-ID dance. Needs users:read.email. "" (with nil err never) if unresolved.
func SlackUserByEmail(botToken, email string) (string, error) {
	var res struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := slackDo("users.lookupByEmail", botToken, map[string]any{"email": email}, &res); err != nil {
		return "", err
	}
	return res.User.ID, nil
}

// SlackResolveDM opens (or returns) the IM channel with the bound user (conversations.open)
// — the "DM this person" shape (docs/log/37 契約5: only ever the explicitly bound user id).
func SlackResolveDM(botToken, userID string) (string, error) {
	var res struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := slackDo("conversations.open", botToken, map[string]any{"users": userID}, &res); err != nil {
		return "", err
	}
	if res.Channel.ID == "" {
		return "", fmt.Errorf("slack: conversations.open returned no channel")
	}
	return res.Channel.ID, nil
}

// --- core post primitives ---------------------------------------------------------------

// slackPostMessage posts to a channel (thread_ts "" = top-level, else a threaded reply) and
// returns the created message ts. blocks (Block Kit) may accompany or replace text; Slack
// still wants a fallback `text` for notifications, so an empty text with blocks is filled.
func slackPostMessage(token, channel, threadTS, text string, blocks []any) (string, error) {
	body := map[string]any{"channel": channel}
	if text != "" {
		body["text"] = text
	}
	if len(blocks) > 0 {
		body["blocks"] = blocks
		if text == "" {
			body["text"] = slackBlocksFallback
		}
	}
	if threadTS != "" {
		body["thread_ts"] = threadTS
	}
	var res struct {
		TS string `json:"ts"`
	}
	err := slackDo("chat.postMessage", token, body, &res)
	return res.TS, err
}

// slackBlocksFallback is the notification-preview text for a buttons-only message.
const slackBlocksFallback = "…"

// SlackAddReaction reacts to a message as the bot (reactions.add) — the receive receipt
// (👀 ≈ Slack's :eyes:). Best-effort; a missing scope just errors and the caller logs.
func SlackAddReaction(token, channel, ts, name string) error {
	return slackDo("reactions.add", token, map[string]any{"channel": channel, "timestamp": ts, "name": name}, nil)
}

// slackReceiptReaction is the emoji NAME (no colons) for the inbound receipt.
const slackReceiptReaction = "eyes"

// SlackUpdateMessage edits a message's text + blocks (chat.update) — after a button click,
// replace the prompt with the outcome and clear the buttons. Pass nil blocks to remove them.
func SlackUpdateMessage(token, channel, ts, text string, blocks []any) error {
	body := map[string]any{"channel": channel, "ts": ts, "text": text}
	body["blocks"] = blocks // explicit (even nil) so old blocks are cleared
	return slackDo("chat.update", token, body, nil)
}

// --- the send Provider ------------------------------------------------------------------

// slackProvider is the Slack send implementation (Provider + ResumableSender).
type slackProvider struct {
	creds   secrets.SlackCreds
	cacheDM func(channelID string)
}

func (sp *slackProvider) Name() string { return "slack" }

func (sp *slackProvider) Caps() Caps { return Caps{CanSend: true} }

func (sp *slackProvider) Wants(eventKey string) bool { return EventEnabled(sp.creds.Events, eventKey) }

func (sp *slackProvider) Send(m Message) error {
	_, err := sp.SendFrom(m, 0)
	return err
}

// SendFrom mirrors discordProvider.SendFrom: deliver m starting at sub-message `from`,
// return the cumulative delivered count so a partial failure resumes without re-posting
// (docs/log/37 重複対策). session-report is suppressed in thread mode (answer-ready already
// delivered the completion there; operator visibility rides the operator-thread mirror).
func (sp *slackProvider) SendFrom(m Message, from int) (int, error) {
	if m.Kind == "session-report" && sp.creds.ChannelID != "" && sp.creds.Threads && m.SessionName != "" {
		return from, nil
	}
	ch, err := sp.destChannel()
	if err != nil {
		return from, err
	}
	msgs := sp.buildSlackMessages(m)
	if len(msgs) == 0 {
		return from, nil
	}
	if sp.creds.ChannelID != "" && sp.creds.Threads && m.SessionName != "" {
		return sp.sendThreaded(m, msgs, from)
	}
	for i := from; i < len(msgs); i++ {
		if _, err = slackPostMessage(sp.creds.BotToken, ch, "", msgs[i].text, msgs[i].blocks); err != nil {
			return i, err
		}
	}
	return len(msgs), nil
}

// slackMsg is one message to post: fallback text and/or Block Kit blocks.
type slackMsg struct {
	text   string
	blocks []any
}

// buildSlackMessages renders the ordered sub-messages for one event (mirrors
// discordProvider.buildMessages): the content chunks (the scrubbed, mrkdwn-rendered body
// alone in full-text mode, else the headline), mention-budgeted, then any button messages.
func (sp *slackProvider) buildSlackMessages(m Message) []slackMsg {
	content := m.textSlack(sp.creds.Lang)
	if sp.creds.FullText && m.Body != "" {
		content = withDivider(renderBodyForSlack(m.Body))
	}
	prefix := ""
	if sp.creds.ChannelID != "" && sp.creds.UserID != "" && sp.shouldMention(m) {
		prefix = "<@" + sp.creds.UserID + "> "
	}
	var msgs []slackMsg
	for _, c := range chunkTo(content, prefix, slackContentLimit) {
		msgs = append(msgs, slackMsg{text: c})
	}
	if sp.interactive() {
		msgs = append(msgs, sp.buttonMessages(m)...)
	}
	return msgs
}

// shouldMention is the Slack copy of discordProvider.shouldMention (docs/log/37 mention time-
// gate): action/abnormal events always ping; read-only events ping only after the thread
// has been quiet for mentionQuietWindow. Reuses alwaysMentionKind + the shared window.
func (sp *slackProvider) shouldMention(m Message) bool {
	if alwaysMentionKind(m.Kind) {
		return true
	}
	if sp.creds.ChannelID == "" || !sp.creds.Threads || m.SessionName == "" {
		return true
	}
	ref, ok := slackThreads.load()[m.SessionName]
	if !ok || ref.LastPostAt == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339, ref.LastPostAt)
	if err != nil {
		return true
	}
	return time.Since(last) >= mentionQuietWindow
}

// destChannel resolves a flat (non-thread) destination: the configured channel, or the
// (cached / lazily resolved) DM channel.
func (sp *slackProvider) destChannel() (string, error) {
	if sp.creds.ChannelID != "" {
		return sp.creds.ChannelID, nil
	}
	if sp.creds.DMChannelID != "" {
		return sp.creds.DMChannelID, nil
	}
	if sp.creds.UserID == "" {
		return "", fmt.Errorf("slack: no destination configured")
	}
	ch, err := SlackResolveDM(sp.creds.BotToken, sp.creds.UserID)
	if err != nil {
		return "", err
	}
	sp.creds.DMChannelID = ch
	if sp.cacheDM != nil {
		sp.cacheDM(ch)
	}
	return ch, nil
}

// sendThreaded posts msgs[from:] into the session's thread, seeding it from the session's
// first notification when needed (the seed's ts IS the thread root — Slack threads have no
// separate object), returning the cumulative delivered count so a partial failure resumes.
func (sp *slackProvider) sendThreaded(m Message, msgs []slackMsg, from int) (int, error) {
	// Mutations go through update (ロック下 load→fn→save): writing back the snapshot
	// read here would roll back a concurrent touch()'s LastPostAt (same as Discord).
	if ref, ok := slackThreads.load()[m.SessionName]; ok && ref.Channel == sp.creds.ChannelID {
		delivered, err := sp.postRangeToThread(m.SessionName, ref.Channel, ref.Thread, msgs, from)
		if err == nil || !isSlackUnknownThread(err) {
			return delivered, err
		}
		// root deleted — drop the stale mapping and reseed
		slackThreads.update(func(ts threadMap) { delete(ts, m.SessionName) })
		from = 0
	}
	rootTS, err := slackPostMessage(sp.creds.BotToken, sp.creds.ChannelID, "", msgs[0].text, msgs[0].blocks)
	if err != nil {
		return 0, err
	}
	slackThreads.update(func(ts threadMap) {
		ts[m.SessionName] = threadRef{Channel: sp.creds.ChannelID, Thread: rootTS}
	})
	return sp.postRangeToThread(m.SessionName, sp.creds.ChannelID, rootTS, msgs, 1)
}

// postRangeToThread posts msgs[from:] as threaded replies (thread_ts = rootTS), stamping the
// mention time-gate on success. Returns the cumulative delivered count.
func (sp *slackProvider) postRangeToThread(session, channel, rootTS string, msgs []slackMsg, from int) (int, error) {
	for i := from; i < len(msgs); i++ {
		if _, err := slackPostMessage(sp.creds.BotToken, channel, rootTS, msgs[i].text, msgs[i].blocks); err != nil {
			return i, err
		}
	}
	slackThreads.touch(session, time.Now())
	return len(msgs), nil
}

// mirrorSlackInput echoes a Console-typed prompt into the session's Slack thread (docs/log/37
// Fix ②). Best-effort, gated the same way as Discord: channel+thread mode, not opted out,
// and a thread already exists (an echo never creates one). Called from MirrorUserInput.
func mirrorSlackInput(sessionName, text string) {
	s, err := secrets.Load()
	if err != nil || s.Slack == nil {
		return
	}
	sl := s.Slack
	if sl.BotToken == "" || sl.ChannelID == "" || !sl.Threads || sl.MirrorInputOff {
		return
	}
	ref, ok := slackThreads.load()[sessionName]
	if !ok || ref.Thread == "" || ref.Channel != sl.ChannelID {
		return
	}
	for _, chunk := range chunkTo(withDivider(renderBodyForSlack(text)), "🧑 ", slackContentLimit) {
		if _, err := slackPostMessage(sl.BotToken, ref.Channel, ref.Thread, chunk, nil); err != nil {
			log.Printf("bridge: mirror console input to slack %s failed: %v", sessionName, err)
			return
		}
	}
	slackThreads.touch(sessionName, time.Now())
}
