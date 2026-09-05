package bridge

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// TestSlackLiveSend is the live contract test of docs/log/37's verification approach for
// Slack (mirrors
// TestDiscordLiveSend): it talks to the real Slack Web API and posts. Skipped unless
// AF_SLACK_LIVE=1; credentials come from env (never committed):
//
//	AF_SLACK_LIVE=1 AF_SLACK_BOT_TOKEN=<xoxb-…> \
//	  AF_SLACK_CHANNEL=<C…> (or AF_SLACK_USER=<U…> for a DM) \
//	  [AF_SLACK_BUTTONS=1] [AF_SLACK_FULLTEXT=1] \
//	  go test ./internal/bridge -run TestSlackLiveSend -v
func TestSlackLiveSend(t *testing.T) {
	if os.Getenv("AF_SLACK_LIVE") != "1" {
		t.Skip("AF_SLACK_LIVE != 1")
	}
	t.Setenv("HOME", t.TempDir()) // keep the thread store off the real config dir
	bot := os.Getenv("AF_SLACK_BOT_TOKEN")
	if bot == "" {
		t.Fatal("AF_SLACK_LIVE=1 but AF_SLACK_BOT_TOKEN is empty")
	}
	auth, err := SlackAuthTest(bot)
	if err != nil {
		t.Fatalf("auth.test: %v", err)
	}
	t.Logf("bot: %s @ %s (user %s)", auth.BotName, auth.Team, auth.BotUserID)

	channel, user := os.Getenv("AF_SLACK_CHANNEL"), os.Getenv("AF_SLACK_USER")
	if channel == "" && user == "" {
		t.Fatal("set AF_SLACK_CHANNEL or AF_SLACK_USER")
	}
	p := &slackProvider{creds: secrets.SlackCreds{BotToken: bot, ChannelID: channel, UserID: user}}
	if err := p.Send(Message{Kind: "answer-ready", DisplayName: "AF_SLACK_LIVE contract test", SessionKind: "claude"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if channel != "" {
		// Thread grouping: two events for one session must land in a single thread.
		tp := &slackProvider{creds: secrets.SlackCreds{BotToken: bot, ChannelID: channel, UserID: user,
			Threads: true, Receive: os.Getenv("AF_SLACK_BUTTONS") == "1", FullText: os.Getenv("AF_SLACK_FULLTEXT") == "1"}}
		for _, kind := range []string{"answer-ready", "session-report"} {
			m := Message{Kind: kind, SessionName: "af-live-thread", DisplayName: "AF live thread test", SessionKind: "claude"}
			if kind == "answer-ready" && tp.creds.FullText {
				m.Body = "## 見出し\n**太字** 本文\n\n| A | B |\n|---|---|\n| 1 | 2 |"
			}
			if err := tp.Send(m); err != nil {
				t.Fatalf("thread %s: %v", kind, err)
			}
		}
		if os.Getenv("AF_SLACK_BUTTONS") == "1" {
			if err := tp.Send(Message{Kind: "permission-request", SessionName: "af-live-thread",
				DisplayName: "AF live buttons", SessionKind: "claude"}); err != nil {
				t.Fatalf("buttons: %v", err)
			}
		}
	}
}

// TestSlackLiveSocket opens a real Socket Mode connection and waits briefly for the hello /
// first frames. Skipped unless AF_SLACK_SOCKET=1 (needs the app-level token). Purely a smoke
// test that the WSS handshake + envelope loop work against the live endpoint.
func TestSlackLiveSocket(t *testing.T) {
	if os.Getenv("AF_SLACK_SOCKET") != "1" {
		t.Skip("AF_SLACK_SOCKET != 1")
	}
	app := os.Getenv("AF_SLACK_APP_TOKEN")
	if app == "" {
		t.Fatal("AF_SLACK_SOCKET=1 but AF_SLACK_APP_TOKEN is empty")
	}
	var events, interactions int
	ss := &slackSocket{
		appToken:   app,
		onEvent:    func(slackInboundMsg) { events++ },
		onInteract: func(slackInboundInteraction) { interactions++ },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := ss.connectOnce(ctx)
	t.Logf("socket closed after %v: events=%d interactions=%d err=%v", 30*time.Second, events, interactions, err)
}
