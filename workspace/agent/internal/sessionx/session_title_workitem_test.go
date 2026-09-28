package sessionx

import (
	"context"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func TestWithWorkItemKey(t *testing.T) {
	github := &session.WorkItemRef{Provider: "github", Key: "k-k1/agent-fleet#1146"}
	jira := &session.WorkItemRef{Provider: "jira", Key: "PROJ-123"}
	long := strings.Repeat("とても長い件名", 10)
	cases := []struct {
		name  string
		title string
		item  *session.WorkItemRef
		want  string
	}{
		{"no item", "AI提案タイムアウトの調整", nil, "AI提案タイムアウトの調整"},
		{"empty key", "AI提案タイムアウトの調整", &session.WorkItemRef{Provider: "github"}, "AI提案タイムアウトの調整"},
		{"empty title", "", github, ""},
		{"github", "AI提案タイムアウトの調整", github, "#1146 AI提案タイムアウトの調整"},
		{"jira", "Login retry backoff", jira, "PROJ-123 Login retry backoff"},
		{"github key already there", "#1146 AI提案タイムアウトの調整", github, "#1146 AI提案タイムアウトの調整"},
		{"github full key", "k-k1/agent-fleet#1146 timeout tuning", github, "#1146 timeout tuning"},
		// A bare number is the title's own content (a count, a year), not the key.
		{"leading count is kept", "1146 errors after migration", github, "#1146 1146 errors after migration"},
		{"github key with colon", "#1146: timeout tuning", github, "#1146 timeout tuning"},
		{"jira lower-cased", "proj-123 Login retry backoff", jira, "PROJ-123 Login retry backoff"},
		{"longer number is not the key", "#11467 timeout tuning", github, "#1146 #11467 timeout tuning"},
		{"longer jira key is not the key", "PROJ-1234 retry", jira, "PROJ-123 PROJ-1234 retry"},
		{"title is only the key", "#1146", github, "#1146"},
		// U+212A KELVIN SIGN folds to "k" but is three bytes long.
		{"case fold across byte lengths", "k-1 Fix", &session.WorkItemRef{Key: "\u212a-1"}, "\u212a-1 Fix"},
		{"wide key is kept whole", "Retry backoff", &session.WorkItemRef{Key: "CUSTOMER-SUPPORT-SERVICE-123"}, "CUSTOMER-SUPPORT-SERVICE-123 Retry backoff"},
		{"key with no room for text is not added", "timeout tuning", &session.WorkItemRef{Key: strings.Repeat("LONGKEY", 7) + "-1"}, "timeout tuning"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := withWorkItemKey(c.title, c.item); got != c.want {
				t.Fatalf("withWorkItemKey(%q) = %q, want %q", c.title, got, c.want)
			}
		})
	}

	t.Run("wide key keeps whole and cuts the text", func(t *testing.T) {
		key := strings.Repeat("K", 40) + "-1"
		got := withWorkItemKey(strings.Repeat("word ", 10), &session.WorkItemRef{Key: key})
		if !strings.HasPrefix(got, key+" w") || truncateToWidth(got, titleWidthCap) != got {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("long title keeps the key within the width cap", func(t *testing.T) {
		got := withWorkItemKey(truncateToWidth(long, titleWidthCap), github)
		if !strings.HasPrefix(got, "#1146 ") {
			t.Fatalf("key lost: %q", got)
		}
		if truncateToWidth(got, titleWidthCap) != got {
			t.Fatalf("%q is wider than %d columns", got, titleWidthCap)
		}
		if _, ok := CleanTitle(got); !ok {
			t.Fatalf("%q is not a valid title", got)
		}
	})
}

// The model's own "#1146" must survive cleaning, or withWorkItemKey could not tell it from a count.
func TestCleanSuggestedTitleKeepsIssueNumbers(t *testing.T) {
	cases := map[string]string{
		"**#1146 AI提案タイムアウトの調整**":          "#1146 AI提案タイムアウトの調整",
		"## k-k1/agent-fleet#1146 timeout": "k-k1/agent-fleet#1146 timeout",
		"# Heading title":                  "Heading title",
		"`code`#":                          "code",
		"#1. Login redirect":               "Login redirect",
		"##1) Login redirect":              "Login redirect",
	}
	for in, want := range cases {
		if got := CleanSuggestedTitle(in); got != want {
			t.Errorf("CleanSuggestedTitle(%q) = %q, want %q", in, got, want)
		}
	}
	item := &session.WorkItemRef{Provider: "github", Key: "k-k1/agent-fleet#1146"}
	if got := withWorkItemKey(CleanSuggestedTitle("**#1146** AI提案タイムアウトの調整"), item); got != "#1146 AI提案タイムアウトの調整" {
		t.Errorf("model-led key after cleaning = %q", got)
	}
}

// stubTitleLLM makes both suggestion paths return reply without running a CLI.
func stubTitleLLM(t *testing.T, reply string) {
	t.Helper()
	prev := titleSuggestLLM
	titleSuggestLLM = func(context.Context, []transcript.Turn) (string, error) { return reply, nil }
	t.Cleanup(func() { titleSuggestLLM = prev })
}

// Both suggestion paths carry the key: the banner (generateSessionTitle, which writes
// SuggestedTitle) and the rename dialog's button (generateTitleNow, which returns it).
func TestTitleSuggestionPathsKeepWorkItemKey(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "user", Text: "タイムアウトを調整して"},
		{Role: "assistant", Text: "調整します"},
	}
	cases := []struct {
		name string
		item *session.WorkItemRef
		want string
	}{
		{"github", &session.WorkItemRef{Provider: "github", Key: "k-k1/agent-fleet#1146"}, "#1146 AI提案タイムアウトの調整"},
		{"jira", &session.WorkItemRef{Provider: "jira", Key: "PROJ-123"}, "PROJ-123 AI提案タイムアウトの調整"},
		{"no item", nil, "AI提案タイムアウトの調整"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("AF_SESSIONS_DIR", t.TempDir())
			stubTitleLLM(t, "AI提案タイムアウトの調整")
			name := "wi-" + c.name
			if c.item == nil {
				name = "wi-none"
			}
			session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, WorkItem: c.item})

			generateSessionTitle(name, turns)
			m, _ := session.ReadMeta(name)
			if m.SuggestedTitle != c.want {
				t.Fatalf("auto suggestion = %q, want %q", m.SuggestedTitle, c.want)
			}

			got, err := generateTitleNow(context.Background(), name, turns, m.WorkItem)
			if err != nil {
				t.Fatalf("manual suggestion failed: %v", err)
			}
			if got != c.want {
				t.Fatalf("manual suggestion = %q, want %q", got, c.want)
			}
		})
	}
}
