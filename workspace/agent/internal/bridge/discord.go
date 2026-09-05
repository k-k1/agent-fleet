package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// discordAPIBase is a var so the contract tests point it at a local httptest
// server; the live test (AF_DISCORD_LIVE=1) keeps the real endpoint.
var discordAPIBase = "https://discord.com/api/v10"

// ErrUnauthorized marks a credential Discord rejected (401/403) — the
// connections handler turns it into a 400 for the card, while a network error
// is tolerated (offline-friendly: outbound may be restricted, docs/log/37).
var ErrUnauthorized = errors.New("discord: unauthorized")

// discordAPIError carries the HTTP status and Discord's JSON error code so the
// thread logic can discriminate "thread archived" (50083 → unarchive + retry)
// and "unknown channel" (10003 / 404 → the thread was deleted, recreate) from
// transient failures that should just retry as-is.
type discordAPIError struct {
	Status int
	Code   int
	Msg    string
}

func (e *discordAPIError) Error() string {
	return fmt.Sprintf("discord: %d (code %d): %s", e.Status, e.Code, e.Msg)
}

func isUnknownChannel(err error) bool {
	var de *discordAPIError
	return errors.As(err, &de) && (de.Status == http.StatusNotFound || de.Code == 10003)
}

func isThreadArchived(err error) bool {
	var de *discordAPIError
	return errors.As(err, &de) && de.Code == 50083
}

// discordProvider is the P1 send-only Discord implementation (docs/log/37 contract 1:
// Discord is fully capable — CanReceive/CanInteract turn on with P2's Gateway
// connection; Send itself needs only REST, no Gateway).
type discordProvider struct {
	creds secrets.DiscordCreds
	// cacheDM persists a freshly resolved DM channel id back to the store so
	// the next drain skips the resolve round-trip. Optional.
	cacheDM func(channelID string)
}

// Providers builds the send-capable providers configured in the secrets store.
// Called per drain — cheap, stateless, and picks up connect/disconnect without
// any daemon-side registry. Discord and Slack coexist (docs/log/37 Slack follow-up): each
// gets its own resume cursor (queued.Delivered keys on Name()) and DM cache callback.
func Providers(s *secrets.Data, cacheDiscordDM, cacheSlackDM func(channelID string)) []Provider {
	var out []Provider
	if d := s.Discord; d != nil && d.Token != "" && !d.NotifyOff {
		out = append(out, &discordProvider{creds: *d, cacheDM: cacheDiscordDM})
	}
	if sl := s.Slack; sl != nil && sl.BotToken != "" && !sl.NotifyOff {
		out = append(out, &slackProvider{creds: *sl, cacheDM: cacheSlackDM})
	}
	return out
}

func (d *discordProvider) Name() string { return "discord" }

func (d *discordProvider) Caps() Caps { return Caps{CanSend: true} }

func (d *discordProvider) Wants(eventKey string) bool {
	return EventEnabled(d.creds.Events, eventKey)
}

// Send is the non-resumable entry point (Provider.Send) — a thin wrapper over
// SendFrom that starts at the first sub-message and drops the delivered count.
func (d *discordProvider) Send(m Message) error {
	_, err := d.SendFrom(m, 0)
	return err
}

// SendFrom delivers m starting at sub-message index `from` and returns the count of
// sub-messages delivered so far (cumulative from 0), so the sender can resume a
// partial delivery without re-posting what already landed (docs/log/37 duplicate
// prevention = ResumableSender). A transient failure returns the count reached and the error.
func (d *discordProvider) SendFrom(m Message, from int) (int, error) {
	// In thread mode the completion is already delivered to the session's thread by
	// answer-ready, so the operator-facing session-report would just double it there
	// (operator visibility rides the operator-thread mirror instead). Suppress it — a
	// no-op success so the queue entry is consumed. Flat/DM keep the report.
	if m.Kind == "session-report" && d.creds.ChannelID != "" && d.creds.Threads && m.SessionName != "" {
		return from, nil
	}
	ch, err := d.destChannel()
	if err != nil {
		return from, err
	}
	msgs := d.buildMessages(m)
	if len(msgs) == 0 {
		return from, nil
	}
	// Thread-per-session (docs/log/37 P1.5): guild channel destination only — threads
	// don't exist in DMs — and only for session-scoped events.
	if d.creds.ChannelID != "" && d.creds.Threads && m.SessionName != "" {
		return d.sendThreaded(m, msgs, from)
	}
	for i := from; i < len(msgs); i++ {
		if _, err = discordPost(d.creds.Token, ch, msgs[i]); err != nil {
			return i, err
		}
	}
	return len(msgs), nil
}

// buildMessages renders the ordered sub-messages for one event: the content chunks
// (the scrubbed, table-reflowed body alone in full-text mode, else the headline),
// each mention-budgeted, then any P2b button messages. The order is stable across
// retries so a delivery cursor (SendFrom's `from`) indexes the same messages.
func (d *discordProvider) buildMessages(m Message) []outMsg {
	content := m.Text(d.creds.Lang)
	// Full-text bridge (docs/log/37 future direction): in full-text mode the answer body IS
	// the message — drop the headline/display-name/link preface and post the scrubbed,
	// Discord-formatted body alone (the thread name already names the session, and
	// the deep link is usually dead in the local-only setup full-text targets). Only
	// answer-ready carries a body; every other kind keeps its headline.
	if d.creds.FullText && m.Body != "" {
		// A trailing divider (docs/log/37 Fix ⑤) so a run of answers doesn't visually merge.
		content = withDivider(renderBodyForDiscord(m.Body))
	}
	// Mention makes mobile push deterministic: Discord's default notification level
	// for guild channels AND threads is "only @mentions", so an unpinged notification
	// silently becomes badge-only (docs/log/37 P1.5). DM mode needs none. It rides the
	// FIRST chunk only (one ping per turn) and is budgeted so the pinged chunk still
	// fits Discord's limit. The time-gate (shouldMention) keeps a rapid answer stream
	// from pinging every turn while still pushing after a lull.
	prefix := ""
	if d.creds.ChannelID != "" && d.creds.MentionUserID != "" && d.shouldMention(m) {
		prefix = "<@" + d.creds.MentionUserID + "> "
	}
	var msgs []outMsg
	for _, c := range chunkMessage(content, prefix) {
		msgs = append(msgs, outMsg{content: c})
	}
	// P2b (docs/log/37): append interactive button messages for attention events when
	// this connection can round-trip clicks (Receive gateway + channel mode).
	if d.interactive() {
		msgs = append(msgs, d.buttonMessages(m)...)
	}
	return msgs
}

// mentionQuietWindow: inside an active thread, a read-only event (answer-ready &
// co.) skips the @mention if the bot posted to that thread within this window — you
// are likely still watching, so a rapid reply→answer exchange won't ping every turn.
// After a lull (or for any action/abnormal event) the mention returns so mobile still
// pushes. Tunable; a var so tests can shrink it.
var mentionQuietWindow = 10 * time.Minute

// shouldMention decides whether this message carries the push @mention (docs/log/37,
// user request 2026-07-22). Action/abnormal events always ping (you must act, or the
// session died); read-only events ping only when the session's thread has been quiet
// for mentionQuietWindow. Without a thread to gate on (flat channel / DM / first
// post) it falls back to the old always-mention behavior.
func (d *discordProvider) shouldMention(m Message) bool {
	if alwaysMentionKind(m.Kind) {
		return true
	}
	if d.creds.ChannelID == "" || !d.creds.Threads || m.SessionName == "" {
		return true // no thread to gate on
	}
	ref, ok := loadThreads()[m.SessionName]
	if !ok || ref.LastPostAt == "" {
		return true // first notification seeds the thread — ping it
	}
	last, err := time.Parse(time.RFC3339, ref.LastPostAt)
	if err != nil {
		return true
	}
	return time.Since(last) >= mentionQuietWindow
}

// alwaysMentionKind is the set of events that must reliably push regardless of how
// recently the thread was active: pending decisions the user has to make, and an
// abnormal exit.
func alwaysMentionKind(kind string) bool {
	switch kind {
	case "question", "plan-approval", "permission-request", "exit":
		return true
	}
	return false
}

// destChannel resolves where a flat (non-thread) message goes: the configured
// guild channel, or the (cached / lazily resolved) DM channel.
func (d *discordProvider) destChannel() (string, error) {
	if d.creds.ChannelID != "" {
		return d.creds.ChannelID, nil
	}
	if d.creds.DMChannelID != "" {
		return d.creds.DMChannelID, nil
	}
	if d.creds.UserID == "" {
		return "", fmt.Errorf("discord: no destination configured")
	}
	resolved, err := DiscordResolveDM(d.creds.Token, d.creds.UserID)
	if err != nil {
		return "", err
	}
	d.creds.DMChannelID = resolved
	if d.cacheDM != nil {
		d.cacheDM(resolved)
	}
	return resolved, nil
}

// sendThreaded posts msgs[from:] into the session's thread, creating it from the
// session's first notification when needed, and returns the cumulative delivered
// count so a partial failure resumes without duplicating (docs/log/37 duplicate prevention). Failure
// policy unchanged: the notification is never lost to thread bookkeeping — a failed
// thread creation falls back to flat delivery and the grouping retries next event.
func (d *discordProvider) sendThreaded(m Message, msgs []outMsg, from int) (int, error) {
	// Mutations go through updateThreads (load->fn->save under the lock): writing back the
	// snapshot read here would roll back a concurrent touch()'s LastPostAt.
	if ref, ok := loadThreads()[m.SessionName]; ok && ref.Channel == d.creds.ChannelID {
		delivered, err := d.postRangeToThread(m.SessionName, ref.Thread, msgs, from)
		if err == nil || !isUnknownChannel(err) {
			return delivered, err // delivered, or a real failure worth resuming as-is
		}
		// Thread deleted by hand — drop the stale mapping and recreate below. Its posts
		// vanished with it, so resume from the start (nothing to skip).
		updateThreads(func(ts threadMap) { delete(ts, m.SessionName) })
		from = 0
	}
	// No (valid) thread: the first message lands flat and seeds the thread; the rest
	// go inside it. Seeding is a fresh delivery (a resume always finds a thread in the
	// map, saved before the in-thread posts below), so start the seed from msgs[0].
	seedID, err := discordPost(d.creds.Token, d.creds.ChannelID, msgs[0])
	if err != nil {
		return 0, err
	}
	threadID, err := DiscordStartThread(d.creds.Token, d.creds.ChannelID, seedID, threadName(m))
	if err != nil {
		// Thread creation failed: deliver the rest flat too (nothing lost); the grouping
		// retries next event. Best-effort — stop at the first flat failure, but report
		// the whole entry delivered so it isn't re-seeded (which would duplicate).
		for i := 1; i < len(msgs); i++ {
			if _, e := discordPost(d.creds.Token, d.creds.ChannelID, msgs[i]); e != nil {
				break
			}
		}
		return len(msgs), nil
	}
	updateThreads(func(ts threadMap) {
		ts[m.SessionName] = threadRef{Channel: d.creds.ChannelID, Thread: threadID}
	})
	// The seed (msgs[0]) already landed in the channel; post the rest into the thread.
	return d.postRangeToThread(m.SessionName, threadID, msgs, 1)
}

// postRangeToThread posts msgs[from:] into the thread in order (unarchiving as
// needed per postToThread), stamping the mention time-gate on success. Returns the
// cumulative delivered count (len(msgs) on full success) and the error at the first
// failed post.
func (d *discordProvider) postRangeToThread(session, threadID string, msgs []outMsg, from int) (int, error) {
	for i := from; i < len(msgs); i++ {
		if err := d.postToThread(threadID, msgs[i]); err != nil {
			return i, err
		}
	}
	touchThreadPost(session, time.Now()) // feed the mention time-gate
	return len(msgs), nil
}

// MirrorUserInput echoes a prompt the user submitted from the Console into the session's
// chat thread(s), so each connected provider's thread stays a faithful two-way mirror
// (docs/log/37 Fix ②). Called async from the Console input path for genuine human prompts
// (report_to == ""); operator/MCP injections are badged elsewhere and chat-origin replies
// already show a 👀. Best-effort per provider.
func MirrorUserInput(sessionName, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	mirrorDiscordInput(sessionName, text)
	mirrorSlackInput(sessionName, text)
}

// mirrorDiscordInput is the Discord half of MirrorUserInput: gated on channel + thread mode,
// not opted out (MirrorInputOff), and an existing thread (an echo never creates one).
func mirrorDiscordInput(sessionName, text string) {
	s, err := secrets.Load()
	if err != nil || s.Discord == nil {
		return
	}
	d := s.Discord
	if d.Token == "" || d.ChannelID == "" || !d.Threads || d.MirrorInputOff {
		return
	}
	ref, ok := loadThreads()[sessionName]
	if !ok || ref.Thread == "" || ref.Channel != d.ChannelID {
		return
	}
	prov := &discordProvider{creds: *d}
	// 🧑 marks it as the human's own input, distinct from the bot's answer posts; the
	// trailing divider (docs/log/37 Fix ⑤) separates it from the answer that follows.
	for _, chunk := range chunkMessage(withDivider(renderBodyForDiscord(text)), "🧑 ") {
		if err := prov.postToThread(ref.Thread, outMsg{content: chunk}); err != nil {
			log.Printf("bridge: mirror console input to %s failed: %v", sessionName, err)
			return
		}
	}
	touchThreadPost(sessionName, time.Now())
}

// postToThread sends into a thread, transparently unarchiving it first when
// the auto-archive window (24h) has passed.
func (d *discordProvider) postToThread(threadID string, om outMsg) error {
	_, err := discordPost(d.creds.Token, threadID, om)
	if isThreadArchived(err) {
		if err = DiscordUnarchiveThread(d.creds.Token, threadID); err != nil {
			return err
		}
		_, err = discordPost(d.creds.Token, threadID, om)
	}
	return err
}

// threadName is the session's thread title (Discord caps names at 100 chars).
func threadName(m Message) string {
	name := m.DisplayName
	if name == "" {
		name = m.SessionName
	}
	return truncate(name, 90)
}

// DiscordBotName validates a bot token by fetching its own account; returns
// the bot username for the connections card. ErrUnauthorized on 401/403.
func DiscordBotName(token string) (string, error) {
	var res struct {
		Username string `json:"username"`
	}
	if err := discordDo("GET", "/users/@me", token, nil, &res); err != nil {
		return "", err
	}
	return res.Username, nil
}

// DiscordApp is the bot's application identity — the id feeds the generated
// invite URL, so the setup wizard can skip the OAuth2 URL Generator entirely.
type DiscordApp struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DiscordAppInfo fetches the application behind a bot token (works with plain
// bot auth; no OAuth2 client secret involved).
func DiscordAppInfo(token string) (DiscordApp, error) {
	var app DiscordApp
	err := discordDo("GET", "/oauth2/applications/@me", token, nil, &app)
	return app, err
}

// discordInvitePermissions = VIEW_CHANNEL(1024) + SEND_MESSAGES(2048) +
// ADD_REACTIONS(64) + CREATE_PUBLIC_THREADS(1<<34) + SEND_MESSAGES_IN_THREADS(1<<38).
// The thread bits back docs/log/37 P1.5 and ADD_REACTIONS backs the P2a inject receipt
// (👀); private guilds usually grant these via @everyone anyway, so older invites keep
// working — a denial surfaces as testError / a silently-logged best-effort skip.
const discordInvitePermissions = "292057779264"

// DiscordInviteURL is the one-click "add the bot to your private server" link
// the connections card shows after token validation.
func DiscordInviteURL(appID string) string {
	return "https://discord.com/oauth2/authorize?client_id=" + appID + "&scope=bot&permissions=" + discordInvitePermissions
}

type DiscordGuild struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DiscordGuilds lists the guilds the bot has been invited to — the wizard polls
// this to detect the invite completing. Needs no privileged intent.
func DiscordGuilds(token string) ([]DiscordGuild, error) {
	var gs []DiscordGuild
	err := discordDo("GET", "/users/@me/guilds", token, nil, &gs)
	return gs, err
}

type DiscordChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type int    `json:"type"`
}

// DiscordGuildChannels lists a guild's text channels (type 0) so the card can
// offer a picker instead of asking the user for a numeric channel id.
func DiscordGuildChannels(token, guildID string) ([]DiscordChannel, error) {
	var chs []DiscordChannel
	if err := discordDo("GET", "/guilds/"+guildID+"/channels", token, nil, &chs); err != nil {
		return nil, err
	}
	var out []DiscordChannel
	for _, c := range chs {
		if c.Type == 0 {
			out = append(out, c)
		}
	}
	return out, nil
}

// DiscordGuildOwner returns a guild's owner_id — the zero-friction way to get
// the user's own id (docs/log/37 P1.5): in the recommended setup the user created
// the private guild, so owner == user. No privileged intent, no Developer Mode.
func DiscordGuildOwner(token, guildID string) (string, error) {
	var g struct {
		OwnerID string `json:"owner_id"`
	}
	err := discordDo("GET", "/guilds/"+guildID, token, nil, &g)
	return g.OwnerID, err
}

// DiscordUserName resolves a user id to a display name (global name falling
// back to the username) so the card can show who will be mentioned.
func DiscordUserName(token, userID string) (string, error) {
	var u struct {
		Username   string `json:"username"`
		GlobalName string `json:"global_name"`
	}
	if err := discordDo("GET", "/users/"+userID, token, nil, &u); err != nil {
		return "", err
	}
	if u.GlobalName != "" {
		return u.GlobalName, nil
	}
	return u.Username, nil
}

// discordPostMessage posts a plain-text message and returns the created message
// id (the thread starter needs it). Thin wrapper over discordPost.
func discordPostMessage(token, channelID, content string) (string, error) {
	return discordPost(token, channelID, outMsg{content: content})
}

// discordPost posts a message (content and/or interactive components — P2b) to a
// channel or thread and returns the created message id.
func discordPost(token, channelID string, om outMsg) (string, error) {
	body := map[string]any{}
	if om.content != "" {
		body["content"] = om.content
	}
	if len(om.components) > 0 {
		body["components"] = om.components
	}
	var res struct {
		ID string `json:"id"`
	}
	err := discordDo("POST", "/channels/"+channelID+"/messages", token, body, &res)
	return res.ID, err
}

// DiscordStartThread starts a public thread from an existing message (the
// session's first notification). 1440 = auto-archive after 24h of silence.
func DiscordStartThread(token, channelID, messageID, name string) (string, error) {
	var res struct {
		ID string `json:"id"`
	}
	err := discordDo("POST", "/channels/"+channelID+"/messages/"+messageID+"/threads", token,
		map[string]any{"name": name, "auto_archive_duration": 1440}, &res)
	return res.ID, err
}

func DiscordUnarchiveThread(token, threadID string) error {
	return discordDo("PATCH", "/channels/"+threadID, token, map[string]any{"archived": false}, nil)
}

// receiptEmoji is the reaction the bot adds to an injected reply to signal "received,
// now working" (docs/log/37 P2a ack). Eyes read as "seen" and pair with the typing pulse.
const receiptEmoji = "👀"

// DiscordAddReaction reacts to a message as the bot (PUT .../reactions/{emoji}/@me).
// The emoji is a raw unicode glyph, percent-encoded into the path. Best-effort: a
// missing ADD_REACTIONS permission (private-guild @everyone usually grants it) just
// errors, which the caller logs — the reply was still injected.
func DiscordAddReaction(token, channelID, messageID, emoji string) error {
	return discordDo("PUT", "/channels/"+channelID+"/messages/"+messageID+
		"/reactions/"+url.PathEscape(emoji)+"/@me", token, nil, nil)
}

// DiscordTriggerTyping fires the typing indicator in a channel/thread (~10s, auto-
// cleared when the next message posts). Used as the liveliness half of the inject ack.
func DiscordTriggerTyping(token, channelID string) error {
	return discordDo("POST", "/channels/"+channelID+"/typing", token, map[string]any{}, nil)
}

// interactionDeferUpdate is Discord's DEFERRED_UPDATE_MESSAGE callback type (6):
// it ACKs a component interaction with no visible loading state, so the answer
// can be applied and the message edited afterwards without racing the 3s deadline.
const interactionDeferUpdate = 6

// DiscordAckInteraction acknowledges a component interaction (P2b) so Discord
// doesn't show "interaction failed". Must be sent within 3s of the click; the
// message is edited separately once the answer is applied.
func DiscordAckInteraction(token, interactionID, interactionToken string) error {
	return discordDo("POST", "/interactions/"+interactionID+"/"+interactionToken+"/callback", token,
		map[string]any{"type": interactionDeferUpdate}, nil)
}

// DiscordEditMessage patches a message's content and components (P2b: after a
// button click, replace the prompt with the outcome and clear the buttons so the
// same decision can't be re-submitted). Pass an empty (non-nil) components slice
// to remove the buttons.
func DiscordEditMessage(token, channelID, messageID, content string, components []any) error {
	if components == nil {
		components = []any{}
	}
	return discordDo("PATCH", "/channels/"+channelID+"/messages/"+messageID, token,
		map[string]any{"content": content, "components": components}, nil)
}

// DiscordResolveDM opens (or returns the existing) DM channel with the bound
// user — the REST shape of "DM this person" (docs/log/37 contract 5: the bot only ever
// initiates toward the explicitly bound user id).
func DiscordResolveDM(token, userID string) (string, error) {
	var res struct {
		ID string `json:"id"`
	}
	err := discordDo("POST", "/users/@me/channels", token, map[string]any{"recipient_id": userID}, &res)
	if err != nil {
		return "", err
	}
	if res.ID == "" {
		return "", fmt.Errorf("discord: DM channel resolve returned no id")
	}
	return res.ID, nil
}

// discordRateRetries / discordRetryCap bound the inline retry on a 429. Handling the
// rate limit HERE (rather than failing the post and re-sending the whole queue entry)
// is the primary fix for the duplicate storm (docs/log/37 duplicate prevention): a burst of posts
// from a long full-text answer routinely trips Discord's per-channel limit, and a
// mid-batch failure used to re-post the delivered chunks. Bounded so a stuck 429
// can't hang the single sender goroutine. Vars so tests can shrink them.
var (
	discordRateRetries = 3
	discordRetryCap    = 5 * time.Second
)

// discordDo is the one REST call shape we need: JSON in/out, bot-token auth, short
// timeout (the sender loop is single-goroutine — a hung call must not stall the queue
// for long), with a bounded inline retry that respects Discord's 429 Retry-After.
func discordDo(method, path, token string, body any, out any) error {
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
		req, err := http.NewRequest(method, discordAPIBase+path, rdr)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bot "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests && attempt < discordRateRetries {
			wait := discordRetryAfter(resp, b)
			log.Printf("bridge: discord 429 on %s %s — retrying in %s", method, path, wait)
			time.Sleep(wait)
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w (%d)", ErrUnauthorized, resp.StatusCode)
		}
		if resp.StatusCode >= 300 {
			var de struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(b, &de)
			msg := de.Message
			if msg == "" {
				msg = truncate(string(b), 200)
			}
			return &discordAPIError{Status: resp.StatusCode, Code: de.Code,
				Msg: fmt.Sprintf("%s %s: %s", method, path, msg)}
		}
		if out != nil {
			if err := json.Unmarshal(b, out); err != nil {
				return fmt.Errorf("discord: decode %s %s: %w", method, path, err)
			}
		}
		return nil
	}
}

// discordRetryAfter reads the retry delay from a 429: the JSON body's retry_after
// (seconds, float) preferred, then the Retry-After header, clamped to discordRetryCap
// (and a small floor so a 0 doesn't busy-loop).
func discordRetryAfter(resp *http.Response, body []byte) time.Duration {
	var j struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(body, &j) == nil && j.RetryAfter > 0 {
		return clampRetry(time.Duration(j.RetryAfter * float64(time.Second)))
	}
	if h := resp.Header.Get("Retry-After"); h != "" {
		if secs, err := strconv.ParseFloat(h, 64); err == nil && secs > 0 {
			return clampRetry(time.Duration(secs * float64(time.Second)))
		}
	}
	return 500 * time.Millisecond
}

func clampRetry(d time.Duration) time.Duration {
	if d < 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	if d > discordRetryCap {
		return discordRetryCap
	}
	return d
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
