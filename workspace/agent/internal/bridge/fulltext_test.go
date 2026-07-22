package bridge

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func TestScrubSecrets(t *testing.T) {
	cases := []struct {
		name         string
		in           string
		wantRedacted []string // substrings that must NOT survive
		wantKept     []string // substrings that MUST survive (no over-redaction)
	}{
		{
			name:         "slack token",
			in:           "set the token to xoxb-123456789012-AbCdEfGhIjKlMn and retry",
			wantRedacted: []string{"xoxb-123456789012"},
			wantKept:     []string{"set the token to", "and retry"},
		},
		{
			name:         "github token",
			in:           "push with ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 done",
			wantRedacted: []string{"ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ"},
		},
		{
			name:         "aws access key id",
			in:           "key AKIAIOSFODNN7EXAMPLE is set",
			wantRedacted: []string{"AKIAIOSFODNN7EXAMPLE"},
			wantKept:     []string{"is set"},
		},
		{
			name:         "uppercase env assignment keeps name drops value",
			in:           "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY next line",
			wantRedacted: []string{"wJalrXUtnFEMI"},
			wantKept:     []string{"AWS_SECRET_ACCESS_KEY=", "next line"},
		},
		{
			name:         "short password assignment",
			in:           "PASSWORD=hunter2",
			wantRedacted: []string{"hunter2"},
			wantKept:     []string{"PASSWORD="},
		},
		{
			name:         "high-entropy standalone token",
			in:           "opaque aB3xK9mP2qR7sT1vW5yZ8nQ4 here",
			wantRedacted: []string{"aB3xK9mP2qR7sT1vW5yZ8nQ4"},
			wantKept:     []string{"opaque", "here"},
		},
		{
			name:     "ordinary prose untouched",
			in:       "The quick brown fox jumps over the lazy dog and reviews the pull request.",
			wantKept: []string{"The quick brown fox", "pull request"},
		},
		{
			name:     "long lowercase word without digit is not a secret",
			in:       "internationalization and counterrevolutionary are long words",
			wantKept: []string{"internationalization", "counterrevolutionary"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := ScrubSecrets(tc.in)
			for _, r := range tc.wantRedacted {
				if strings.Contains(out, r) {
					t.Errorf("secret survived: %q still contains %q", out, r)
				}
			}
			for _, k := range tc.wantKept {
				if !strings.Contains(out, k) {
					t.Errorf("over-redacted: %q dropped %q", out, k)
				}
			}
		})
	}
}

// TestTablesToCodeBlocks: a Markdown table (which Discord doesn't render) is wrapped
// in a code fence so its columns survive; surrounding prose and non-tables are untouched.
func TestTablesToCodeBlocks(t *testing.T) {
	in := "Here are the results:\n" +
		"| Name | Score |\n" +
		"| --- | --- |\n" +
		"| foo | 12 |\n" +
		"| bar | 7 |\n" +
		"\nThat's all."
	out := tablesToCodeBlocks(in)
	if strings.Count(out, "```") != 2 {
		t.Fatalf("table should be wrapped in one code fence pair:\n%s", out)
	}
	if !strings.Contains(out, "Here are the results:") || !strings.Contains(out, "That's all.") {
		t.Fatalf("surrounding prose dropped:\n%s", out)
	}
	if !strings.Contains(out, "| Name | Score |") || !strings.Contains(out, "| bar | 7 |") {
		t.Fatalf("table rows lost:\n%s", out)
	}
	// A bare horizontal rule is NOT a table (no header pipe) → untouched.
	if got := tablesToCodeBlocks("intro\n\n---\n\noutro"); strings.Contains(got, "```") {
		t.Fatalf("bare hr must not be treated as a table: %q", got)
	}
	// No pipes at all → returned as-is.
	if got := tablesToCodeBlocks("just prose"); got != "just prose" {
		t.Fatalf("plain prose changed: %q", got)
	}
}

func TestChunkMessageSingleChunk(t *testing.T) {
	got := chunkMessage("hello world", "")
	if len(got) != 1 || got[0] != "hello world" {
		t.Fatalf("chunks=%v", got)
	}
	// A short message with a mention prefix stays one chunk, prefix prepended.
	got = chunkMessage("hi", "<@9> ")
	if len(got) != 1 || got[0] != "<@9> hi" {
		t.Fatalf("prefixed chunks=%v", got)
	}
}

func TestChunkMessageSplitsAtLimit(t *testing.T) {
	// Two "paragraphs" each near the limit force a split on the blank line.
	para := strings.Repeat("x", discordContentLimit-50)
	content := para + "\n\n" + para
	chunks := chunkMessage(content, "<@owner> ")
	if len(chunks) < 2 {
		t.Fatalf("want >=2 chunks, got %d", len(chunks))
	}
	if !strings.HasPrefix(chunks[0], "<@owner> ") {
		t.Fatalf("first chunk must carry the mention: %q", chunks[0][:20])
	}
	for i, c := range chunks {
		if n := len([]rune(c)); n > discordContentLimit {
			t.Fatalf("chunk %d has %d runes, over the %d limit", i, n, discordContentLimit)
		}
		if i > 0 && strings.Contains(c, "<@owner>") {
			t.Fatalf("mention leaked into chunk %d", i)
		}
	}
}

func TestChunkMessageBoundsOverflow(t *testing.T) {
	// Far more than maxBodyChunks worth of content: bounded + ellipsis-marked.
	content := strings.Repeat("word ", discordContentLimit*maxBodyChunks)
	chunks := chunkMessage(content, "")
	if len(chunks) != maxBodyChunks {
		t.Fatalf("chunk count=%d, want capped at %d", len(chunks), maxBodyChunks)
	}
	if !strings.HasSuffix(chunks[len(chunks)-1], "…") {
		t.Fatalf("overflow must be ellipsis-marked: %q", chunks[len(chunks)-1])
	}
}

func TestSendFullTextAppendsScrubbedBody(t *testing.T) {
	srv, sent := fakeDiscord(t, "tok")
	old := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() { discordAPIBase = old })

	body := "Done. Result summary here. leftover token xoxb-123456789012-AbCdEfGhIjKlMn"
	p := &discordProvider{creds: secrets.DiscordCreds{Token: "tok", ChannelID: "42", FullText: true}}
	if err := p.Send(Message{Kind: "answer-ready", DisplayName: "Proj", SessionKind: "claude", Body: body}); err != nil {
		t.Fatal(err)
	}
	got := *sent
	if len(got) != 1 {
		t.Fatalf("sent=%v", got)
	}
	if !strings.Contains(got[0]["content"], "Result summary here") {
		t.Fatalf("full-text body missing: %q", got[0]["content"])
	}
	if strings.Contains(got[0]["content"], "xoxb-123456789012") {
		t.Fatalf("secret not scrubbed from body: %q", got[0]["content"])
	}
}

func TestSendWithoutFullTextOmitsBody(t *testing.T) {
	srv, sent := fakeDiscord(t, "tok")
	old := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() { discordAPIBase = old })

	p := &discordProvider{creds: secrets.DiscordCreds{Token: "tok", ChannelID: "42"}} // FullText off
	if err := p.Send(Message{Kind: "answer-ready", DisplayName: "Proj", Body: "SECRET BODY TEXT"}); err != nil {
		t.Fatal(err)
	}
	if got := *sent; len(got) != 1 || strings.Contains(got[0]["content"], "SECRET BODY TEXT") {
		t.Fatalf("body must not appear when full-text is off: %v", got)
	}
}

func TestSendFullTextChunksLongBody(t *testing.T) {
	srv, sent := fakeDiscord(t, "tok")
	old := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() { discordAPIBase = old })

	// A body well over one message; mention pings only the first chunk.
	body := strings.Repeat("alpha beta gamma delta ", 400) // ~9k chars
	p := &discordProvider{creds: secrets.DiscordCreds{Token: "tok", ChannelID: "42", MentionUserID: "owner9", FullText: true}}
	if err := p.Send(Message{Kind: "answer-ready", DisplayName: "Proj", Body: body}); err != nil {
		t.Fatal(err)
	}
	got := *sent
	if len(got) < 2 {
		t.Fatalf("long body should span multiple messages, got %d", len(got))
	}
	if !strings.HasPrefix(got[0]["content"], "<@owner9> ") {
		t.Fatalf("first message must ping: %q", got[0]["content"][:20])
	}
	for i, m := range got {
		if n := len([]rune(m["content"])); n > 2000 {
			t.Fatalf("message %d exceeds Discord's 2000-char limit (%d)", i, n)
		}
		if i > 0 && strings.Contains(m["content"], "<@owner9>") {
			t.Fatalf("mention leaked into message %d", i)
		}
	}
}
