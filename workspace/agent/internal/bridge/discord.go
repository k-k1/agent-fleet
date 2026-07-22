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
	ch := d.creds.ChannelID
	if ch == "" {
		ch = d.creds.DMChannelID
	}
	if ch == "" {
		if d.creds.UserID == "" {
			return fmt.Errorf("discord: no destination configured")
		}
		resolved, err := DiscordResolveDM(d.creds.Token, d.creds.UserID)
		if err != nil {
			return err
		}
		ch = resolved
		d.creds.DMChannelID = resolved
		if d.cacheDM != nil {
			d.cacheDM(resolved)
		}
	}
	body := map[string]any{"content": m.Text()}
	return discordDo("POST", "/channels/"+ch+"/messages", d.creds.Token, body, nil)
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

// discordInvitePermissions = VIEW_CHANNEL(1024) + SEND_MESSAGES(2048). Send
// alone is not enough when a channel doesn't inherit view for @everyone.
const discordInvitePermissions = "3072"

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
		return fmt.Errorf("discord: %s %s: %d: %s", method, path, resp.StatusCode, truncate(string(b), 200))
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
