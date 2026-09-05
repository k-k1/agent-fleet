package usagex

import "testing"

func TestContextWindowGuess(t *testing.T) {
	cases := []struct {
		model string
		used  int
		want  int
	}{
		// Default 1M. Unknown future models land here too, so a missing entry is never
		// misread as 200k.
		{"claude-fable-5", 0, 1_000_000},
		{"claude-opus-4-8", 0, 1_000_000},
		{"claude-opus-4-6", 0, 1_000_000},
		{"claude-sonnet-4-6", 0, 1_000_000},
		{"claude-opus-5", 0, 1_000_000},
		{"claude-sonnet-5", 0, 1_000_000},
		{"anthropic/claude-sonnet-5", 0, 1_000_000}, // provider-prefixed, as opencode reports it
		{"claude-opus-9", 0, 1_000_000},             // unknown future model
		// The 200k exceptions. Do not mistake the generation number "4-5" for "5".
		{"claude-opus-4-5", 0, 200_000},
		{"claude-sonnet-4-5-20250929", 0, 200_000},
		{"claude-opus-4-1", 0, 200_000},
		{"claude-opus-4-20250514", 0, 200_000}, // dated id
		{"claude-3-5-sonnet-20241022", 0, 200_000},
		{"claude-3-7-sonnet", 0, 200_000},
		{"claude-haiku-4-5-20251001", 0, 200_000},
		// gpt-5.x, plus non-Claude models of unknown provenance (widened once the
		// observed usage exceeds it).
		{"gpt-5.1-codex", 0, 272_000},
		{"some-unknown-model", 0, 200_000},
		{"some-unknown-model", 250_000, 1_000_000},
	}
	for _, c := range cases {
		if got := WindowGuess(c.model, c.used); got != c.want {
			t.Errorf("guess(%q, %d) = %d, want %d", c.model, c.used, got, c.want)
		}
	}
}
