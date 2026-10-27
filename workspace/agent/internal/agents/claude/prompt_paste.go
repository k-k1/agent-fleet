package claude

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

var _ agents.PromptPaste = agentImpl{}

// PasteMinBytes is the length from which claude gets its prompt as a bracketed paste
// even when it has no line break. claude's own notes treat a long literal-key chunk as a
// paste (image_paste.go), so one explicit paste is the predictable form. The
// threshold is a guess, unverified against a live TUI; short single-line prompts keep the
// literal-key path.
const PasteMinBytes = 2048

// PasteNeeded reports whether a prompt must reach claude's composer as a bracketed paste
// rather than literal keys. A line break in a literal stream is an Enter (docs/build/92), so a
// multi-line prompt is submitted line by line. Suspected, not confirmed on a live TUI, to be
// what left a busy session with only the tail of a peer message (issue #2195); a paste carries
// the breaks as text and ends in one submit either way.
func (agentImpl) PasteNeeded(text string) bool {
	return len(text) >= PasteMinBytes || strings.ContainsAny(text, "\r\n")
}
