package bridge

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// TestDiscordLiveSend is the live contract test of docs/37 検証方針: it talks to
// the real Discord API and posts one message. Skipped unless AF_DISCORD_LIVE=1;
// credentials come from env (never committed):
//
//	AF_DISCORD_LIVE=1 AF_DISCORD_TOKEN=<bot token> \
//	  AF_DISCORD_CHANNEL=<channel id> (or AF_DISCORD_USER=<user id> for a DM) \
//	  go test ./internal/bridge -run TestDiscordLiveSend -v
func TestDiscordLiveSend(t *testing.T) {
	if os.Getenv("AF_DISCORD_LIVE") != "1" {
		t.Skip("AF_DISCORD_LIVE != 1")
	}
	t.Setenv("HOME", t.TempDir()) // keep the thread store off the real config dir
	token := os.Getenv("AF_DISCORD_TOKEN")
	if token == "" {
		t.Fatal("AF_DISCORD_LIVE=1 but AF_DISCORD_TOKEN is empty")
	}
	name, err := DiscordBotName(token)
	if err != nil {
		t.Fatalf("users/@me: %v", err)
	}
	t.Logf("bot: %s", name)
	p := &discordProvider{creds: secrets.DiscordCreds{Token: token,
		ChannelID: os.Getenv("AF_DISCORD_CHANNEL"), UserID: os.Getenv("AF_DISCORD_USER")}}
	if p.creds.ChannelID == "" && p.creds.UserID == "" {
		t.Fatal("set AF_DISCORD_CHANNEL or AF_DISCORD_USER")
	}
	err = p.Send(Message{Kind: "answer-ready", DisplayName: "AF_DISCORD_LIVE contract test",
		SessionKind: "claude"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	// Thread grouping (docs/37 P1.5): two events for one session must land in a
	// single thread under the channel. Verify visually on the Discord side.
	if p.creds.ChannelID != "" {
		tp := &discordProvider{creds: secrets.DiscordCreds{Token: token,
			ChannelID: p.creds.ChannelID, Threads: true,
			MentionUserID: os.Getenv("AF_DISCORD_MENTION")}}
		for _, kind := range []string{"answer-ready", "session-report"} {
			if err := tp.Send(Message{Kind: kind, SessionName: "af-live-thread",
				DisplayName: "AF live thread test", SessionKind: "claude"}); err != nil {
				t.Fatalf("thread %s: %v", kind, err)
			}
		}
	}
}

// TestDiscordLiveReceive is the live RECEIVE smoke test (docs/37 P2a): it opens a REAL
// Discord Gateway connection and logs every message the bot sees for ~40s, so the receive
// path can be verified in isolation — before the full session round-trip. Post a message in
// your server (and in a session thread) while it runs and confirm it arrives WITH content;
// an empty content line means the MESSAGE_CONTENT privileged intent isn't actually enabled.
// Token/bound-user come from env, falling back to the stored Discord connection:
//
//	AF_DISCORD_LIVE=1 AF_DISCORD_GATEWAY=1 [AF_DISCORD_TOKEN=<bot token>] \
//	  [AF_DISCORD_USER=<your user id>] \
//	  go test ./internal/bridge -run TestDiscordLiveReceive -v -timeout 90s
func TestDiscordLiveReceive(t *testing.T) {
	if os.Getenv("AF_DISCORD_LIVE") != "1" || os.Getenv("AF_DISCORD_GATEWAY") != "1" {
		t.Skip("AF_DISCORD_LIVE != 1 or AF_DISCORD_GATEWAY != 1")
	}
	token := os.Getenv("AF_DISCORD_TOKEN")
	bound := os.Getenv("AF_DISCORD_USER")
	if token == "" { // fall back to the configured connection (no need to paste the token)
		if s, err := secrets.Load(); err == nil && s.Discord != nil {
			token = s.Discord.Token
			if bound == "" {
				if bound = s.Discord.MentionUserID; bound == "" {
					bound = s.Discord.UserID
				}
			}
		}
	}
	if token == "" {
		t.Fatal("no token: set AF_DISCORD_TOKEN or configure the Discord connection")
	}
	name, err := DiscordBotName(token)
	if err != nil {
		t.Fatalf("users/@me: %v", err)
	}
	t.Logf("bot %q connecting to the gateway — post messages in your server for ~40s (bound user=%q)…", name, bound)

	var count int
	gw := &gateway{token: token, onMsg: func(m gatewayMessage) {
		count++
		tag := ""
		if bound != "" && m.Author.ID == bound {
			tag = " [BOUND USER ✓ would route]"
		}
		content := m.Content
		if content == "" {
			content = "<EMPTY — MESSAGE_CONTENT not enabled?>"
		}
		t.Logf("MESSAGE_CREATE author=%s bot=%v channel=%s%s content=%q",
			m.Author.ID, m.Author.Bot, m.ChannelID, tag, content)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	err = gw.connectOnce(ctx)
	t.Logf("gateway closed after %d message(s): %v", count, err)
	if errors.Is(err, errDisallowedIntent) {
		t.Fatal("Discord rejected the intents (close 4014) — enable MESSAGE_CONTENT in the Developer Portal")
	}
}
