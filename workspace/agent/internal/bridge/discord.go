package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// discordAPIBase is a var so the contract tests point it at a local httptest
// server; the live test (AF_DISCORD_LIVE=1) keeps the real endpoint.
var discordAPIBase = "https://discord.com/api/v10"

// ErrUnauthorized marks a credential Discord rejected (401/403) — the
// connections handler turns it into a 400 for the card, while a network error
// is tolerated (offline-friendly: outbound may be restricted, docs/37).
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

// discordProvider is the P1 send-only Discord implementation (docs/37 契約1:
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
// any daemon-side registry (P1: Discord only; Slack joins here).
func Providers(s *secrets.Data, cacheDiscordDM func(channelID string)) []Provider {
	var out []Provider
	if d := s.Discord; d != nil && d.Token != "" {
		out = append(out, &discordProvider{creds: *d, cacheDM: cacheDiscordDM})
	}
	return out
}

func (d *discordProvider) Name() string { return "discord" }

func (d *discordProvider) Caps() Caps { return Caps{CanSend: true} }

func (d *discordProvider) Wants(eventKey string) bool {
	return EventEnabled(d.creds.Events, eventKey)
}

func (d *discordProvider) Send(m Message) error {
	ch, err := d.destChannel()
	if err != nil {
		return err
	}
	content := m.Text()
	// Mention makes mobile push deterministic: Discord's default notification
	// level for guild channels AND threads is "only @mentions", so an unpinged
	// notification silently becomes badge-only (docs/37 P1.5). DM mode needs none.
	if d.creds.ChannelID != "" && d.creds.MentionUserID != "" {
		content = "<@" + d.creds.MentionUserID + "> " + content
	}
	// Thread-per-session (docs/37 P1.5): guild channel destination only —
	// threads don't exist in DMs — and only for session-scoped events.
	if d.creds.ChannelID != "" && d.creds.Threads && m.SessionName != "" {
		return d.sendThreaded(m, content)
	}
	_, err = discordPostMessage(d.creds.Token, ch, content)
	return err
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

// sendThreaded posts into the session's thread, creating it from the session's
// first notification when needed. Failure policy: the notification itself must
// never be lost to thread bookkeeping — if the thread can't be created the
// message has already landed flat in the channel and we simply try again on
// the session's next event.
func (d *discordProvider) sendThreaded(m Message, content string) error {
	ts := loadThreads()
	if ref, ok := ts[m.SessionName]; ok && ref.Channel == d.creds.ChannelID {
		err := d.postToThread(ref.Thread, content)
		if !isUnknownChannel(err) {
			return err // delivered, or a real failure worth retrying as-is
		}
		// Thread deleted by hand — drop the stale mapping and start fresh.
		delete(ts, m.SessionName)
		saveThreads(ts)
	}
	msgID, err := discordPostMessage(d.creds.Token, d.creds.ChannelID, content)
	if err != nil {
		return err
	}
	threadID, err := DiscordStartThread(d.creds.Token, d.creds.ChannelID, msgID, threadName(m))
	if err != nil {
		return nil // message delivered flat; thread grouping retries next event
	}
	ts[m.SessionName] = threadRef{Channel: d.creds.ChannelID, Thread: threadID}
	saveThreads(ts)
	return nil
}

// postToThread sends into a thread, transparently unarchiving it first when
// the auto-archive window (24h) has passed.
func (d *discordProvider) postToThread(threadID, content string) error {
	_, err := discordPostMessage(d.creds.Token, threadID, content)
	if isThreadArchived(err) {
		if err = DiscordUnarchiveThread(d.creds.Token, threadID); err != nil {
			return err
		}
		_, err = discordPostMessage(d.creds.Token, threadID, content)
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
// CREATE_PUBLIC_THREADS(1<<34) + SEND_MESSAGES_IN_THREADS(1<<38). The thread
// bits back docs/37 P1.5; private guilds usually grant them via @everyone
// anyway, so older invites keep working — a denial surfaces as testError.
const discordInvitePermissions = "292057779200"

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
// the user's own id (docs/37 P1.5): in the recommended setup the user created
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

// discordPostMessage posts to a channel (or thread — threads are channels) and
// returns the created message id (the thread starter needs it).
func discordPostMessage(token, channelID, content string) (string, error) {
	var res struct {
		ID string `json:"id"`
	}
	err := discordDo("POST", "/channels/"+channelID+"/messages", token, map[string]any{"content": content}, &res)
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

// DiscordResolveDM opens (or returns the existing) DM channel with the bound
// user — the REST shape of "DM this person" (docs/37 契約5: the bot only ever
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

// discordDo is the one REST call shape we need: JSON in/out, bot-token auth,
// short timeout (the sender loop is single-goroutine — a hung call must not
// stall the queue for long).
func discordDo(method, path, token string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
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
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
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

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
