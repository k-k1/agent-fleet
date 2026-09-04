package agy

import (
	"testing"
)

// A cleaned capture of the real v1.1.4 /context panel (2026-07-20, measured with a tmux
// probe on a resumed conversation), prefixed with an earlier stale panel to
// exercise the last-render cut — a resumed conversation replays its history,
// which can itself contain a rendered /context panel.
const contextPanel = `
? for shortcuts
└ Context Usage
◉ ◉ ◉ ◉ ◉ □ □ □     Old Model · 999k/200k tokens (99.0%)
` + "└" + ` Context Usage
◉ ◉ ◉ ◉ ◉ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □     Gemini 3.5 Flash (Medium) · 26.0k/1.0M tokens
□ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □      (2.5%)
□ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □     Token usage by category
□ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □     ◉ User messages: 80 tokens (0.0%)
□ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □     ◉ Agent responses: 1.9k tokens (0.2%)
□ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □     ◉ Tool calls: 88 tokens (0.0%)
□ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □     ⛁ System prompt: 6.0k tokens (0.6%)
□ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □     ⛁ System tools: 16.7k tokens (1.6%)
□ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □ □     □ Free space: 1.0M (97.5%)
Related: /artifact · /skill · /rewind
`

func TestParseContext(t *testing.T) {
	c, err := parseContext(contextPanel)
	if err != nil {
		t.Fatal(err)
	}
	if c.Tokens != 26000 {
		t.Errorf("tokens = %d, want 26000", c.Tokens)
	}
	if c.Window != 1000000 {
		t.Errorf("window = %d, want 1000000", c.Window)
	}
	if c.At == "" {
		t.Error("missing At timestamp")
	}
}

// A fresh conversation before any generation renders "0/1.0M" with the
// "Estimated usage (awaiting generation)" caption — 0 is a valid reading
// (the Console hides the bar at 0 itself).
func TestParseContextEmptyConversation(t *testing.T) {
	c, err := parseContext(`
└ Context Usage
□ □ □ □     Gemini 3.5 Flash (Medium) · 0/1.0M tokens (0.0%)
□ □ □ □     Estimated usage (awaiting generation)
□ □ □ □     □ Free space: 1.0M (100.0%)
`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Tokens != 0 || c.Window != 1000000 {
		t.Errorf("got %d/%d, want 0/1000000", c.Tokens, c.Window)
	}
}

func TestParseContextNoTotal(t *testing.T) {
	if _, err := parseContext("? for shortcuts\nno panel here"); err == nil {
		t.Fatal("expected an error on output without a total line")
	}
}

func TestParseTokCount(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"80", 80},
		{"1.9k", 1900},
		{"26.0k", 26000},
		{"1.0M", 1000000},
		{"272k", 272000},
	}
	for _, c := range cases {
		got, err := parseTokCount(c.in)
		if err != nil {
			t.Errorf("parseTokCount(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseTokCount(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	if _, err := parseTokCount("abc"); err == nil {
		t.Error("expected an error for a non-numeric count")
	}
}
