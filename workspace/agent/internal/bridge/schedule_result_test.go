package bridge

// #1560: a scheduled run's result is posted to the one connection its schedule named, whatever
// that connection's event toggles say, and never to a connection that is not bound to its member.

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// namedProvider is flakyProvider under another name.
type namedProvider struct {
	flakyProvider
	name string
}

func (n *namedProvider) Name() string { return n.name }

func TestDrainSendsATargetedMessageToItsTargetOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	EnqueueTo("discord", Message{Kind: KindScheduleResult, DisplayName: "nightly", Body: "2 jobs failed"})
	// A notification for everyone rides beside it, unaffected.
	Enqueue(Message{Kind: "answer-ready", DisplayName: "A"})
	// discord has muted every event group but answer-ready; slack takes everything.
	d := &namedProvider{flakyProvider{events: []string{"answer-ready"}}, "discord"}
	s := &namedProvider{flakyProvider{}, "slack"}
	drainWith([]Provider{d, s})
	if len(d.got) != 2 || d.got[0].Kind != KindScheduleResult || d.got[0].Body != "2 jobs failed" {
		t.Fatalf("discord got %+v, want the schedule result and the answer-ready", d.got)
	}
	if len(s.got) != 1 || s.got[0].Kind != "answer-ready" {
		t.Fatalf("slack got %+v, want only the answer-ready", s.got)
	}
	if n := len(queueFiles(queueDir())); n != 0 {
		t.Fatalf("queue=%d", n)
	}
}

// The schedule-result kind belongs to no event group, so a plain Enqueue never queues it: only a
// schedule that named a connection can send one.
func TestScheduleResultIsNotBroadcast(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Enqueue(Message{Kind: KindScheduleResult, Body: "x"})
	if n := len(queueFiles(queueDir())); n != 0 {
		t.Fatalf("a schedule result was queued for every connection")
	}
}

func TestTargetReadyRequiresABoundConnection(t *testing.T) {
	for name, tc := range map[string]struct {
		data   secrets.Data
		target string
		want   bool
	}{
		"discord bound by mention":    {secrets.Data{Discord: &secrets.DiscordCreds{Token: "t", ChannelID: "c", MentionUserID: "u"}}, "discord", true},
		"discord DM":                  {secrets.Data{Discord: &secrets.DiscordCreds{Token: "t", UserID: "u"}}, "discord", true},
		"discord channel, unbound":    {secrets.Data{Discord: &secrets.DiscordCreds{Token: "t", ChannelID: "c"}}, "discord", false},
		"discord muted":               {secrets.Data{Discord: &secrets.DiscordCreds{Token: "t", UserID: "u", NotifyOff: true}}, "discord", false},
		"discord not connected":       {secrets.Data{}, "discord", false},
		"slack bound":                 {secrets.Data{Slack: &secrets.SlackCreds{BotToken: "b", ChannelID: "c", UserID: "u"}}, "slack", true},
		"slack channel, unbound":      {secrets.Data{Slack: &secrets.SlackCreds{BotToken: "b", ChannelID: "c"}}, "slack", false},
		"discord does not mean slack": {secrets.Data{Discord: &secrets.DiscordCreds{Token: "t", UserID: "u"}}, "slack", false},
		"unknown target":              {secrets.Data{Discord: &secrets.DiscordCreds{Token: "t", UserID: "u"}}, "webhook", false},
	} {
		if got := targetReady(&tc.data, tc.target); got != tc.want {
			t.Errorf("%s: targetReady = %v, want %v", name, got, tc.want)
		}
	}
}

// The post keeps its headline above the answer even when the connection is not in full-text
// mode, and a failure says so.
func TestScheduleResultMessages(t *testing.T) {
	d := &discordProvider{creds: secrets.DiscordCreds{Token: "t", UserID: "u", Lang: "en"}}
	msgs := d.buildMessages(Message{Kind: KindScheduleResult, DisplayName: "nightly", Body: "2 jobs failed"})
	if len(msgs) == 0 || !strings.HasPrefix(msgs[0].content, "Scheduled run result") || !strings.Contains(msgs[0].content, "2 jobs failed") {
		t.Fatalf("discord = %+v", msgs)
	}
	sp := &slackProvider{creds: secrets.SlackCreds{BotToken: "b", UserID: "u"}}
	sm := sp.buildSlackMessages(Message{Kind: KindScheduleResult, DisplayName: "nightly", Detail: "oom"})
	if len(sm) == 0 || !strings.HasPrefix(sm[0].text, "定時実行が正常に終わりませんでした（oom）") {
		t.Fatalf("slack = %+v", sm)
	}
}
