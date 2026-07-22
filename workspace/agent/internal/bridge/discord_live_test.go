package bridge

import (
	"os"
	"testing"

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
}
