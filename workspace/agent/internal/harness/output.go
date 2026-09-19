package harness

import "fmt"

// defaultMaxToolOutput bounds one tool result's Content before it goes back into
// the message history, so one verbose command (a full test run, a large grep) does
// not eat the context window it is this package's whole reason for existing to
// protect (ADR 0093's motivation). Order-of-magnitude match to the other kinds'
// own tool-output caps, not a value read from anywhere else.
const defaultMaxToolOutput = 30_000

// truncateOutput keeps the head and tail of s when it exceeds limit — a bash
// command's early output and its final status line are both usually more useful
// than the middle. limit <= 0 disables truncation (a caller that wants no cap).
func truncateOutput(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	head := limit * 4 / 5
	tail := limit - head
	notice := fmt.Sprintf("\n... [truncated %d of %d bytes] ...\n", len(s)-limit, len(s))
	return s[:head] + notice + s[len(s)-tail:]
}
