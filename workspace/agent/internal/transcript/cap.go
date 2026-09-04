package transcript

import "strings"

// Three display-side truncation helpers, shared by the codex/opencode/claude parsers.

// CapOutput bounds a tool output so a huge result can't bloat the chat payload.
func CapOutput(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 4000 {
		return string(r[:4000]) + "\n…（省略）"
	}
	return s
}

// Clip renders a one-line summary: first line only, capped at 80 runes.
func Clip(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	return s
}

// editCap bounds each before/after block so a huge Write can't bloat the messages
// payload. The full turn is sent once (the poll uses a cursor), so this only guards
// pathological cases; the Console shows the truncation marker.
const editCap = 20000

// CapEdit bounds one before/after block of an edit-family tool part.
func CapEdit(s string) string {
	if r := []rune(s); len(r) > editCap {
		return string(r[:editCap]) + "\n…（省略）"
	}
	return s
}
