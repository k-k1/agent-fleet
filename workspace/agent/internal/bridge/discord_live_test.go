package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// TestDiscordLiveSend is the live contract test of docs/log/37 §verification policy: it talks to
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
	// Thread grouping (docs/log/37 P1.5): two events for one session must land in a
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
		// P2b (docs/log/37): with AF_DISCORD_BUTTONS=1, post a question with option
		// buttons plus a permission prompt so the rendering can be eyeballed and
		// clicked (the click round-trips through TestDiscordLiveReceive).
		if os.Getenv("AF_DISCORD_BUTTONS") == "1" {
			bp := &discordProvider{creds: secrets.DiscordCreds{Token: token,
				ChannelID: p.creds.ChannelID, Threads: true, Receive: true,
				MentionUserID: os.Getenv("AF_DISCORD_MENTION")}}
			raw := json.RawMessage(`[{"header":"Env","question":"どの環境にデプロイしますか？","options":[{"label":"dev"},{"label":"staging"},{"label":"prod"}]}]`)
			if err := bp.Send(Message{Kind: "question", SessionName: "af-live-buttons",
				DisplayName: "AF live buttons test", SessionKind: "claude", Questions: raw}); err != nil {
				t.Fatalf("question buttons send: %v", err)
			}
			if err := bp.Send(Message{Kind: "permission-request", SessionName: "af-live-buttons",
				DisplayName: "AF live buttons test", SessionKind: "claude"}); err != nil {
				t.Fatalf("permission buttons send: %v", err)
			}
		}
		// Full-text bridge (docs/log/37, the future direction): with AF_DISCORD_FULLTEXT=1, post an
		// answer-ready whose body exceeds one message so chunking + the scrubber
		// can be eyeballed in the thread (mention on the first chunk only).
		if os.Getenv("AF_DISCORD_FULLTEXT") == "1" {
			ftp := &discordProvider{creds: secrets.DiscordCreds{Token: token,
				ChannelID: p.creds.ChannelID, Threads: true, FullText: true,
				MentionUserID: os.Getenv("AF_DISCORD_MENTION")}}
			body := "全文ブリッジのライブ確認です。以下は分割の確認用に長くしています。\n\n" +
				strings.Repeat("あいうえお かきくけこ さしすせそ たちつてと なにぬねの ", 120) +
				"\n\nダミーの鍵: xoxb-123456789012-AbCdEfGhIjKlMn（伏字化されるはず）"
			if err := ftp.Send(Message{Kind: "answer-ready", SessionName: "af-live-fulltext",
				DisplayName: "AF live full-text test", SessionKind: "claude", Body: body}); err != nil {
				t.Fatalf("full-text send: %v", err)
			}
		}

		// P3, brought forward (docs/log/37): with AF_DISCORD_OPERATOR=1, open the standing operator
		// thread and post a reply into it, so the create-thread + return-leg (chunk +
		// scrub) can be eyeballed. Uses a throwaway conv id — no turn is run here.
		if os.Getenv("AF_DISCORD_OPERATOR") == "1" {
			thread, err := CreateOperatorThread(token, p.creds.ChannelID, operatorLiveName(), "🛰 operator thread (live contract test)")
			if err != nil {
				t.Fatalf("create operator thread: %v", err)
			}
			SaveOperatorState(p.creds.ChannelID, thread, "af-live-operator-conv")
			t.Cleanup(ResetOperatorThread)
			if err := postOperatorChunks(token, thread, "オペレーターの応答（ライブ確認）: フリートは正常です。"); err != nil {
				t.Fatalf("operator reply post: %v", err)
			}
			// P3 approval gate (docs/log/37): with AF_DISCORD_APPROVAL=1, post an approve/reject
			// button into the operator thread so the gate's buttons render and a click
			// round-trips through TestDiscordLiveReceive (which logs the parsed "op" id).
			if os.Getenv("AF_DISCORD_APPROVAL") == "1" {
				body := ScrubSecrets("🔒 承認が必要な操作\n**ブランチを削除** — myrepo / temp/foo\n実行してよろしいですか？")
				if _, err := discordPost(token, thread, outMsg{content: body, components: approvalRow("live-appr-1", false)}); err != nil {
					t.Fatalf("approval buttons post: %v", err)
				}
			}
		}
	}
}

// operatorLiveName mirrors the JA thread name package main uses (kept local so the
// bridge package's live test doesn't reach into main).
func operatorLiveName() string { return "🛰 フリート・オペレーター" }

// TestDiscordLiveReceive is the live RECEIVE smoke test (docs/log/37 P2a): it opens a REAL
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
	}, onInteract: func(gi gatewayInteraction) {
		// P2b: log button clicks so a live tester can confirm the round-trip.
		count++
		pi, _ := ParseCustomID(gi.Data.CustomID)
		tag := ""
		if bound != "" && gi.authorID() == bound {
			tag = " [BOUND USER ✓ would answer]"
		}
		t.Logf("INTERACTION_CREATE author=%s%s custom_id=%q parsed=%+v",
			gi.authorID(), tag, gi.Data.CustomID, pi)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	err = gw.connectOnce(ctx)
	t.Logf("gateway closed after %d message(s): %v", count, err)
	if errors.Is(err, errDisallowedIntent) {
		t.Fatal("Discord rejected the intents (close 4014) — enable MESSAGE_CONTENT in the Developer Portal")
	}
}
