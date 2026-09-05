package main

// Pins the values in the shipped tmux config (workspace/tmux.conf, /etc/tmux.conf in the image)
// that directly decide perceived latency.
//
// escape-time is how long tmux waits to tell a bare Esc from the start of an Esc-prefixed
// sequence (arrows, F keys); tmux 3.3a defaults to 500ms. Measured (in this worktree's container,
// tmux 3.3a):
//
//	default 500 -> Esc echoes in 501ms / 20 -> 20ms
//
// So dropping the setting delays every Esc in claude, codex and vim by half a second. The input
// here comes from xterm.js, which sends one onData as one WebSocket frame without splitting the
// sequence, so no long grace period is needed. If the setting disappears (falling back to the
// default) the only symptom is "the terminal feels sluggish", which is a long way from the cause -
// hence pinning the value itself.
import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

func TestShippedTmuxConfKeepsEscapeTimeLow(t *testing.T) {
	b, err := os.ReadFile("../tmux.conf")
	if err != nil {
		t.Fatalf("read workspace/tmux.conf: %v", err)
	}
	m := regexp.MustCompile(`(?m)^\s*set\s+-sg\s+escape-time\s+(\d+)\s*$`).FindSubmatch(b)
	if m == nil {
		t.Fatal("workspace/tmux.conf sets no escape-time — tmux falls back to its 500ms default, " +
			"which delays every Esc in a TUI session by half a second")
	}
	ms, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("escape-time is not a number: %q", m[1])
	}
	// Not 0: 0 means "do not wait", and in the rare case a frame is split an arrow key degenerates
	// into a literal Esc + "[A". A few tens of ms are imperceptible and absorb the split.
	if ms <= 0 || ms > 50 {
		t.Fatalf("escape-time = %d ms; want 1..50 (500 = tmux's default and half a second of Esc lag)", ms)
	}
}
