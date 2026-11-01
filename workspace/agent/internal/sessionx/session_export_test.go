package sessionx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// A token-shaped string in tool output or in assistant text must not survive into the file,
// whichever option the member picked.
func TestExportScrubsSecrets(t *testing.T) {
	tok := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	turns := []transcript.Turn{{Role: "assistant", Parts: []transcript.Part{
		{Kind: "text", Text: "the token is " + tok},
		{Kind: "tool", Tool: "Bash", Info: "env", Output: "GITHUB_TOKEN=" + tok},
	}}}
	md := renderSessionExport(session.Meta{Name: "s1", Kind: "claude"}, turns, transcript.ToolsFull, "Asia/Tokyo", time.Now())
	if strings.Contains(md, tok) {
		t.Fatalf("token survived the export:\n%s", md)
	}
	if !strings.Contains(md, bridge.RedactedMark) {
		t.Fatalf("no redaction mark in:\n%s", md)
	}
}

func TestExportUsesTheRequestedZone(t *testing.T) {
	turns := []transcript.Turn{{Role: "user", TS: "2026-10-15T01:00:00Z", Parts: []transcript.Part{{Kind: "text", Text: "hi"}}}}
	md := renderSessionExport(session.Meta{Name: "s1", Kind: "claude"}, turns, transcript.ToolsNames, "Asia/Tokyo", time.Now())
	if !strings.Contains(md, "2026-10-15 10:00") {
		t.Fatalf("turn time not shown in the requested zone:\n%s", md)
	}
}

func TestExportFileNameMasksATokenShapedSlug(t *testing.T) {
	if got := exportFileName("ghp_" + strings.Repeat("A1b2C3d4", 4)); got != "session.md" {
		t.Errorf("got %q", got)
	}
	if got := exportFileName("s1abc"); got != "s1abc.md" {
		t.Errorf("got %q", got)
	}
}

// A question the agent is still waiting on is held apart from the turns; the file keeps it,
// once.
func TestExportAppendsThePendingQuestion(t *testing.T) {
	q := []transcript.Question{{Question: "Which one?"}}
	turns := []transcript.Turn{{Role: "user", Parts: []transcript.Part{{Kind: "text", Text: "go"}}}}
	got := withPendingQuestions(turns, q)
	if len(got) != 2 || got[1].Parts[0].Kind != "question" || got[1].Parts[0].Questions[0].Question != "Which one?" {
		t.Fatalf("pending question not appended: %+v", got)
	}
	if again := withPendingQuestions(got, q); len(again) != 2 {
		t.Fatalf("a question already in the turns was appended twice")
	}
}

// With the real scrubber, an empty PASSWORD= right before the closing fence keeps the fence:
// the scrubber's assignment rule spans a newline.
func TestExportScrubKeepsTheClosingFence(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "assistant", Parts: []transcript.Part{{Kind: "tool", Tool: "Bash", Output: "`````\nPASSWORD="}}},
		{Role: "user", Parts: []transcript.Part{{Kind: "text", Text: "next"}}},
	}
	md := renderSessionExport(session.Meta{Name: "s1", Kind: "claude"}, turns, transcript.ToolsFull, "UTC", time.Now())
	if !strings.Contains(md, "PASSWORD=\n``````\n\n---\n\n## ") {
		t.Fatalf("closing fence lost:\n%s", md)
	}
}

func TestExportToolDetailParam(t *testing.T) {
	for in, want := range map[string]transcript.ToolDetail{
		"none": transcript.ToolsNone, "names": transcript.ToolsNames, "full": transcript.ToolsFull,
		"": transcript.ToolsNames, "bogus": transcript.ToolsNames,
	} {
		if got := exportToolDetail(in); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

// The scrubber runs in every mode, tool-free included.
func TestExportScrubsSecretsInEveryMode(t *testing.T) {
	tok := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	turns := []transcript.Turn{{Role: "assistant", Parts: []transcript.Part{
		{Kind: "text", Text: "the token is " + tok},
		{Kind: "tool", Tool: "Bash", Info: "env", Output: "GITHUB_TOKEN=" + tok},
	}}}
	for _, m := range []transcript.ToolDetail{transcript.ToolsNone, transcript.ToolsNames, transcript.ToolsFull} {
		md := renderSessionExport(session.Meta{Name: "s1", Kind: "claude"}, turns, m, "UTC", time.Now())
		if strings.Contains(md, tok) || !strings.Contains(md, bridge.RedactedMark) {
			t.Errorf("mode %v:\n%s", m, md)
		}
	}
}

// An unknown or missing zone must neither fail nor use the container's clock: the file is UTC and
// says so in the header.
func TestExportFallsBackToLabelledUTC(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")
	prev := time.Local
	time.Local = time.FixedZone("JST", 9*3600)
	defer func() { time.Local = prev }()
	turns := []transcript.Turn{{Role: "user", TS: "2026-10-15T01:00:00Z", Parts: []transcript.Part{{Kind: "text", Text: "hi"}}}}
	now := time.Date(2026, 10, 15, 1, 5, 0, 0, time.UTC)
	for _, tz := range []string{"", "Mars/Olympus_Mons", "Local"} {
		md := renderSessionExport(session.Meta{Name: "s1", Kind: "claude"}, turns, transcript.ToolsNames, tz, now)
		if !strings.Contains(md, "2026-10-15 01:00") || !strings.Contains(md, "2026-10-15 01:05 UTC") {
			t.Errorf("tz %q: expected UTC times with a UTC label:\n%s", tz, md)
		}
	}
}

func TestPickExportZone(t *testing.T) {
	for _, c := range []struct{ query, pref, want string }{
		{"Asia/Tokyo", "America/New_York", "Asia/Tokyo"}, // ?tz= overrides the setting
		{"", "America/New_York", "America/New_York"},     // the setting is the default source
		{"Mars/X", "America/New_York", "America/New_York"},
		{"", "", ""},
		{"Local", "Nope/Zone", ""},
	} {
		if got := pickExportZone(c.query, c.pref); got != c.want {
			t.Errorf("pick(%q,%q)=%q want %q", c.query, c.pref, got, c.want)
		}
	}
}

// The setting is read from ui-prefs, like the display language; "auto" is the browser's, which the
// Agent cannot know.
func TestUIPrefsTimeZone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	write := func(body string) {
		p := uiprefs.Path()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for body, want := range map[string]string{
		`{"timeZone":"Asia/Tokyo"}`: "Asia/Tokyo", `{"timeZone":"auto"}`: "", `{"timeZone":5}`: "", `{}`: "",
	} {
		write(body)
		if got := uiprefs.TimeZone(); got != want {
			t.Errorf("%s: got %q want %q", body, got, want)
		}
	}
}
